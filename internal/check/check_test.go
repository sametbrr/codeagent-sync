package check

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

func TestRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	home := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(home, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	link := func(rel, target string) {
		p := filepath.Join(home, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.Symlink(target, p)
	}
	write(".agents/skills/shared/SKILL.md", "ok")
	link(".claude/skills/shared", "../../.agents/skills/shared")        // fine
	link(".claude/skills/gone", "../../.agents/skills/gone")            // broken
	write(".claude/skills/impeccable/SKILL.md", "claude variant")       // variant, marked
	write(".agents/skills/impeccable/SKILL.md", "codex variant")        //
	write(".claude/skills/twice/SKILL.md", "one")                       // duplicate, not marked
	write(".agents/skills/twice/SKILL.md", "two")                       //
	write(".agents/skills/real/notes.md", "no SKILL.md")                // missing SKILL.md
	write(".agents/skills/linked-md/target.md", "x")                    //
	link(".agents/skills/linked-md/SKILL.md", "target.md")              // Codex ignores it
	write(".codex/agents/Frontend_Dev.md", "---\nname: x\n---\n")       // wrong format
	write(".claude/skills/synced/uuid/whatever/notes.md", "app skills") // ignored

	d := platform.ResolveDirs(home, func(string) string { return "" })
	got := map[string]string{}
	for _, f := range Run(d, map[string]bool{"impeccable": true}) {
		rel, _ := filepath.Rel(home, f.Path)
		got[filepath.ToSlash(rel)] = f.Reason
	}
	want := map[string]string{
		".claude/skills/gone":               "does not exist",
		".claude/skills/twice":              "exists separately",
		".agents/skills/real":               "no SKILL.md",
		".agents/skills/linked-md/SKILL.md": "symlink",
		".codex/agents/Frontend_Dev.md":     ".toml",
	}
	for path, reason := range want {
		if !strings.Contains(got[path], reason) {
			t.Errorf("%s: finding %q, want one mentioning %q", path, got[path], reason)
		}
	}
	if len(got) != len(want) {
		t.Errorf("findings = %v", got)
	}
}
