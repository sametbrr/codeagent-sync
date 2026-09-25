package r2

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/storage/s3/s3fake"
	"github.com/sametbrr/codeagent-sync/internal/storage/storagetest"
)

// The R2 client is built with R2's defaults (checksums on, region "auto");
// the fake checks it still speaks plain S3 with conditional headers.
func TestSuiteAgainstFakeS3(t *testing.T) {
	srv := httptest.NewServer(s3fake.New("test-bucket"))
	t.Cleanup(srv.Close)

	s, err := New(&storage.StorageConfig{
		Provider:        storage.ProviderR2,
		Bucket:          "test-bucket",
		AccountID:       "account",
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "secret",
		Endpoint:        srv.URL,
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, s, "suite/")
}

// TestLiveBucket runs the suite against a real R2 bucket. It only runs when
// CODEAGENT_LIVE_R2_CONFIG names a config file with a "storage" section (the
// format of ~/.codeagent-sync/config.yaml).
// All objects go under _probe/ and are deleted afterwards.
func TestLiveBucket(t *testing.T) {
	path := os.Getenv("CODEAGENT_LIVE_R2_CONFIG")
	if path == "" {
		t.Skip("set CODEAGENT_LIVE_R2_CONFIG to run against a real bucket")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Storage storage.StorageConfig `yaml:"storage"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Provider != storage.ProviderR2 {
		t.Fatalf("provider is %q, want r2", cfg.Storage.Provider)
	}
	s, err := New(&cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, s, "_probe/storagetest/")
}
