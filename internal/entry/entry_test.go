package entry

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/homepath"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

type testHome struct {
	t    *testing.T
	dir  string
	dirs platform.Dirs
}

func newTestHome(t *testing.T) *testHome {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &testHome{t: t, dir: dir, dirs: platform.ResolveDirs(dir, func(string) string { return "" })}
}

func (h *testHome) write(rel, content string, perm os.FileMode) {
	h.t.Helper()
	p := filepath.Join(h.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil {
		h.t.Fatal(err)
	}
}

func (h *testHome) symlink(rel, target string) {
	h.t.Helper()
	p := filepath.Join(h.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Symlink(filepath.FromSlash(target), p); err != nil {
		h.t.Fatal(err)
	}
}

func (h *testHome) scan() ([]Entry, []Problem) {
	h.t.Helper()
	s := &Scanner{
		Roots:  tools.Roots(h.dirs),
		Mapper: homepath.New(h.dir, platform.Current()),
		OS:     platform.Current(),
	}
	entries, problems, err := s.Scan()
	if err != nil {
		h.t.Fatal(err)
	}
	return entries, problems
}

func byKey(entries []Entry) map[string]Entry {
	m := map[string]Entry{}
	for _, e := range entries {
		m[e.Key()] = e
	}
	return m
}

func keysOf(entries []Entry) []string {
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key())
	}
	return keys
}

func problemKeys(problems []Problem) []string {
	var keys []string
	for _, p := range problems {
		keys = append(keys, p.Key)
	}
	return keys
}

func TestScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	h := newTestHome(t)
	h.write(".claude/CLAUDE.md", "wiki: "+h.dir+"/wiki\nuses $HOME too\n", 0o644)
	h.write(".claude/skills/own/SKILL.md", "own skill", 0o644)
	h.write(".claude/skills/own/run.sh", "#!/bin/sh\n", 0o755)
	h.write(".claude/skills/own/icon.bin", "\x89PNG\x00"+h.dir, 0o644)
	h.write(".agents/skills/shared/SKILL.md", "shared skill", 0o644)
	h.symlink(".claude/skills/shared", "../../.agents/skills/shared")
	h.symlink(".claude/skills/pending", "../../.agents/skills/not-yet")
	h.symlink(".claude/skills/escape", "/etc")
	h.write(".claude/skills/copied/"+tools.LinkMarker, "agents/skills/shared\n", 0o644)
	h.write(".claude/skills/copied/SKILL.md", "copy of shared", 0o644)

	// Never synced.
	h.write(".claude/projects/-home/session.jsonl", "{}", 0o644)
	h.write(".claude/settings.local.json", "{}", 0o644)
	h.write(".claude/skills/synced/uuid/x/SKILL.md", "app-synced", 0o644)
	h.write(".claude/skills/own/.DS_Store", "junk", 0o644)
	h.write(".codex/auth.json", "secret", 0o600)
	h.write(".codex/AGENTS.md", "rules", 0o644)
	h.write("skills-lock.json", "{}", 0o644)
	h.write("unrelated.txt", "not ours", 0o644)

	entries, problems := h.scan()

	wantKeys := []string{
		"v1/agents/skills/shared/SKILL.md",
		"v1/claude/CLAUDE.md",
		"v1/claude/skills/copied",
		"v1/claude/skills/own/SKILL.md",
		"v1/claude/skills/own/icon.bin",
		"v1/claude/skills/own/run.sh",
		"v1/claude/skills/pending",
		"v1/claude/skills/shared",
		"v1/codex/AGENTS.md",
		"v1/home/skills-lock.json",
	}
	if got := keysOf(entries); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("keys = %q\nwant   %q", got, wantKeys)
	}
	if got := problemKeys(problems); !reflect.DeepEqual(got, []string{"v1/claude/skills/escape"}) {
		t.Errorf("problems = %+v, want only the link that escapes the synced paths", problems)
	}

	m := byKey(entries)
	if got := string(m["v1/claude/CLAUDE.md"].Content); got != "wiki: "+homepath.Placeholder+"/wiki\nuses $HOME too\n" {
		t.Errorf("CLAUDE.md content = %q; the home directory should be a placeholder and $HOME untouched", got)
	}
	if got := string(m["v1/claude/skills/own/icon.bin"].Content); !strings.Contains(got, h.dir) {
		t.Errorf("binary content was translated: %q", got)
	}
	if !m["v1/claude/skills/own/run.sh"].Executable || m["v1/claude/skills/own/SKILL.md"].Executable {
		t.Error("execute bits were not read correctly")
	}
	if e := m["v1/claude/CLAUDE.md"]; e.Hash != envelope.Hash(e.Content) || e.Kind != envelope.KindFile {
		t.Errorf("file entry = %+v", e)
	}

	for key, want := range map[string]Entry{
		"v1/claude/skills/shared":  {Target: "agents/skills/shared", TargetIsDir: true},
		"v1/claude/skills/pending": {Target: "agents/skills/not-yet", TargetIsDir: false},
		"v1/claude/skills/copied":  {Target: "agents/skills/shared", TargetIsDir: true},
	} {
		got := m[key]
		if got.Kind != envelope.KindLink || got.Target != want.Target || got.TargetIsDir != want.TargetIsDir {
			t.Errorf("%s = %+v, want a link to %s (dir %v)", key, got, want.Target, want.TargetIsDir)
		}
	}
}

// When the tool directory itself is a symlink (dotfile managers do this),
// entries and link targets are still found under the right roots.
func TestScanWithSymlinkedToolDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	h := newTestHome(t)
	h.write("dotfiles/claude/skills/own/SKILL.md", "own", 0o644)
	h.write(".agents/skills/shared/SKILL.md", "shared", 0o644)
	h.symlink(".claude", "dotfiles/claude")
	h.symlink("dotfiles/claude/skills/shared", "../../../.agents/skills/shared")

	entries, problems := h.scan()
	m := byKey(entries)
	if _, ok := m["v1/claude/skills/own/SKILL.md"]; !ok {
		t.Errorf("file under the symlinked tool dir not found: %q", keysOf(entries))
	}
	if got := m["v1/claude/skills/shared"]; got.Kind != envelope.KindLink || got.Target != "agents/skills/shared" {
		t.Errorf("shared skill link = %+v, problems %+v", got, problems)
	}
}

func TestScanSkipsFilesOverTheSizeLimit(t *testing.T) {
	h := newTestHome(t)
	h.write(".claude/skills/big/data.txt", strings.Repeat("x", 100), 0o644)
	h.write(".claude/skills/big/SKILL.md", "small", 0o644)

	s := &Scanner{Roots: tools.Roots(h.dirs), Mapper: homepath.New(h.dir, platform.Current()), OS: platform.Current(), MaxSize: 50}
	entries, problems, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(entries); !reflect.DeepEqual(got, []string{"v1/claude/skills/big/SKILL.md"}) {
		t.Errorf("keys = %q", got)
	}
	if len(problems) != 1 || problems[0].Key != "v1/claude/skills/big/data.txt" {
		t.Errorf("problems = %+v", problems)
	}
}

func TestScanOfAnEmptyHome(t *testing.T) {
	entries, problems := newTestHome(t).scan()
	if len(entries) != 0 || len(problems) != 0 {
		t.Errorf("empty home gave %d entries and %+v", len(entries), problems)
	}
}

func TestDropCaseCollisions(t *testing.T) {
	entries := []Entry{
		{Root: "agents", Rel: "skills/Foo/SKILL.md"},
		{Root: "agents", Rel: "skills/foo/SKILL.md"},
		{Root: "agents", Rel: "skills/foo/ref.md"},
		{Root: "agents", Rel: "skills/bar/SKILL.md"},
	}
	kept, problems := DropCaseCollisions(entries)
	if got := keysOf(kept); !reflect.DeepEqual(got, []string{"v1/agents/skills/bar/SKILL.md"}) {
		t.Errorf("kept = %q", got)
	}
	if len(problems) != 3 {
		t.Errorf("problems = %+v, want the three entries under the colliding directories", problems)
	}
}

func TestParseKey(t *testing.T) {
	tests := []struct {
		key, root, rel string
		ok             bool
	}{
		{"v1/claude/skills/foo/SKILL.md", "claude", "skills/foo/SKILL.md", true},
		{"v1/home/skills-lock.json", "home", "skills-lock.json", true},
		{"v1/_meta/kdf.json", "", "", false},
		{"v1/claude", "", "", false},
		{"claude/CLAUDE.md", "", "", false},
	}
	for _, tc := range tests {
		root, rel, ok := ParseKey(tc.key)
		if root != tc.root || rel != tc.rel || ok != tc.ok {
			t.Errorf("ParseKey(%q) = %q, %q, %v", tc.key, root, rel, ok)
		}
		if ok && Key(root, rel) != tc.key {
			t.Errorf("Key(%q, %q) does not rebuild %q", root, rel, tc.key)
		}
	}
}
