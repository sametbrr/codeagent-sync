package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/homepath"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// apply carries out the actions: first on this machine's disk, backing up
// everything it touches, then the uploads. It reports whether an upload lost
// a race against another machine.
func (e *Engine) apply(ctx context.Context, st *State, actions []*Action, res *Result, opts Options) (bool, error) {
	var backup *backupSet
	if touchesDisk(actions) {
		what := "sync"
		if opts.Background {
			what = "background sync"
		}
		var err error
		if backup, err = e.newBackup(what); err != nil {
			return false, fmt.Errorf("prepare backup: %w", err)
		}
		if err := writeJournal(e.StateDir, backup.ID); err != nil {
			return false, err
		}
		res.BackupID = backup.ID
	}

	var uploads []*Action
	for _, a := range actions {
		switch a.Kind {
		case Download, RemoveLocal, Merge:
			notice, err := e.applyLocal(a, st, backup)
			if err != nil {
				res.Problems = append(res.Problems, entry.Problem{Key: a.Key, Reason: err.Error()})
				continue
			}
			if notice != "" {
				res.Notices = append(res.Notices, notice)
			}
			if a.Kind == Merge {
				uploads = append(uploads, a) // reported once uploaded
				continue
			}
		case Conflict:
			if err := e.saveConflict(a, sideRemote); err != nil {
				res.Problems = append(res.Problems, entry.Problem{Key: a.Key, Reason: "saving the other version: " + err.Error()})
			}
			continue // reported in res.Conflicts; the state stays as it was
		case Record:
			st.Entries[a.Key] = e.recorded(a, st.Entries[a.Key])
		case Forget:
			delete(st.Entries, a.Key)
			e.dropBase(a.Key)
		case Upload, UploadDelete:
			uploads = append(uploads, a)
			continue
		}
		res.Actions = append(res.Actions, a)
	}

	retry := e.uploadAll(ctx, st, uploads, res)

	if backup != nil {
		if err := backup.close(); err != nil {
			return retry, fmt.Errorf("write backup manifest: %w", err)
		}
		e.pruneBackups()
	}
	st.LastSync = e.now().UTC()
	if len(res.Pending) == 0 && opts.Mode != PullOnly {
		// A pull does not look at what only this machine has.
		settled := true
		st.Settled = &settled
	}
	if err := st.Save(filepath.Join(e.StateDir, stateFile)); err != nil {
		return retry, fmt.Errorf("save state: %w", err)
	}
	return retry, clearJournal(e.StateDir)
}

func touchesDisk(actions []*Action) bool {
	for _, a := range actions {
		if a.Kind == Download || a.Kind == RemoveLocal || a.Kind == Merge {
			return true
		}
	}
	return false
}

// recorded is the state of an entry both sides already agree on.
func (e *Engine) recorded(a *Action, prev EntryState) EntryState {
	etag := ""
	if a.Remote != nil {
		etag = a.Remote.ETag
	}
	if a.Local == nil {
		e.dropBase(a.Key)
		return EntryState{ETag: etag, Kind: envelope.KindTombstone}
	}
	s := stateOf(a.Local, etag)
	if a.Local.Format != "" {
		s.Held = prev.Held
		e.saveBase(a.Key, a.Local.Content)
	}
	return s
}

// uploadAll stores the uploads in parallel. Each is conditional, so one that
// finds the remote object changed since it was read is skipped and reported
// as a lost race.
func (e *Engine) uploadAll(ctx context.Context, st *State, uploads []*Action, res *Result) (retry bool) {
	previous := map[string]EntryState{}
	for _, a := range uploads {
		if s, ok := st.Entries[a.Key]; ok {
			previous[a.Key] = s
		}
	}

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fetchWorkers)
	for _, a := range uploads {
		g.Go(func() error {
			newState, err := e.upload(gctx, a, previous[a.Key], st.Machine)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, storage.ErrPreconditionFailed):
				retry = true
			case err != nil:
				res.Problems = append(res.Problems, entry.Problem{Key: a.Key, Reason: err.Error()})
			default:
				st.Entries[a.Key] = newState
				res.Actions = append(res.Actions, a)
			}
			return nil
		})
	}
	g.Wait()
	return retry
}

// upload stores one entry, or its tombstone, and returns its new state.
func (e *Engine) upload(ctx context.Context, a *Action, prev EntryState, machine string) (EntryState, error) {
	h := envelope.Header{Key: a.Key, Machine: machine, Written: e.now().UTC()}
	var content []byte
	var st EntryState
	switch {
	case a.Kind == UploadDelete:
		h.Kind = envelope.KindTombstone
		st = EntryState{Kind: envelope.KindTombstone}
	case a.Kind == Merge:
		h.Kind, h.ModTime = envelope.KindFile, e.now().UTC()
		content = a.Merged
		st = EntryState{Kind: envelope.KindFile, Hash: a.mergedHash, Held: a.mergedHeld}
	case a.Local.Kind == envelope.KindLink:
		h.Kind, h.LinkTarget, h.LinkIsDir = envelope.KindLink, a.Local.Target, a.Local.TargetIsDir
		st = stateOf(a.Local, "")
	default:
		exec := a.Local.Executable
		if !e.OS.TracksExecBit() {
			// This file system cannot see the bit; keep what was synced.
			exec = prev.Exec
		}
		h.Kind, h.Executable, h.ModTime = envelope.KindFile, exec, a.Local.ModTime.UTC()
		content = a.Local.Content
		st = stateOf(a.Local, "")
		st.Exec = exec
		if a.Local.Format != "" {
			st.Held = prev.Held
		}
	}

	obj, err := envelope.Seal(e.Cipher, h, content)
	if err != nil {
		return EntryState{}, err
	}
	pre := storage.Precondition{IfMatch: a.IfMatch, IfNoneMatch: a.IfMatch == ""}
	etag, err := e.Store.Put(ctx, a.Key, obj, pre)
	if err != nil {
		return EntryState{}, err
	}
	st.ETag = etag
	if _, ok := e.format(a.Key); ok {
		if h.Kind == envelope.KindFile {
			e.saveBase(a.Key, content)
		} else {
			e.dropBase(a.Key)
		}
	}
	return st, nil
}

// applyLocal writes or removes one entry on this machine's disk. It returns
// a notice for the user when the change needs their attention.
func (e *Engine) applyLocal(a *Action, st *State, b *backupSet) (string, error) {
	root, _ := tools.Find(e.Roots, a.Root)
	p, err := e.localPath(root, a.Rel)
	if err != nil {
		return "", err
	}

	if a.Kind == RemoveLocal {
		if err := b.save(p); err != nil {
			return "", fmt.Errorf("back up: %w", err)
		}
		if err := removeEntry(p); err != nil {
			return "", err
		}
		pruneEmptyDirs(root, filepath.Dir(p))
		e.dropBase(a.Key)
		st.Entries[a.Key] = EntryState{ETag: a.Remote.ETag, Kind: envelope.KindTombstone}
		return "", nil
	}

	if a.KeepLocalCopy && a.Local != nil {
		if err := e.saveConflict(a, sideLocal); err != nil {
			return "", fmt.Errorf("save this machine's copy: %w", err)
		}
	}
	if err := b.save(p); err != nil {
		return "", fmt.Errorf("back up: %w", err)
	}
	if a.ReplaceDir {
		if err := os.RemoveAll(p); err != nil {
			return "", err
		}
	}

	h := a.Remote.Header
	var notices []string
	if _, structured := e.format(a.Key); structured && h.Kind == envelope.KindFile {
		content := a.Remote.Content
		if a.Kind == Merge {
			content = a.Merged
		}
		hash, held, reasons, err := e.writeStructured(p, a.Key, content)
		if err != nil {
			return "", err
		}
		if n := heldNotice(a.Key, held, reasons, st.Entries[a.Key].Held); n != "" {
			notices = append(notices, n)
		}
		if a.Kind == Merge {
			a.mergedHash, a.mergedHeld = hash, held // the state follows once uploaded
		} else {
			st.Entries[a.Key] = EntryState{ETag: a.Remote.ETag, Kind: envelope.KindFile, Hash: hash, Held: held}
			e.saveBase(a.Key, content)
		}
	} else {
		switch h.Kind {
		case envelope.KindFile:
			hash, err := e.writeFile(p, a.Remote)
			if err != nil {
				return "", err
			}
			st.Entries[a.Key] = EntryState{ETag: a.Remote.ETag, Kind: envelope.KindFile, Hash: hash, Exec: h.Executable}
		case envelope.KindLink:
			if err := e.writeLink(p, h); err != nil {
				return "", err
			}
			st.Entries[a.Key] = EntryState{ETag: a.Remote.ETag, Kind: envelope.KindLink, Target: h.LinkTarget, TargetIsDir: h.LinkIsDir}
		default:
			return "", fmt.Errorf("cannot write a %s", h.Kind)
		}
	}
	if a.Key == entry.Key(tools.Codex, "hooks.json") {
		notices = append(notices, "Codex hooks changed: Codex runs them only after you approve them with /hooks")
	}
	return strings.Join(notices, "\n"), nil
}

// heldNotice tells about the items of a structured file newly held back on
// this machine.
func heldNotice(key string, held []string, reasons map[string]string, before []string) string {
	was := map[string]bool{}
	for _, n := range before {
		was[n] = true
	}
	var fresh []string
	for _, n := range held {
		if !was[n] && reasons[n] != "" && !isSubItem(n, held) {
			fresh = append(fresh, n+" ("+reasons[n]+")")
		}
	}
	if len(fresh) == 0 {
		return ""
	}
	root, rel, _ := entry.ParseKey(key)
	return root + "/" + rel + ": not set up on this machine, kept for the others: " + strings.Join(fresh, ", ")
}

// isSubItem reports whether name lies below another held item (a held TOML
// table's subtable): it is reported with its parent.
func isSubItem(name string, held []string) bool {
	for _, other := range held {
		if other != name && strings.HasPrefix(name, other+".") {
			return true
		}
	}
	return false
}

// localPath returns where rel lives in root. Every existing directory on
// the way must be a real directory: writing through a link would change the
// entry the link points to instead.
func (e *Engine) localPath(root tools.Root, rel string) (string, error) {
	if err := platform.ValidateRelPath(rel, e.OS); err != nil {
		return "", err
	}
	parts := strings.Split(rel, "/")
	cur := root.Dir
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if !fi.IsDir() || fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return "", fmt.Errorf("%s is in the way: it is not a plain directory", cur)
		}
	}
	return filepath.Join(root.Dir, filepath.FromSlash(rel)), nil
}

// writeFile writes a remote file in this machine's form and returns the hash
// this machine's scanner will compute for it.
func (e *Engine) writeFile(p string, r *remoteEntry) (string, error) {
	content := r.Content
	text := homepath.IsText(content)
	if text {
		content = e.Mapper.ToLocal(content)
	}
	if err := clearPath(p); err != nil {
		return "", err
	}
	if err := platform.WriteFileAtomic(p, content, platform.FilePerm(r.Header.Executable)); err != nil {
		return "", err
	}
	if mt := r.Header.ModTime; !mt.IsZero() {
		os.Chtimes(p, mt, mt)
	}
	if text {
		content = e.Mapper.ToPortable(content)
	}
	return envelope.Hash(content), nil
}

// writeLink creates the link an entry describes.
func (e *Engine) writeLink(p string, h envelope.Header) error {
	return e.MakeLink(p, h.LinkTarget, h.LinkIsDir)
}

// MakeLink creates a link at p to the synced path target ("<root>/<rel>"),
// relative to the link's real directory, so it resolves wherever this
// machine keeps its roots. Where links cannot be made, a directory target
// gets a managed copy instead.
func (e *Engine) MakeLink(p, target string, isDir bool) error {
	targetRootName, targetRel, ok := strings.Cut(target, "/")
	targetRoot, known := tools.Find(e.Roots, targetRootName)
	if !ok || !known || !targetRoot.Includes(targetRel) {
		return fmt.Errorf("link target %q is not a synced path here", target)
	}
	if err := platform.ValidateRelPath(targetRel, e.OS); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	targetAbs := filepath.Join(physical(targetRoot.Dir), filepath.FromSlash(targetRel))
	parent := physical(filepath.Dir(p))
	rel, err := filepath.Rel(parent, targetAbs)
	if err != nil {
		return err
	}

	if cur, _, isLink, _ := platform.ReadLink(p); isLink {
		existing := filepath.FromSlash(cur)
		if !filepath.IsAbs(existing) {
			existing = filepath.Join(parent, existing)
		}
		if filepath.Clean(existing) == filepath.Clean(targetAbs) {
			return nil // already the right link
		}
	}
	if err := clearPath(p); err != nil {
		return err
	}
	create := e.CreateLink
	if create == nil {
		create = platform.CreateLink
	}
	_, err = create(p, filepath.ToSlash(rel), isDir)
	if errors.Is(err, platform.ErrLinkUnsupported) && isDir {
		return writeManagedCopy(p, targetAbs, target)
	}
	return err
}

// physical resolves symlinks in a directory path, falling back to the path
// itself when it does not exist.
func physical(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

// clearPath makes room for a new entry at p: an existing link is removed, a
// file is left for the atomic rename to replace, and a directory is removed
// only when it holds nothing but litter (such as .DS_Store). A directory
// with real content is a layout conflict.
func clearPath(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, _, isLink, _ := platform.ReadLink(p); isLink {
		return os.Remove(p)
	}
	if !fi.IsDir() {
		return nil
	}
	if !onlyLitter(p) {
		return fmt.Errorf("%s is a directory with content in the way", p)
	}
	return os.RemoveAll(p)
}

// onlyLitter reports whether dir contains nothing but directories and
// litter files. A link anywhere inside counts as content.
func onlyLitter(dir string) bool {
	clean := true
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil, d.Type()&fs.ModeSymlink != 0, !d.IsDir() && !tools.IsLitter(d.Name()):
			clean = false
			return fs.SkipAll
		}
		return nil
	})
	return clean
}

// removeEntry deletes a file or a link at p (a link's target is untouched).
// A directory standing in for a link (a managed copy) goes as a whole.
func removeEntry(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, _, isLink, _ := platform.ReadLink(p); isLink || !fi.IsDir() {
		return os.Remove(p)
	}
	if _, err := os.Stat(filepath.Join(p, tools.LinkMarker)); err == nil {
		return os.RemoveAll(p)
	}
	return fmt.Errorf("%s is a directory, not a synced entry", p)
}

// pruneEmptyDirs removes dir and its parents, up to the root, while they
// hold nothing but litter.
func pruneEmptyDirs(root tools.Root, dir string) {
	for {
		rel, err := filepath.Rel(root.Dir, dir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return
		}
		if !onlyLitter(dir) || os.RemoveAll(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
