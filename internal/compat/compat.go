// Package compat finds skills, MCP servers and plugin marketplaces that one
// of Claude Code and Codex has and the other does not, and judges whether
// they would work in the other tool too.
//
// The judgement is static and explainable: every reason names the file and
// line it comes from. Hard reasons (a tool-specific API, a transport the
// other tool lacks) block sharing; soft ones (a general mention of
// subagents, a path that may not exist on every machine) only warn.
package compat

import (
	"path/filepath"
	"strings"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
)

// Direction says which tool gets the thing when it is shared.
type Direction string

const (
	ToCodex  Direction = "to-codex"  // Claude Code has it now
	ToClaude Direction = "to-claude" // Codex has it now
)

// Tool is the tool that has the thing now.
func (d Direction) Tool() string {
	if d == ToCodex {
		return "Claude Code"
	}
	return "Codex"
}

// Target is the tool that would get it.
func (d Direction) Target() string {
	if d == ToCodex {
		return "Codex"
	}
	return "Claude Code"
}

// Verdict is the judgement.
type Verdict string

const (
	OK      Verdict = "ok"      // works in the other tool as it is
	Warn    Verdict = "warn"    // works, but something may need adapting
	Blocked Verdict = "blocked" // depends on the tool it is in
	Variant Verdict = "variant" // both tools have their own, different version
)

// Reason explains a verdict.
type Reason struct {
	Hard bool   `json:"hard"`
	Text string `json:"text"`
	File string `json:"file,omitempty"` // relative to the skill's directory
	Line int    `json:"line,omitempty"`
}

// Candidate is something one tool has and the other does not.
type Candidate struct {
	Kind      string    `json:"kind"` // registry.Skill, registry.MCP or registry.Plugin
	Name      string    `json:"name"`
	Direction Direction `json:"direction"`
	Verdict   Verdict   `json:"verdict"`
	Reasons   []Reason  `json:"reasons,omitempty"`
	// Installer names what installed it, when known ("notebooklm CLI").
	Installer string `json:"installer,omitempty"`
	// Suggest says what sharing does, or the command it runs.
	Suggest string `json:"suggest"`
	// Path is a skill's directory.
	Path string `json:"path,omitempty"`
	// Source is a plugin marketplace's source (owner/repo).
	Source string `json:"source,omitempty"`
}

// Env is what a scan looks at.
type Env struct {
	Dirs     platform.Dirs
	Registry *registry.Registry
	// Exists reports whether a command path exists on this machine.
	Exists func(string) bool
}

// short writes a path under the home directory as ~/…, for messages.
func (env Env) short(p string) string {
	if rel, err := filepath.Rel(env.Dirs.Home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join("~", rel)
	}
	return p
}

// Scan returns the undecided candidates of every kind.
func Scan(env Env) ([]Candidate, error) {
	mcp, err := loadMCP(env.Dirs)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	out = append(out, skillCandidates(env, mcp)...)
	out = append(out, mcpCandidates(env, mcp)...)
	plugins, err := pluginCandidates(env)
	if err != nil {
		return nil, err
	}
	return append(out, plugins...), nil
}

func verdictOf(reasons []Reason) Verdict {
	v := OK
	for _, r := range reasons {
		if r.Hard {
			return Blocked
		}
		v = Warn
	}
	return v
}
