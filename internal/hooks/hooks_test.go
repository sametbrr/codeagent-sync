package hooks

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// claudeSettings mirrors a settings.json with hooks of its own.
const claudeSettings = `{
  "model": "opus",
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "node \"$HOME/.claude/hooks/design-mode.js\"",
            "timeout": 5
          }
        ]
      }
    ]
  },
  "statusLine": {
    "type": "command",
    "command": "bash ~/.claude/statusline.sh"
  }
}
`

// codexHooks mirrors a hooks.json with a hook written for cmd.exe.
const codexHooks = `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ ! -f '/Users/ad/x' ] || '/Users/ad/x' hook",
            "commandWindows": "if exist \"x.cmd\" (\"x.cmd\" hook & exit /b)",
            "timeout": 30
          }
        ]
      }
    ]
  }
}
`

var mac, _ = NewProgram("/Users/ad/.local/bin/codeagent-sync", "/Users/ad")

func TestInstallAndRemoveInClaudeSettings(t *testing.T) {
	out, err := Install([]byte(claudeSettings), Claude, mac)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Installed(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"UserPromptSubmit", "SessionStart", "Stop"}; !reflect.DeepEqual(got, want) {
		t.Errorf("installed = %v, want %v", got, want)
	}
	var doc struct {
		Model string `json:"model"`
		Hooks map[string][]struct {
			Matcher string           `json:"matcher"`
			Hooks   []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Model != "opus" || len(doc.Hooks["UserPromptSubmit"]) != 2 {
		t.Fatalf("the other settings or hooks changed:\n%s", out)
	}
	start := doc.Hooks["SessionStart"][0]
	if start.Matcher != "startup|resume" || start.Hooks[0]["command"] != "/Users/ad/.local/bin/codeagent-sync" ||
		!reflect.DeepEqual(start.Hooks[0]["args"], []any{"hook", "sync"}) || start.Hooks[0]["async"] != true {
		t.Errorf("SessionStart hook = %+v", start)
	}

	again, err := Install(out, Claude, mac)
	if err != nil || string(again) != string(out) {
		t.Errorf("installing twice changed the file:\n%s", again)
	}

	back, err := Remove(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != claudeSettings {
		t.Errorf("remove did not restore the file:\n%s", back)
	}
}

func TestInstallAndRemoveInCodexHooks(t *testing.T) {
	out, err := Install([]byte(codexHooks), Codex, mac)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`"command": "\"$HOME/.local/bin/codeagent-sync\" hook sync"`,
		`"commandWindows": "cmd /d /c \"/Users/ad/.local/bin/codeagent-sync.exe\" hook notify"`,
		`hook & exit /b`, // not escaped as \u0026
	} {
		if !strings.Contains(s, want) {
			t.Errorf("hooks.json lacks %s:\n%s", want, s)
		}
	}
	back, err := Remove(out)
	if err != nil || string(back) != codexHooks {
		t.Errorf("remove did not restore the file (%v):\n%s", err, back)
	}
}

func TestOurHandlerNextToAnotherInOneGroup(t *testing.T) {
	file := `{"hooks": {"Stop": [{"hooks": [
    {"type": "command", "command": "notify-send done"},
    {"type": "command", "command": "\"$HOME/.local/bin/codeagent-sync\" hook sync", "async": true}
  ]}]}}`
	out, err := Remove([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "codeagent-sync") || !strings.Contains(string(out), "notify-send done") {
		t.Errorf("remove:\n%s", out)
	}
}

func TestInstallIntoAMissingFile(t *testing.T) {
	opt, _ := NewProgram("/opt/bin/codeagent-sync", "/Users/ad")
	out, err := Install(nil, Codex, opt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"command": "'/opt/bin/codeagent-sync' hook sync"`) ||
		!strings.Contains(string(out), `"commandWindows": "cmd /d /c codeagent-sync.exe hook sync"`) {
		t.Errorf("hooks.json:\n%s", out)
	}
	if back, _ := Remove(out); string(back) != "{}\n" {
		t.Errorf("after remove: %s", back)
	}
}

func TestIsOurs(t *testing.T) {
	for cmd, want := range map[string]bool{
		`"$HOME/.local/bin/codeagent-sync" hook sync`:                   true,
		`'/opt/bin/codeagent-sync' hook notify`:                         true,
		`"%USERPROFILE%\.local\bin\codeagent-sync.exe" hook sync`:       true,
		`codeagent-sync hook sync`:                                      true,
		`codeagent-sync sync -q`:                                        false,
		`node "$HOME/.claude/hooks/design-mode.js"`:                     false,
		`echo codeagent-sync is great && ~/bin/other hook codeagent-sy`: false,
	} {
		if got := IsOurs(map[string]any{"command": cmd}); got != want {
			t.Errorf("IsOurs(%s) = %v", cmd, got)
		}
	}
	if !IsOurs(map[string]any{"command": `C:\Users\ad\.local\bin\codeagent-sync`, "args": []any{"hook", "sync"}}) {
		t.Error("a Windows path with args is ours")
	}
}

func TestPrograms(t *testing.T) {
	for tool, want := range map[Tool]string{Claude: "/Users/ad/.local/bin/codeagent-sync", Codex: "/Users/ad/.local/bin/codeagent-sync"} {
		file, err := Install(nil, tool, mac)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Programs(file, "/Users/ad")
		if err != nil || len(got) != 1 || got[0] != filepath.FromSlash(want) {
			t.Errorf("%s: programs = %v, %v", tool, got, err)
		}
	}
}

func TestProgramPathsAShellWouldRead(t *testing.T) {
	for _, exe := range []string{`/Users/a"b/codeagent-sync`, "/Users/$x/codeagent-sync", "/Users/a`b`/codeagent-sync", `C:\Users\a%b%\codeagent-sync.exe`} {
		if _, err := NewProgram(exe, "/Users/x"); err == nil {
			t.Errorf("%s was accepted", exe)
		}
	}
	p, err := NewProgram(`C:\Users\Ad Min\.local\bin\codeagent-sync.exe`, `C:\Users\Ad Min`)
	if err != nil || p.Path != `C:\Users\Ad Min\.local\bin\codeagent-sync` {
		t.Errorf("a Windows path with a space: %+v, %v", p, err)
	}
}
