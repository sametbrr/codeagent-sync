package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomicCreatesAndReplaces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "settings.json")

	if err := WriteFileAtomic(p, []byte("one"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two" {
		t.Errorf("content = %q, want %q", data, "two")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("perm = %o, want 600", fi.Mode().Perm())
		}
	}
	assertOnlyEntries(t, filepath.Dir(p), "settings.json")
}

func TestWriteFileAtomicLeavesNoTempFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "occupied")
	// A non-empty directory at the target makes the final rename fail.
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(target, []byte("data"), 0o644); err == nil {
		t.Fatal("expected an error when the target is a directory")
	}
	assertOnlyEntries(t, dir, "occupied")
}

func assertOnlyEntries(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(names) {
		t.Fatalf("entries in %s = %v, want %v", dir, got, names)
	}
	for i := range names {
		if got[i] != names[i] {
			t.Fatalf("entries in %s = %v, want %v", dir, got, names)
		}
	}
}
