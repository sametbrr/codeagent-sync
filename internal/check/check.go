// Package check looks for configurations that the agent tools silently
// ignore or read differently from what was intended: broken skill links,
// skills Codex skips, agents in a format Codex does not load, and a skill
// that exists separately for each tool.
package check

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// Finding is one thing to look at.
type Finding struct {
	Path   string
	Reason string
	Hint   string
}

// Run checks the tool directories. variants names skills that deliberately
// exist separately for Claude and for Codex.
func Run(d platform.Dirs, variants map[string]bool) []Finding {
	var out []Finding
	claudeSkills := filepath.Join(d.Claude, "skills")
	agentsSkills := filepath.Join(d.Agents, "skills")

	for _, name := range dirNames(claudeSkills) {
		if name == "synced" {
			continue // the Claude app's own skills
		}
		p := filepath.Join(claudeSkills, name)
		if target, _, isLink, _ := platform.ReadLink(p); isLink {
			if _, err := os.Stat(p); err != nil {
				out = append(out, Finding{p, "the link points to " + target + ", which does not exist", "run a sync, or remove the link"})
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(p, tools.LinkMarker)); err == nil {
			continue // a managed copy; the sync keeps it
		}
		if isDir(filepath.Join(agentsSkills, name)) && !variants[name] {
			out = append(out, Finding{p, "a skill with this name also exists separately for Codex (" + filepath.Join(agentsSkills, name) + "); the two tools see different versions",
				"if intended, mark it: codeagent-sync mark " + name + " --variant"})
		}
		out = append(out, missingSkillFile(p)...)
	}

	for _, name := range dirNames(agentsSkills) {
		p := filepath.Join(agentsSkills, name)
		md := filepath.Join(p, "SKILL.md")
		if fi, err := os.Lstat(md); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			out = append(out, Finding{md, "Codex ignores a SKILL.md that is itself a symlink", "replace it with the file"})
			continue
		}
		out = append(out, missingSkillFile(p)...)
	}

	agents, _ := filepath.Glob(filepath.Join(d.Codex, "agents", "*.md"))
	for _, p := range agents {
		out = append(out, Finding{p, "Codex loads only .toml agent files; this one is ignored", "convert it to .toml or move it to ~/.claude/agents"})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func missingSkillFile(dir string) []Finding {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); errors.Is(err, fs.ErrNotExist) {
		return []Finding{{dir, "no SKILL.md: neither tool loads this skill", "add SKILL.md or remove the directory"}}
	}
	return nil
}

// dirNames lists the entries of dir that are directories or links to them.
func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if _, _, isLink, _ := platform.ReadLink(p); isLink || e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func isDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}
