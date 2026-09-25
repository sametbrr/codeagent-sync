package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/storage/s3/s3fake"
)

// cliMachine runs the command line with HOME pointing at its own directory.
type cliMachine struct {
	t    *testing.T
	home string
}

// newCLIMachine returns a machine with Claude Code and Codex installed.
func newCLIMachine(t *testing.T) *cliMachine {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &cliMachine{t: t, home: home}
}

func (m *cliMachine) run(args ...string) (code int, stdout, stderr string) {
	m.t.Helper()
	m.t.Setenv("HOME", m.home)
	m.t.Setenv("USERPROFILE", m.home) // the home directory on Windows
	m.t.Setenv("CLAUDE_CONFIG_DIR", "")
	m.t.Setenv("CODEX_HOME", "")
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func (m *cliMachine) mustRun(args ...string) string {
	m.t.Helper()
	code, out, errOut := m.run(args...)
	if code != 0 {
		m.t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

func (m *cliMachine) write(rel, content string) {
	m.t.Helper()
	p := filepath.Join(m.home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *cliMachine) read(rel string) string {
	m.t.Helper()
	data, err := os.ReadFile(filepath.Join(m.home, filepath.FromSlash(rel)))
	if err != nil {
		m.t.Fatal(err)
	}
	return string(data)
}

func TestCommandLineAcrossMachines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	srv := httptest.NewServer(s3fake.New("codeagent-sync"))
	t.Cleanup(srv.Close)
	initArgs := []string{"init", "--provider", "s3", "--endpoint", srv.URL, "--path-style",
		"--region", "us-east-1", "--access-key-id", "AKID", "--secret-access-key", "secret", "--yes"}
	t.Setenv(passphraseEnv, "correct horse battery staple")

	a := newCLIMachine(t)
	a.write(".claude/CLAUDE.md", "rules in "+a.home+"\n")
	a.write(".agents/skills/foo/SKILL.md", "shared skill")
	if err := os.MkdirAll(filepath.Join(a.home, ".claude/skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.agents/skills/foo", filepath.Join(a.home, ".claude/skills/foo")); err != nil {
		t.Fatal(err)
	}
	if out := a.mustRun(initArgs...); !strings.Contains(out, "Set up the bucket") || !strings.Contains(out, "3 uploaded") {
		t.Fatalf("init on the first machine:\n%s", out)
	}
	if code, _, errOut := a.run(initArgs...); code != 1 || !strings.Contains(errOut, "already set up") {
		t.Errorf("second init: exit %d, %s", code, errOut)
	}

	b := newCLIMachine(t)
	if out := b.mustRun(initArgs...); !strings.Contains(out, "Joined the existing bucket") {
		t.Fatalf("init on the second machine:\n%s", out)
	}
	if got := b.read(".claude/CLAUDE.md"); got != "rules in "+b.home+"\n" {
		t.Errorf("CLAUDE.md on b = %q", got)
	}
	if got := b.read(".claude/skills/foo/SKILL.md"); got != "shared skill" {
		t.Errorf("shared skill through the link = %q", got)
	}

	c := newCLIMachine(t)
	t.Setenv(passphraseEnv, "a wrong passphrase!!")
	if code, _, errOut := c.run(initArgs...); code != 1 || !strings.Contains(errOut, "does not match") {
		t.Errorf("init with a wrong passphrase: exit %d, %s", code, errOut)
	}
	t.Setenv(passphraseEnv, "correct horse battery staple")

	b.write(".agents/skills/foo/SKILL.md", "edited on b")
	b.mustRun("sync")
	if out := a.mustRun("sync"); !strings.Contains(out, "1 from your other machines") {
		t.Errorf("sync on a:\n%s", out)
	}
	if got := a.read(".agents/skills/foo/SKILL.md"); got != "edited on b" {
		t.Errorf("a has %q", got)
	}

	// A conflict: exit code 2, listed, then resolved.
	a.write(".claude/CLAUDE.md", "a's rules")
	b.write(".claude/CLAUDE.md", "b's rules")
	a.mustRun("sync")
	code, out, _ := b.run("--json", "sync")
	if code != 2 {
		t.Fatalf("sync with a conflict: exit %d\n%s", code, out)
	}
	var res jsonResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("--json output: %v\n%s", err, out)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].Path != "claude/CLAUDE.md" {
		t.Errorf("conflicts = %+v", res.Conflicts)
	}
	if out := b.mustRun("conflicts"); !strings.Contains(out, "claude/CLAUDE.md") {
		t.Errorf("conflicts list:\n%s", out)
	}
	b.mustRun("conflicts", "resolve", "claude/CLAUDE.md", "--keep", "remote")
	if got := b.read(".claude/CLAUDE.md"); got != "a's rules" {
		t.Errorf("after resolving with the remote version: %q", got)
	}
	if out := b.mustRun("status"); !strings.Contains(out, "Everything is in sync") {
		t.Errorf("status:\n%s", out)
	}
}
