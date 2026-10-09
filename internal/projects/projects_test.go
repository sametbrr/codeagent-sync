package projects

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/sametbrr/codeagent-sync.git":          "github.com/sametbrr/codeagent-sync",
		"https://github.com/sametbrr/codeagent-sync":              "github.com/sametbrr/codeagent-sync",
		"https://sametbrr@bitbucket.org/ercesa/tezgahtar.api.git": "bitbucket.org/ercesa/tezgahtar.api",
		"git@bitbucket.org:ercesa/tezgahtar.api.git":              "bitbucket.org/ercesa/tezgahtar.api",
		"ssh://git@GitHub.com:22/SametBrr/Repo.git/":              "github.com/sametbrr/repo",
		"git@github.com:sametbrr/repo":                            "github.com/sametbrr/repo",
		"/srv/git/repo.git":                                       "",
		"file:///srv/git/repo.git":                                "",
		"../repo":                                                 "",
		"C:/repos/x":                                              "",
		"":                                                        "",
	}
	for in, want := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeRepo makes dir look like a clone of remote, without running git.
func fakeRepo(t *testing.T, dir, remote string) {
	t.Helper()
	cfg := "[core]\n\tbare = false\n"
	if remote != "" {
		cfg += "[remote \"upstream\"]\n\turl = https://example.com/other.git\n" +
			"[remote \"origin\"]\n\turl = " + remote + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	}
	writeFile(t, filepath.Join(dir, ".git", "config"), cfg)
}

func TestIdentify(t *testing.T) {
	base := t.TempDir()

	repo := filepath.Join(base, "repo")
	fakeRepo(t, repo, "git@github.com:a/repo.git")
	if id, isRepo, _ := Identify(repo); id != "repo:github.com/a/repo" || !isRepo {
		t.Errorf("repo: got %q %v", id, isRepo)
	}

	ws := filepath.Join(base, "ws")
	fakeRepo(t, filepath.Join(ws, "web"), "https://u@bitbucket.org/x/web.git")
	fakeRepo(t, filepath.Join(ws, "api"), "https://bitbucket.org/x/api")
	fakeRepo(t, filepath.Join(ws, "local"), "")
	writeFile(t, filepath.Join(ws, "notes.txt"), "x")
	if id, isRepo, _ := Identify(ws); id != "workspace:bitbucket.org/x/api+bitbucket.org/x/web" || isRepo {
		t.Errorf("workspace: got %q %v", id, isRepo)
	}

	worktree := filepath.Join(base, "wt")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: /somewhere/.git/worktrees/wt\n")
	if id, _, reason := Identify(worktree); id != "" || reason == "" {
		t.Errorf("worktree: got %q, reason %q", id, reason)
	}

	noOrigin := filepath.Join(base, "noorigin")
	fakeRepo(t, noOrigin, "")
	if id, _, reason := Identify(noOrigin); id != "" || reason == "" {
		t.Errorf("no origin: got %q, reason %q", id, reason)
	}

	plain := filepath.Join(base, "plain")
	writeFile(t, filepath.Join(plain, "a.txt"), "x")
	if id, _, reason := Identify(plain); id != "" || reason != "" {
		t.Errorf("plain dir: got %q, reason %q", id, reason)
	}
}

func TestDiscover(t *testing.T) {
	home := t.TempDir()
	d := platform.ResolveDirs(home, func(string) string { return "" })
	p := func(rel string) string { return filepath.Join(home, filepath.FromSlash(rel)) }

	fakeRepo(t, p("Projects/app"), "git@github.com:a/app.git")
	fakeRepo(t, p("Projects/copy-of-app"), "https://github.com/a/app")
	fakeRepo(t, p("Projects/lib"), "git@github.com:a/lib.git")
	fakeRepo(t, p("Projects/lib/.claude/worktrees/x"), "git@github.com:a/lib.git")
	fakeRepo(t, p("Projects/ws/one"), "git@github.com:a/one.git")
	writeFile(t, p("Projects/plain/a.txt"), "x")
	fakeRepo(t, home, "git@github.com:a/dotfiles.git")

	listed := map[string]any{}
	for _, dir := range []string{home, p("Projects/app"), p("Projects/copy-of-app"), p("Projects/lib") + string(filepath.Separator),
		p("Projects/lib/.claude/worktrees/x"), p("Projects/plain"), p("Projects/gone")} {
		listed[dir] = map[string]any{}
	}
	claudeJSON, _ := json.Marshal(map[string]any{"projects": listed})
	writeFile(t, d.ClaudeJSON, string(claudeJSON))
	writeFile(t, filepath.Join(d.Codex, "config.toml"),
		"[projects.'"+p("Projects/ws")+"']\ntrust_level = \"trusted\"\n"+
			"[projects.'"+p("Projects/lib")+"']\ntrust_level = \"trusted\"\n")

	tracked := func(dir string) ([]string, error) {
		if dir == p("Projects/lib") {
			return []string{".claude/settings.json"}, nil
		}
		return nil, nil
	}
	found, skipped := discover(d, candidates(d), tracked)

	got := map[string]string{}
	for _, f := range found {
		got[f.Dir] = f.Identity
	}
	want := map[string]string{
		p("Projects/lib"): "repo:github.com/a/lib",
		p("Projects/ws"):  "workspace:github.com/a/one",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("found %v, want %v", got, want)
	}
	if len(skipped) != 2 || !strings.Contains(skipped[0].Reason, "same project") {
		t.Errorf("skipped: %+v", skipped)
	}

	roots := Roots(found)
	var lib = roots[0]
	if lib.Dir != p("Projects/lib") {
		lib = roots[1]
	}
	if lib.Name != RootName("repo:github.com/a/lib") || lib.Project == "" {
		t.Errorf("root: %+v", lib)
	}
	if !lib.Includes(".mcp.json") || !lib.Includes(".claude/settings.local.json") || !lib.Includes(".claude/skills/x/SKILL.md") {
		t.Error("project root should sync its agent config")
	}
	if lib.Includes(".claude/settings.json") {
		t.Error("a file git tracks must not sync")
	}
	if lib.Includes(".claude/worktrees/x/.mcp.json") || lib.Includes("src/main.go") || lib.Includes(".claude/.DS_Store") {
		t.Error("project root syncs too much")
	}
}

func TestEscapeGlob(t *testing.T) {
	roots := Roots([]Project{{Identity: "repo:x/y", Dir: "/p", Tracked: []string{".claude/skills/[draft]*/SKILL.md"}}})
	if roots[0].Includes(".claude/skills/[draft]*/SKILL.md") {
		t.Error("a tracked path with glob characters must be excluded literally")
	}
	if !roots[0].Includes(".claude/skills/d/SKILL.md") {
		t.Error("an escaped path must not exclude other files")
	}
}

func TestGitTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	writeFile(t, filepath.Join(dir, ".claude", "settings.json"), "{}")
	writeFile(t, filepath.Join(dir, ".claude", "settings.local.json"), "{}")
	writeFile(t, filepath.Join(dir, ".mcp.json"), "{}")
	writeFile(t, filepath.Join(dir, "main.go"), "package main")
	run("add", ".claude/settings.json", "main.go")

	got, err := gitTracked(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{".claude/settings.json"}) {
		t.Errorf("tracked = %v", got)
	}
}
