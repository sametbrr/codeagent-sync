package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var (
	// ErrNotFound means the object does not exist.
	ErrNotFound = errors.New("object not found")

	// ErrPreconditionFailed means a conditional write found the object in a
	// different state than expected: it already existed (IfNoneMatch) or had
	// changed since it was read (IfMatch).
	ErrPreconditionFailed = errors.New("precondition failed")
)

// Precondition restricts a write to a known state of the object. The zero
// value writes unconditionally.
type Precondition struct {
	// IfMatch requires the object to exist with exactly this ETag.
	IfMatch string
	// IfNoneMatch requires the object not to exist.
	IfNoneMatch bool
}

// ObjectStore is the storage interface of the sync engine.
//
// ETags are opaque version tokens: whatever Put, Get, Head and List return for
// an object can be passed back in Precondition.IfMatch. Deletions are never
// conditional (R2 ignores If-Match on DELETE), so the engine records a
// deletion by overwriting the object with a tombstone instead.
type ObjectStore interface {
	// Put stores data at key if pre holds and returns the object's new ETag.
	// The ETag is empty when the backend does not report it; List and Head
	// provide it later. A failed precondition returns ErrPreconditionFailed.
	Put(ctx context.Context, key string, data []byte, pre Precondition) (etag string, err error)

	// Get returns an object's data and ETag, or ErrNotFound.
	Get(ctx context.Context, key string) (data []byte, etag string, err error)

	// Head returns an object's metadata, or ErrNotFound.
	Head(ctx context.Context, key string) (*ObjectInfo, error)

	// List returns all objects whose keys start with prefix.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)

	// Delete removes an object unconditionally. Deleting a missing object
	// is not an error.
	Delete(ctx context.Context, key string) error

	// BucketExists reports whether the configured bucket is reachable.
	BucketExists(ctx context.Context) (bool, error)
}

// BucketCreator is implemented by stores that can create their bucket
// (given credentials that allow it).
type BucketCreator interface {
	CreateBucket(ctx context.Context) error
}

// ProbeResult is what Probe found out about a live store.
type ProbeResult struct {
	// CreateGuard: a write with IfNoneMatch fails when the object exists.
	CreateGuard bool
	// MatchGuard: a write with IfMatch succeeds with the current ETag and
	// fails with a stale one.
	MatchGuard bool
	// ReportsETag: Put returns the new ETag instead of an empty string.
	ReportsETag bool
}

// ConditionalWrites reports whether the store enforces both kinds of
// preconditions, which the engine needs to rule out lost updates.
func (r ProbeResult) ConditionalWrites() bool { return r.CreateGuard && r.MatchGuard }

// Probe checks against a live store whether conditional writes are enforced.
// Servers that do not understand the headers silently ignore them, so this
// can only be learned by trying. It writes two small objects under prefix and
// deletes them again.
func Probe(ctx context.Context, s ObjectStore, prefix string) (ProbeResult, error) {
	var r ProbeResult
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return r, err
	}
	createKey := prefix + "probe-create-" + hex.EncodeToString(id)
	matchKey := prefix + "probe-match-" + hex.EncodeToString(id)
	defer func() {
		cleanup := context.WithoutCancel(ctx)
		s.Delete(cleanup, createKey)
		s.Delete(cleanup, matchKey)
	}()

	if _, err := s.Put(ctx, createKey, []byte("1"), Precondition{IfNoneMatch: true}); err != nil {
		return r, fmt.Errorf("probe: first write: %w", err)
	}
	_, err := s.Put(ctx, createKey, []byte("2"), Precondition{IfNoneMatch: true})
	switch {
	case errors.Is(err, ErrPreconditionFailed):
		r.CreateGuard = true
	case err != nil:
		return r, fmt.Errorf("probe: guarded create: %w", err)
	}

	first, err := s.Put(ctx, matchKey, []byte("1"), Precondition{})
	if err != nil {
		return r, fmt.Errorf("probe: write: %w", err)
	}
	r.ReportsETag = first != ""
	if first == "" {
		info, err := s.Head(ctx, matchKey)
		if err != nil {
			return r, fmt.Errorf("probe: head: %w", err)
		}
		first = info.ETag
	}
	if _, err := s.Put(ctx, matchKey, []byte("2"), Precondition{IfMatch: first}); err != nil {
		if errors.Is(err, ErrPreconditionFailed) {
			return r, nil // the current ETag was rejected: If-Match is unusable
		}
		return r, fmt.Errorf("probe: matching write: %w", err)
	}
	_, err = s.Put(ctx, matchKey, []byte("3"), Precondition{IfMatch: first})
	switch {
	case errors.Is(err, ErrPreconditionFailed):
		r.MatchGuard = true
	case err != nil:
		return r, fmt.Errorf("probe: stale write: %w", err)
	}
	return r, nil
}

// ReadAllLimited reads an object body, refusing objects larger than
// MaxDownloadSize so a corrupt or hostile remote cannot exhaust memory.
func ReadAllLimited(r io.Reader, key string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxDownloadSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	if int64(len(data)) > MaxDownloadSize {
		return nil, fmt.Errorf("%s exceeds the maximum object size of %d bytes", key, MaxDownloadSize)
	}
	return data, nil
}
