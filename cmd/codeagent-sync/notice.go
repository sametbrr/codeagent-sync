package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sametbrr/codeagent-sync/internal/compat"
	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/registry"
)

// The hooks tell the agent what the user should hear about, once each.
const (
	noticeFile   = "notice.json"   // waiting for the next prompt
	notifiedFile = "notified.json" // told already: id -> when
	staleAfter   = 24 * time.Hour  // a sync failing for this long is news
)

// noticeItem is one thing the user should hear about.
type noticeItem struct {
	ID   string `json:"id"` // what it is about, so it is told once
	Text string `json:"text"`
}

const noticeHeader = "codeagent-sync, which keeps the user's Claude Code and Codex configuration the same on all their machines, " +
	"has news for the user. Tell them briefly, in their language, once; run one of these commands only when they ask for it:"

// gatherNotice collects what the user should hear about now. Things only
// one tool has are judged as scan judges them: what depends on its tool is
// recorded as such and mentioned once; the rest is offered.
func gatherNotice(s *sharing, eng *engine.Engine, res *engine.Result, syncErr error, now time.Time) []noticeItem {
	var items []noticeItem
	if syncErr != nil {
		last, _, _ := eng.LastSync()
		if now.Sub(last) > staleAfter {
			text := fmt.Sprintf("It has not synced for %s: %v. To see why: codeagent-sync doctor", ago(now.Sub(last)), syncErr)
			if last.IsZero() {
				text = fmt.Sprintf("It has never synced on this machine: %v. To see why: codeagent-sync doctor", syncErr)
			}
			items = append(items, noticeItem{ID: "error:" + now.Format("2006-01-02"), Text: text})
		}
	}
	if copies, err := eng.Conflicts(); err == nil {
		seen := map[string]bool{}
		for _, c := range copies {
			if seen[c.Key] {
				continue
			}
			seen[c.Key] = true
			items = append(items, noticeItem{ID: "conflict:" + c.Key,
				Text: fmt.Sprintf("%s was changed differently on two machines. To see both versions: codeagent-sync conflicts; to settle it: codeagent-sync conflicts resolve %s --keep local|remote",
					displayPath(c.Key), displayPath(c.Key))})
		}
	}
	if res != nil && len(res.Pending) > 0 {
		items = append(items, noticeItem{ID: "pending",
			Text: fmt.Sprintf("%d files of this machine are not uploaded until the user confirms its first sync. To list them: codeagent-sync status; to upload them: codeagent-sync sync --yes", len(res.Pending))})
	}
	if res != nil {
		for _, n := range res.Notices {
			items = append(items, noticeItem{ID: "notice:" + n, Text: n})
		}
	}

	cands, err := compat.Scan(s.env)
	if err != nil {
		return items
	}
	recorded := false
	for _, c := range cands {
		id := c.Kind + ":" + c.Name
		switch c.Verdict {
		case compat.Blocked, compat.Variant:
			decision := toolOnly(c.Direction)
			what := "stays with " + c.Direction.Tool()
			if c.Verdict == compat.Variant {
				decision, what = registry.Variant, "stays as one version per tool"
			}
			s.env.Registry.Set(c.Kind, c.Name, decision)
			recorded = true
			items = append(items, noticeItem{ID: "recorded:" + id,
				Text: fmt.Sprintf("The %s %s (new in %s) %s: %s. Nothing to do; to have it judged again: codeagent-sync mark %s --forget",
					kindName(c.Kind), c.Name, c.Direction.Tool(), what, firstText(c), id)})
		default:
			text := fmt.Sprintf("The %s %s is only in %s and would work in %s too", kindName(c.Kind), c.Name, c.Direction.Tool(), c.Direction.Target())
			if c.Verdict == compat.Warn {
				text = fmt.Sprintf("The %s %s is only in %s and may work in %s (%s)", kindName(c.Kind), c.Name, c.Direction.Tool(), c.Direction.Target(), firstText(c))
			}
			items = append(items, noticeItem{ID: "share:" + id,
				Text: text + fmt.Sprintf(". To share it: codeagent-sync share %s --yes; to keep it with %s only: codeagent-sync mark %s %s",
					id, c.Direction.Tool(), id, "--"+toolOnly(c.Direction))})
		}
	}
	if recorded {
		if err := s.save(); err != nil {
			hookLog(s.dirs.State, "record decisions: "+err.Error(), now)
		}
	}
	return items
}

func kindName(kind string) string {
	switch kind {
	case registry.MCP:
		return "MCP server"
	case registry.Plugin:
		return "plugin marketplace"
	}
	return kind
}

// oneShot reports whether an item is told about when it happens (a
// decision recorded, a notice of the sync) rather than for as long as it
// lasts (a conflict, something to share). One-shot items are told every
// time they happen.
func oneShot(id string) bool {
	return strings.HasPrefix(id, "recorded:") || strings.HasPrefix(id, "notice:")
}

// needsSync reports whether only a sync that ran can tell whether an item
// still holds.
func needsSync(id string) bool { return id == "pending" || strings.HasPrefix(id, "error:") }

// forget reports whether having told about an item can be forgotten now
// that it was not found: a lasting item that is over is forgotten at once,
// so that it is told again should it come back.
func forget(id string, when time.Time, synced bool, now time.Time) bool {
	switch {
	case strings.HasPrefix(id, "error:"):
		return now.Sub(when) > 2*staleAfter
	case needsSync(id):
		return synced
	}
	return true
}

// lockNotice serializes the hooks' use of the notice files.
func lockNotice(stateDir string) (*platform.FileLock, error) {
	return platform.Lock(filepath.Join(stateDir, "notice.lock"), 5*time.Second)
}

// queueNotice keeps what the user has not been told about for the next
// prompt: the items found now, and one-shot items queued earlier. synced
// says whether a sync ran, so that items only a sync can find are known.
func queueNotice(stateDir string, items []noticeItem, synced bool, now time.Time) error {
	lock, err := lockNotice(stateDir)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	told := loadNotified(stateDir)
	current := map[string]bool{}
	var queue []noticeItem
	for _, it := range loadQueue(stateDir) {
		if oneShot(it.ID) {
			current[it.ID] = true
			queue = append(queue, it)
		}
	}
	for _, it := range items {
		if current[it.ID] {
			continue
		}
		current[it.ID] = true
		if _, ok := told[it.ID]; !ok || oneShot(it.ID) {
			queue = append(queue, it)
		}
	}
	for id, when := range told {
		if !current[id] && forget(id, when, synced, now) {
			delete(told, id)
		}
	}
	if err := saveJSON(filepath.Join(stateDir, notifiedFile), told); err != nil {
		return err
	}
	path := filepath.Join(stateDir, noticeFile)
	if len(queue) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return saveJSON(path, queue)
}

// takeNotice returns the queued notice as text for the agent, and marks
// its items told.
func takeNotice(stateDir string, now time.Time) (string, error) {
	path := filepath.Join(stateDir, noticeFile)
	if _, err := os.Stat(path); err != nil {
		return "", nil // nothing queued: the common case, kept cheap
	}
	lock, err := lockNotice(stateDir)
	if err != nil {
		return "", err
	}
	defer lock.Unlock()

	items := loadQueue(stateDir)
	if len(items) == 0 {
		os.Remove(path)
		return "", nil
	}
	told := loadNotified(stateDir)
	var b strings.Builder
	b.WriteString(noticeHeader + "\n")
	for _, it := range items {
		b.WriteString("- " + it.Text + "\n")
		if !oneShot(it.ID) {
			told[it.ID] = now
		}
	}
	if err := saveJSON(filepath.Join(stateDir, notifiedFile), told); err != nil {
		return "", err
	}
	return b.String(), os.Remove(path)
}

func loadQueue(stateDir string) []noticeItem {
	var items []noticeItem
	if data, err := os.ReadFile(filepath.Join(stateDir, noticeFile)); err == nil {
		json.Unmarshal(data, &items)
	}
	return items
}

func loadNotified(stateDir string) map[string]time.Time {
	told := map[string]time.Time{}
	if data, err := os.ReadFile(filepath.Join(stateDir, notifiedFile)); err == nil {
		json.Unmarshal(data, &told)
	}
	if told == nil { // the file said null
		told = map[string]time.Time{}
	}
	return told
}

func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return platform.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

// hookLog appends a line to the log of background syncs, keeping it short.
func hookLog(stateDir, line string, now time.Time) {
	path := filepath.Join(stateDir, "hook.log")
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(data) == 0 {
		lines = nil
	}
	lines = append(lines, now.Format(time.RFC3339)+" "+line)
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	platform.WriteFileAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}
