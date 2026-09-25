package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/storage"
)

func TestSaveAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".codeagent-sync")
	yes := true
	c := &Config{
		Storage: storage.StorageConfig{
			Provider: storage.ProviderR2, Bucket: "codeagent-sync",
			AccountID: "acct", AccessKeyID: "key", SecretAccessKey: "secret",
		},
		KeyFile:           "~/.codeagent-sync/age-key.txt",
		ConditionalWrites: &yes,
	}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(Path(dir))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("config file mode = %v, %v; want 600 (it holds credentials)", fi, err)
		}
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Storage != c.Storage || got.KeyFile != c.KeyFile || got.ConditionalWrites == nil || !*got.ConditionalWrites {
		t.Errorf("Load = %+v, want %+v", got, c)
	}
	if want := filepath.Join("/home/ad", ".codeagent-sync", "age-key.txt"); got.KeyPath("/home/ad") != want {
		t.Errorf("KeyPath = %q, want %q", got.KeyPath("/home/ad"), want)
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("Load of an empty dir = %v, want ErrNotInitialized", err)
	}
}
