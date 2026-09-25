package compat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
)

// marketplaces reads the plugin marketplaces each tool knows, as
// name -> owner/repo. Claude Code keeps the ones added with
// "claude plugin marketplace add" in plugins/known_marketplaces.json and the
// declared ones in settings.json.
func marketplaces(d platform.Dirs) (claude, codex map[string]string) {
	type source struct {
		Repo string `json:"repo"`
		URL  string `json:"url"`
	}
	claude = map[string]string{}
	if data, err := os.ReadFile(filepath.Join(d.Claude, "plugins", "known_marketplaces.json")); err == nil {
		var known map[string]struct {
			Source source `json:"source"`
		}
		if json.Unmarshal(data, &known) == nil {
			for name, m := range known {
				claude[name] = normalizeSource(m.Source.Repo + m.Source.URL)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(d.Claude, "settings.json")); err == nil {
		var doc struct {
			Marketplaces map[string]struct {
				Source source `json:"source"`
			} `json:"extraKnownMarketplaces"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for name, m := range doc.Marketplaces {
				claude[name] = normalizeSource(m.Source.Repo + m.Source.URL)
			}
		}
	}
	codex = map[string]string{}
	if data, err := os.ReadFile(filepath.Join(d.Codex, "config.toml")); err == nil {
		var doc struct {
			Marketplaces map[string]struct {
				Source string `toml:"source"`
			} `toml:"marketplaces"`
		}
		if toml.Unmarshal(data, &doc) == nil {
			for name, m := range doc.Marketplaces {
				codex[name] = normalizeSource(m.Source)
			}
		}
	}
	return claude, codex
}

// HasMarketplace reports whether either tool knows a plugin marketplace.
func HasMarketplace(d platform.Dirs, name string) bool {
	claude, codex := marketplaces(d)
	_, inClaude := claude[name]
	_, inCodex := codex[name]
	return inClaude || inCodex
}

// pluginCandidates finds plugin marketplaces only one tool knows. Codex can
// read Claude Code's marketplaces (their .claude-plugin/marketplace.json);
// Claude Code cannot read Codex's.
func pluginCandidates(env Env) ([]Candidate, error) {
	claude, codex := marketplaces(env.Dirs)
	codexSources := map[string]bool{}
	for _, src := range codex {
		codexSources[src] = true
	}

	var out []Candidate
	for _, name := range sortedKeys(claude) {
		src := claude[name]
		if _, ok := codex[name]; ok || codexSources[src] || env.Registry.Get(registry.Plugin, name) != "" || src == "" {
			continue
		}
		c := Candidate{Kind: registry.Plugin, Name: name, Direction: ToCodex, Source: src, Suggest: "codex plugin marketplace add " + src}
		repo := src[strings.LastIndex(src, "/")+1:]
		if strings.HasPrefix(src, "anthropics/") {
			c.Reasons = []Reason{{Hard: true, Text: "Anthropic's plugins are built for Claude Code"}}
		} else if strings.HasSuffix(repo, "-cc") || strings.Contains(strings.ToLower(repo), "claude") {
			c.Reasons = []Reason{{Hard: true, Text: "built for Claude Code, by its name (" + repo + ")"}}
		} else {
			c.Reasons = []Reason{{Text: "Codex reads Claude plugin marketplaces, but not every plugin works there; try it"}}
		}
		c.Verdict = verdictOf(c.Reasons)
		out = append(out, c)
	}
	for _, name := range sortedKeys(codex) {
		if _, ok := claude[name]; ok || env.Registry.Get(registry.Plugin, name) != "" {
			continue
		}
		out = append(out, Candidate{Kind: registry.Plugin, Name: name, Direction: ToClaude, Source: codex[name], Verdict: Blocked,
			Reasons: []Reason{{Hard: true, Text: "Claude Code reads only its own plugin marketplaces"}}})
	}
	return out, nil
}

// normalizeSource turns a GitHub URL into owner/repo.
func normalizeSource(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		if strings.HasPrefix(s, prefix) {
			return strings.TrimSuffix(strings.TrimPrefix(s, prefix), ".git")
		}
	}
	return s
}
