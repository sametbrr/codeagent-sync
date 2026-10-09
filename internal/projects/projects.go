// Package projects finds the projects the agent tools were used in and turns
// each into a root, so a project's own agent configuration (.mcp.json,
// .claude/settings.local.json, …) syncs to the same project on other
// machines, wherever it is checked out there.
//
// A project is recognized by its git remote, not by its path: a repository
// by the normalized URL of its origin, and a plain directory holding several
// repositories (a workspace) by the remotes of those repositories. Files git
// tracks are never synced; git already moves them.
package projects

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// RootPrefix starts the name of every project root.
const RootPrefix = tools.ProjectRootPrefix

// Include lists what syncs in a project, relative to its directory.
var Include = []string{
	".mcp.json",
	"CLAUDE.local.md",
	".claude/settings.json",
	".claude/settings.local.json",
	".claude/agents/**",
	".claude/commands/**",
	".claude/skills/**",
	".codex/config.toml",
	".codex/agents/**",
}

// Project is a project directory on this machine and what identifies it on
// every machine.
type Project struct {
	// Identity is "repo:<remote>" for a repository, or
	// "workspace:<remote>+<remote>…" for a directory holding repositories.
	Identity string
	Dir      string
	// Tracked lists the synced paths git tracks; they are left out.
	Tracked []string
}

// Root returns the project's root name: it depends only on the identity.
func (p Project) Root() string { return RootName(p.Identity) }

// RootName returns the root name of an identity.
func RootName(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return RootPrefix + hex.EncodeToString(sum[:])[:12]
}

// Skipped is a project directory that is not synced, and why.
type Skipped struct {
	Dir    string
	Reason string
}

// Discover finds the projects of this machine: the directories Claude Code
// and Codex list as projects that can be identified by their git remotes.
func Discover(d platform.Dirs) ([]Project, []Skipped) {
	return discover(d, candidates(d), gitTracked)
}

func discover(d platform.Dirs, dirs []string, tracked func(dir string) ([]string, error)) ([]Project, []Skipped) {
	var skipped []Skipped
	byIdentity := map[string][]Project{}
	var seen []os.FileInfo
	for _, dir := range dirs {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() || sameAsAny(fi, seen) || isWorktreePath(dir) || inToolDirs(d, fi) {
			continue // gone, a duplicate spelling, a worktree or a tool's own directory
		}
		seen = append(seen, fi)
		identity, isRepo, reason := Identify(dir)
		if identity == "" {
			if reason != "" {
				skipped = append(skipped, Skipped{Dir: dir, Reason: reason})
			}
			continue
		}
		p := Project{Identity: identity, Dir: dir}
		if isRepo {
			paths, err := tracked(dir)
			if err != nil {
				skipped = append(skipped, Skipped{Dir: dir, Reason: "cannot ask git which files it tracks: " + err.Error()})
				continue
			}
			p.Tracked = paths
		}
		byIdentity[identity] = append(byIdentity[identity], p)
	}

	var out []Project
	for _, ps := range byIdentity {
		if len(ps) > 1 {
			for _, p := range ps {
				skipped = append(skipped, Skipped{Dir: p.Dir, Reason: "another directory here is the same project; neither is synced"})
			}
			continue
		}
		out = append(out, ps[0])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Dir < skipped[j].Dir })
	return out, skipped
}

func sameAsAny(fi os.FileInfo, seen []os.FileInfo) bool {
	for _, s := range seen {
		if os.SameFile(fi, s) {
			return true
		}
	}
	return false
}

// inToolDirs reports the home directory and the tools' own directories,
// which Claude Code also lists as projects when it was started there.
func inToolDirs(d platform.Dirs, fi os.FileInfo) bool {
	for _, p := range []string{d.Home, d.Claude, d.Codex, d.Agents, d.State} {
		if o, err := os.Stat(p); err == nil && os.SameFile(fi, o) {
			return true
		}
	}
	return false
}

func isWorktreePath(dir string) bool {
	s := filepath.ToSlash(dir)
	return strings.Contains(s, "/.claude/worktrees/") || strings.Contains(s, "/.claude-worktrees/")
}

// candidates are the directories the tools list as projects.
func candidates(d platform.Dirs) []string {
	var out []string
	if data, err := os.ReadFile(d.ClaudeJSON); err == nil {
		var doc struct {
			Projects map[string]json.RawMessage `json:"projects"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for p := range doc.Projects {
				out = append(out, p)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(d.Codex, "config.toml")); err == nil {
		var doc struct {
			Projects map[string]any `toml:"projects"`
		}
		if toml.Unmarshal(data, &doc) == nil {
			for p := range doc.Projects {
				out = append(out, p)
			}
		}
	}
	for i, p := range out {
		out[i] = filepath.Clean(p)
	}
	sort.Strings(out)
	return out
}

// Identify returns what identifies dir on every machine. isRepo is true for
// a repository. An empty identity comes with the reason, or none when dir is
// simply not a project worth mentioning (no repository at all).
func Identify(dir string) (identity string, isRepo bool, reason string) {
	gitPath := filepath.Join(dir, ".git")
	if fi, err := os.Lstat(gitPath); err == nil {
		if !fi.IsDir() {
			return "", false, "a git worktree or submodule; its main checkout is synced instead"
		}
		remote := originURL(gitPath)
		if remote == "" {
			return "", false, "a git repository with no origin remote"
		}
		return "repo:" + remote, true, ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false, ""
	}
	var remotes []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(dir, e.Name(), ".git")
		if fi, err := os.Lstat(child); err == nil && fi.IsDir() {
			if remote := originURL(child); remote != "" {
				remotes = append(remotes, remote)
			}
		}
	}
	if len(remotes) == 0 {
		return "", false, ""
	}
	sort.Strings(remotes)
	return "workspace:" + strings.Join(remotes, "+"), false, ""
}

// originURL reads the normalized URL of the origin remote from a .git
// directory's config.
func originURL(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return ""
	}
	inOrigin := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.EqualFold(strings.Join(strings.Fields(line), " "), `[remote "origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && strings.EqualFold(strings.TrimSpace(key), "url") {
			return NormalizeRemote(strings.Trim(strings.TrimSpace(value), `"`))
		}
	}
	return ""
}

// NormalizeRemote returns host/path for a git remote URL, the same for its
// https and ssh forms and without user names, ports or a .git suffix. Local
// paths, which differ between machines, give "".
func NormalizeRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	var host, path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "file" || u.Hostname() == "" {
			return ""
		}
		host, path = u.Hostname(), u.Path
	default:
		// scp-like: [user@]host:path, with no slash before the colon. A
		// single letter is a Windows drive, as git reads it.
		colon := strings.Index(raw, ":")
		if colon <= 1 || strings.Contains(raw[:colon], "/") {
			return ""
		}
		host, path = raw[:colon], raw[colon+1:]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return ""
	}
	return strings.ToLower(host + "/" + path)
}

// gitTracked lists the synced paths git tracks in a repository.
func gitTracked(dir string) ([]string, error) {
	args := []string{"-C", dir, "ls-files", "-z", "--"}
	seen := map[string]bool{}
	for _, p := range Include {
		prefix := strings.TrimSuffix(p, "/**")
		if !seen[prefix] {
			seen[prefix] = true
			args = append(args, prefix)
		}
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// Roots returns a root for each project.
func Roots(ps []Project) []tools.Root {
	var out []tools.Root
	for _, p := range ps {
		exclude := append([]string{}, tools.CommonExclude...)
		for _, t := range p.Tracked {
			exclude = append(exclude, escapeGlob(t))
		}
		out = append(out, tools.Root{
			Name:    p.Root(),
			Dir:     p.Dir,
			Include: append([]string{}, Include...),
			Exclude: exclude,
			Project: p.Identity,
		})
	}
	return out
}

// escapeGlob makes a path match itself as a doublestar pattern.
func escapeGlob(p string) string {
	var b strings.Builder
	for _, r := range p {
		if strings.ContainsRune(`*?[]{}\\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
