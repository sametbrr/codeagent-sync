package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/sametbrr/codeagent-sync/internal/storage/s3/s3fake"
)

// newSyncedPair returns two machines set up with the same bucket.
func newSyncedPair(t *testing.T) (a, b *cliMachine) {
	t.Helper()
	srv := httptest.NewServer(s3fake.New("codeagent-sync"))
	t.Cleanup(srv.Close)
	args := []string{"init", "--provider", "s3", "--endpoint", srv.URL, "--path-style",
		"--region", "us-east-1", "--access-key-id", "AKID", "--secret-access-key", "secret", "--yes"}
	t.Setenv(passphraseEnv, "correct horse battery staple")
	a = newCLIMachine(t)
	a.mustRun(args...)
	b = newCLIMachine(t)
	b.mustRun(args...)
	return a, b
}

func TestAutoSyncHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	executable = func() (string, error) { return filepath.Join(os.Getenv("HOME"), ".local/bin/codeagent-sync"), nil }
	codexTrust = func(context.Context, string) (map[string]string, error) { return nil, errors.New("no codex here") }
	hookMinInterval = 0
	t.Cleanup(func() {
		executable, codexTrust, hookMinInterval = os.Executable, nil, 15e9
	})

	a, b := newSyncedPair(t)
	a.write(".claude/settings.json", `{"model": "opus"}`)
	code, out, errOut := a.run("auto", "enable")
	if code != 0 || !strings.Contains(out, "Automatic sync is on for Claude Code and Codex") || !strings.Contains(errOut, "/hooks") {
		t.Errorf("auto enable: exit %d\n%s%s", code, out, errOut)
	}
	settings := a.read(".claude/settings.json")
	if !strings.Contains(settings, `"command": "`+a.home+`/.local/bin/codeagent-sync"`) || !strings.Contains(settings, `"model": "opus"`) {
		t.Errorf("settings.json:\n%s", settings)
	}
	if hooksJSON := a.read(".codex/hooks.json"); !strings.Contains(hooksJSON, `\"$HOME/.local/bin/codeagent-sync\" hook notify`) {
		t.Errorf("hooks.json:\n%s", hooksJSON)
	}
	if out := a.mustRun("auto", "enable"); !strings.Contains(out, "already on") {
		t.Errorf("second auto enable:\n%s", out)
	}
	var states []autoState
	if err := json.Unmarshal([]byte(a.mustRun("--json", "auto", "status")), &states); err != nil || len(states) != 2 || len(states[0].Hooks) != 3 {
		t.Errorf("auto status: %+v, %v", states, err)
	}

	// doctor finds that the program the hooks start is missing.
	code, out, _ = a.run("--json", "doctor")
	var findings []finding
	if err := json.Unmarshal([]byte(out), &findings); err != nil || code != 1 {
		t.Fatalf("doctor: exit %d, %v\n%s", code, err, out)
	}
	byCheck := map[string][]string{}
	for _, f := range findings {
		byCheck[f.Check] = append(byCheck[f.Check], f.Status+": "+f.Detail)
	}
	for _, want := range []string{"setup", "storage", "key", "writes", "last sync", "conflicts", "layout"} {
		if len(byCheck[want]) == 0 || !strings.HasPrefix(byCheck[want][0], "ok") {
			t.Errorf("doctor %s: %v", want, byCheck[want])
		}
	}
	if got := strings.Join(byCheck["auto sync"], "\n"); !strings.Contains(got, "fail: Claude Code's hooks start "+a.home+"/.local/bin/codeagent-sync, which is not installed here") {
		t.Errorf("doctor auto sync:\n%s", got)
	}
	a.write(".local/bin/codeagent-sync", "#!/bin/sh\n")
	if code, out, errOut := a.run("doctor"); code != 0 {
		t.Errorf("doctor with the program in place: exit %d\n%s%s", code, out, errOut)
	}

	// The hooks reach the other machine, with its own home directory.
	a.mustRun("sync")
	b.mustRun("sync")
	if got := b.read(".claude/settings.json"); !strings.Contains(got, `"command": "`+b.home+`/.local/bin/codeagent-sync"`) {
		t.Errorf("settings.json on b:\n%s", got)
	}

	// A new skill only Claude has is offered once, through the next prompt.
	a.write(".claude/skills/plain/SKILL.md", "---\nname: plain\ndescription: Formats dates.\n---\nFormat the date.\n")
	a.write(".claude/skills/hooky/SKILL.md", "---\nname: hooky\ndescription: x\n---\nRun with $ARGUMENTS.\n")
	a.mustRun("hook", "sync")
	notice := a.mustRun("hook", "notify")
	for _, want := range []string{"codeagent-sync share skill:plain --yes", "The skill hooky (new in Claude Code) stays with Claude Code"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice lacks %q:\n%s", want, notice)
		}
	}
	if again := a.mustRun("hook", "notify"); again != "" {
		t.Errorf("told twice:\n%s", again)
	}
	a.mustRun("hook", "sync")
	if again := a.mustRun("hook", "notify"); again != "" {
		t.Errorf("told again after the next sync:\n%s", again)
	}

	// A conflict found by a background sync is told as well.
	a.write(".claude/CLAUDE.md", "a's rules")
	b.write(".claude/CLAUDE.md", "b's rules")
	a.mustRun("sync")
	b.mustRun("hook", "sync")
	if notice := b.mustRun("hook", "notify"); !strings.Contains(notice, "claude/CLAUDE.md was changed differently on two machines") {
		t.Errorf("conflict notice:\n%s", notice)
	}

	// Moving the program updates the hooks.
	executable = func() (string, error) { return filepath.Join(os.Getenv("HOME"), "tools/codeagent-sync"), nil }
	if out := a.mustRun("auto", "enable"); strings.Contains(out, "already on") {
		t.Errorf("auto enable after a move:\n%s", out)
	}
	if got := a.read(".codex/hooks.json"); !strings.Contains(got, `$HOME/tools/codeagent-sync`) || strings.Contains(got, ".local/bin") {
		t.Errorf("hooks.json after a move:\n%s", got)
	}

	if out := a.mustRun("auto", "disable"); !strings.Contains(out, "off for Claude Code and Codex") {
		t.Errorf("auto disable:\n%s", out)
	}
	if got := a.read(".claude/settings.json"); strings.Contains(got, "codeagent-sync") || !strings.Contains(got, `"model": "opus"`) {
		t.Errorf("settings.json after disable:\n%s", got)
	}
}

func TestHooksDoNothingBeforeSetup(t *testing.T) {
	m := newCLIMachine(t)
	if out := m.mustRun("hook", "sync"); out != "" {
		t.Errorf("hook sync printed %q", out)
	}
	if out := m.mustRun("hook", "notify"); out != "" {
		t.Errorf("hook notify printed %q", out)
	}
	if code, _, errOut := m.run("auto", "enable"); code != 1 || !strings.Contains(errOut, "init") {
		t.Errorf("auto enable before init: exit %d, %s", code, errOut)
	}
}

func TestJoinCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	a, _ := newSyncedPair(t)
	a.write(".claude/CLAUDE.md", "rules")
	a.mustRun("sync")

	var got map[string]string
	if err := json.Unmarshal([]byte(a.mustRun("--json", "join-code")), &got); err != nil || !strings.HasPrefix(got["code"], joinPrefix) {
		t.Fatalf("join-code: %v, %v", got, err)
	}
	if strings.Contains(got["code"], "secret") || strings.Contains(got["code"], "AKID") {
		t.Fatal("the code shows the credentials")
	}

	c := newCLIMachine(t)
	if out := c.mustRun("init", "--join", got["code"], "--yes"); !strings.Contains(out, "Joined the existing bucket") {
		t.Fatalf("init --join:\n%s", out)
	}
	if got := c.read(".claude/CLAUDE.md"); got != "rules" {
		t.Errorf("CLAUDE.md on the joined machine = %q", got)
	}

	d := newCLIMachine(t)
	t.Setenv(passphraseEnv, "not the passphrase at all")
	if code, _, errOut := d.run("init", "--join", got["code"]); code != 1 || !strings.Contains(errOut, "does not open the join code") {
		t.Errorf("init --join with a wrong passphrase: exit %d, %s", code, errOut)
	}
	if code, _, errOut := a.run("join-code"); code != 1 || !strings.Contains(errOut, "not the bucket's passphrase") {
		t.Errorf("join-code with a wrong passphrase: exit %d, %s", code, errOut)
	}
}

func TestJoinCodeOfAKeyFileBucket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	srv := httptest.NewServer(s3fake.New("codeagent-sync"))
	t.Cleanup(srv.Close)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	a := newCLIMachine(t)
	a.write("key.txt", id.String()+"\n")
	a.mustRun("init", "--provider", "s3", "--endpoint", srv.URL, "--path-style", "--region", "us-east-1",
		"--access-key-id", "AKID", "--secret-access-key", "secret", "--key-file", filepath.Join(a.home, "key.txt"), "--yes")

	t.Setenv(passphraseEnv, "a code passphrase, long enough")
	var code string
	for _, line := range strings.Split(a.mustRun("join-code"), "\n") {
		if strings.HasPrefix(line, joinPrefix) {
			code = line
		}
	}
	b := newCLIMachine(t)
	if out := b.mustRun("init", "--join", code, "--yes"); !strings.Contains(out, "Joined the existing bucket") {
		t.Fatalf("init --join:\n%s", out)
	}
	if key := b.read(".codeagent-sync/age-key.txt"); strings.TrimSpace(key) != id.String() {
		t.Error("the joined machine did not get the bucket's key")
	}
}

func TestAutoEnableEdgeCases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	codexTrust = func(context.Context, string) (map[string]string, error) { return nil, errors.New("no codex here") }
	t.Cleanup(func() { executable, codexTrust = os.Executable, nil })
	a, _ := newSyncedPair(t)

	executable = func() (string, error) {
		return filepath.Join(os.Getenv("HOME"), "bin/codeagent-sync-darwin-arm64"), nil
	}
	if code, _, errOut := a.run("auto", "enable"); code != 1 || !strings.Contains(errOut, "rename") {
		t.Errorf("a program with another name: exit %d, %s", code, errOut)
	}

	// A settings file linked from a dotfiles repository stays a link.
	executable = func() (string, error) { return filepath.Join(os.Getenv("HOME"), ".local/bin/codeagent-sync"), nil }
	a.write("dotfiles/settings.json", `{"model": "opus"}`)
	if err := os.Symlink("../dotfiles/settings.json", filepath.Join(a.home, ".claude/settings.json")); err != nil {
		t.Fatal(err)
	}
	a.mustRun("auto", "enable")
	if fi, err := os.Lstat(filepath.Join(a.home, ".claude/settings.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings.json is no longer a link: %v", err)
	}
	if got := a.read("dotfiles/settings.json"); !strings.Contains(got, "codeagent-sync") {
		t.Errorf("the link's target was not written:\n%s", got)
	}

	// Undo lists the change with what it was, and asks first.
	var list []map[string]any
	if err := json.Unmarshal([]byte(a.mustRun("--json", "undo", "--list")), &list); err != nil || len(list) == 0 || list[0]["what"] != "auto enable" {
		t.Fatalf("undo --list: %v, %v", list, err)
	}
	if code, _, errOut := a.run("undo"); code != 1 || !strings.Contains(errOut, "--yes") {
		t.Errorf("undo without a terminal: exit %d, %s", code, errOut)
	}
	if out := a.mustRun("undo", "--yes"); !strings.Contains(out, "from before auto enable") {
		t.Errorf("undo:\n%s", out)
	}
	if got := a.read("dotfiles/settings.json"); strings.Contains(got, "codeagent-sync") {
		t.Errorf("undo left the hooks:\n%s", got)
	}

	// Agent CLIs that codeagent-sync starts itself run no codeagent-sync hooks.
	a.write(".claude/skills/plain/SKILL.md", "---\nname: plain\ndescription: x\n---\nx\n")
	hookMinInterval = 0
	t.Cleanup(func() { hookMinInterval = 15e9 })
	a.mustRun("hook", "sync")
	t.Setenv(noHooksEnv, "1")
	if out := a.mustRun("hook", "notify"); out != "" {
		t.Errorf("hook notify ran inside a child CLI:\n%s", out)
	}
	t.Setenv(noHooksEnv, "")
	if out := a.mustRun("hook", "notify"); !strings.Contains(out, "skill:plain") {
		t.Errorf("the notice was not kept for the real session:\n%s", out)
	}
}

func TestMachinesShowWhatIsMissingHere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell scripts as programs")
	}
	a, b := newSyncedPair(t)
	tools := t.TempDir()
	for name, out := range map[string]string{"codex": "codex-cli 0.157.0", "codegraph": "1.0.0"} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a.write(".claude.json", `{"mcpServers": {"codegraph": {"command": "codegraph", "args": ["serve"]}}}`)
	t.Setenv("PATH", tools)
	a.mustRun("machines")
	a.mustRun("sync", "--yes")

	t.Setenv("PATH", t.TempDir())
	b.mustRun("sync")
	var got struct {
		Machines    []map[string]any `json:"machines"`
		MissingHere []struct {
			Name string `json:"name"`
			On   string `json:"on"`
		} `json:"missing_here"`
	}
	if err := json.Unmarshal([]byte(b.mustRun("--json", "machines")), &got); err != nil {
		t.Fatal(err)
	}
	missing := map[string]bool{}
	for _, m := range got.MissingHere {
		missing[m.Name] = true
	}
	if len(got.Machines) != 2 || !missing["codex"] || !missing["codegraph"] {
		t.Errorf("machines = %d, missing here = %+v", len(got.Machines), got.MissingHere)
	}
}
