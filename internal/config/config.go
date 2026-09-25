// Package config reads and writes codeagent-sync's configuration file,
// config.yaml in the state directory (~/.codeagent-sync).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
)

const (
	// FileName is the configuration file inside the state directory.
	FileName = "config.yaml"
	// KeyFileName is where init stores the age identity derived from the
	// passphrase, so later runs do not need the passphrase.
	KeyFileName = "age-key.txt"
)

// ErrNotInitialized means init has not been run on this machine.
var ErrNotInitialized = errors.New("codeagent-sync is not set up on this machine; run: codeagent-sync init")

// Config is the configuration file.
type Config struct {
	Storage storage.StorageConfig `yaml:"storage"`
	// KeyFile holds the age identity. A leading ~ is the home directory.
	KeyFile string `yaml:"key_file"`
	// ConditionalWrites records what init found out about the store: false
	// means it ignores preconditions, so machines syncing at the same moment
	// can overwrite each other.
	ConditionalWrites *bool `yaml:"conditional_writes,omitempty"`
}

// Path returns the configuration file in stateDir.
func Path(stateDir string) string { return filepath.Join(stateDir, FileName) }

// Load reads the configuration from stateDir.
func Load(stateDir string) (*Config, error) {
	data, err := os.ReadFile(Path(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(stateDir), err)
	}
	if err := c.Storage.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(stateDir), err)
	}
	return &c, nil
}

// Save writes the configuration to stateDir, readable only by the user: it
// holds storage credentials.
func (c *Config) Save(stateDir string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return platform.WriteFileAtomic(Path(stateDir), data, 0o600)
}

// KeyPath returns the key file's path with ~ expanded.
func (c *Config) KeyPath(home string) string { return ExpandHome(c.KeyFile, home) }

// ExpandHome expands a leading ~ to home.
func ExpandHome(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"), strings.HasPrefix(p, `~\`):
		return filepath.Join(home, p[2:])
	}
	return p
}
