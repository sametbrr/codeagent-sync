package engine

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/structured"
)

const settingsRel = ".claude/settings.json"

func jsonField(t *testing.T, m *machine, rel, field string) any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(m.read(rel)), &doc); err != nil {
		t.Fatalf("%s is not JSON: %v", rel, err)
	}
	return doc[field]
}

func sharedSettings(t *testing.T) (*world, *machine, *machine) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(settingsRel, `{"model": "opus", "effortLevel": "high", "hooks": {}}`)
	a.sync(Options{AdoptLocal: true})
	b := w.machine(platform.Current())
	b.sync(Options{})
	if got := jsonField(t, b, settingsRel, "model"); got != "opus" {
		t.Fatalf("b's settings.json model = %v", got)
	}
	return w, a, b
}

func TestSettingsChangedOnBothSidesMerge(t *testing.T) {
	_, a, b := sharedSettings(t)
	a.write(settingsRel, `{"model": "sonnet", "effortLevel": "high", "hooks": {}}`)
	b.write(settingsRel, `{
  "model": "opus",
  "effortLevel": "max",
  "hooks": {}
}`)
	a.sync(Options{})

	res := b.sync(Options{})
	if len(res.Conflicts) != 0 || find(res.Actions, Merge, "v1/claude/settings.json") == nil {
		t.Fatalf("expected a merge, got actions %v conflicts %v", kinds(res.Actions), kinds(res.Conflicts))
	}
	for m, name := range map[*machine]string{b: "b"} {
		if jsonField(t, m, settingsRel, "model") != "sonnet" || jsonField(t, m, settingsRel, "effortLevel") != "max" {
			t.Errorf("%s did not merge both changes:\n%s", name, m.read(settingsRel))
		}
	}
	a.sync(Options{})
	if jsonField(t, a, settingsRel, "effortLevel") != "max" || jsonField(t, a, settingsRel, "model") != "sonnet" {
		t.Errorf("a did not get the merge:\n%s", a.read(settingsRel))
	}
	a.quiet()
	b.quiet()
}

func TestSameSettingChangedDifferentlyConflicts(t *testing.T) {
	_, a, b := sharedSettings(t)
	a.write(settingsRel, `{"model": "sonnet", "effortLevel": "high", "hooks": {}}`)
	b.write(settingsRel, `{"model": "haiku", "effortLevel": "high", "hooks": {}}`)
	a.sync(Options{})

	res := b.sync(Options{})
	c := find(res.Conflicts, Conflict, "v1/claude/settings.json")
	if c == nil || !strings.Contains(c.Note, "model") {
		t.Fatalf("expected a conflict on model, got %v", kinds(res.Conflicts))
	}
	if got := jsonField(t, b, settingsRel, "model"); got != "haiku" {
		t.Errorf("b's value was replaced: %v", got)
	}
}

// A merge whose upload loses a race must not be lost: the next round merges
// again with the newer remote version.
func TestMergeSurvivesALostRace(t *testing.T) {
	w, a, b := sharedSettings(t)
	a.write(settingsRel, `{"model": "sonnet", "effortLevel": "high", "hooks": {}}`)
	a.sync(Options{})
	b.write(settingsRel, `{"model": "opus", "effortLevel": "max", "hooks": {}}`)

	raced := false
	w.store.BeforePut = func(key string) {
		if key != "v1/claude/settings.json" || raced {
			return
		}
		raced = true
		w.store.BeforePut = nil
		a.write(settingsRel, `{"model": "sonnet", "effortLevel": "high", "hooks": {"Stop": []}}`)
		a.sync(Options{})
	}
	b.sync(Options{})
	if !raced {
		t.Fatal("the race was not staged")
	}
	a.sync(Options{})
	for _, m := range []*machine{a, b} {
		doc := m.read(settingsRel)
		if jsonField(t, m, settingsRel, "model") != "sonnet" || jsonField(t, m, settingsRel, "effortLevel") != "max" || !strings.Contains(doc, `"Stop"`) {
			t.Errorf("a change was lost:\n%s", doc)
		}
	}
}

func TestClaudeJSONSyncsOnlyMCPServers(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(".claude.json", `{"numStartups": 7, "oauthAccount": {"email": "a@example.com"},
  "mcpServers": {"codegraph": {"type": "stdio", "command": "codegraph"}}}`)
	a.sync(Options{AdoptLocal: true})

	b := w.machine(platform.Current())
	b.write(".claude.json", `{"numStartups": 99, "oauthAccount": {"email": "b@example.com"},
  "mcpServers": {"dokploy": {"type": "stdio", "command": "dokploy-mcp"}}}`)
	b.sync(Options{})

	got := b.read(".claude.json")
	for _, want := range []string{`"numStartups": 99`, `b@example.com`, `"codegraph"`, `"dokploy"`} {
		if !strings.Contains(got, want) {
			t.Errorf("b's .claude.json lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a@example.com") {
		t.Errorf("a's account leaked into b:\n%s", got)
	}
	a.sync(Options{})
	if !strings.Contains(a.read(".claude.json"), `"dokploy"`) || !strings.Contains(a.read(".claude.json"), `"numStartups": 7`) {
		t.Errorf("a did not get b's server or lost its state:\n%s", a.read(".claude.json"))
	}
}

func TestServersWithMissingCommandsAreHeldAndReleased(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write("bin/tool-mcp", "#!/bin/sh\n")
	a.write(".claude.json", `{"mcpServers": {
  "tool": {"type": "stdio", "command": "`+a.path("bin/tool-mcp")+`"},
  "codegraph": {"type": "stdio", "command": "codegraph"}}}`)
	a.sync(Options{AdoptLocal: true})

	b := w.machine(platform.Current())
	res := b.sync(Options{})
	got := b.read(".claude.json")
	if strings.Contains(got, `"tool"`) || !strings.Contains(got, `"codegraph"`) {
		t.Fatalf("b should hold the server whose command is missing:\n%s", got)
	}
	if len(res.Notices) == 0 || !strings.Contains(strings.Join(res.Notices, " "), "tool") {
		t.Errorf("no notice about the held server: %q", res.Notices)
	}
	b.quiet() // held is not deleted
	a.sync(Options{})
	if !strings.Contains(a.read(".claude.json"), `"tool"`) {
		t.Fatal("b's holding deleted the server on a")
	}

	b.write("bin/tool-mcp", "#!/bin/sh\n")
	res = b.sync(Options{})
	if !strings.Contains(b.read(".claude.json"), `"tool"`) {
		t.Errorf("the server was not written once its command arrived; actions %v", kinds(res.Actions))
	}
	if !strings.Contains(b.read(".claude.json"), b.path("bin/tool-mcp")) {
		t.Errorf("the command path was not translated to b's home:\n%s", b.read(".claude.json"))
	}
	b.quiet()
}

func TestCodexConfigKeepsMachineState(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(".codex/config.toml", `model = "gpt-5.5"

[projects."`+a.home+`/Projects/x"]
trust_level = "trusted"

[features]
multi_agent = true
`)
	a.sync(Options{AdoptLocal: true})

	b := w.machine(platform.Current())
	b.write(".codex/config.toml", `model = "gpt-5.5"

[projects."`+b.home+`/work/y"]
trust_level = "trusted"
`)
	b.sync(Options{})
	a.write(".codex/config.toml", strings.Replace(a.read(".codex/config.toml"), `"gpt-5.5"`, `"gpt-6"`, 1))
	a.sync(Options{})
	b.sync(Options{})

	got := b.read(".codex/config.toml")
	var doc map[string]any
	if err := toml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("invalid TOML on b: %v\n%s", err, got)
	}
	if doc["model"] != "gpt-6" || !strings.Contains(got, "[features]") {
		t.Errorf("shared settings did not arrive:\n%s", got)
	}
	if !strings.Contains(got, "/work/y") || strings.Contains(got, "/Projects/x") {
		t.Errorf("trusted projects must stay per machine:\n%s", got)
	}
	a.quiet()
	b.quiet()
}

func TestStructuredFilesAreNeverDeleted(t *testing.T) {
	w, a, b := sharedSettings(t)
	a.remove(settingsRel)
	res := a.sync(Options{})
	if dl := find(res.Actions, Download, "v1/claude/settings.json"); dl == nil {
		t.Fatalf("the deleted settings.json should be restored: %v", kinds(res.Actions))
	}
	if got := jsonField(t, a, settingsRel, "model"); got != "opus" {
		t.Errorf("restored settings.json = %v", got)
	}
	// No tombstone ever reached the store.
	data, _, err := w.store.Get(context.Background(), "v1/claude/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if h, _, _ := envelope.Open(w.cipher, "v1/claude/settings.json", data); h.Kind != envelope.KindFile {
		t.Errorf("remote settings.json became a %s", h.Kind)
	}
	b.quiet()
	if _, err := os.Stat(b.path(settingsRel)); err != nil {
		t.Error(err)
	}
}

func TestDecisionsRecordedOnTwoMachinesMerge(t *testing.T) {
	const rel = ".codeagent-sync/registry.yaml"
	decisions := func(d map[string]string) string { return string(structured.RenderDecisions(d)) }
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(rel, decisions(map[string]string{"plugin/skill-hub": "claude-only"}))
	a.sync(Options{AdoptLocal: true})
	b := w.machine(platform.Current())
	b.sync(Options{})

	a.write(rel, decisions(map[string]string{"plugin/skill-hub": "claude-only", "skill/x": "claude-only"}))
	b.write(rel, decisions(map[string]string{"plugin/skill-hub": "claude-only", "skill/y": "variant"}))
	a.sync(Options{})
	if res := b.sync(Options{}); len(res.Conflicts) != 0 {
		t.Fatalf("conflicts: %v", kinds(res.Conflicts))
	}
	a.sync(Options{})
	want := decisions(map[string]string{"plugin/skill-hub": "claude-only", "skill/x": "claude-only", "skill/y": "variant"})
	for name, m := range map[string]*machine{"a": a, "b": b} {
		if got := m.read(rel); got != want {
			t.Errorf("%s has:\n%s", name, got)
		}
	}
	a.quiet()
	b.quiet()
}
