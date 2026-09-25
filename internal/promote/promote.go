// Package promote moves skills, MCP servers and plugin marketplaces into or
// out of the layout shared by Claude Code and Codex, and records the
// decision. Every change is backed up first, so undo takes it back; the next
// sync carries it to the other machines.
package promote

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sametbrr/codeagent-sync/internal/compat"
	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
	"github.com/sametbrr/codeagent-sync/internal/structured"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// Promoter carries out decisions on one machine.
type Promoter struct {
	Engine   *engine.Engine
	Dirs     platform.Dirs
	Registry *registry.Registry
	// Run runs a tool's own command (an installer); nil runs it for real.
	Run func(name string, args ...string) error
}

func (p *Promoter) run(name string, args ...string) error {
	if p.Run != nil {
		return p.Run(name, args...)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// step makes one change as one undoable step: it holds the sync lock, and
// backs up what the change touches together with the registry, so undo takes
// back the decision as well. A change that fails is rolled back.
func (p *Promoter) step(what string, paths []string, change func() error, kind, name, decision string) error {
	return p.Engine.WithLock(func() error {
		b, err := p.Engine.StartBackup(what)
		if err != nil {
			return err
		}
		for _, path := range append(paths, filepath.Join(p.Dirs.State, registry.FileName)) {
			if err := b.Save(path); err != nil {
				_ = b.Rollback() // nothing changed yet but the backup itself
				return err
			}
		}
		err = change()
		if err == nil {
			p.Registry.Set(kind, name, decision)
			err = p.Registry.Save(p.Dirs.State)
		}
		if err != nil {
			if rerr := b.Rollback(); rerr != nil {
				return fmt.Errorf("%w (and putting things back failed: %v; codeagent-sync undo can)", err, rerr)
			}
			return err
		}
		return b.Close()
	})
}

// codexConfig is the file Codex keeps MCP servers and plugin marketplaces in.
func (p *Promoter) codexConfig() string {
	return platform.LinkTarget(filepath.Join(p.Dirs.Codex, "config.toml"))
}

// claudeJSON is where Claude Code keeps its MCP servers.
func (p *Promoter) claudeJSON() string { return platform.LinkTarget(p.Dirs.ClaudeJSON) }

// claudePluginFiles are the files "claude plugin marketplace" edits.
func (p *Promoter) claudePluginFiles() []string {
	return []string{
		platform.LinkTarget(filepath.Join(p.Dirs.Claude, "plugins", "known_marketplaces.json")),
		platform.LinkTarget(filepath.Join(p.Dirs.Claude, "settings.json")),
	}
}

// Share puts a candidate into the shared layout.
func (p *Promoter) Share(c compat.Candidate) error {
	if c.Verdict == compat.Blocked || c.Verdict == compat.Variant {
		return fmt.Errorf("%s %s cannot be shared: %s", c.Kind, c.Name, firstReason(c))
	}
	var paths []string
	var change func() error
	switch c.Kind {
	case registry.Skill:
		claudeDir, agentsDir := p.skillDirs(c.Name)
		paths = []string{claudeDir, agentsDir}
		change = func() error { return p.shareSkill(c) }
	case registry.MCP:
		paths = []string{p.codexConfig()}
		if c.Direction == compat.ToClaude {
			paths = []string{p.claudeJSON()}
		}
		change = func() error { return p.shareMCP(c) }
	case registry.Plugin:
		paths = []string{p.codexConfig()}
		change = func() error { return p.run("codex", "plugin", "marketplace", "add", c.Source) }
	default:
		return fmt.Errorf("unknown kind %q", c.Kind)
	}
	what := fmt.Sprintf("share %s %s with %s", c.Kind, c.Name, c.Direction.Target())
	return p.step(what, paths, change, c.Kind, c.Name, registry.Shared)
}

func firstReason(c compat.Candidate) string {
	for _, r := range c.Reasons {
		if r.Hard {
			return r.Text
		}
	}
	return string(c.Verdict)
}

func (p *Promoter) skillDirs(name string) (claudeDir, agentsDir string) {
	return filepath.Join(p.Dirs.Claude, "skills", name), filepath.Join(p.Dirs.Agents, "skills", name)
}

// shareSkill gives Codex a Claude skill (moved to ~/.agents/skills and
// linked back), or Claude a Codex skill (linked in).
func (p *Promoter) shareSkill(c compat.Candidate) error {
	claudeDir, agentsDir := p.skillDirs(c.Name)
	if c.Direction == compat.ToCodex {
		switch {
		case c.Installer == "notebooklm CLI" && onPath("notebooklm"):
			// Let the skill's own installer put it where Codex reads it,
			// so its later updates land in the shared copy.
			if err := p.run("notebooklm", "skill", "install", "--scope", "user", "--target", "agents"); err != nil {
				return err
			}
			if err := os.RemoveAll(claudeDir); err != nil {
				return err
			}
		case isDir(agentsDir):
			// Judged identical when scanned; a sync may have changed it since.
			if !compat.SameTree(claudeDir, agentsDir) {
				return fmt.Errorf("%s now differs from %s; run codeagent-sync scan again", agentsDir, claudeDir)
			}
			if err := os.RemoveAll(claudeDir); err != nil {
				return err
			}
		default:
			if err := move(claudeDir, agentsDir); err != nil {
				return err
			}
		}
		if compat.DisablesModelInvocation(agentsDir) {
			if err := userOnlyForCodex(agentsDir); err != nil {
				return err
			}
		}
	}
	return p.Engine.MakeLink(claudeDir, tools.Agents+"/skills/"+c.Name, true)
}

// userOnlyForCodex carries Claude's disable-model-invocation over to Codex,
// which reads it from agents/openai.yaml.
func userOnlyForCodex(skillDir string) error {
	p := filepath.Join(skillDir, "agents", "openai.yaml")
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return platform.WriteFileAtomic(p, []byte("policy:\n  allow_implicit_invocation: false\n"), 0o644)
}

// shareMCP adds an MCP server to the tool that lacks it, converted to that
// tool's form.
func (p *Promoter) shareMCP(c compat.Candidate) error {
	server, err := compat.Server(p.Dirs, c.Name, c.Direction)
	if err != nil {
		return err
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	if c.Direction == compat.ToCodex {
		text, reasons := compat.ClaudeToCodex(c.Name, server, exists)
		if hard := hardReason(reasons); hard != "" {
			return errors.New(hard)
		}
		return p.addItems(p.codexConfig(), structured.CodexConfig, []byte(text))
	}
	value, reasons := compat.CodexToClaude(c.Name, server, exists)
	if hard := hardReason(reasons); hard != "" {
		return errors.New(hard)
	}
	entry, err := json.Marshal(map[string]any{"mcpServers": map[string]any{c.Name: value}})
	if err != nil {
		return err
	}
	return p.addItems(p.claudeJSON(), structured.ClaudeMCP, entry)
}

func hardReason(reasons []compat.Reason) string {
	for _, r := range reasons {
		if r.Hard {
			return r.Text
		}
	}
	return ""
}

// addItems adds the items of a snippet to a structured file, leaving the
// rest of the file as it is.
func (p *Promoter) addItems(path, format string, snippet []byte) error {
	return p.editItems(path, format, func(items []structured.Item, f structured.Format) ([]structured.Item, error) {
		extra, err := f.Parse(snippet)
		if err != nil {
			return nil, err
		}
		return append(items, extra...), nil
	})
}

// editItems rewrites the synced items of a structured file.
func (p *Promoter) editItems(path, format string, edit func([]structured.Item, structured.Format) ([]structured.Item, error)) error {
	f, _ := structured.Get(format)
	current, err := os.ReadFile(path)
	mode := fs.FileMode(0o644)
	switch {
	case err == nil:
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
	case errors.Is(err, fs.ErrNotExist):
		current = nil
	default:
		return err
	}
	items, err := f.Project(current)
	if err != nil {
		return err
	}
	edited, err := edit(items, f)
	if err != nil {
		return err
	}
	out, err := f.Apply(current, edited, nil)
	if err != nil {
		return err
	}
	return platform.WriteFileAtomic(path, out, mode)
}

// Unshare takes something out of the shared layout, keeping it with one
// tool ("claude" or "codex").
func (p *Promoter) Unshare(kind, name, keepWith string) error {
	decision := registry.ClaudeOnly
	if keepWith == "codex" {
		decision = registry.CodexOnly
	} else if keepWith != "claude" {
		return errors.New("keep it with claude or codex")
	}
	var paths []string
	var change func() error
	switch kind {
	case registry.Skill:
		claudeDir, agentsDir := p.skillDirs(name)
		if _, _, isLink, _ := platform.ReadLink(claudeDir); !isLink {
			if _, err := os.Stat(filepath.Join(claudeDir, tools.LinkMarker)); err != nil {
				return fmt.Errorf("%s is not a shared skill (no link in %s)", name, claudeDir)
			}
		}
		paths = []string{claudeDir, agentsDir}
		change = func() error { return p.unshareSkill(name, keepWith) }
	case registry.MCP:
		paths = []string{p.codexConfig()}
		if keepWith == "codex" {
			paths = []string{p.claudeJSON()}
		}
		change = func() error { return p.unshareMCP(name, keepWith) }
	case registry.Plugin:
		paths = []string{p.codexConfig()}
		change = func() error { return p.run("codex", "plugin", "marketplace", "remove", name) }
		if keepWith == "codex" {
			paths = p.claudePluginFiles()
			change = func() error { return p.run("claude", "plugin", "marketplace", "remove", name) }
		}
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
	what := fmt.Sprintf("unshare %s %s, keeping it with %s", kind, name, keepWith)
	return p.step(what, paths, change, kind, name, decision)
}

// unshareSkill removes Claude's link; kept with Claude, the skill moves back
// into Claude's own directory.
func (p *Promoter) unshareSkill(name, keepWith string) error {
	claudeDir, agentsDir := p.skillDirs(name)
	if err := os.RemoveAll(claudeDir); err != nil {
		return err
	}
	if keepWith == "claude" {
		return move(agentsDir, claudeDir)
	}
	return nil
}

// unshareMCP removes the server from the other tool.
func (p *Promoter) unshareMCP(name, keepWith string) error {
	if keepWith == "claude" {
		prefix := "mcp_servers." + tomlKey(name)
		return p.editItems(filepath.Join(p.Dirs.Codex, "config.toml"), structured.CodexConfig, removeItems(func(n string) bool {
			return n == prefix || strings.HasPrefix(n, prefix+".")
		}))
	}
	return p.editItems(p.claudeJSON(), structured.ClaudeMCP, removeItems(func(n string) bool { return n == name }))
}

func removeItems(drop func(string) bool) func([]structured.Item, structured.Format) ([]structured.Item, error) {
	return func(items []structured.Item, _ structured.Format) ([]structured.Item, error) {
		var kept []structured.Item
		for _, it := range items {
			if !drop(it.Name) {
				kept = append(kept, it)
			}
		}
		return kept, nil
	}
}

func tomlKey(k string) string {
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			q, _ := json.Marshal(k)
			return string(q)
		}
	}
	return k
}

// move renames a directory, copying when the rename crosses file systems.
func move(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	if err := engine.CopyTree(from, to); err != nil {
		return err
	}
	return os.RemoveAll(from)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
