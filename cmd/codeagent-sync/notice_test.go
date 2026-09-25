package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNoticesAreToldOnce(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	queue := func(ids ...string) {
		t.Helper()
		var items []noticeItem
		for _, id := range ids {
			items = append(items, noticeItem{ID: id, Text: "about " + id})
		}
		if err := queueNotice(dir, items, true, now); err != nil {
			t.Fatal(err)
		}
	}
	take := func() string {
		t.Helper()
		text, err := takeNotice(dir, now)
		if err != nil {
			t.Fatal(err)
		}
		return text
	}

	queue("recorded:skill:x", "share:skill:y")
	if got := take(); !strings.Contains(got, "about recorded:skill:x") || !strings.Contains(got, "about share:skill:y") {
		t.Fatalf("first notice:\n%s", got)
	}
	queue("share:skill:y")
	if got := take(); got != "" {
		t.Errorf("told again:\n%s", got)
	}

	// A one-shot item survives a second background sync before the prompt.
	queue("recorded:skill:z")
	queue()
	if got := take(); !strings.Contains(got, "about recorded:skill:z") {
		t.Errorf("one-shot item lost:\n%s", got)
	}

	// A lasting item that is over is forgotten, and told again if it returns.
	queue("conflict:v1/claude/CLAUDE.md")
	take()
	queue()
	queue("conflict:v1/claude/CLAUDE.md")
	if got := take(); !strings.Contains(got, "about conflict:v1/claude/CLAUDE.md") {
		t.Errorf("returning conflict not told:\n%s", got)
	}
	// One that ends before it is told is not told.
	queue("share:mcp:docs")
	queue()
	if got := take(); got != "" {
		t.Errorf("told about something that is over:\n%s", got)
	}

	// A one-shot notice is told each time it happens.
	queue("notice:Codex hooks changed; approve them with /hooks")
	take()
	queue("notice:Codex hooks changed; approve them with /hooks")
	if got := take(); !strings.Contains(got, "/hooks") {
		t.Errorf("repeated one-shot notice dropped:\n%s", got)
	}

	// What only a sync can find stays told while syncs are skipped.
	queue("pending")
	take()
	if err := queueNotice(dir, nil, false, now); err != nil {
		t.Fatal(err)
	}
	queue("pending")
	if got := take(); got != "" {
		t.Errorf("pending told again after a skipped sync:\n%s", got)
	}
}

func TestNotifiedFileWithNull(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := saveJSON(filepath.Join(dir, notifiedFile), nil); err != nil {
		t.Fatal(err)
	}
	if err := queueNotice(dir, []noticeItem{{ID: "share:skill:x", Text: "x"}}, true, now); err != nil {
		t.Fatal(err)
	}
	if got, err := takeNotice(dir, now); err != nil || !strings.Contains(got, "x") {
		t.Errorf("takeNotice = %q, %v", got, err)
	}
}
