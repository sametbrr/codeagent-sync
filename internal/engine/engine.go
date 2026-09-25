// Package engine syncs the entries of the roots with the remote store.
//
// Every entry is compared three ways: what is on this machine's disk, what
// is in the store, and what both looked like at this machine's last sync
// (the state). Remote writes are conditional on the ETag last seen, so two
// machines can never overwrite each other's changes unnoticed; deletions are
// recorded as tombstones because stores do not offer conditional deletes.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/homepath"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/structured"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// Engine syncs one machine's roots with a store.
type Engine struct {
	Store  storage.ObjectStore
	Cipher envelope.Cipher
	Roots  []tools.Root
	Mapper *homepath.Mapper
	OS     platform.OS
	// StateDir holds the state file, the lock, backups and conflict copies.
	StateDir string
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// LockTimeout is how long to wait for another sync on this machine to
	// finish; 0 means 30 seconds.
	LockTimeout time.Duration
	// CreateLink makes links; nil means platform.CreateLink. Tests replace it
	// to act like a Windows machine that cannot create links.
	CreateLink func(linkPath, relTarget string, isDir bool) (platform.LinkKind, error)
}

// Mode restricts a sync to one direction.
type Mode int

const (
	Both     Mode = iota // download and upload
	PullOnly             // only apply remote changes here
	PushOnly             // only upload this machine's changes
)

// Options control one run.
type Options struct {
	Mode   Mode
	DryRun bool
	// AdoptLocal uploads, on this machine's first sync, entries that exist
	// only here. Without it they are reported as pending.
	AdoptLocal bool
	// Background marks a run started by a hook, in the backup's description.
	Background bool
}

// Result is what a run did, or would do in a dry run.
type Result struct {
	Actions   []*Action // changes made (planned, in a dry run)
	Pending   []*Action // waiting for confirmation
	Conflicts []*Action // changed on both sides
	Problems  []entry.Problem
	Notices   []string
	// BackupID names the backup of the files this run changed, for Undo.
	BackupID string
}

// ExitCode is 2 when conflicts need attention, 0 otherwise.
func (r *Result) ExitCode() int {
	if len(r.Conflicts) > 0 {
		return 2
	}
	return 0
}

// LastSync returns when this machine last completed a sync, and its name.
func (e *Engine) LastSync() (time.Time, string, error) {
	st, err := LoadState(filepath.Join(e.StateDir, stateFile))
	if err != nil {
		return time.Time{}, "", err
	}
	return st.LastSync, st.Machine, nil
}

// Held returns, per structured file, the items held back on this machine
// (MCP servers whose command is not installed here) at the last sync.
func (e *Engine) Held() (map[string][]string, error) {
	st, err := LoadState(filepath.Join(e.StateDir, stateFile))
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for key, es := range st.Entries {
		if len(es.Held) > 0 {
			out[key] = es.Held
		}
	}
	return out, nil
}

// Busy reports whether a sync is running on this machine now.
func (e *Engine) Busy() bool {
	lock, err := platform.TryLock(filepath.Join(e.StateDir, lockFile))
	if err != nil {
		return errors.Is(err, platform.ErrLocked)
	}
	lock.Unlock()
	return false
}

// ErrBusy means another sync is running on this machine.
var ErrBusy = errors.New("another sync is running on this machine")

const (
	stateFile       = "state.json"
	lockFile        = "sync.lock"
	fetchWorkers    = 8
	maxRounds       = 2
	defaultLockWait = 30 * time.Second
)

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Run syncs once. When an upload finds that the remote object changed after
// it was read (another machine synced in between), the whole sync runs again
// so the new remote version is taken into account.
func (e *Engine) Run(ctx context.Context, opts Options) (*Result, error) {
	wait := e.LockTimeout
	if wait == 0 {
		wait = defaultLockWait
	}
	lock, err := platform.Lock(filepath.Join(e.StateDir, lockFile), wait)
	if errors.Is(err, platform.ErrLocked) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()

	st, err := LoadState(filepath.Join(e.StateDir, stateFile))
	if err != nil {
		return nil, err
	}
	st.ensureMachine()

	res := &Result{}
	if id, ok := interruptedRun(e.StateDir); ok {
		res.Notices = append(res.Notices, fmt.Sprintf(
			"the previous sync was interrupted; the files it changed are in backup %s (undo restores them)", id))
	}

	for round := 0; round < maxRounds; round++ {
		retry, err := e.round(ctx, st, opts, res)
		if err != nil || !retry || opts.DryRun {
			return res, err
		}
	}
	res.Notices = append(res.Notices, "the remote side kept changing during the sync; run it again")
	return res, nil
}

// round plans and applies once. It reports whether an upload lost a race.
func (e *Engine) round(ctx context.Context, st *State, opts Options, res *Result) (retry bool, err error) {
	// A tool that is not installed here is left alone: its entries are
	// neither written nor treated as deleted.
	roots := tools.Active(e.Roots, dirExists)
	scanner := &entry.Scanner{Roots: roots, Mapper: e.Mapper, OS: e.OS}
	locals, problems, err := scanner.Scan()
	if err != nil {
		return false, fmt.Errorf("read local entries: %w", err)
	}
	res.Problems = problems

	listed, err := e.Store.List(ctx, entry.ListPrefix)
	if err != nil {
		return false, fmt.Errorf("list remote entries: %w", err)
	}

	p := e.newPlanner(st, opts, roots)
	for i := range locals {
		l := &locals[i]
		if s, ok := st.Entries[l.Key()]; ok && l.Format != "" {
			e.withHeld(l, s.Held)
		}
		if l.Format != "" {
			e.pinHidden(l)
		}
		p.local[l.Key()] = l
	}
	for i := range listed {
		if _, _, ok := entry.ParseKey(listed[i].Key); ok {
			p.listing[listed[i].Key] = listed[i]
		}
	}
	if err := p.fetch(ctx, e); err != nil {
		return false, err
	}
	actions := p.plan()
	res.Problems = append(res.Problems, p.problems...)
	sort.Slice(res.Problems, func(i, j int) bool { return res.Problems[i].Key < res.Problems[j].Key })

	var todo []*Action
	res.Pending, res.Conflicts = nil, nil
	for _, a := range actions {
		switch a.Kind {
		case Pending:
			res.Pending = append(res.Pending, a)
		case Conflict:
			res.Conflicts = append(res.Conflicts, a)
			todo = append(todo, a) // saving the other version is part of the run
		default:
			todo = append(todo, a)
		}
	}
	if opts.DryRun {
		res.Actions = append(res.Actions, todo...)
		return false, nil
	}
	retry, err = e.apply(ctx, st, todo, res, opts)
	if err == nil {
		e.refreshManagedCopies(locals, res)
	}
	return retry, err
}

// planner gathers what the decisions need.
type planner struct {
	e        *Engine
	roots    []tools.Root // the active roots
	st       *State
	opts     Options
	local    map[string]*entry.Entry
	listing  map[string]storage.ObjectInfo
	fetched  map[string]*remoteEntry
	problems []entry.Problem
}

func (e *Engine) newPlanner(st *State, opts Options, roots []tools.Root) *planner {
	return &planner{
		e: e, st: st, opts: opts, roots: roots,
		local:   map[string]*entry.Entry{},
		listing: map[string]storage.ObjectInfo{},
		fetched: map[string]*remoteEntry{},
	}
}

// fetch reads, in parallel, every remote entry a decision depends on.
func (p *planner) fetch(ctx context.Context, e *Engine) error {
	var keys []string
	for key, info := range p.listing {
		s, ok := p.st.Entries[key]
		var sp *EntryState
		if ok {
			sp = &s
		}
		if p.relevant(key) && (needsFetch(info, sp) || p.structuredGone(key, sp)) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fetchWorkers)
	for _, key := range keys {
		g.Go(func() error {
			data, etag, err := e.Store.Get(gctx, key)
			if errors.Is(err, storage.ErrNotFound) {
				mu.Lock()
				delete(p.listing, key) // deleted since the listing
				mu.Unlock()
				return nil
			}
			if err != nil {
				return fmt.Errorf("read %s: %w", key, err)
			}
			h, content, err := envelope.Open(e.Cipher, key, data)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				p.problems = append(p.problems, entry.Problem{Key: key, Reason: err.Error()})
				return nil
			}
			r := &remoteEntry{Header: h, Content: content, ETag: etag, Fingerprint: h.SHA256}
			if f, ok := e.format(key); ok && h.Kind == envelope.KindFile {
				items, err := f.Parse(content)
				if err != nil {
					p.problems = append(p.problems, entry.Problem{Key: key, Reason: "cannot be read: " + err.Error()})
					return nil
				}
				r.Fingerprint = structured.Fingerprint(items)
			}
			p.fetched[key] = r
			return nil
		})
	}
	return g.Wait()
}

// structuredGone reports a structured file deleted here: its remote version
// is read so it can be restored in the same run.
func (p *planner) structuredGone(key string, s *EntryState) bool {
	_, structured := p.e.format(key)
	return structured && p.local[key] == nil && s != nil && s.Kind == envelope.KindFile
}

// relevant reports whether this machine syncs key: its root is known here
// and its name is valid on this platform.
func (p *planner) relevant(key string) bool {
	root, rel, ok := entry.ParseKey(key)
	if !ok {
		return false
	}
	if r, known := tools.Find(p.roots, root); !known || !r.Includes(rel) {
		return false
	}
	return platform.ValidateRelPath(rel, p.e.OS) == nil
}

// plan decides every key.
func (p *planner) plan() []*Action {
	keys := map[string]bool{}
	for k := range p.local {
		keys[k] = true
	}
	for k := range p.listing {
		keys[k] = true
	}
	for k := range p.st.Entries {
		keys[k] = true
	}

	// On a case-insensitive file system, remote keys that differ only in
	// case (written on Linux) cannot all exist here.
	skip := map[string]string{}
	for k := range p.listing {
		if root, rel, ok := entry.ParseKey(k); ok {
			if err := platform.ValidateRelPath(rel, p.e.OS); err != nil {
				if _, known := tools.Find(p.roots, root); known {
					skip[k] = "not a valid name on this platform: " + err.Error()
				}
			}
		}
	}
	if p.e.OS.CaseInsensitive() {
		var all []string
		for k := range keys {
			all = append(all, k)
		}
		for k := range entry.CaseCollidingKeys(all) {
			skip[k] = "differs from another path only in letter case"
		}
	}

	firstSync := p.st.FirstSync()
	actions := map[string]*Action{}
	for key := range keys {
		if reason, ok := skip[key]; ok {
			p.problems = append(p.problems, entry.Problem{Key: key, Reason: reason})
			continue
		}
		root, rel, ok := entry.ParseKey(key)
		if !ok {
			continue
		}
		if r, known := tools.Find(p.roots, root); !known || !r.Includes(rel) {
			// Not synced here (a rule leaves it out): left alone on both
			// sides, not taken for a deletion.
			continue
		}

		l := p.local[key]
		var info *storage.ObjectInfo
		if i, ok := p.listing[key]; ok {
			info = &i
		}
		var s *EntryState
		if es, ok := p.st.Entries[key]; ok {
			s = &es
		}
		r := p.fetched[key]
		if info != nil && needsFetch(*info, s) && r == nil {
			continue // unreadable; already reported
		}

		a := decide(key, l, info, s, r, firstSync, p.opts.AdoptLocal, p.e.OS)
		if a == nil && l != nil {
			a = p.release(key, l, s)
		}
		if a == nil {
			continue
		}
		p.e.mergeStructured(a)
		p.e.protectStructured(a)
		a.Root, a.Rel = root, rel
		actions[key] = a
	}

	p.absorbOldCopies(actions)
	for key, outer := range layoutConflicts(actions, p.local) {
		reason := "inside " + outer + ", which is a directory on one machine but a link or file on another"
		if key == outer {
			reason = "a directory on one machine but a link or file on another"
		}
		p.problems = append(p.problems, entry.Problem{Key: key, Reason: reason})
		delete(actions, key)
	}

	var out []*Action
	for _, a := range actions {
		if p.allowed(a) {
			out = append(out, a)
		}
	}
	sortForApply(out)
	return out
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// absorbOldCopies finds directories that a downloaded link will replace and
// that hold nothing but files identical to the link target's: a shared skill
// copied as a plain directory by an older sync tool. The link replaces the
// directory (it is backed up) and its files are not uploaded.
func (p *planner) absorbOldCopies(actions map[string]*Action) {
	for key, a := range actions {
		if a.Kind != Download || a.Remote.Header.Kind != envelope.KindLink || p.local[key] != nil {
			continue
		}
		children, ok := p.sameAsTarget(key, a.Remote.Header.LinkTarget, actions)
		if !ok {
			continue
		}
		for _, c := range children {
			delete(actions, c)
			delete(p.local, c) // gone once the link replaces the directory
		}
		a.ReplaceDir = true
		a.Note = "the directory here held the same files; replaced by the shared link (backed up)"
	}
}

// sameAsTarget reports whether every entry below key on this machine is a
// never-synced file identical to its counterpart below target.
func (p *planner) sameAsTarget(key, target string, actions map[string]*Action) ([]string, bool) {
	prefix, targetPrefix := key+"/", entry.ListPrefix+target+"/"
	var children []string
	for k, l := range p.local {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if _, synced := p.st.Entries[k]; synced || l.Kind != envelope.KindFile {
			return nil, false
		}
		counterpart := targetPrefix + strings.TrimPrefix(k, prefix)
		hash, ok := p.finalHash(counterpart, actions)
		if !ok || hash != l.Hash {
			return nil, false
		}
		children = append(children, k)
	}
	return children, len(children) > 0
}

// finalHash is what key will hold on this machine after the sync.
func (p *planner) finalHash(key string, actions map[string]*Action) (string, bool) {
	if a := actions[key]; a != nil && a.Kind == Download && a.Remote.Header.Kind == envelope.KindFile {
		return a.Remote.Fingerprint, true
	}
	if l := p.local[key]; l != nil && l.Kind == envelope.KindFile {
		return l.Hash, true
	}
	return "", false
}

// release writes the items of a structured file that were held back and can
// be used now (their command got installed since).
func (p *planner) release(key string, l *entry.Entry, s *EntryState) *Action {
	content, names := p.e.released(key, s)
	if content == nil {
		return nil
	}
	return &Action{
		Kind:  Download,
		Key:   key,
		Local: l,
		Remote: &remoteEntry{
			Header:      envelope.Header{Kind: envelope.KindFile, Key: key},
			Content:     content,
			ETag:        s.ETag,
			Fingerprint: s.Hash,
		},
		Note: "now usable on this machine: " + strings.Join(names, ", "),
	}
}

// allowed applies the direction of the run.
func (p *planner) allowed(a *Action) bool {
	switch p.opts.Mode {
	case PullOnly:
		return a.Kind != Upload && a.Kind != UploadDelete && a.Kind != Pending && a.Kind != Merge
	case PushOnly:
		return a.Kind != Download && a.Kind != RemoveLocal && a.Kind != Conflict && a.Kind != Merge
	}
	return true
}
