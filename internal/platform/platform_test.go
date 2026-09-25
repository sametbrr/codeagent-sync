package platform

import (
	"path/filepath"
	"testing"
)

func TestResolveDirsDefaults(t *testing.T) {
	home := filepath.FromSlash("/home/ad")
	got := ResolveDirs(home, func(string) string { return "" })
	want := Dirs{
		Home:       home,
		Claude:     filepath.Join(home, ".claude"),
		ClaudeJSON: filepath.Join(home, ".claude.json"),
		Codex:      filepath.Join(home, ".codex"),
		Agents:     filepath.Join(home, ".agents"),
		State:      filepath.Join(home, ".codeagent-sync"),
	}
	if got != want {
		t.Errorf("ResolveDirs() = %+v, want %+v", got, want)
	}
}

func TestResolveDirsEnvOverrides(t *testing.T) {
	home := filepath.FromSlash("/home/ad")
	env := map[string]string{
		"CLAUDE_CONFIG_DIR": "~/work/claude",
		"CODEX_HOME":        "/opt/codex/",
	}
	got := ResolveDirs(home, func(k string) string { return env[k] })

	if want := filepath.Join(home, "work", "claude"); got.Claude != want {
		t.Errorf("Claude = %q, want %q", got.Claude, want)
	}
	if want := filepath.Join(home, "work", "claude", ".claude.json"); got.ClaudeJSON != want {
		t.Errorf("ClaudeJSON = %q, want %q", got.ClaudeJSON, want)
	}
	if want := filepath.FromSlash("/opt/codex"); got.Codex != want {
		t.Errorf("Codex = %q, want %q", got.Codex, want)
	}
	if want := filepath.Join(home, ".agents"); got.Agents != want {
		t.Errorf("Agents = %q, want %q", got.Agents, want)
	}
}

func TestOSProperties(t *testing.T) {
	tests := []struct {
		os              OS
		caseInsensitive bool
		execBit         bool
	}{
		{Darwin, true, true},
		{Linux, false, true},
		{Windows, true, false},
	}
	for _, tc := range tests {
		if got := tc.os.CaseInsensitive(); got != tc.caseInsensitive {
			t.Errorf("%s.CaseInsensitive() = %v, want %v", tc.os, got, tc.caseInsensitive)
		}
		if got := tc.os.TracksExecBit(); got != tc.execBit {
			t.Errorf("%s.TracksExecBit() = %v, want %v", tc.os, got, tc.execBit)
		}
	}
}

func TestExecutableAndFilePerm(t *testing.T) {
	if !Executable(0o755) || !Executable(0o744) || Executable(0o644) {
		t.Error("Executable() misreads execute bits")
	}
	if FilePerm(true) != 0o755 || FilePerm(false) != 0o644 {
		t.Error("FilePerm() returned unexpected bits")
	}
}
