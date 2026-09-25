package storage

import (
	"testing"
)

func TestNew_InvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *StorageConfig
		errMsg string
	}{
		{
			name:   "nil config",
			config: nil,
			errMsg: "bucket is required",
		},
		{
			name: "empty provider",
			config: &StorageConfig{
				Bucket: "test",
			},
			errMsg: "provider is required",
		},
		{
			name: "invalid provider",
			config: &StorageConfig{
				Provider: "invalid",
				Bucket:   "test",
			},
			errMsg: "unsupported provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			if cfg == nil {
				cfg = &StorageConfig{}
			}
			_, err := New(cfg)
			if err == nil {
				t.Errorf("New() expected error, got nil")
				return
			}
			if !contains(err.Error(), tt.errMsg) {
				t.Errorf("New() error = %v, want error containing %q", err, tt.errMsg)
			}
		})
	}
}

func TestProviderConstants(t *testing.T) {
	// Ensure provider constants are correct
	if ProviderR2 != "r2" {
		t.Errorf("ProviderR2 = %q, want %q", ProviderR2, "r2")
	}
	if ProviderS3 != "s3" {
		t.Errorf("ProviderS3 = %q, want %q", ProviderS3, "s3")
	}
	if ProviderGCS != "gcs" {
		t.Errorf("ProviderGCS = %q, want %q", ProviderGCS, "gcs")
	}
}
