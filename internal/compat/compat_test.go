package compat

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
)

type fixture struct {
	t    *testing.T
	home string
	dirs platform.Dirs
}

func newFixture(t *testing.T) *fixture {
	home := t.TempDir()
	return &fixture{t: t, home: home, dirs: platform.ResolveDirs(home, func(string) string { return "" })}
}

func (f *fixture) write(rel, content string) {
	p := filepath.Join(f.home, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) scan(reg *registry.Registry) map[string]Candidate {
	if reg == nil {
		reg, _ = registry.Load(f.t.TempDir())
	}
	cands, err := Scan(Env{Dirs: f.dirs, Registry: reg, Exists: func(p string) bool { _, e := os.Stat(p); return e == nil }})
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]Candidate{}
	for _, c := range cands {
		out[c.Kind+"/"+c.Name] = c
	}
	return out
}

const skillHead = "---\nname: %s\ndescription: test\n---\n"

func skill(name, body string) string { return strings.Replace(skillHead, "%s", name, 1) + body }

func TestSkillVerdicts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	f := newFixture(t)
	f.write(".claude/skills/plain/SKILL.md", skill("plain", "Explain the code clearly.\n"))
	f.write(".claude/skills/agents/SKILL.md", skill("agents", "Launch one subagent per batch.\n"))
	f.write(".claude/skills/asks/SKILL.md", skill("asks", "Call AskUserQuestion to clarify.\n"))
	f.write(".claude/skills/plugin/SKILL.md", skill("plugin", "Run ${CLAUDE_PLUGIN_ROOT}/scripts/x.sh\n"))
	f.write(".claude/skills/writer/SKILL.md", skill("writer", "See ~/.claude/notes in the docs.\n"))
	f.write(".claude/skills/writer/save.sh", "#!/bin/sh\ncp x ~/.claude/notes\n")
	f.write(".claude/skills/paths/SKILL.md", skill("paths", "Example: /Users/someone/project\n"))
	f.write(".claude/skills/paths/run.sh", "cd /Users/someone/project\n")
	f.write(".claude/skills/nb/SKILL.md", skill("nb", "<!-- notebooklm-py v0.8.2 -->\n"))
	f.write(".claude/skills/twin/SKILL.md", skill("twin", "same"))
	f.write(".agents/skills/twin/SKILL.md", skill("twin", "same"))
	f.write(".claude/skills/impeccable/SKILL.md", skill("impeccable", "claude version"))
	f.write(".agents/skills/impeccable/SKILL.md", skill("impeccable", "codex version"))
	f.write(".agents/skills/codex-only/SKILL.md", skill("codex-only", "Use apply_patch to edit.\n"))
	f.write(".agents/skills/portable/SKILL.md", skill("portable", "Write good commit messages.\n"))
	os.MkdirAll(filepath.Join(f.home, ".claude/skills"), 0o755)
	os.Symlink("../../.agents/skills/shared", filepath.Join(f.home, ".claude/skills/shared"))
	f.write(".agents/skills/shared/SKILL.md", skill("shared", "already shared"))

	got := f.scan(nil)
	want := map[string]Verdict{
		"skill/plain":      OK,
		"skill/agents":     Warn,
		"skill/asks":       Blocked,
		"skill/plugin":     Blocked,
		"skill/writer":     Blocked, // the script writes to ~/.claude
		"skill/paths":      Warn,    // a machine path in a script; prose is ignored
		"skill/nb":         OK,
		"skill/twin":       OK,
		"skill/impeccable": Variant,
		"skill/codex-only": Blocked,
		"skill/portable":   OK,
	}
	for key, verdict := range want {
		c, ok := got[key]
		if !ok || c.Verdict != verdict {
			t.Errorf("%s: verdict %q (reasons %+v), want %q", key, c.Verdict, c.Reasons, verdict)
		}
	}
	if _, listed := got["skill/shared"]; listed {
		t.Error("an already shared skill is a candidate")
	}
	if got["skill/nb"].Installer != "notebooklm CLI" || !strings.Contains(got["skill/nb"].Suggest, "notebooklm skill install") {
		t.Errorf("notebooklm installer not detected: %+v", got["skill/nb"])
	}
	if got["skill/codex-only"].Direction != ToClaude || got["skill/plain"].Direction != ToCodex {
		t.Error("directions are wrong")
	}
	asks := got["skill/asks"].Reasons[0]
	if asks.File != "SKILL.md" || asks.Line != 5 {
		t.Errorf("reason location = %s:%d, want SKILL.md:5", asks.File, asks.Line)
	}

	reg, _ := registry.Load(t.TempDir())
	reg.Set(registry.Skill, "asks", registry.ClaudeOnly)
	if _, listed := f.scan(reg)["skill/asks"]; listed {
		t.Error("a decided skill is still a candidate")
	}
}

func TestClaudeToCodex(t *testing.T) {
	exists := func(p string) bool { return p == "/opt/homebrew/bin/tool" }
	text, reasons := ClaudeToCodex("tool", map[string]any{
		"type": "stdio", "command": "/opt/homebrew/bin/tool", "args": []any{"serve"},
		"env": map[string]any{"API_KEY": "${API_KEY}", "MODE": "fast"},
	}, exists)
	if len(reasons) != 0 {
		t.Errorf("reasons = %+v", reasons)
	}
	var doc struct {
		MCP map[string]map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("invalid TOML: %v\n%s", err, text)
	}
	s := doc.MCP["tool"]
	if s["command"] != "/opt/homebrew/bin/tool" || strs(s["env_vars"])[0] != "API_KEY" || anyMap(s["env"])["MODE"] != "fast" {
		t.Errorf("converted = %v\n%s", s, text)
	}

	text, reasons = ClaudeToCodex("remote", map[string]any{
		"type": "http", "url": "https://mcp.example.com",
		"headers": map[string]any{"Authorization": "Bearer ${TOKEN}", "X-Team": "${TEAM}", "X-Plain": "v"},
	}, exists)
	if len(reasons) != 0 {
		t.Errorf("reasons = %+v", reasons)
	}
	for _, want := range []string{`bearer_token_env_var = 'TOKEN'`, `X-Team = 'TEAM'`, `X-Plain = 'v'`, "[mcp_servers.remote.env_http_headers]"} {
		if !strings.Contains(strings.ReplaceAll(text, `"`, "'"), want) {
			t.Errorf("converted TOML lacks %s:\n%s", want, text)
		}
	}

	for name, s := range map[string]map[string]any{
		"sse":      {"type": "sse", "url": "https://x"},
		"mixedEnv": {"command": "x", "env": map[string]any{"K": "prefix-${K}"}},
		"renamed":  {"command": "x", "env": map[string]any{"K": "${OTHER}"}},
		"args":     {"command": "x", "args": []any{"--token=${T}"}},
		"bad name": {"command": "x"},
	} {
		if _, reasons := ClaudeToCodex(name, s, exists); verdictOf(reasons) != Blocked {
			t.Errorf("%s: verdict %s, want blocked (%+v)", name, verdictOf(reasons), reasons)
		}
	}
	if _, reasons := ClaudeToCodex("missing", map[string]any{"command": "/nowhere/tool"}, exists); verdictOf(reasons) != Warn {
		t.Errorf("a missing absolute command should warn: %+v", reasons)
	}
}

func TestCodexToClaude(t *testing.T) {
	out, reasons := CodexToClaude("docs", map[string]any{"url": "https://developers.openai.com/mcp", "bearer_token_env_var": "T"}, nil)
	if len(reasons) != 0 || out["type"] != "http" || anyMap(out["headers"])["Authorization"] != "Bearer ${T}" {
		t.Errorf("url server = %v, %+v", out, reasons)
	}
	out, _ = CodexToClaude("cg", map[string]any{"command": "codegraph", "args": []any{"serve"}, "env_vars": []any{"HOME"}}, nil)
	if out["type"] != "stdio" || anyMap(out["env"])["HOME"] != "${HOME}" {
		t.Errorf("stdio server = %v", out)
	}
	for _, s := range []map[string]any{
		{"command": "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl"},
		{"command": "./Codex Computer Use.app/x"},
		{"command": "node", "env": map[string]any{"CODEX_HOME": "/x"}},
	} {
		if _, reasons := CodexToClaude("app", s, nil); verdictOf(reasons) != Blocked {
			t.Errorf("%v should stay with Codex: %+v", s, reasons)
		}
	}
}

func TestPluginMarketplaces(t *testing.T) {
	f := newFixture(t)
	f.write(".claude/settings.json", `{"extraKnownMarketplaces": {
  "claude-plugins-official": {"source": {"source": "github", "repo": "anthropics/claude-plugins-official"}},
  "skill-hub": {"source": {"source": "github", "repo": "sametbrr/skill-hub"}},
  "openai-codex": {"source": {"source": "github", "repo": "openai/codex-plugin-cc"}},
  "both": {"source": {"source": "github", "repo": "someone/both"}}}}`)
	f.write(".codex/config.toml", `[marketplaces.both]
source_type = "git"
source = "https://github.com/someone/both.git"

[marketplaces.openai-bundled]
source_type = "local"
source = "/Applications/ChatGPT.app/plugins"
`)
	got := f.scan(nil)
	want := map[string]Verdict{
		"plugin/skill-hub":               Warn,
		"plugin/claude-plugins-official": Blocked,
		"plugin/openai-codex":            Blocked,
		"plugin/openai-bundled":          Blocked,
	}
	for key, verdict := range want {
		if got[key].Verdict != verdict {
			t.Errorf("%s = %q, want %q", key, got[key].Verdict, verdict)
		}
	}
	if _, listed := got["plugin/both"]; listed {
		t.Error("a marketplace both tools know is a candidate")
	}
	if got["plugin/skill-hub"].Suggest != "codex plugin marketplace add sametbrr/skill-hub" {
		t.Errorf("suggestion = %q", got["plugin/skill-hub"].Suggest)
	}
}
