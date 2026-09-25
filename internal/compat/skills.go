package compat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// marker is text that ties a skill to one tool.
type marker struct {
	re *regexp.Regexp
	// tool is the tool the marker ties the skill to ("" for either).
	tool Direction
	// hard markers block sharing; in markdown, path markers only warn.
	hard     bool
	scripts  bool // hard only in scripts (a path the skill writes to)
	text     string
	mcpGroup int // for mcp__server__ references: the group with the server
}

var markers = []marker{
	{re: regexp.MustCompile(`CLAUDE_PLUGIN_ROOT|\$\{?CLAUDE_[A-Z_]+`), tool: ToCodex, hard: true, text: "uses a Claude Code variable"},
	{re: regexp.MustCompile("!`"), tool: ToCodex, hard: true, text: "runs a command inline (!`…`), which only Claude Code does"},
	{re: regexp.MustCompile(`\$ARGUMENTS\b`), tool: ToCodex, hard: true, text: "takes $ARGUMENTS, which only Claude Code fills in"},
	{re: regexp.MustCompile(`\b(AskUserQuestion|TodoWrite|ExitPlanMode)\b`), tool: ToCodex, hard: true, text: "calls a Claude Code tool"},
	{re: regexp.MustCompile(`\bSkill tool\b`), tool: ToCodex, hard: true, text: "calls Claude Code's Skill tool"},
	{re: regexp.MustCompile(`~/\.claude/`), tool: ToCodex, scripts: true, text: "uses ~/.claude"},
	{re: regexp.MustCompile(`\brequest_user_input\b|\bapply_patch\b`), tool: ToClaude, hard: true, text: "calls a Codex tool"},
	{re: regexp.MustCompile(`~/\.codex/`), tool: ToClaude, scripts: true, text: "uses ~/.codex"},
	{re: regexp.MustCompile(`mcp__([A-Za-z0-9_-]+)__`), text: "uses the MCP server %s, which the other tool does not have", mcpGroup: 1},
	{re: regexp.MustCompile(`(?i)\bsub-?agents?\b|\bTask\(`), text: "starts subagents; the other tool does it differently"},
	{re: regexp.MustCompile(`(?i)\bslash command\b|(^|\s)/loop\b`), text: "refers to slash commands or /loop"},
	{re: regexp.MustCompile(`/Users/[^/\s"']+/|/home/[^/\s"']+/|[A-Za-z]:\\\\?Users\\\\?`), scripts: true, text: "contains a path of one machine"},
}

var scriptExt = map[string]bool{".sh": true, ".bash": true, ".zsh": true, ".py": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".rb": true, ".ps1": true, ".cmd": true, ".bat": true}

const maxReasonsPerText = 2

// skillCandidates finds skills only one tool has. A Claude skill is a real
// directory in ~/.claude/skills; a Codex one sits in ~/.agents/skills with
// no Claude link to it. Identical copies in both places are candidates too;
// different ones are variants.
func skillCandidates(env Env, mcp mcpServers) []Candidate {
	claudeSkills := filepath.Join(env.Dirs.Claude, "skills")
	agentsSkills := filepath.Join(env.Dirs.Agents, "skills")
	lock := skillsLock(env.Dirs)
	var out []Candidate

	for _, name := range dirs(claudeSkills) {
		p := filepath.Join(claudeSkills, name)
		if name == "synced" || isLinkOrCopy(p) || env.Registry.Get(registry.Skill, name) != "" {
			continue
		}
		c := Candidate{Kind: registry.Skill, Name: name, Direction: ToCodex, Path: p}
		detectInstaller(&c, p, lock)
		if other := filepath.Join(agentsSkills, name); isRealDir(other) {
			if SameTree(p, other) {
				c.Verdict = OK
				c.Suggest = "the same files are already shared with Codex; replace this copy with a link to " + env.short(other)
			} else {
				c.Verdict = Variant
				c.Reasons = []Reason{{Hard: true, Text: "Codex has its own, different version in " + env.short(other)}}
				c.Suggest = "keep one version per tool"
			}
			out = append(out, c)
			continue
		}
		c.Reasons = evaluateSkill(p, ToCodex, mcp)
		c.Verdict = verdictOf(c.Reasons)
		if c.Installer == "impeccable" {
			c.Verdict = Blocked
			c.Reasons = append(c.Reasons, Reason{Hard: true, Text: "impeccable installs a separate version for each tool; install it for Codex with its own installer"})
		}
		if c.Suggest == "" {
			c.Suggest = "move it to " + env.short(filepath.Join(agentsSkills, name)) + " and link it back into " + env.short(claudeSkills)
		}
		out = append(out, c)
	}

	for _, name := range dirs(agentsSkills) {
		p := filepath.Join(agentsSkills, name)
		if exists(filepath.Join(claudeSkills, name)) || env.Registry.Get(registry.Skill, name) != "" {
			continue // linked (shared), or a Claude copy handled above
		}
		c := Candidate{Kind: registry.Skill, Name: name, Direction: ToClaude, Path: p}
		detectInstaller(&c, p, lock)
		c.Reasons = evaluateSkill(p, ToClaude, mcp)
		c.Verdict = verdictOf(c.Reasons)
		if c.Installer == "impeccable" {
			c.Verdict = Blocked
			c.Reasons = append(c.Reasons, Reason{Hard: true, Text: "impeccable installs a separate version for each tool; install it for Claude Code with its own installer"})
		}
		c.Suggest = "link it into " + env.short(claudeSkills)
		out = append(out, c)
	}
	return out
}

// evaluateSkill reads a skill's text files for signs that tie it to the tool
// it is in.
func evaluateSkill(dir string, to Direction, mcp mcpServers) []Reason {
	var reasons []Reason
	counts := map[string]int{}
	add := func(r Reason) {
		key := r.Text
		if counts[key] >= maxReasonsPerText {
			return
		}
		counts[key]++
		reasons = append(reasons, r)
	}

	skillMD := filepath.Join(dir, "SKILL.md")
	if fi, err := os.Lstat(skillMD); err == nil && fi.Mode()&fs.ModeSymlink != 0 && to == ToCodex {
		add(Reason{Hard: true, Text: "SKILL.md is a symlink, which Codex ignores", File: "SKILL.md"})
	}
	if name, desc, ok := frontmatter(skillMD); ok {
		if len(name) > 64 {
			add(Reason{Text: "the name is longer than the 64 characters Codex allows", File: "SKILL.md"})
		}
		if len(desc) > 1024 {
			add(Reason{Text: "the description is longer than the 1024 characters Codex allows", File: "SKILL.md"})
		}
	}

	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || tools.IsLitter(d.Name()) {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if err := platform.ValidateRelPath(rel, platform.Windows); err != nil {
			add(Reason{Text: "a file name Windows does not allow: " + rel, File: rel})
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		script := scriptExt[strings.ToLower(filepath.Ext(p))]
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for line := 1; sc.Scan(); line++ {
			for _, m := range markers {
				if m.tool != "" && m.tool != to {
					continue
				}
				// A machine's paths only matter where a script uses them;
				// documentation shows example paths all the time.
				if m.scripts && m.tool == "" && !script {
					continue
				}
				for _, match := range m.re.FindAllStringSubmatch(sc.Text(), -1) {
					text := m.text
					// A tool's own directory is a hard tie where a script
					// reads or writes it; in prose it only warns.
					hard := m.hard || (m.scripts && m.tool != "" && script)
					if m.mcpGroup > 0 {
						server := match[m.mcpGroup]
						if mcp.has(to, server) {
							continue
						}
						text = strings.Replace(text, "%s", server, 1)
						hard = true
					}
					add(Reason{Hard: hard, Text: text, File: rel, Line: line})
				}
			}
		}
		return nil
	})
	sort.SliceStable(reasons, func(i, j int) bool { return reasons[i].Hard && !reasons[j].Hard })
	return reasons
}

// frontmatter reads name and description from a SKILL.md.
func frontmatter(path string) (name, desc string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(data, []byte("---")) {
		return "", "", false
	}
	end := bytes.Index(data[3:], []byte("\n---"))
	if end < 0 {
		return "", "", false
	}
	for _, line := range strings.Split(string(data[3:3+end]), "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "description":
			desc = v
		}
	}
	return name, desc, true
}

// DisablesModelInvocation reports whether a skill may only be started by the
// user (Claude's disable-model-invocation), which Codex expresses in
// agents/openai.yaml.
func DisablesModelInvocation(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return false
	}
	return regexp.MustCompile(`(?m)^disable-model-invocation:\s*true\s*$`).Match(data)
}

// detectInstaller records what installed a skill, when it is known.
func detectInstaller(c *Candidate, dir string, lock map[string]string) {
	data, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	switch {
	case bytes.Contains(data, []byte("notebooklm-py")):
		c.Installer = "notebooklm CLI"
		if c.Direction == ToCodex {
			c.Suggest = "notebooklm skill install --scope user --target agents, then link it back into Claude"
		}
	case c.Name == "impeccable" || exists(filepath.Join(dir, "scripts", "impeccable")):
		c.Installer = "impeccable"
	case lock[c.Name] != "":
		c.Installer = "npx skills (" + lock[c.Name] + ")"
	}
}

// skillsLock reads the `npx skills` lock file: skill name to source.
func skillsLock(d platform.Dirs) map[string]string {
	out := map[string]string{}
	for _, p := range []string{filepath.Join(d.Home, "skills-lock.json"), filepath.Join(d.Agents, ".skill-lock.json")} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var lock struct {
			Skills map[string]struct {
				Source string `json:"source"`
			} `json:"skills"`
		}
		if json.Unmarshal(data, &lock) == nil {
			for name, s := range lock.Skills {
				out[name] = s.Source
			}
		}
	}
	return out
}

// SameTree reports whether two directories hold the same files.
func SameTree(a, b string) bool {
	ha, err1 := treeFiles(a)
	hb, err2 := treeFiles(b)
	if err1 != nil || err2 != nil || len(ha) != len(hb) {
		return false
	}
	for k, v := range ha {
		if hb[k] != v {
			return false
		}
	}
	return true
}

func treeFiles(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || tools.IsLitter(d.Name()) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = envelope.Hash(data)
		return nil
	})
	return out, err
}

func dirs(parent string) []string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(parent, e.Name())
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func isLinkOrCopy(p string) bool {
	if _, _, isLink, _ := platform.ReadLink(p); isLink {
		return true
	}
	return exists(filepath.Join(p, tools.LinkMarker))
}

func isRealDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir() && !isLinkOrCopy(p)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return !errors.Is(err, fs.ErrNotExist)
}
