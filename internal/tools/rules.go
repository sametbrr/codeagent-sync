package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

// Rule files in the state directory. The shared one syncs, so every machine
// follows it; the local one stays on its machine and adds to it.
const (
	SharedRulesFile = "sync.yaml"
	LocalRulesFile  = "sync.local.yaml"
)

// Rules change what is synced: paths added (include) or left out (exclude),
// written as <root>/<pattern>, such as claude/plans/** or
// codex/agents/old-*.toml. An exclude may also name items of a settings
// file after a #: claude/settings.json#permissions,
// codex/config.toml#mcp_servers.*, claude-state/.claude.json#dokploy.
// Such an item keeps this machine's own version and is neither sent nor
// received; other machines keep theirs.
type Rules struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

// includable are the roots paths may be added to. The others are single
// files (.claude.json, skills-lock.json) or hold secrets (the state
// directory), so they only shrink.
var includable = map[string]bool{Claude: true, Codex: true, Agents: true}

// never lists what no rule can add: credentials, and sessions and history,
// which are per machine and grow without end.
var never = map[string][]string{
	Claude: {".credentials.json", "projects/**", "history.jsonl", "todos/**", "shell-snapshots/**", "statsig/**",
		"ide/**", "session-env/**", "file-history/**", "plugins/cache/**", "plugins/marketplaces/**"},
	Codex: {"auth.json", "sessions/**", "archived_sessions/**", "history.jsonl", "log/**", "logs*", "*.sqlite*",
		"cache/**", "tmp/**", ".tmp/**", "plugins/cache/**"},
}

// LoadRules reads the shared and the local rule files; a missing file has
// no rules.
func LoadRules(stateDir string) (shared, local Rules, err error) {
	if shared, err = readRules(filepath.Join(stateDir, SharedRulesFile)); err != nil {
		return
	}
	local, err = readRules(filepath.Join(stateDir, LocalRulesFile))
	return
}

func readRules(p string) (Rules, error) {
	var r Rules
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := yaml.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	return r, nil
}

// SaveRules writes a rule file.
func SaveRules(stateDir, name string, r Rules) error {
	data, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	header := "# What codeagent-sync syncs besides (include) or leaves out of (exclude) its defaults.\n" +
		"# Patterns are <root>/<glob>; roots: claude, codex, agents, claude-state, home, codeagent.\n" +
		"# An exclude can name settings items after #: claude/settings.json#permissions\n"
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return platform.WriteFileAtomic(filepath.Join(stateDir, name), append([]byte(header), data...), 0o644)
}

// Merge returns the rules of both.
func (r Rules) Merge(o Rules) Rules {
	return Rules{Include: append(append([]string{}, r.Include...), o.Include...), Exclude: append(append([]string{}, r.Exclude...), o.Exclude...)}
}

// CheckRule reports whether a pattern can be a rule.
func CheckRule(pattern string, include bool, roots []Root) error {
	file, items, hasItems := strings.Cut(pattern, "#")
	root, rel, ok := strings.Cut(file, "/")
	if !ok || rel == "" {
		return fmt.Errorf("%q: write it as <root>/<pattern>, such as claude/plans/**", pattern)
	}
	r, known := Find(roots, root)
	if !known {
		return fmt.Errorf("%q: no root %q (claude, codex, agents, claude-state, home, codeagent)", pattern, root)
	}
	if strings.HasPrefix(rel, "/") || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return fmt.Errorf("%q leaves its root", pattern)
	}
	if !doublestar.ValidatePattern(rel) {
		return fmt.Errorf("%q is not a valid pattern", pattern)
	}
	if hasItems {
		if include {
			return fmt.Errorf("%q: items can only be excluded", pattern)
		}
		if r.Structured[rel] == "" {
			return fmt.Errorf("%q: only settings files merged item by item have items (%s)", pattern, structuredFiles(roots))
		}
		if items == "" {
			return fmt.Errorf("%q: name the items after #", pattern)
		}
		if _, err := path.Match(items, ""); err != nil {
			return fmt.Errorf("%q: %w", pattern, err)
		}
	}
	if include && !includable[root] {
		return fmt.Errorf("%q: nothing can be added to %s, only left out", pattern, root)
	}
	if include {
		for _, n := range never[root] {
			if n == rel || strings.HasPrefix(rel, strings.TrimSuffix(n, "**")) && strings.HasSuffix(n, "/**") {
				return fmt.Errorf("%q is never synced (credentials, sessions or history)", pattern)
			}
		}
	}
	return nil
}

func structuredFiles(roots []Root) string {
	var out []string
	for _, r := range roots {
		for rel := range r.Structured {
			out = append(out, r.Name+"/"+rel)
		}
	}
	return strings.Join(out, ", ")
}

// WithRules returns the roots changed by the rules. Rules that cannot be
// applied are returned as errors and skipped: a rule a newer version wrote
// must not stop the sync.
func WithRules(roots []Root, r Rules) ([]Root, []error) {
	out := make([]Root, len(roots))
	for i, root := range roots {
		root.Include = append([]string{}, root.Include...)
		root.Exclude = append([]string{}, root.Exclude...)
		out[i] = root
	}
	var problems []error
	apply := func(pattern string, include bool) {
		if err := CheckRule(pattern, include, roots); err != nil {
			problems = append(problems, err)
			return
		}
		file, items, hasItems := strings.Cut(pattern, "#")
		name, rel, _ := strings.Cut(file, "/")
		for i := range out {
			if out[i].Name != name {
				continue
			}
			switch {
			case hasItems:
				if out[i].Hidden == nil {
					out[i].Hidden = map[string][]string{}
				}
				out[i].Hidden[rel] = append(out[i].Hidden[rel], items)
			case include:
				out[i].Include = append(out[i].Include, rel)
			default:
				out[i].Exclude = append(out[i].Exclude, rel)
			}
		}
	}
	for _, p := range r.Include {
		apply(p, true)
	}
	for _, p := range r.Exclude {
		apply(p, false)
	}
	for i := range out {
		out[i].Exclude = append(out[i].Exclude, never[out[i].Name]...)
	}
	return out, problems
}
