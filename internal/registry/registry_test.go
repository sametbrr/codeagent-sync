package registry

import (
	"reflect"
	"testing"
)

func TestRegistryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	if err != nil || len(r.Decisions) != 0 {
		t.Fatalf("empty Load = %+v, %v", r, err)
	}
	r.Set(Skill, "impeccable", Variant)
	r.Set(Skill, "skill-publish", ClaudeOnly)
	r.Set(MCP, "node_repl", CodexOnly)
	r.Set(Skill, "forgotten", Shared)
	r.Set(Skill, "forgotten", "")
	if err := r.Save(dir); err != nil {
		t.Fatal(err)
	}

	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Get(Skill, "skill-publish") != ClaudeOnly || back.Get(MCP, "node_repl") != CodexOnly || back.Get(Skill, "forgotten") != "" {
		t.Errorf("decisions = %v", back.Decisions)
	}
	if !reflect.DeepEqual(back.Variants(), map[string]bool{"impeccable": true}) {
		t.Errorf("variants = %v", back.Variants())
	}
}

func TestSaveKeepsDecisionsMadeElsewhere(t *testing.T) {
	dir := t.TempDir()
	first, _ := Load(dir)
	second, _ := Load(dir)
	first.Set(Skill, "a", ClaudeOnly)
	if err := first.Save(dir); err != nil {
		t.Fatal(err)
	}
	second.Set(Skill, "b", Shared)
	if err := second.Save(dir); err != nil {
		t.Fatal(err)
	}
	second.Set(Skill, "a", "")
	if err := second.Save(dir); err != nil {
		t.Fatal(err)
	}
	back, _ := Load(dir)
	if !reflect.DeepEqual(back.Decisions, map[string]string{"skill/b": Shared}) {
		t.Errorf("decisions = %v", back.Decisions)
	}
}
