package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/structured"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// Structured files (settings.json, .claude.json, config.toml) merge item by
// item. Merging three ways needs what both sides had at the last sync: the
// base, kept per entry under the state directory.
const baseDir = "base"

func (e *Engine) basePath(key string) string {
	root, rel, _ := entry.ParseKey(key)
	return filepath.Join(e.StateDir, baseDir, root, filepath.FromSlash(rel))
}

// saveBase keeps what both sides agreed on, for the next merge. It is best
// effort: without a base, the next change on both sides is reported as a
// conflict instead of merged, and nothing is lost.
func (e *Engine) saveBase(key string, synced []byte) {
	_ = platform.WriteFileAtomic(e.basePath(key), synced, 0o600)
}

func (e *Engine) loadBase(key string) []byte {
	data, err := os.ReadFile(e.basePath(key))
	if err != nil {
		return nil
	}
	return data
}

func (e *Engine) dropBase(key string) { os.Remove(e.basePath(key)) }

// format returns the structured format of key, if it has one.
func (e *Engine) format(key string) (structured.Format, bool) {
	root, rel, ok := entry.ParseKey(key)
	if !ok {
		return nil, false
	}
	r, found := tools.Find(e.Roots, root)
	if !found || r.Format(rel) == "" {
		return nil, false
	}
	f, ok := structured.Get(r.Format(rel))
	if !ok {
		return nil, false
	}
	return structured.Hiding(f, r.HiddenItems(rel)), true
}

// pinHidden makes the items this machine keeps to itself (rules like
// claude/settings.json#permissions) read as their last synced versions, so
// they are neither sent nor taken away from other machines.
func (e *Engine) pinHidden(l *entry.Entry) {
	r, found := tools.Find(e.Roots, l.Root)
	if !found || len(r.HiddenItems(l.Rel)) == 0 {
		return
	}
	f, ok := e.format(l.Key())
	if !ok {
		return
	}
	items, err := f.Parse(l.Content)
	if err != nil {
		return
	}
	base, _ := f.Parse(e.loadBase(l.Key()))
	items = structured.Pin(items, base, r.HiddenItems(l.Rel))
	l.Content = f.Render(items)
	l.Hash = structured.Fingerprint(items)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// withHeld adds to this machine's synced form of a structured file the items
// it holds back (MCP servers whose command is missing here), taken from the
// base: they are absent from the file on purpose and must not look deleted.
func (e *Engine) withHeld(l *entry.Entry, held []string) {
	f, ok := e.format(l.Key())
	if !ok || len(held) == 0 {
		return
	}
	base, err := f.Parse(e.loadBase(l.Key()))
	if err != nil {
		return
	}
	items, err := f.Parse(l.Content)
	if err != nil {
		return
	}
	have := map[string]bool{}
	for _, it := range items {
		have[it.Name] = true
	}
	for _, name := range held {
		for _, it := range base {
			if it.Name == name && !have[name] {
				items = append(items, it)
			}
		}
	}
	l.Content = f.Render(items)
	l.Hash = structured.Fingerprint(items)
}

// mergeStructured turns a decision on a structured file into an item-level
// merge where it can: a conflict whose changes touch different items, and a
// first sync where both sides have the file.
func (e *Engine) mergeStructured(a *Action) {
	f, ok := e.format(a.Key)
	if !ok || a.Local == nil || a.Remote == nil || a.Remote.Header.Kind != envelope.KindFile {
		return
	}
	firstSync := a.Kind == Download && a.KeepLocalCopy
	if a.Kind != Conflict && !firstSync {
		return
	}
	local, err1 := f.Parse(a.Local.Content)
	remote, err2 := f.Parse(a.Remote.Content)
	if err1 != nil || err2 != nil {
		return
	}
	var base []structured.Item
	if !firstSync {
		if base, err1 = f.Parse(e.loadBase(a.Key)); err1 != nil {
			return
		}
	}
	merged, conflicts := structured.Merge3(base, local, remote, firstSync)
	if len(conflicts) > 0 {
		a.Note = "changed differently on both sides: " + strings.Join(conflicts, ", ")
		return
	}
	switch fp := structured.Fingerprint(merged); fp {
	case a.Remote.Fingerprint:
		a.Kind, a.IfMatch = Download, ""
	case a.Local.Hash:
		a.Kind, a.IfMatch = Upload, a.Remote.ETag
	default:
		a.Kind, a.IfMatch, a.Merged = Merge, a.Remote.ETag, f.Render(merged)
	}
	if firstSync {
		a.Note = "merged with the shared version (the shared value wins where both set one); this machine's copy was saved to the conflicts directory"
	} else {
		a.KeepLocalCopy = false
		a.Note = "merged: the two machines changed different settings"
	}
}

// protectStructured keeps sync from ever deleting a structured file:
// .claude.json also holds Claude Code's own state for this machine, and a
// shared settings file that disappeared somewhere is not a decision to
// remove it everywhere. A file deleted here is restored from the shared
// version; a deletion recorded elsewhere is answered by uploading this
// machine's version again.
func (e *Engine) protectStructured(a *Action) {
	if _, ok := e.format(a.Key); !ok {
		return
	}
	switch {
	case a.Kind == UploadDelete:
		if a.Remote != nil && a.Remote.Header.Kind == envelope.KindFile {
			a.Kind, a.Note = Download, "restored: sync never deletes this file"
		} else {
			a.Kind, a.Note = Forget, ""
		}
	case a.Kind == RemoveLocal && a.Local != nil:
		a.Kind, a.IfMatch, a.Note = Upload, a.Remote.ETag, "kept: sync never deletes this file"
	}
}

// released reports the held items of a structured file that this machine can
// use now (their command got installed), and the synced form to write them.
func (e *Engine) released(key string, s *EntryState) (content []byte, names []string) {
	f, ok := e.format(key)
	if !ok || s == nil || len(s.Held) == 0 {
		return nil, nil
	}
	base := e.loadBase(key)
	items, err := f.Parse(e.Mapper.ToLocal(base))
	if err != nil {
		return nil, nil
	}
	still := structured.HeldNames(f, items, fileExists)
	for _, name := range s.Held {
		if _, held := still[name]; !held {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	return base, names
}

// writeStructured writes a structured file's synced form into the file on
// disk, holding back the items this machine cannot use. It returns the
// fingerprint the scanner will see (held items included) and the held names.
func (e *Engine) writeStructured(p, key string, synced []byte) (string, []string, map[string]string, error) {
	f, ok := e.format(key)
	if !ok {
		return "", nil, nil, fmt.Errorf("%s is not a structured file", key)
	}
	items, err := f.Parse(e.Mapper.ToLocal(synced))
	if err != nil {
		return "", nil, nil, err
	}
	heldReasons := structured.HeldNames(f, items, fileExists)
	keep := map[string]bool{}
	for name := range heldReasons {
		keep[name] = true
	}

	current, err := os.ReadFile(p)
	mode := fs.FileMode(0o644)
	switch {
	case err == nil:
		if fi, serr := os.Stat(p); serr == nil {
			mode = fi.Mode().Perm()
		}
	case errors.Is(err, fs.ErrNotExist):
		current = nil
	default:
		return "", nil, nil, err
	}
	out, err := f.Apply(current, items, keep)
	if err != nil {
		return "", nil, nil, err
	}
	if err := clearPath(p); err != nil {
		return "", nil, nil, err
	}
	if err := platform.WriteFileAtomic(p, out, mode); err != nil {
		return "", nil, nil, err
	}

	written, err := f.Project(e.Mapper.ToPortable(out))
	if err != nil {
		return "", nil, nil, err
	}
	have := map[string]bool{}
	for _, it := range written {
		have[it.Name] = true
	}
	portable, _ := f.Parse(synced)
	var held []string
	for _, it := range portable {
		if keep[it.Name] && !have[it.Name] {
			written = append(written, it)
		}
		if keep[it.Name] {
			held = append(held, it.Name)
		}
	}
	sort.Strings(held)
	return structured.Fingerprint(written), held, heldReasons, nil
}
