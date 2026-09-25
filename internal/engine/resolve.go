package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// ErrNoConflict means the entry is not in conflict (any more).
var ErrNoConflict = errors.New("this entry has no conflict to resolve")

// Resolve settles a conflict on key: keepLocal uploads this machine's
// version over the other one; otherwise the other machine's version is
// written here (with a backup, so Undo can take it back).
func (e *Engine) Resolve(ctx context.Context, key string, keepLocal bool) error {
	lock, err := platform.Lock(filepath.Join(e.StateDir, lockFile), defaultLockWait)
	if errors.Is(err, platform.ErrLocked) {
		return ErrBusy
	}
	if err != nil {
		return err
	}
	defer lock.Unlock()

	statePath := filepath.Join(e.StateDir, stateFile)
	st, err := LoadState(statePath)
	if err != nil {
		return err
	}
	st.ensureMachine()

	root, rel, ok := entry.ParseKey(key)
	if _, known := tools.Find(e.Roots, root); !ok || !known {
		return fmt.Errorf("%s is not a synced path", key)
	}
	a := &Action{Key: key, Root: root, Rel: rel}

	scanner := &entry.Scanner{Roots: e.Roots, Mapper: e.Mapper, OS: e.OS}
	locals, _, err := scanner.Scan()
	if err != nil {
		return err
	}
	for i := range locals {
		if locals[i].Key() == key {
			a.Local = &locals[i]
		}
	}

	data, etag, err := e.Store.Get(ctx, key)
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return err
	default:
		h, content, err := envelope.Open(e.Cipher, key, data)
		if err != nil {
			return err
		}
		a.Remote = &remoteEntry{Header: h, Content: content, ETag: etag}
	}

	var s *EntryState
	if es, ok := st.Entries[key]; ok {
		s = &es
	}
	if a.Remote == nil || sameContent(a.Local, a.Remote, e.OS) || (s != nil && s.ETag == etag) {
		e.dropConflictCopies(key)
		return ErrNoConflict
	}

	if keepLocal {
		a.Kind, a.IfMatch = Upload, etag
		if a.Local == nil {
			a.Kind = UploadDelete
		}
		prev := EntryState{}
		if s != nil {
			prev = *s
		}
		newState, err := e.upload(ctx, a, prev, st.Machine)
		if errors.Is(err, storage.ErrPreconditionFailed) {
			return errors.New("the other version changed again; run a sync and resolve then")
		}
		if err != nil {
			return err
		}
		st.Entries[key] = newState
	} else {
		a.Kind = Download
		if a.Remote.Header.Kind == envelope.KindTombstone {
			a.Kind = RemoveLocal
		}
		backup, err := e.newBackup("resolve " + key)
		if err != nil {
			return err
		}
		if _, err := e.applyLocal(a, st, backup); err != nil {
			return err
		}
		if err := backup.close(); err != nil {
			return err
		}
	}
	e.dropConflictCopies(key)
	return st.Save(statePath)
}

// dropConflictCopies forgets the set-aside versions of key.
func (e *Engine) dropConflictCopies(key string) {
	idx, err := e.loadConflicts()
	if err != nil {
		return
	}
	for _, side := range []string{sideRemote, sideLocal} {
		id := side + ":" + key
		if c, ok := idx[id]; ok {
			os.Remove(c.Path)
			delete(idx, id)
		}
	}
	e.saveConflicts(idx)
}
