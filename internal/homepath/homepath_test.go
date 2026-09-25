package homepath

import (
	"encoding/json"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

var (
	mac   = New("/Users/samet", platform.Darwin)
	linux = New("/home/ad", platform.Linux)
	win   = New(`C:\Users\ad`, platform.Windows)
)

func TestToPortablePOSIX(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/Users/samet/.claude/hooks/x.js", "__CODEAGENT_HOME__/.claude/hooks/x.js"},
		{`node "/Users/samet/.claude/hooks/design-mode.js"`, `node "__CODEAGENT_HOME__/.claude/hooks/design-mode.js"`},
		{"/Users/samet", "__CODEAGENT_HOME__"},
		{"/Users/samet /Users/samet", "__CODEAGENT_HOME__ __CODEAGENT_HOME__"},
		{"PATH=/usr/bin:/Users/samet/bin", "PATH=/usr/bin:__CODEAGENT_HOME__/bin"},
		{"file:///Users/samet/x", "file://__CODEAGENT_HOME__/x"},

		// Not the home directory: a longer user name, or part of another path.
		{"/Users/samet2/x", "/Users/samet2/x"},
		{"/Users/samet.old/x", "/Users/samet.old/x"},
		{"/Users/samet-backup", "/Users/samet-backup"},
		{"/Users/sametş/x", "/Users/sametş/x"},
		{"/Volumes/Backup/Users/samet/x", "/Volumes/Backup/Users/samet/x"},

		// Portable spellings stay as they are.
		{`"$HOME/.claude/hooks/design-mode.js"`, `"$HOME/.claude/hooks/design-mode.js"`},
		{"${HOME}/x", "${HOME}/x"},
		{"~/.claude/CLAUDE.md", "~/.claude/CLAUDE.md"},
	}
	for _, tc := range tests {
		if got := string(mac.ToPortable([]byte(tc.in))); got != tc.want {
			t.Errorf("ToPortable(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestToPortableWindows(t *testing.T) {
	tests := []struct{ in, want string }{
		{`C:\Users\ad\.claude\CLAUDE.md`, `__CODEAGENT_HOME_WIN__\.claude\CLAUDE.md`},
		{`{"installPath":"C:\\Users\\ad\\.claude\\plugins\\cache\\x"}`,
			`{"installPath":"__CODEAGENT_HOME_WIN_ESCAPED__\\.claude\\plugins\\cache\\x"}`},
		{"C:/Users/ad/.agents/skills", "__CODEAGENT_HOME__/.agents/skills"},
		{"/c/Users/ad/.agents/skills", "__CODEAGENT_HOME__/.agents/skills"},
		{`c:\users\AD\x`, `__CODEAGENT_HOME_WIN__\x`},

		{`C:\Users\adam\x`, `C:\Users\adam\x`},
		{"/mnt/c/Users/ad/x", "/mnt/c/Users/ad/x"},
		{`D:\Users\ad\x`, `D:\Users\ad\x`},
	}
	for _, tc := range tests {
		if got := string(win.ToPortable([]byte(tc.in))); got != tc.want {
			t.Errorf("ToPortable(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Content that never leaves a machine must come back byte for byte.
func TestRoundTripOnSameMachine(t *testing.T) {
	tests := []struct {
		m    *Mapper
		text string
	}{
		{mac, `{"command":"node \"/Users/samet/.claude/hooks/a.js\"","other":"/Users/samet2"}`},
		{mac, "cd /Users/samet && ls $HOME /Users/samet/x\n"},
		{linux, "/home/ad/.codex/AGENTS.md"},
		{win, `{"commandWindows":"if exist \"C:\\Users\\ad\\.agents\\x.cmd\" (\"C:\\Users\\ad\\.agents\\x.cmd\" hook)"}`},
		{win, `set PATH=C:\Users\ad\bin;%PATH%`},
		{win, "C:/Users/ad/.claude/statusline.sh"},
	}
	for _, tc := range tests {
		portable := tc.m.ToPortable([]byte(tc.text))
		if got := string(tc.m.ToLocal(portable)); got != tc.text {
			t.Errorf("round trip of %q gave %q (portable %q)", tc.text, got, portable)
		}
	}
}

func TestAcrossMachines(t *testing.T) {
	tests := []struct {
		name     string
		from, to *Mapper
		in, want string
		isJSON   bool
	}{
		{
			name: "Windows JSON to macOS",
			from: win, to: mac,
			in:     `{"installPath":"C:\\Users\\ad\\.claude\\plugins\\cache\\x"}`,
			want:   `{"installPath":"/Users/samet/.claude/plugins/cache/x"}`,
			isJSON: true,
		},
		{
			name: "escaped path with a space, then an escape sequence",
			from: win, to: linux,
			in:     `{"p":"C:\\Users\\ad\\My Docs\\a.txt\nnext"}`,
			want:   `{"p":"/home/ad/My Docs/a.txt\nnext"}`,
			isJSON: true,
		},
		{
			name: "raw Windows path in prose",
			from: win, to: mac,
			in:   `see C:\Users\ad\.claude\CLAUDE.md now`,
			want: "see /Users/samet/.claude/CLAUDE.md now",
		},
		{
			name: "TOML basic string from Windows",
			from: win, to: linux,
			in:   `command = "C:\\Users\\ad\\bin\\tool.exe"`,
			want: `command = "/home/ad/bin/tool.exe"`,
		},
		{
			name: "macOS JSON to Windows",
			from: mac, to: win,
			in:     `{"command":"/Users/samet/.claude/statusline.sh"}`,
			want:   `{"command":"C:/Users/ad/.claude/statusline.sh"}`,
			isJSON: true,
		},
		{
			name: "Linux to macOS",
			from: linux, to: mac,
			in:   "/home/ad/.agents/skills/foo",
			want: "/Users/samet/.agents/skills/foo",
		},
	}
	for _, tc := range tests {
		got := tc.to.ToLocal(tc.from.ToPortable([]byte(tc.in)))
		if string(got) != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if tc.isJSON && !json.Valid(got) {
			t.Errorf("%s: result is not valid JSON: %q", tc.name, got)
		}
	}
}

func TestAliases(t *testing.T) {
	m := New("/home/ad", platform.Linux, "/var/home/ad")
	for _, in := range []string{"/home/ad/x", "/var/home/ad/x"} {
		if got := string(m.ToPortable([]byte(in))); got != "__CODEAGENT_HOME__/x" {
			t.Errorf("ToPortable(%q) = %q, want __CODEAGENT_HOME__/x", in, got)
		}
	}
}

func TestToLocalIgnoresLookalikes(t *testing.T) {
	in := "__CODEAGENT_HOMEWORK__ and __CODEAGENT_HOME_"
	if got := string(mac.ToLocal([]byte(in))); got != in {
		t.Errorf("ToLocal(%q) = %q, want it unchanged", in, got)
	}
}

func TestIsText(t *testing.T) {
	if !IsText([]byte("plain text\n")) || !IsText(nil) {
		t.Error("text reported as binary")
	}
	if IsText([]byte{0x89, 'P', 'N', 'G', 0, 0}) {
		t.Error("binary reported as text")
	}
}
