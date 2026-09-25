package gcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	appstorage "github.com/sametbrr/codeagent-sync/internal/storage"
)

func init() {
	appstorage.NewGCS = New
}

// Client implements storage for Google Cloud Storage.
//
// GCS versions every object with a generation number and expresses
// conditional writes as generation preconditions, so the generation, in
// decimal, is what this client reports and accepts as the ETag.
type Client struct {
	client  *storage.Client
	bucket  string
	project string
}

var (
	_ appstorage.ObjectStore   = (*Client)(nil)
	_ appstorage.BucketCreator = (*Client)(nil)
)

// New creates a new GCS storage client
func New(cfg *appstorage.StorageConfig) (appstorage.ObjectStore, error) {
	ctx := context.Background()

	var opts []option.ClientOption

	// Configure authentication
	if cfg.CredentialsFile != "" {
		// Expand ~ in path
		credPath := cfg.CredentialsFile
		if len(credPath) > 0 && credPath[0] == '~' {
			home, _ := os.UserHomeDir()
			credPath = home + credPath[1:]
		}
		opts = append(opts, option.WithCredentialsFile(credPath))
	} else if cfg.CredentialsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(cfg.CredentialsJSON)))
	}
	// If no credentials specified, the client will use Application Default Credentials

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}

	return &Client{
		client:  client,
		bucket:  cfg.Bucket,
		project: cfg.ProjectID,
	}, nil
}

// Put stores data at key if pre holds and returns the new generation.
func (c *Client) Put(ctx context.Context, key string, data []byte, pre appstorage.Precondition) (string, error) {
	obj := c.client.Bucket(c.bucket).Object(key)
	cond, err := conditionsFor(pre)
	if err != nil {
		return "", fmt.Errorf("put %s: %w", key, err)
	}
	if cond != nil {
		obj = obj.If(*cond)
	}

	w := obj.NewWriter(ctx)
	w.ContentType = "application/octet-stream"
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("put %s: %w", key, mapError(err))
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("put %s: %w", key, mapError(err))
	}
	return generationETag(w.Attrs().Generation), nil
}

// Get returns an object's data and generation.
func (c *Client) Get(ctx context.Context, key string) ([]byte, string, error) {
	r, err := c.client.Bucket(c.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("get %s: %w", key, mapError(err))
	}
	defer func() { _ = r.Close() }()
	data, err := appstorage.ReadAllLimited(r, key)
	if err != nil {
		return nil, "", err
	}
	return data, generationETag(r.Attrs.Generation), nil
}

// conditionsFor maps a precondition onto GCS generation preconditions.
func conditionsFor(pre appstorage.Precondition) (*storage.Conditions, error) {
	switch {
	case pre.IfNoneMatch:
		return &storage.Conditions{DoesNotExist: true}, nil
	case pre.IfMatch != "":
		gen, err := strconv.ParseInt(pre.IfMatch, 10, 64)
		if err != nil || gen <= 0 {
			// Not a generation this client handed out, so it cannot match.
			return nil, fmt.Errorf("%w: %q is not a GCS generation", appstorage.ErrPreconditionFailed, pre.IfMatch)
		}
		return &storage.Conditions{GenerationMatch: gen}, nil
	}
	return nil, nil
}

// mapError adds the storage sentinel the engine acts on to GCS errors.
func mapError(err error) error {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%w: %w", appstorage.ErrNotFound, err)
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusPreconditionFailed {
		return fmt.Errorf("%w: %w", appstorage.ErrPreconditionFailed, err)
	}
	return err
}

func generationETag(gen int64) string { return strconv.FormatInt(gen, 10) }

// Delete removes the object with the given key. A missing object is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	err := c.client.Bucket(c.bucket).Object(key).Delete(ctx)
	if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("failed to delete %s: %w", key, err)
	}
	return nil
}

// List returns all objects with the given prefix
func (c *Client) List(ctx context.Context, prefix string) ([]appstorage.ObjectInfo, error) {
	var objects []appstorage.ObjectInfo

	query := &storage.Query{}
	if prefix != "" {
		query.Prefix = prefix
	}

	it := c.client.Bucket(c.bucket).Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		objects = append(objects, appstorage.ObjectInfo{
			Key:          attrs.Name,
			Size:         attrs.Size,
			LastModified: attrs.Updated,
			ETag:         generationETag(attrs.Generation),
		})
	}

	return objects, nil
}

// Head returns metadata for the given key without downloading content
func (c *Client) Head(ctx context.Context, key string) (*appstorage.ObjectInfo, error) {
	attrs, err := c.client.Bucket(c.bucket).Object(key).Attrs(ctx)
	if err != nil {
		return nil, fmt.Errorf("head %s: %w", key, mapError(err))
	}

	return &appstorage.ObjectInfo{
		Key:          attrs.Name,
		Size:         attrs.Size,
		LastModified: attrs.Updated,
		ETag:         generationETag(attrs.Generation),
	}, nil
}

// CreateBucket creates the configured bucket in the configured project.
func (c *Client) CreateBucket(ctx context.Context) error {
	if err := c.client.Bucket(c.bucket).Create(ctx, c.project, nil); err != nil {
		return fmt.Errorf("create bucket %s: %w", c.bucket, err)
	}
	return nil
}

// BucketExists checks if the configured bucket exists
func (c *Client) BucketExists(ctx context.Context) (bool, error) {
	_, err := c.client.Bucket(c.bucket).Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrBucketNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check bucket: %w", err)
	}
	return true, nil
}
