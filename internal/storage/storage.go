package storage

import (
	"fmt"
	"time"
)

// Provider represents a storage provider type
type Provider string

const (
	ProviderR2     Provider = "r2"
	ProviderS3     Provider = "s3"
	ProviderGCS    Provider = "gcs"
	ProviderWebDAV Provider = "webdav"
)

// MaxDownloadSize is the maximum allowed size for a single downloaded object (100MB).
// This prevents memory exhaustion from oversized or malicious remote files.
const MaxDownloadSize = 100 * 1024 * 1024

// ObjectInfo contains metadata about a stored object
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

// New returns the store for cfg.
func New(cfg *StorageConfig) (ObjectStore, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid storage config: %w", err)
	}

	switch cfg.Provider {
	case ProviderR2:
		return NewR2(cfg)
	case ProviderS3:
		return NewS3(cfg)
	case ProviderGCS:
		return NewGCS(cfg)
	case ProviderWebDAV:
		return NewWebDAV(cfg)
	default:
		return nil, fmt.Errorf("unsupported storage provider: %s", cfg.Provider)
	}
}

// NewR2 creates a new R2 storage adapter (implemented in r2/r2.go)
var NewR2 func(cfg *StorageConfig) (ObjectStore, error)

// NewS3 creates a new S3 storage adapter (implemented in s3/s3.go)
var NewS3 func(cfg *StorageConfig) (ObjectStore, error)

// NewGCS creates a new GCS storage adapter (implemented in gcs/gcs.go)
var NewGCS func(cfg *StorageConfig) (ObjectStore, error)

// NewWebDAV creates a new WebDAV storage adapter (implemented in webdav/webdav.go)
var NewWebDAV func(cfg *StorageConfig) (ObjectStore, error)
