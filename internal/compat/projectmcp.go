package compat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/sametbrr/codeagent-sync/internal/projects"
	"github.com/sametbrr/codeagent-sync/internal/registry"
)

// A project keeps its own MCP servers: Claude Code in .mcp.json, Codex in
// .codex/config.toml, which Codex reads only in projects it trusts.
const (
	ProjectClaudeMCP = ".mcp.json"
	ProjectCodexMCP  = ".codex/config.toml"
)

// projectMCP loads a project's MCP servers of both tools.
func projectMCP(dir string) (mcpServers, error) {
	out := mcpServers{claude: map[string]map[string]any{}, codex: map[string]map[string]any{}}
	claudeFile := filepath.Join(dir, ProjectClaudeMCP)
	if data, err := os.ReadFile(claudeFile); err == nil {
		var doc struct {
			MCPServers map[string]map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return out, fmt.Errorf("%s: %w", claudeFile, err)
		}
		if doc.MCPServers != nil {
			out.claude = doc.MCPServers
		}
	}
	codexFile := filepath.Join(dir, filepath.FromSlash(ProjectCodexMCP))
	if data, err := os.ReadFile(codexFile); err == nil {
		var doc struct {
			MCPServers map[string]map[string]any `toml:"mcp_servers"`
		}
		if err := toml.Unmarshal(data, &doc); err != nil {
			return out, fmt.Errorf("%s: %w", codexFile, err)
		}
		if doc.MCPServers != nil {
			out.codex = doc.MCPServers
		}
	}
	return out, nil
}

// projectMCPCandidates are the project MCP servers one tool has and the
// other does not, per project.
func projectMCPCandidates(env Env) ([]Candidate, error) {
	found := env.Projects
	if found == nil {
		found, _ = projects.Discover(env.Dirs)
	}
	trusted := codexTrusted(env)
	var out []Candidate
	for _, p := range found {
		mcp, err := projectMCP(p.Dir)
		if err != nil {
			return nil, err
		}
		where := env.short(p.Dir)
		for _, name := range sortedKeys(mcp.claude) {
			if _, ok := mcp.codex[name]; ok {
				continue
			}
			c := Candidate{Kind: registry.MCP, Name: name, Direction: ToCodex, Project: p.Dir, Scope: p.Root()}
			if env.Registry.Get(registry.MCP, c.ID()) != "" {
				continue
			}
			_, c.Reasons = ClaudeToCodex(name, mcp.claude[name], env.Exists)
			if !trusted[filepath.Clean(p.Dir)] {
				c.Reasons = append(c.Reasons, Reason{Text: "Codex reads " + ProjectCodexMCP + " only in projects it trusts; trust " + where + " in Codex"})
			}
			c.Verdict = verdictOf(c.Reasons)
			c.Suggest = "add it to " + filepath.Join(where, filepath.FromSlash(ProjectCodexMCP)) + " as [mcp_servers." + name + "]"
			out = append(out, c)
		}
		for _, name := range sortedKeys(mcp.codex) {
			if _, ok := mcp.claude[name]; ok {
				continue
			}
			c := Candidate{Kind: registry.MCP, Name: name, Direction: ToClaude, Project: p.Dir, Scope: p.Root()}
			if env.Registry.Get(registry.MCP, c.ID()) != "" {
				continue
			}
			_, c.Reasons = CodexToClaude(name, mcp.codex[name], env.Exists)
			c.Verdict = verdictOf(c.Reasons)
			c.Suggest = "add it to the mcpServers of " + filepath.Join(where, ProjectClaudeMCP)
			out = append(out, c)
		}
	}
	return out, nil
}

// codexTrusted returns the project directories Codex trusts.
func codexTrusted(env Env) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(env.Dirs.Codex, "config.toml"))
	if err != nil {
		return out
	}
	var doc struct {
		Projects map[string]struct {
			TrustLevel string `toml:"trust_level"`
		} `toml:"projects"`
	}
	if toml.Unmarshal(data, &doc) != nil {
		return out
	}
	for dir, p := range doc.Projects {
		if p.TrustLevel == "trusted" {
			out[filepath.Clean(dir)] = true
		}
	}
	return out
}

// ProjectServer returns the definition of a project MCP server in the tool
// that has it now: Claude Code for ToCodex, Codex for ToClaude.
func ProjectServer(dir, name string, from Direction) (map[string]any, error) {
	mcp, err := projectMCP(dir)
	if err != nil {
		return nil, err
	}
	servers := mcp.claude
	if from == ToClaude {
		servers = mcp.codex
	}
	s, ok := servers[name]
	if !ok {
		return nil, fmt.Errorf("%s has no MCP server %q in %s", from.Tool(), name, dir)
	}
	return s, nil
}
