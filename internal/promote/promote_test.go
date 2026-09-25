package promote

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/compat"
	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/homepath"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

type fixture struct {
	t    *testing.T
	home string
	p    *Promoter
	ran  [][]string
}

func newFixture(t *testing.T) *fixture {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	home, _ := filepath.EvalSymlinks(t.TempDir())
	d := platform.ResolveDirs(home, func(string) string { return "" })
	reg, _ := registry.Load(d.State)
	f := &fixture{t: t, home: home}
	f.p = &Promoter{
		Engine: &engine.Engine{Roots: tools.Roots(d), Mapper: homepath.New(home, platform.Current()),
			OS: platform.Current(), StateDir: d.State},
		Dirs: d, Registry: reg,
		Run: func(name string, args ...string) error {
			f.ran = append(f.ran, append([]string{name}, args...))
			return nil
		},
	}
	return f
}

func (f *fixture) write(rel, content string) {
	p := filepath.Join(f.home, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) string {
	data, err := os.ReadFile(filepath.Join(f.home, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func (f *fixture) isLink(rel string) bool {
	fi, err := os.Lstat(filepath.Join(f.home, filepath.FromSlash(rel)))
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func (f *fixture) candidate(kind, name string) compat.Candidate {
	f.t.Helper()
	cands, err := compat.Scan(compat.Env{Dirs: f.p.Dirs, Registry: f.p.Registry})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range cands {
		if c.Kind == kind && c.Name == name {
			return c
		}
	}
	f.t.Fatalf("no candidate %s/%s", kind, name)
	return compat.Candidate{}
}

func TestShareAClaudeSkillAndUndo(t *testing.T) {
	f := newFixture(t)
	f.write(".claude/skills/commit/SKILL.md", "---\nname: commit\ndescription: x\ndisable-model-invocation: true\n---\nCommit.\n")
	f.write(".claude/skills/commit/scripts/run.sh", "#!/bin/sh\n")

	if err := f.p.Share(f.candidate(registry.Skill, "commit")); err != nil {
		t.Fatal(err)
	}
	if !f.isLink(".claude/skills/commit") || f.read(".claude/skills/commit/SKILL.md") == "" {
		t.Fatal("Claude does not reach the skill through a link")
	}
	if !strings.Contains(f.read(".agents/skills/commit/agents/openai.yaml"), "allow_implicit_invocation: false") {
		t.Error("the user-only setting did not carry over to Codex")
	}
	if f.p.Registry.Get(registry.Skill, "commit") != registry.Shared {
		t.Error("decision not recorded")
	}

	if _, _, err := f.p.Engine.Undo(""); err != nil {
		t.Fatal(err)
	}
	if f.isLink(".claude/skills/commit") || f.read(".claude/skills/commit/scripts/run.sh") != "#!/bin/sh\n" {
		t.Error("undo did not restore the original skill")
	}
	if _, err := os.Stat(filepath.Join(f.home, ".agents/skills/commit")); !os.IsNotExist(err) {
		t.Error("undo left the shared copy behind")
	}
	if reg, _ := registry.Load(f.p.Dirs.State); reg.Get(registry.Skill, "commit") != "" {
		t.Error("undo left the decision recorded")
	}
}

func TestAFailedShareRecordsNothing(t *testing.T) {
	f := newFixture(t)
	f.write(".claude/settings.json", `{"extraKnownMarketplaces": {"tools": {"source": {"source": "github", "repo": "someone/tools"}}}}`)
	f.p.Run = func(string, ...string) error { return os.ErrPermission }
	if err := f.p.Share(f.candidate(registry.Plugin, "tools")); err == nil {
		t.Fatal("the failure was not reported")
	}
	if reg, _ := registry.Load(f.p.Dirs.State); reg.Get(registry.Plugin, "tools") != "" {
		t.Error("a failed share was recorded as shared")
	}
	if list, _ := f.p.Engine.Backups(); len(list) != 0 {
		t.Errorf("a failed share left a backup for undo: %+v", list)
	}
}

func TestShareRefusesATwinThatChangedSinceTheScan(t *testing.T) {
	f := newFixture(t)
	f.write(".claude/skills/twin/SKILL.md", "---\nname: twin\ndescription: x\n---\nsame\n")
	f.write(".agents/skills/twin/SKILL.md", "---\nname: twin\ndescription: x\n---\nsame\n")
	c := f.candidate(registry.Skill, "twin")
	f.write(".agents/skills/twin/SKILL.md", "---\nname: twin\ndescription: x\n---\nchanged by a sync\n")
	if err := f.p.Share(c); err == nil {
		t.Fatal("shared over a different version")
	}
	if f.isLink(".claude/skills/twin") || !strings.Contains(f.read(".claude/skills/twin/SKILL.md"), "same") {
		t.Error("the Claude version was replaced")
	}
}

func TestShareMCPIntoALinkedConfigKeepsTheLink(t *testing.T) {
	f := newFixture(t)
	f.write(".claude.json", `{"mcpServers": {"docs": {"command": "npx", "args": ["-y", "docs-mcp"]}}}`)
	f.write("dotfiles/codex-config.toml", "model = \"gpt-5.5\"\n")
	os.MkdirAll(filepath.Join(f.home, ".codex"), 0o755)
	if err := os.Symlink("../dotfiles/codex-config.toml", filepath.Join(f.home, ".codex/config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Share(f.candidate(registry.MCP, "docs")); err != nil {
		t.Fatal(err)
	}
	if !f.isLink(".codex/config.toml") || !strings.Contains(f.read("dotfiles/codex-config.toml"), "[mcp_servers.docs]") {
		t.Errorf("the link was replaced, or the target not written:\n%s", f.read(".codex/config.toml"))
	}
	if _, _, err := f.p.Engine.Undo(""); err != nil {
		t.Fatal(err)
	}
	if !f.isLink(".codex/config.toml") || strings.Contains(f.read("dotfiles/codex-config.toml"), "docs") {
		t.Errorf("undo did not restore the target:\n%s", f.read("dotfiles/codex-config.toml"))
	}
}

func TestShareAnIdenticalTwinAndACodexSkill(t *testing.T) {
	f := newFixture(t)
	f.write(".claude/skills/twin/SKILL.md", "---\nname: twin\ndescription: x\n---\nsame\n")
	f.write(".agents/skills/twin/SKILL.md", "---\nname: twin\ndescription: x\n---\nsame\n")
	f.write(".agents/skills/codexy/SKILL.md", "---\nname: codexy\ndescription: x\n---\nportable\n")

	for _, name := range []string{"twin", "codexy"} {
		if err := f.p.Share(f.candidate(registry.Skill, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !f.isLink(".claude/skills/" + name) {
			t.Errorf("%s is not linked into Claude", name)
		}
	}
}

func TestShareMCPBothWays(t *testing.T) {
	f := newFixture(t)
	f.write(".claude.json", `{"numStartups": 3, "mcpServers": {"codegraph": {"type": "stdio", "command": "codegraph", "args": ["serve", "--mcp"]}}}`)
	f.write(".codex/config.toml", "# mine\nmodel = \"gpt-5.5\"\n\n[mcp_servers.openaiDeveloperDocs]\nurl = \"https://developers.openai.com/mcp\"\n")

	if err := f.p.Share(f.candidate(registry.MCP, "codegraph")); err != nil {
		t.Fatal(err)
	}
	cfg := f.read(".codex/config.toml")
	if !strings.Contains(cfg, "[mcp_servers.codegraph]") || !strings.HasPrefix(cfg, "# mine\nmodel = \"gpt-5.5\"\n") {
		t.Errorf("config.toml:\n%s", cfg)
	}

	if err := f.p.Share(f.candidate(registry.MCP, "openaiDeveloperDocs")); err != nil {
		t.Fatal(err)
	}
	cj := f.read(".claude.json")
	if !strings.Contains(cj, `"openaiDeveloperDocs"`) || !strings.Contains(cj, `"numStartups": 3`) || !strings.Contains(cj, `"type": "http"`) {
		t.Errorf(".claude.json:\n%s", cj)
	}

	if err := f.p.Unshare(registry.MCP, "codegraph", "claude"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.read(".codex/config.toml"), "codegraph") || f.p.Registry.Get(registry.MCP, "codegraph") != registry.ClaudeOnly {
		t.Errorf("unshare left codegraph in Codex:\n%s", f.read(".codex/config.toml"))
	}
}

func TestUnshareASkill(t *testing.T) {
	for _, keep := range []string{"claude", "codex"} {
		f := newFixture(t)
		f.write(".agents/skills/foo/SKILL.md", "---\nname: foo\ndescription: x\n---\nfoo\n")
		os.MkdirAll(filepath.Join(f.home, ".claude/skills"), 0o755)
		os.Symlink("../../.agents/skills/foo", filepath.Join(f.home, ".claude/skills/foo"))

		if err := f.p.Unshare(registry.Skill, "foo", keep); err != nil {
			t.Fatal(err)
		}
		_, agentsErr := os.Stat(filepath.Join(f.home, ".agents/skills/foo"))
		_, claudeErr := os.Lstat(filepath.Join(f.home, ".claude/skills/foo"))
		if keep == "claude" && (agentsErr == nil || claudeErr != nil || f.isLink(".claude/skills/foo")) {
			t.Errorf("keep with Claude: the skill should be a plain Claude directory only")
		}
		if keep == "codex" && (agentsErr != nil || claudeErr == nil) {
			t.Errorf("keep with Codex: the link should be gone and the Codex copy stay")
		}
	}
}

func TestSharePluginRunsCodexAndRefusesBlocked(t *testing.T) {
	f := newFixture(t)
	f.write(".claude/settings.json", `{"extraKnownMarketplaces": {
  "skill-hub": {"source": {"source": "github", "repo": "sametbrr/skill-hub"}},
  "claude-plugins-official": {"source": {"source": "github", "repo": "anthropics/claude-plugins-official"}}}}`)
	if err := f.p.Share(f.candidate(registry.Plugin, "skill-hub")); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 1 || strings.Join(f.ran[0], " ") != "codex plugin marketplace add sametbrr/skill-hub" {
		t.Errorf("ran %v", f.ran)
	}
	if err := f.p.Share(f.candidate(registry.Plugin, "claude-plugins-official")); err == nil {
		t.Error("a blocked marketplace was shared")
	}
}
