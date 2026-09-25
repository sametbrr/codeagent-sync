//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndReadLink(t *testing.T) {
	home := t.TempDir()
	skill := filepath.Join(home, ".agents", "skills", "foo")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(home, ".claude", "skills", "foo")
	kind, err := CreateLink(link, "../../.agents/skills/foo", true)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if kind != LinkSymlink {
		t.Errorf("kind = %q, want %q", kind, LinkSymlink)
	}
	if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md is not reachable through the link: %v", err)
	}

	target, kind, ok, err := ReadLink(link)
	if err != nil || !ok {
		t.Fatalf("ReadLink = %q, %q, %v, %v", target, kind, ok, err)
	}
	if target != "../../.agents/skills/foo" || kind != LinkSymlink {
		t.Errorf("ReadLink = %q (%s), want ../../.agents/skills/foo (symlink)", target, kind)
	}

	if _, err := CreateLink(link, "../../.agents/skills/foo", true); err == nil {
		t.Error("CreateLink over an existing entry should fail")
	}
}

func TestReadLinkOnRegularEntries(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok, err := ReadLink(dir); ok || err != nil {
		t.Errorf("ReadLink(dir) = ok %v, err %v; want not a link", ok, err)
	}
	if _, _, _, err := ReadLink(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("ReadLink(missing) error = %v, want not-exist", err)
	}
}
