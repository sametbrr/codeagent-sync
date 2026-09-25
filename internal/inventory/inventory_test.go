package inventory

import (
	"runtime"
	"strings"
	"testing"
)

func TestInstaller(t *testing.T) {
	for real, want := range map[string]string{
		"/opt/homebrew/lib/node_modules/@openai/codex/bin/codex.js": "npm install -g @openai/codex",
		"/usr/local/lib/node_modules/uisight/src/cli.mjs":           "npm install -g uisight",
		"/opt/homebrew/Cellar/uv/0.12.9/bin/uvx":                    "brew install uv",
		"/opt/homebrew/Caskroom/codex/0.157.0/codex":                "brew install --cask codex",
		"/home/u/.local/pipx/venvs/markitdown-mcp/bin/markitdown":   "pipx install markitdown-mcp",
		"/home/u/.local/share/uv/tools/serena/bin/serena":           "uv tool install serena",
	} {
		if _, got := installer("x", real); got != want {
			t.Errorf("%s: %q, want %q", real, got, want)
		}
	}
	if kind, cmd := installer("claude", "/Users/u/.local/share/claude/versions/2.1.282"); kind != "native" || !strings.Contains(cmd, "claude.ai/install") {
		t.Errorf("claude native: %s %s", kind, cmd)
	}
	if kind, _ := installer("node_repl", "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl"); kind != "app" {
		t.Errorf("app: %s", kind)
	}
}

func TestCompare(t *testing.T) {
	here := Machine{Name: "b", OS: runtime.GOOS, Tools: []Program{{Name: "claude", Version: "2.1.0", Found: true}, {Name: "codex"}}}
	other := Machine{Name: "a", OS: runtime.GOOS,
		Tools:    []Program{{Name: "claude", Version: "2.1.282", Found: true}, {Name: "codex", Version: "0.157.0", Found: true, Install: "npm install -g @openai/codex"}},
		Programs: []Program{{Name: "codegraph", Found: true, Install: "npm install -g @colbymchenry/codegraph"}, {Name: "gone"}}}
	got := Compare(here, []Machine{here, other})
	names := map[string]Missing{}
	for _, m := range got {
		names[m.Name] = m
	}
	if len(got) != 3 || names["claude"].Here != "2.1.0" || names["codex"].Install == "" || !names["codegraph"].SameKind {
		t.Errorf("Compare = %+v", got)
	}
}
