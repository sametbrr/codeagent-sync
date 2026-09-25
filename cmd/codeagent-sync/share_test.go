package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScanShareUnshareAndMark(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	var ran []string
	runTool = func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { runTool = nil })

	m := newCLIMachine(t)
	m.write(".claude/skills/plain/SKILL.md", "---\nname: plain\ndescription: Formats dates.\n---\nFormat the date.\n")
	m.write(".claude/skills/hooky/SKILL.md", "---\nname: hooky\ndescription: Uses arguments.\n---\nRun it with $ARGUMENTS.\n")
	m.write(".claude.json", `{"mcpServers":{"docs":{"command":"npx","args":["-y","docs-mcp"]}}}`)
	m.write(".claude/plugins/known_marketplaces.json", `{"tools":{"source":{"source":"github","repo":"someone/tools"}}}`)

	var report scanReport
	if err := json.Unmarshal([]byte(m.mustRun("--json", "scan")), &report); err != nil {
		t.Fatal(err)
	}
	var shareable, recorded []string
	for _, c := range report.Shareable {
		shareable = append(shareable, c.Kind+":"+c.Name)
	}
	for _, c := range report.Recorded {
		recorded = append(recorded, c.Kind+":"+c.Name)
	}
	if got := strings.Join(shareable, " "); got != "skill:plain mcp:docs plugin:tools" {
		t.Errorf("shareable = %s", got)
	}
	if got := strings.Join(recorded, " "); got != "skill:hooky" {
		t.Errorf("recorded = %s", got)
	}
	if reg := m.read(".codeagent-sync/registry.yaml"); !strings.Contains(reg, "skill/hooky: claude-only") {
		t.Errorf("registry after scan:\n%s", reg)
	}

	// Without a terminal and without --yes, scan only lists.
	if out := m.mustRun("scan"); !strings.Contains(out, "codeagent-sync share <name>") || strings.Contains(out, "Shared") {
		t.Errorf("scan without a terminal:\n%s", out)
	}
	if _, err := os.Lstat(filepath.Join(m.home, ".agents/skills/plain")); err == nil {
		t.Fatal("scan without --yes shared a skill")
	}

	out := m.mustRun("scan", "--yes")
	for _, want := range []string{"Shared skill plain", "Shared mcp docs", "Shared plugin tools"} {
		if !strings.Contains(out, want) {
			t.Errorf("scan --yes lacks %q:\n%s", want, out)
		}
	}
	if got := m.read(".claude/skills/plain/SKILL.md"); !strings.Contains(got, "Format the date.") {
		t.Errorf("skill through the link = %q", got)
	}
	if target, err := os.Readlink(filepath.Join(m.home, ".claude/skills/plain")); err != nil || target != "../../.agents/skills/plain" {
		t.Errorf("link = %q, %v", target, err)
	}
	if cfg := m.read(".codex/config.toml"); !strings.Contains(cfg, "[mcp_servers.docs]") {
		t.Errorf("config.toml:\n%s", cfg)
	}
	if len(ran) != 1 || ran[0] != "codex plugin marketplace add someone/tools" {
		t.Errorf("ran %q", ran)
	}
	if out := m.mustRun("scan"); !strings.Contains(out, "Nothing new to share") {
		t.Errorf("second scan:\n%s", out)
	}

	m.mustRun("unshare", "plain", "--to", "claude")
	if fi, err := os.Lstat(filepath.Join(m.home, ".claude/skills/plain")); err != nil || !fi.IsDir() {
		t.Fatalf("after unshare: %v, %v", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(m.home, ".agents/skills/plain")); !os.IsNotExist(err) {
		t.Errorf("shared copy left behind: %v", err)
	}
	if reg := m.read(".codeagent-sync/registry.yaml"); !strings.Contains(reg, "skill/plain: claude-only") {
		t.Errorf("registry after unshare:\n%s", reg)
	}

	m.mustRun("mark", "skill:plain", "--forget")
	if out := m.mustRun("share", "plain", "--yes"); !strings.Contains(out, "Shared skill plain") {
		t.Errorf("share:\n%s", out)
	}
	m.mustRun("undo", "--yes")
	if fi, err := os.Lstat(filepath.Join(m.home, ".claude/skills/plain")); err != nil || !fi.IsDir() {
		t.Errorf("undo did not take the share back: %v, %v", fi, err)
	}
	if reg := m.read(".codeagent-sync/registry.yaml"); strings.Contains(reg, "skill/plain") {
		t.Errorf("undo left the decision:\n%s", reg)
	}

	if code, _, errOut := m.run("mark", "nosuch", "--shared"); code != 1 || !strings.Contains(errOut, "no skill, MCP server or plugin marketplace is called nosuch") {
		t.Errorf("mark nosuch: exit %d, %s", code, errOut)
	}
	if code, _, errOut := m.run("mark", "plain"); code != 1 || !strings.Contains(errOut, "exactly one of") {
		t.Errorf("mark without a decision: exit %d, %s", code, errOut)
	}
	if code, _, errOut := m.run("share", "hooky"); code != 1 || !strings.Contains(errOut, "not something only one tool has") {
		t.Errorf("share of a decided skill: exit %d, %s", code, errOut)
	}
	m.mustRun("mark", "mcp:docs", "--variant")
	if reg := m.read(".codeagent-sync/registry.yaml"); !strings.Contains(reg, "mcp/docs: variant") {
		t.Errorf("registry after mark:\n%s", reg)
	}
}

func TestPathsRules(t *testing.T) {
	m := newCLIMachine(t)
	m.mustRun("paths", "include", "claude/plans/**")
	m.mustRun("paths", "exclude", "claude/settings.json#permissions", "--local")
	if got := m.read(".codeagent-sync/sync.yaml"); !strings.Contains(got, "claude/plans/**") {
		t.Errorf("sync.yaml:\n%s", got)
	}
	if got := m.read(".codeagent-sync/sync.local.yaml"); !strings.Contains(got, "permissions") {
		t.Errorf("sync.local.yaml:\n%s", got)
	}
	out := m.mustRun("paths")
	for _, want := range []string{"plans/**", "kept per machine: settings.json#permissions"} {
		if !strings.Contains(out, want) {
			t.Errorf("paths lacks %q:\n%s", want, out)
		}
	}
	if code, _, errOut := m.run("paths", "include", "claude/projects/**"); code != 1 || !strings.Contains(errOut, "never synced") {
		t.Errorf("including sessions: exit %d, %s", code, errOut)
	}
	m.mustRun("paths", "reset", "claude/plans/**")
	if got := m.read(".codeagent-sync/sync.yaml"); strings.Contains(got, "plans") {
		t.Errorf("reset left the rule:\n%s", got)
	}
}
