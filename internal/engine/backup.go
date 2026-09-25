package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

const (
	backupsDir  = "backups"
	manifest    = "manifest.json"
	keepBackups = 20
)

// backupSet records, before a sync changes anything on disk, what every
// touched path held, so Undo can put it back.
type backupSet struct {
	ID     string       `json:"id"`
	What   string       `json:"what,omitempty"` // the change: "sync", "share skill foo", …
	Time   time.Time    `json:"time,omitzero"`
	Items  []backupItem `json:"items"`
	Undone bool         `json:"undone,omitempty"`

	dir string
}

type backupItem struct {
	Path    string      `json:"path"`
	Existed bool        `json:"existed"`
	Kind    string      `json:"kind,omitempty"` // file, link or dir
	Mode    fs.FileMode `json:"mode,omitempty"`
	Target  string      `json:"target,omitempty"` // link target, as stored in the link
	Copy    string      `json:"copy,omitempty"`   // name of the copy inside the backup
}

func (e *Engine) newBackup(what string) (*backupSet, error) {
	now := e.now().UTC()
	base := now.Format("20060102-150405")
	root := filepath.Join(e.StateDir, backupsDir)
	for i := 0; ; i++ {
		id := base
		if i > 0 {
			id += "-" + strconv.Itoa(i)
		}
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, err
		}
		if err := os.Mkdir(dir, 0o700); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		return &backupSet{ID: id, What: what, Time: now, dir: dir}, nil
	}
}

// save records what p holds now.
func (b *backupSet) save(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		b.Items = append(b.Items, backupItem{Path: p})
		return nil
	}
	if err != nil {
		return err
	}
	item := backupItem{Path: p, Existed: true, Mode: fi.Mode().Perm()}
	if target, _, isLink, _ := platform.ReadLink(p); isLink {
		raw, err := os.Readlink(p)
		if err != nil {
			raw = target
		}
		item.Kind, item.Target = "link", raw
		b.Items = append(b.Items, item)
		return nil
	}
	item.Copy = strconv.Itoa(len(b.Items))
	dst := filepath.Join(b.dir, item.Copy)
	if fi.IsDir() {
		item.Kind = "dir"
		err = copyTree(p, dst)
	} else {
		item.Kind = "file"
		err = copyFile(p, dst, fi.Mode().Perm())
	}
	if err != nil {
		return err
	}
	b.Items = append(b.Items, item)
	return nil
}

func (b *backupSet) close() error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return platform.WriteFileAtomic(filepath.Join(b.dir, manifest), data, 0o600)
}

// Backup records paths before a change made outside a sync (sharing a
// skill, for one), so that Undo takes it back like a sync.
type Backup struct{ set *backupSet }

// StartBackup begins a backup of the change described by what.
func (e *Engine) StartBackup(what string) (*Backup, error) {
	set, err := e.newBackup(what)
	if err != nil {
		return nil, err
	}
	return &Backup{set}, nil
}

// Save records what path holds now, before it changes.
func (b *Backup) Save(path string) error { return b.set.save(path) }

// Close finishes the backup.
func (b *Backup) Close() error { return b.set.close() }

// Rollback puts back everything saved, after a change that failed half
// way, and drops the backup: there is nothing left to undo.
func (b *Backup) Rollback() error {
	if err := b.set.restoreAll(); err != nil {
		b.set.close() // keep it for undo
		return err
	}
	return os.RemoveAll(b.set.dir)
}

// ID names the backup.
func (b *Backup) ID() string { return b.set.ID }

// WithLock runs fn holding the lock a sync takes, so that no sync sees a
// change made outside it half done.
func (e *Engine) WithLock(fn func() error) error {
	lock, err := platform.Lock(filepath.Join(e.StateDir, lockFile), defaultLockWait)
	if errors.Is(err, platform.ErrLocked) {
		return ErrBusy
	}
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return fn()
}

// CopyTree copies a directory, keeping symlinks as symlinks.
func CopyTree(src, dst string) error { return copyTree(src, dst) }

// BackupInfo describes a change that Undo can take back.
type BackupInfo struct {
	ID     string    `json:"id"`
	What   string    `json:"what"`
	Time   time.Time `json:"time"`
	Paths  []string  `json:"paths"`
	Undone bool      `json:"undone,omitempty"`
}

func (b *backupSet) info() BackupInfo {
	in := BackupInfo{ID: b.ID, What: b.What, Time: b.Time, Undone: b.Undone}
	if in.What == "" {
		in.What = "sync"
	}
	for _, item := range b.Items {
		in.Paths = append(in.Paths, item.Path)
	}
	return in
}

// Backups lists the changes kept for Undo, newest first.
func (e *Engine) Backups() ([]BackupInfo, error) {
	sets, err := e.backupSets()
	if err != nil {
		return nil, err
	}
	out := make([]BackupInfo, 0, len(sets))
	for i := len(sets) - 1; i >= 0; i-- {
		out = append(out, sets[i].info())
	}
	return out, nil
}

// Undo restores what a change — the one with the given backup ID, or with
// "" the most recent one not undone yet — did on this machine's disk. It
// changes nothing remotely; the next sync uploads the restored versions,
// which reverts the change on the other machines as well.
func (e *Engine) Undo(id string) (BackupInfo, int, error) {
	lock, err := platform.Lock(filepath.Join(e.StateDir, lockFile), defaultLockWait)
	if err != nil {
		return BackupInfo{}, 0, err
	}
	defer lock.Unlock()

	b, err := e.findBackup(id)
	if err != nil {
		return BackupInfo{}, 0, err
	}
	if err := b.restoreAll(); err != nil {
		return b.info(), 0, err
	}
	b.Undone = true
	return b.info(), len(b.Items), b.close()
}

// restoreAll puts every saved path back, newest save first.
func (b *backupSet) restoreAll() error {
	for i := len(b.Items) - 1; i >= 0; i-- {
		item := b.Items[i]
		if err := removeAny(item.Path); err != nil {
			return fmt.Errorf("restore %s: %w", item.Path, err)
		}
		if item.Existed {
			if err := b.restore(item); err != nil {
				return fmt.Errorf("restore %s: %w", item.Path, err)
			}
		}
	}
	return nil
}

func (b *backupSet) restore(item backupItem) error {
	if err := os.MkdirAll(filepath.Dir(item.Path), 0o755); err != nil {
		return err
	}
	switch item.Kind {
	case "link":
		return os.Symlink(item.Target, item.Path)
	case "dir":
		return copyTree(filepath.Join(b.dir, item.Copy), item.Path)
	default:
		return copyFile(filepath.Join(b.dir, item.Copy), item.Path, item.Mode)
	}
}

// backupSets reads every complete backup, oldest first.
func (e *Engine) backupSets() ([]*backupSet, error) {
	ids, err := e.backupIDs()
	if err != nil {
		return nil, err
	}
	var out []*backupSet
	for _, id := range ids {
		dir := filepath.Join(e.StateDir, backupsDir, id)
		data, err := os.ReadFile(filepath.Join(dir, manifest))
		if err != nil {
			continue // interrupted before the manifest was written
		}
		var b backupSet
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, fmt.Errorf("backup %s: %w", id, err)
		}
		b.dir = dir
		out = append(out, &b)
	}
	return out, nil
}

// findBackup returns the backup with the given ID, or with "" the newest
// one not undone.
func (e *Engine) findBackup(id string) (*backupSet, error) {
	sets, err := e.backupSets()
	if err != nil {
		return nil, err
	}
	for i := len(sets) - 1; i >= 0; i-- {
		b := sets[i]
		switch {
		case id == "" && !b.Undone:
			return b, nil
		case id != "" && b.ID == id:
			if b.Undone {
				return nil, fmt.Errorf("%s is undone already", id)
			}
			return b, nil
		}
	}
	if id != "" {
		return nil, fmt.Errorf("there is no backup %s (see codeagent-sync undo --list)", id)
	}
	return nil, errors.New("there is nothing to undo")
}

func (e *Engine) backupIDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(e.StateDir, backupsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, d := range entries {
		if d.IsDir() {
			ids = append(ids, d.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (e *Engine) pruneBackups() {
	ids, err := e.backupIDs()
	if err != nil {
		return
	}
	for len(ids) > keepBackups {
		os.RemoveAll(filepath.Join(e.StateDir, backupsDir, ids[0]))
		ids = ids[1:]
	}
}

// removeAny deletes whatever is at p: file, link or directory.
func removeAny(p string) error {
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, _, isLink, _ := platform.ReadLink(p); isLink {
		return os.Remove(p)
	}
	return os.RemoveAll(p)
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	return platform.WriteFileAtomic(dst, data, perm)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			return copyFile(p, target, info.Mode().Perm())
		}
	})
}
