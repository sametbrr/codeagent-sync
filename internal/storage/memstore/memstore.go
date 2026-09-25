// Package memstore is an in-memory storage.ObjectStore for tests. Its
// conditional writes behave like S3 and R2 (checked against R2): IfNoneMatch
// fails when the object exists, IfMatch fails unless the object exists with
// that ETag, and the ETag is the quoted MD5 of the content.
package memstore

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sametbrr/codeagent-sync/internal/storage"
)

// Store is an in-memory object store. The zero value is not usable; call New.
type Store struct {
	mu      sync.Mutex
	objects map[string]object

	// BeforePut, if set, runs at the start of every Put, before the
	// precondition is checked. Tests use it to let "another machine" write
	// between the engine's read and its write. It must not call Put while it
	// is still installed.
	BeforePut func(key string)
}

type object struct {
	data     []byte
	etag     string
	modified time.Time
}

var _ storage.ObjectStore = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{objects: make(map[string]object)}
}

func (s *Store) Put(ctx context.Context, key string, data []byte, pre storage.Precondition) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.BeforePut != nil {
		s.BeforePut(key)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur, exists := s.objects[key]
	if pre.IfNoneMatch && exists {
		return "", fmt.Errorf("put %s: %w", key, storage.ErrPreconditionFailed)
	}
	if pre.IfMatch != "" && (!exists || cur.etag != pre.IfMatch) {
		return "", fmt.Errorf("put %s: %w", key, storage.ErrPreconditionFailed)
	}
	sum := md5.Sum(data)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	s.objects[key] = object{data: bytes.Clone(data), etag: etag, modified: time.Now()}
	return etag, nil
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, "", fmt.Errorf("get %s: %w", key, storage.ErrNotFound)
	}
	return bytes.Clone(o.data), o.etag, nil
}

func (s *Store) Head(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("head %s: %w", key, storage.ErrNotFound)
	}
	info := o.info(key)
	return &info, nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []storage.ObjectInfo
	for key, o := range s.objects {
		if strings.HasPrefix(key, prefix) {
			out = append(out, o.info(key))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *Store) BucketExists(ctx context.Context) (bool, error) {
	return true, ctx.Err()
}

// Keys returns the stored keys in order.
func (s *Store) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (o object) info(key string) storage.ObjectInfo {
	return storage.ObjectInfo{Key: key, Size: int64(len(o.data)), LastModified: o.modified, ETag: o.etag}
}
