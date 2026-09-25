// Package tools describes what codeagent-sync syncs: the roots (directories
// of the agent tools) and, inside each root, the paths that belong to the
// shared configuration. Everything else in those directories — sessions,
// credentials, caches, machine state — is never read.
package tools

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/structured"
)

// Root is a directory whose selected contents are synced.
type Root struct {
	// Name identifies the root on every machine and prefixes its remote keys.
	// It never depends on where the directory is (CLAUDE_CONFIG_DIR and
	// CODEX_HOME can move them).
	Name string
	// Dir is the root's local directory.
	Dir string
	// Include lists doublestar patterns, relative to Dir, of what is synced.
	// Only the static prefix of each pattern is walked.
	Include []string
	// Exclude lists patterns that are never synced, even when included.
	Exclude []string
	// Structured maps files that are merged item by item to their format
	// (see package structured); all other files sync whole.
	Structured map[string]string
	// Needs names the root whose directory must exist for this root to be
	// synced: a tool that is not installed on a machine is left alone there.
	Needs string
	// Hidden maps structured files to patterns of items that keep this
	// machine's own version (set by the rules; see Rules).
	Hidden map[string][]string
}

// Root names.
const (
	Claude      = "claude"       // ~/.claude
	ClaudeState = "claude-state" // the directory holding .claude.json
	Codex       = "codex"        // ~/.codex
	Agents      = "agents"       // ~/.agents, shared skills
	Home        = "home"         // single files directly in the home directory
	Codeagent   = "codeagent"    // codeagent-sync's own shared decisions
)

// LinkMarker is the file that turns a directory into a managed copy of a
// link, for machines where links cannot be created (Windows without
// Developer Mode). It holds the link target.
const LinkMarker = ".codeagent-link"

// commonExclude is never synced from any root: OS and editor litter, our own
// temporary and conflict files, and the marker of a managed link copy.
var commonExclude = []string{
	"**/.DS_Store",
	"**/Thumbs.db",
	"**/desktop.ini",
	"**/*.swp",
	"**/*~",
	"**/.#*",
	"**/.*.tmp-*",
	"**/*.conflict.*",
	"**/" + LinkMarker,
}

// IsLitter reports whether a file name is operating-system or editor
// litter, or a leftover temporary file of ours. A directory holding nothing
// else counts as empty. Only these names qualify — a root's exclude patterns
// also cover real data that is merely not synced (such as skills/synced).
func IsLitter(name string) bool {
	switch name {
	case ".DS_Store", "Thumbs.db", "desktop.ini":
		return true
	}
	return strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-")
}

// Roots returns the roots for a machine's directories.
func Roots(d platform.Dirs) []Root {
	return []Root{
		{
			Name: Claude,
			Dir:  d.Claude,
			Include: []string{
				"CLAUDE.md",
				"settings.json",
				"agents/**",
				"commands/**",
				"hooks/**",
				"skills/**",
				"look-again/**",
				"plugins/installed_plugins.json",
				"plugins/known_marketplaces.json",
				"statusline.sh",
			},
			// Skills the Claude app syncs by itself.
			Exclude:    append([]string{"skills/synced/**"}, commonExclude...),
			Structured: map[string]string{"settings.json": structured.JSONKeys},
			Needs:      Claude,
		},
		{
			// Only the MCP servers of .claude.json; the rest of it is
			// Claude Code's own state for this machine.
			Name:       ClaudeState,
			Dir:        filepath.Dir(d.ClaudeJSON),
			Include:    []string{".claude.json"},
			Exclude:    commonExclude,
			Structured: map[string]string{".claude.json": structured.ClaudeMCP},
			Needs:      Claude,
		},
		{
			Name:       Codex,
			Dir:        d.Codex,
			Include:    []string{"AGENTS.md", "hooks.json", "config.toml", "agents/**"},
			Exclude:    commonExclude,
			Structured: map[string]string{"config.toml": structured.CodexConfig},
			Needs:      Codex,
		},
		{
			Name:    Agents,
			Dir:     d.Agents,
			Include: []string{"skills/**"},
			Exclude: commonExclude,
		},
		{
			Name:    Home,
			Dir:     d.Home,
			Include: []string{"skills-lock.json"},
			Exclude: commonExclude,
		},
		{
			// Only the decisions about what is shared; the configuration,
			// keys and state next to it never leave the machine.
			Name:       Codeagent,
			Dir:        d.State,
			Include:    []string{"registry.yaml", SharedRulesFile, "machines/*.yaml"},
			Exclude:    commonExclude,
			Structured: map[string]string{"registry.yaml": structured.Decisions},
		},
	}
}

// Active returns the roots synced on this machine: those of tools that are
// not installed are left out.
func Active(roots []Root, dirExists func(string) bool) []Root {
	var out []Root
	for _, r := range roots {
		if r.Needs != "" {
			if needed, ok := Find(roots, r.Needs); !ok || !dirExists(needed.Dir) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// Format returns the structured format of rel in root, if it has one.
func (r Root) Format(rel string) string { return r.Structured[rel] }

// HiddenItems returns the patterns of items of rel that keep this machine's
// own version.
func (r Root) HiddenItems(rel string) []string { return r.Hidden[rel] }

// Find returns the root named name.
func Find(roots []Root, name string) (Root, bool) {
	for _, r := range roots {
		if r.Name == name {
			return r, true
		}
	}
	return Root{}, false
}

// Owner returns the root that contains the absolute path p, and p's
// slash-separated path relative to it. Roots nest (the home root contains
// all others), so the root with the longest directory wins; roots sharing a
// directory are told apart by which one includes the path.
func Owner(roots []Root, p string) (Root, string, bool) {
	var best Root
	var bestRel string
	found, bestIncludes := false, false
	for _, r := range roots {
		rel, err := filepath.Rel(r.Dir, p)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		rel = filepath.ToSlash(rel)
		includes := r.Includes(rel)
		switch {
		case !found, len(r.Dir) > len(best.Dir), len(r.Dir) == len(best.Dir) && includes && !bestIncludes:
			best, bestRel, found, bestIncludes = r, rel, true, includes
		}
	}
	return best, bestRel, found
}

// Includes reports whether rel, a slash-separated path relative to the
// root, is synced.
func (r Root) Includes(rel string) bool {
	return matchAny(r.Include, rel) && !r.Excludes(rel)
}

// Excludes reports whether rel matches an exclude pattern. For a directory
// this means nothing below it is synced.
func (r Root) Excludes(rel string) bool {
	return matchAny(r.Exclude, rel)
}

// WalkPrefixes returns the static prefixes of the include patterns: the
// only paths below the root that are visited.
func (r Root) WalkPrefixes() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range r.Include {
		prefix := staticPrefix(p)
		if !seen[prefix] {
			seen[prefix] = true
			out = append(out, prefix)
		}
	}
	return out
}

func staticPrefix(pattern string) string {
	var kept []string
	for _, seg := range strings.Split(pattern, "/") {
		if strings.ContainsAny(seg, `*?[{\`) {
			break
		}
		kept = append(kept, seg)
	}
	return strings.Join(kept, "/")
}

func matchAny(patterns []string, rel string) bool {
	for _, p := range patterns {
		if ok, _ := doublestar.Match(p, rel); ok {
			return true
		}
	}
	return false
}
