package tools

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

func testRoots() []Root {
	home := filepath.FromSlash("/home/ad")
	return Roots(platform.ResolveDirs(home, func(string) string { return "" }))
}

func root(t *testing.T, name string) Root {
	t.Helper()
	r, ok := Find(testRoots(), name)
	if !ok {
		t.Fatalf("no root %q", name)
	}
	return r
}

func TestIncludes(t *testing.T) {
	claude := root(t, Claude)
	synced := []string{
		"CLAUDE.md",
		"skills/impeccable/SKILL.md",
		"skills/impeccable/reference/adapt.md",
		"agents/impeccable-documenter.md",
		"hooks/design-mode.js",
		"look-again/2026-09-24-1948-plan.md",
		"plugins/installed_plugins.json",
		"statusline.sh",
	}
	notSynced := []string{
		"projects/-Users-ad/abc.jsonl",
		"history.jsonl",
		"settings.local.json",
		".credentials.json",
		".design-mode",
		"sessions/123.json",
		"plugins/cache/x/plugin.json",
		"skills/synced/uuid/notebooklm/SKILL.md",
		"skills/foo/.DS_Store",
		"skills/foo/.SKILL.md.tmp-123",
		"skills/foo/SKILL.md.conflict.20260925",
		"skills/foo/" + LinkMarker,
	}
	for _, rel := range synced {
		if !claude.Includes(rel) {
			t.Errorf("claude root should sync %q", rel)
		}
	}
	for _, rel := range notSynced {
		if claude.Includes(rel) {
			t.Errorf("claude root should not sync %q", rel)
		}
	}

	codex := root(t, Codex)
	for _, rel := range []string{"auth.json", "sessions/2026/09/25/rollout-x.jsonl", "state_5.sqlite", "skills/.system/imagegen/SKILL.md", "installation_id"} {
		if codex.Includes(rel) {
			t.Errorf("codex root should not sync %q", rel)
		}
	}
	for _, rel := range []string{"AGENTS.md", "hooks.json", "config.toml", "agents/reviewer.toml"} {
		if !codex.Includes(rel) {
			t.Errorf("codex root should sync %q", rel)
		}
	}
}

func TestExcludedDirectoriesArePruned(t *testing.T) {
	if !root(t, Claude).Excludes("skills/synced") {
		t.Error("skills/synced should be excluded as a directory")
	}
}

func TestWalkPrefixes(t *testing.T) {
	got := root(t, Claude).WalkPrefixes()
	want := []string{"CLAUDE.md", "settings.json", "agents", "commands", "hooks", "skills", "look-again",
		"plugins/installed_plugins.json", "plugins/known_marketplaces.json", "statusline.sh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WalkPrefixes() = %q, want %q", got, want)
	}
	if got := root(t, Home).WalkPrefixes(); !reflect.DeepEqual(got, []string{"skills-lock.json"}) {
		t.Errorf("home root walks %q; it must only visit skills-lock.json", got)
	}
}

// The home root contains every other root; a path belongs to the most
// specific one.
func TestOwnerPicksTheMostSpecificRoot(t *testing.T) {
	roots := testRoots()
	tests := []struct {
		path, root, rel string
		ok              bool
	}{
		{"/home/ad/.agents/skills/foo", Agents, "skills/foo", true},
		{"/home/ad/.claude/skills/foo", Claude, "skills/foo", true},
		{"/home/ad/skills-lock.json", Home, "skills-lock.json", true},
		{"/etc/passwd", "", "", false},
		{"/home/adam/x", "", "", false},
	}
	for _, tc := range tests {
		r, rel, ok := Owner(roots, filepath.FromSlash(tc.path))
		if ok != tc.ok || r.Name != tc.root || rel != tc.rel {
			t.Errorf("Owner(%q) = %q, %q, %v; want %q, %q, %v", tc.path, r.Name, rel, ok, tc.root, tc.rel, tc.ok)
		}
	}

	// A path inside the home directory that no root syncs is found but not
	// included, whichever of the roots sharing that directory is returned.
	r, rel, ok := Owner(roots, filepath.FromSlash("/home/ad/.agents"))
	if !ok || r.Includes(rel) {
		t.Errorf("Owner(~/.agents) = %q, %q, %v; want a root that does not include it", r.Name, rel, ok)
	}
}

func TestRootsFollowConfigDirOverrides(t *testing.T) {
	home := filepath.FromSlash("/home/ad")
	env := map[string]string{"CLAUDE_CONFIG_DIR": "/data/claude", "CODEX_HOME": "/data/codex"}
	roots := Roots(platform.ResolveDirs(home, func(k string) string { return env[k] }))

	for name, want := range map[string]string{
		Claude:      "/data/claude",
		ClaudeState: "/data/claude",
		Codex:       "/data/codex",
		Agents:      "/home/ad/.agents",
	} {
		r, _ := Find(roots, name)
		if r.Dir != filepath.FromSlash(want) {
			t.Errorf("%s root dir = %q, want %q", name, r.Dir, want)
		}
	}
}
