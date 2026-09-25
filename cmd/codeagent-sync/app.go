package main

import (
	"io"
	"path/filepath"

	"github.com/sametbrr/codeagent-sync/internal/config"
	"github.com/sametbrr/codeagent-sync/internal/crypto"
	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/homepath"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// app holds what every command shares.
type app struct {
	in          io.Reader
	out, errOut io.Writer
	quiet       bool
	jsonOut     bool

	passphrase string // typed once already (for a join code); used by the next prompt
}

// open loads the configuration and returns an engine for this machine.
func (a *app) open() (*engine.Engine, *config.Config, error) {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(dirs.State)
	if err != nil {
		return nil, nil, err
	}
	store, err := storage.New(&cfg.Storage)
	if err != nil {
		return nil, nil, err
	}
	enc, err := crypto.NewEncryptor(cfg.KeyPath(dirs.Home))
	if err != nil {
		return nil, nil, err
	}
	return newEngine(dirs, store, enc), cfg, nil
}

// newEngine builds the engine for dirs. When the home directory is reached
// through a symlink (/home -> /var/home), its real path is translated too.
func newEngine(dirs platform.Dirs, store storage.ObjectStore, cipher envelope.Cipher) *engine.Engine {
	var aliases []string
	if resolved, err := filepath.EvalSymlinks(dirs.Home); err == nil && resolved != dirs.Home {
		aliases = append(aliases, resolved)
	}
	roots, _ := syncRoots(dirs)
	return &engine.Engine{
		Store:    store,
		Cipher:   cipher,
		Roots:    roots,
		Mapper:   homepath.New(dirs.Home, platform.Current(), aliases...),
		OS:       platform.Current(),
		StateDir: dirs.State,
	}
}

// syncRoots returns what this machine syncs: the defaults changed by the
// shared and the local rules. Rules that cannot apply are returned and
// skipped.
func syncRoots(dirs platform.Dirs) ([]tools.Root, []error) {
	shared, local, err := tools.LoadRules(dirs.State)
	if err != nil {
		return tools.Roots(dirs), []error{err}
	}
	return tools.WithRules(tools.Roots(dirs), shared.Merge(local))
}

// variants returns the skills marked as deliberately separate for each tool.
func (a *app) variants(dirs platform.Dirs) map[string]bool {
	reg, err := registry.Load(dirs.State)
	if err != nil {
		return nil
	}
	return reg.Variants()
}
