package tools

import (
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

func TestCheckRule(t *testing.T) {
	roots := Roots(platform.ResolveDirs("/home/u", func(string) string { return "" }))
	for pattern, include := range map[string]bool{
		"claude/plans/**":                   true,
		"codex/agents/old-*.toml":           false,
		"claude-state/.claude.json":         false,
		"claude/settings.json#permissions":  false,
		"codex/config.toml#mcp_servers.*":   false,
		"claude-state/.claude.json#dokploy": false,
	} {
		if err := CheckRule(pattern, include, roots); err != nil {
			t.Errorf("%s: %v", pattern, err)
		}
	}
	for pattern, include := range map[string]bool{
		"plans/**":                   true,  // no root
		"nosuch/x":                   false, // unknown root
		"claude/../.ssh/**":          true,  // leaves its root
		"home/**":                    true,  // nothing can be added to home
		"codeagent/config.yaml":      true,  // nor to the state directory
		"claude/projects/**":         true,  // sessions
		"codex/auth.json":            true,  // credentials
		"claude/CLAUDE.md#x":         false, // not a settings file
		"claude/settings.json#model": true,  // items only excluded
	} {
		if err := CheckRule(pattern, include, roots); err == nil {
			t.Errorf("%s was accepted", pattern)
		}
	}
}

func TestWithRules(t *testing.T) {
	roots := Roots(platform.ResolveDirs("/home/u", func(string) string { return "" }))
	out, problems := WithRules(roots, Rules{
		Include: []string{"claude/plans/**", "home/**"},
		Exclude: []string{"claude/look-again/**", "claude/settings.json#permissions"},
	})
	if len(problems) != 1 {
		t.Errorf("problems = %v, want the home/** one", problems)
	}
	claude, _ := Find(out, Claude)
	if !claude.Includes("plans/a.md") || claude.Includes("look-again/x.md") || claude.Includes("projects/p/s.jsonl") {
		t.Error("claude root does not follow the rules")
	}
	if got := claude.HiddenItems("settings.json"); len(got) != 1 || got[0] != "permissions" {
		t.Errorf("hidden = %v", got)
	}
	if orig, _ := Find(roots, Claude); orig.Includes("plans/a.md") {
		t.Error("WithRules changed the roots it was given")
	}
}
