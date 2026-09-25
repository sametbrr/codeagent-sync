package compat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
)

// mcpServers are each tool's MCP servers, as generic values.
type mcpServers struct {
	claude map[string]map[string]any
	codex  map[string]map[string]any
}

func (m mcpServers) has(to Direction, server string) bool {
	if to == ToCodex {
		_, ok := m.codex[server]
		return ok
	}
	_, ok := m.claude[server]
	return ok
}

func loadMCP(d platform.Dirs) (mcpServers, error) {
	out := mcpServers{claude: map[string]map[string]any{}, codex: map[string]map[string]any{}}
	if data, err := os.ReadFile(d.ClaudeJSON); err == nil {
		var doc struct {
			MCPServers map[string]map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return out, fmt.Errorf("%s: %w", d.ClaudeJSON, err)
		}
		if doc.MCPServers != nil {
			out.claude = doc.MCPServers
		}
	}
	codexConfig := filepath.Join(d.Codex, "config.toml")
	if data, err := os.ReadFile(codexConfig); err == nil {
		var doc struct {
			MCPServers map[string]map[string]any `toml:"mcp_servers"`
		}
		if err := toml.Unmarshal(data, &doc); err != nil {
			return out, fmt.Errorf("%s: %w", codexConfig, err)
		}
		if doc.MCPServers != nil {
			out.codex = doc.MCPServers
		}
	}
	return out, nil
}

func mcpCandidates(env Env, mcp mcpServers) []Candidate {
	var out []Candidate
	for _, name := range sortedKeys(mcp.claude) {
		if _, ok := mcp.codex[name]; ok || env.Registry.Get(registry.MCP, name) != "" {
			continue
		}
		_, reasons := ClaudeToCodex(name, mcp.claude[name], env.Exists)
		out = append(out, Candidate{Kind: registry.MCP, Name: name, Direction: ToCodex, Reasons: reasons,
			Verdict: verdictOf(reasons), Suggest: "add it to ~/.codex/config.toml as [mcp_servers." + name + "]"})
	}
	for _, name := range sortedKeys(mcp.codex) {
		if _, ok := mcp.claude[name]; ok || env.Registry.Get(registry.MCP, name) != "" {
			continue
		}
		_, reasons := CodexToClaude(name, mcp.codex[name], env.Exists)
		out = append(out, Candidate{Kind: registry.MCP, Name: name, Direction: ToClaude, Reasons: reasons,
			Verdict: verdictOf(reasons), Suggest: "add it to the mcpServers of ~/.claude.json"})
	}
	return out
}

var (
	codexName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	pureVar   = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	bearerVar = regexp.MustCompile(`^Bearer \$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

// ClaudeToCodex converts a Claude Code MCP server into the TOML of Codex's
// [mcp_servers.<name>] table, following the rules of Codex's own importer:
// Codex does not expand ${VAR}, so only whole-value variables carry over
// (as env_vars, env_http_headers or bearer_token_env_var).
func ClaudeToCodex(name string, s map[string]any, exists func(string) bool) (string, []Reason) {
	var reasons []Reason
	hard := func(text string) { reasons = append(reasons, Reason{Hard: true, Text: text}) }
	if !codexName.MatchString(name) {
		hard("Codex allows only letters, digits, - and _ in server names")
	}
	typ, command, url := str(s["type"]), str(s["command"]), str(s["url"])
	if typ == "sse" {
		hard("Codex does not support SSE servers")
	}

	var main, env, headers, envHeaders []string
	switch {
	case command != "":
		if strings.Contains(command, "${") {
			hard("the command uses ${…}, which Codex does not expand")
		}
		if isAbs(command) && exists != nil && !exists(command) {
			reasons = append(reasons, Reason{Text: "its command " + command + " is not on this machine; install it first"})
		}
		main = append(main, kv("command", command))
		if args := strs(s["args"]); len(args) > 0 {
			for _, a := range args {
				if strings.Contains(a, "${") {
					hard("an argument uses ${…}, which Codex does not expand")
				}
			}
			main = append(main, kv("args", args))
		}
		var envVars []string
		for _, k := range sortedKeys(anyMap(s["env"])) {
			v := str(anyMap(s["env"])[k])
			switch m := pureVar.FindStringSubmatch(v); {
			case m != nil && m[1] == k:
				envVars = append(envVars, k)
			case strings.Contains(v, "${"):
				hard("the env value of " + k + " uses ${…} in a way Codex cannot express")
			default:
				env = append(env, kv(k, v))
			}
		}
		if len(envVars) > 0 {
			main = append(main, kv("env_vars", envVars))
		}
	case url != "":
		if strings.Contains(url, "${") {
			hard("the URL uses ${…}, which Codex does not expand")
		}
		main = append(main, kv("url", url))
		for _, h := range sortedKeys(anyMap(s["headers"])) {
			v := str(anyMap(s["headers"])[h])
			if m := bearerVar.FindStringSubmatch(v); m != nil && strings.EqualFold(h, "Authorization") {
				main = append(main, kv("bearer_token_env_var", m[1]))
			} else if m := pureVar.FindStringSubmatch(v); m != nil {
				envHeaders = append(envHeaders, kv(h, m[1]))
			} else if strings.Contains(v, "${") {
				hard("the header " + h + " uses ${…} in a way Codex cannot express")
			} else {
				headers = append(headers, kv(h, v))
			}
		}
	default:
		hard("it has neither a command nor a URL")
	}

	table := "mcp_servers." + tomlKey(name)
	var b strings.Builder
	section := func(header string, lines []string) {
		if len(lines) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[" + header + "]\n" + strings.Join(lines, ""))
	}
	section(table, main)
	section(table+".env", env)
	section(table+".http_headers", headers)
	section(table+".env_http_headers", envHeaders)
	return b.String(), reasons
}

// CodexToClaude converts a Codex MCP server into a Claude Code mcpServers
// entry. Servers that come with the Codex app cannot move.
func CodexToClaude(name string, s map[string]any, exists func(string) bool) (map[string]any, []Reason) {
	var reasons []Reason
	command, url := str(s["command"]), str(s["url"])
	out := map[string]any{}
	switch {
	case command != "":
		if strings.HasPrefix(command, "./") || strings.Contains(command, ".app/") || strings.Contains(command, "/Applications/") {
			reasons = append(reasons, Reason{Hard: true, Text: "it comes with the Codex app (" + command + ")"})
		}
		for k := range anyMap(s["env"]) {
			if strings.HasPrefix(k, "CODEX_") {
				reasons = append(reasons, Reason{Hard: true, Text: "it depends on the Codex app's environment (" + k + ")"})
				break
			}
		}
		if isAbs(command) && exists != nil && !exists(command) && len(reasons) == 0 {
			reasons = append(reasons, Reason{Text: "its command " + command + " is not on this machine; install it first"})
		}
		out["type"], out["command"] = "stdio", command
		if args := strs(s["args"]); len(args) > 0 {
			out["args"] = args
		}
		env := map[string]any{}
		for k, v := range anyMap(s["env"]) {
			env[k] = str(v)
		}
		for _, k := range strs(s["env_vars"]) {
			env[k] = "${" + k + "}"
		}
		if len(env) > 0 {
			out["env"] = env
		}
	case url != "":
		out["type"], out["url"] = "http", url
		headers := map[string]any{}
		for k, v := range anyMap(s["http_headers"]) {
			headers[k] = str(v)
		}
		for k, v := range anyMap(s["env_http_headers"]) {
			headers[k] = "${" + str(v) + "}"
		}
		if t := str(s["bearer_token_env_var"]); t != "" {
			headers["Authorization"] = "Bearer ${" + t + "}"
		}
		if len(headers) > 0 {
			out["headers"] = headers
		}
	default:
		reasons = append(reasons, Reason{Hard: true, Text: "it has neither a command nor a URL"})
	}
	if enabled, ok := s["enabled"].(bool); ok && !enabled {
		reasons = append(reasons, Reason{Text: "it is disabled in Codex"})
	}
	return out, reasons
}

func kv(key string, v any) string {
	out, err := toml.Marshal(map[string]any{key: v})
	if err != nil {
		return ""
	}
	return string(out)
}

func tomlKey(k string) string {
	if codexName.MatchString(k) {
		return k
	}
	q, _ := json.Marshal(k)
	return string(q)
}

func isAbs(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strs(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
	case []string:
		out = x
	}
	return out
}

func anyMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Server returns the definition of an MCP server in the tool that has it
// now: Claude Code for ToCodex, Codex for ToClaude.
func Server(d platform.Dirs, name string, from Direction) (map[string]any, error) {
	mcp, err := loadMCP(d)
	if err != nil {
		return nil, err
	}
	servers := mcp.claude
	if from == ToClaude {
		servers = mcp.codex
	}
	s, ok := servers[name]
	if !ok {
		return nil, fmt.Errorf("%s has no MCP server %q", from.Tool(), name)
	}
	return s, nil
}
