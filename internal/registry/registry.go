// Package registry keeps the decisions about what is shared between Claude
// Code and Codex: a skill, MCP server or plugin marketplace that should stay
// with one tool, or exist deliberately as one version per tool. The file,
// registry.yaml in the state directory, syncs to every machine, so a
// question answered on one machine is not asked again on another.
package registry

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/structured"
)

// FileName is the registry file inside the state directory.
const FileName = "registry.yaml"

// Decisions.
const (
	Shared     = "shared"      // in the shared layout
	ClaudeOnly = "claude-only" // stays with Claude Code
	CodexOnly  = "codex-only"  // stays with Codex
	Variant    = "variant"     // one version per tool, on purpose
)

// Kinds of things decided about.
const (
	Skill  = "skill"
	MCP    = "mcp"
	Plugin = "plugin" // a plugin marketplace
)

// Registry maps "<kind>/<name>" to a decision.
type Registry struct {
	Decisions map[string]string

	changed map[string]bool // decided since Load
}

// Load reads the registry from stateDir; a missing file is an empty one.
func Load(stateDir string) (*Registry, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, FileName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	d, err := structured.ParseDecisions(data)
	if err != nil {
		return nil, err
	}
	return &Registry{Decisions: d, changed: map[string]bool{}}, nil
}

// Save writes the decisions made since Load into the file as it is now, so
// that decisions a sync brought in meanwhile stay.
func (r *Registry) Save(stateDir string) error {
	current, err := Load(stateDir)
	if err != nil {
		return err
	}
	for id := range r.changed {
		if decision, ok := r.Decisions[id]; ok {
			current.Decisions[id] = decision
		} else {
			delete(current.Decisions, id)
		}
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := platform.WriteFileAtomic(filepath.Join(stateDir, FileName), structured.RenderDecisions(current.Decisions), 0o644); err != nil {
		return err
	}
	r.Decisions, r.changed = current.Decisions, map[string]bool{}
	return nil
}

func id(kind, name string) string { return kind + "/" + name }

// Get returns the decision about a thing, or "".
func (r *Registry) Get(kind, name string) string { return r.Decisions[id(kind, name)] }

// Set records a decision; an empty decision forgets it.
func (r *Registry) Set(kind, name, decision string) {
	if r.changed == nil {
		r.changed = map[string]bool{}
	}
	r.changed[id(kind, name)] = true
	if decision == "" {
		delete(r.Decisions, id(kind, name))
		return
	}
	r.Decisions[id(kind, name)] = decision
}

// Variants returns the skills that exist deliberately as one version per tool.
func (r *Registry) Variants() map[string]bool {
	out := map[string]bool{}
	prefix := Skill + "/"
	for k, v := range r.Decisions {
		if v == Variant && len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out[k[len(prefix):]] = true
		}
	}
	return out
}

// Names returns the decided things in order, for listing.
func (r *Registry) Names() []string {
	var out []string
	for k := range r.Decisions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
