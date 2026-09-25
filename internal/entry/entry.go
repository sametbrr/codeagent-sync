// Package entry reads the synced entries of the roots from disk: files, with
// their content made portable, and links to other synced paths.
package entry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/homepath"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/structured"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// Entry is one synced item found on disk.
type Entry struct {
	Root string
	Rel  string // slash-separated, relative to the root's directory
	Kind envelope.Kind

	// Files (KindFile).
	Content    []byte // portable content: home directory replaced by a placeholder
	Hash       string // envelope.Hash(Content)
	Executable bool   // always false where the file system has no execute bit
	ModTime    time.Time

	// Links (KindLink): the linked path as "<root>/<rel>".
	Target      string
	TargetIsDir bool

	// Format names the structured format of a file merged item by item
	// (package structured). Content is then the file's synced form and Hash
	// its fingerprint, which ignores formatting.
	Format string
}

// Key returns the entry's remote key.
func (e Entry) Key() string { return Key(e.Root, e.Rel) }

const keyPrefix = "v1/"

// MetaPrefix is where objects that are not entries live (key derivation
// parameters, the key check).
const MetaPrefix = keyPrefix + "_meta/"

// Key returns the remote key of rel in root.
func Key(root, rel string) string { return keyPrefix + root + "/" + rel }

// ParseKey splits a remote key into root and rel. ok is false for keys that
// are not entries.
func ParseKey(key string) (root, rel string, ok bool) {
	rest, found := strings.CutPrefix(key, keyPrefix)
	if !found {
		return "", "", false
	}
	root, rel, ok = strings.Cut(rest, "/")
	if !ok || root == "" || rel == "" || strings.HasPrefix(root, "_") {
		return "", "", false
	}
	return root, rel, true
}

// ListPrefix is the prefix under which all entries are stored.
const ListPrefix = keyPrefix

// Problem is a path that is not synced, and why.
type Problem struct {
	Key    string
	Reason string
}

// Scanner reads the roots' entries from disk.
type Scanner struct {
	Roots  []tools.Root
	Mapper *homepath.Mapper
	OS     platform.OS
	// MaxSize is the largest file synced; 0 means envelope.MaxContentSize.
	MaxSize int64
}

// Scan returns the entries of every root sorted by key, and the paths found
// that cannot be synced. Missing root directories are simply empty.
func (s *Scanner) Scan() ([]Entry, []Problem, error) {
	resolved := resolveRoots(s.Roots)
	var entries []Entry
	var problems []Problem
	for _, r := range s.Roots {
		w := &walker{s: s, root: r, resolved: resolved, seen: map[string]bool{}}
		for _, prefix := range r.WalkPrefixes() {
			if err := w.walk(prefix); err != nil {
				return nil, nil, err
			}
		}
		entries = append(entries, w.entries...)
		problems = append(problems, w.problems...)
	}

	entries, dropped := DropCaseCollisions(entries)
	problems = append(problems, dropped...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key() < entries[j].Key() })
	sort.Slice(problems, func(i, j int) bool { return problems[i].Key < problems[j].Key })
	return entries, problems, nil
}

// resolveRoots returns the roots with symlinks in their directories resolved,
// for mapping link targets: a link's destination is a physical path.
func resolveRoots(roots []tools.Root) []tools.Root {
	out := make([]tools.Root, len(roots))
	for i, r := range roots {
		out[i] = r
		if dir, err := filepath.EvalSymlinks(r.Dir); err == nil {
			out[i].Dir = dir
		}
	}
	return out
}

type walker struct {
	s        *Scanner
	root     tools.Root
	resolved []tools.Root
	seen     map[string]bool
	entries  []Entry
	problems []Problem
}

func (w *walker) walk(prefix string) error {
	start := filepath.Join(w.root.Dir, filepath.FromSlash(prefix))
	fi, err := os.Lstat(start)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		w.problem(prefix, err.Error())
		return nil
	}
	// WalkDir follows a symlink given as its starting point; a link at the
	// start of a prefix is an entry of its own instead.
	if fi.Mode()&fs.ModeSymlink != 0 {
		w.link(start, prefix)
		return nil
	}
	return filepath.WalkDir(start, w.visit)
}

func (w *walker) visit(path string, d fs.DirEntry, err error) error {
	rel, relErr := filepath.Rel(w.root.Dir, path)
	if relErr != nil {
		return relErr
	}
	rel = filepath.ToSlash(rel)
	if err != nil {
		if w.root.Includes(rel) || d == nil || d.IsDir() {
			w.problem(rel, err.Error())
		}
		if d != nil && d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}

	mode := d.Type()
	if w.s.OS == platform.Windows && (d.IsDir() || mode&fs.ModeIrregular != 0) {
		// Junctions look like directories or irregular files there.
		if _, _, ok, _ := platform.ReadLink(path); ok {
			w.link(path, rel)
			return skipIfDir(d)
		}
	}

	switch {
	case mode&fs.ModeSymlink != 0:
		w.link(path, rel)
	case d.IsDir():
		if rel != "." && w.root.Excludes(rel) {
			return fs.SkipDir
		}
		if marker, err := os.ReadFile(filepath.Join(path, tools.LinkMarker)); err == nil {
			// The first line is the target; the second, the hash of the copy.
			target, _, _ := strings.Cut(strings.TrimSpace(string(marker)), "\n")
			w.managedCopy(rel, strings.TrimSpace(target))
			return fs.SkipDir
		}
	case mode.IsRegular():
		if w.root.Includes(rel) {
			w.file(path, rel, d, w.root.Format(rel))
		}
	default:
		if w.root.Includes(rel) {
			w.problem(rel, "not a regular file")
		}
	}
	return nil
}

func skipIfDir(d fs.DirEntry) error {
	if d.IsDir() {
		return fs.SkipDir
	}
	return nil
}

func (w *walker) file(path, rel string, d fs.DirEntry, format string) {
	info, err := d.Info()
	if err != nil {
		w.problem(rel, err.Error())
		return
	}
	limit := w.s.MaxSize
	if limit == 0 {
		limit = envelope.MaxContentSize
	}
	if info.Size() > limit {
		w.problem(rel, fmt.Sprintf("larger than the %d byte limit", limit))
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		w.problem(rel, err.Error())
		return
	}
	if homepath.IsText(data) {
		data = w.s.Mapper.ToPortable(data)
	}
	if format != "" {
		content, hash, err := Project(format, data)
		if err != nil {
			w.problem(rel, "cannot be read: "+err.Error())
			return
		}
		w.add(Entry{Root: w.root.Name, Rel: rel, Kind: envelope.KindFile, Content: content, Hash: hash, ModTime: info.ModTime(), Format: format})
		return
	}
	w.add(Entry{
		Root:       w.root.Name,
		Rel:        rel,
		Kind:       envelope.KindFile,
		Content:    data,
		Hash:       envelope.Hash(data),
		Executable: w.s.OS.TracksExecBit() && platform.Executable(info.Mode()),
		ModTime:    info.ModTime(),
	})
}

// link records the link at path. Its destination must be a synced path of
// some root; the entry stores it as "<root>/<rel>".
func (w *walker) link(path, rel string) {
	if !w.root.Includes(rel) {
		return
	}
	raw, _, ok, err := platform.ReadLink(path)
	if err != nil || !ok {
		w.problem(rel, fmt.Sprintf("unreadable link: %v", err))
		return
	}

	dest, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Not there yet (it may arrive with this sync): resolve by hand
		// against the link's real parent directory.
		dest = filepath.FromSlash(raw)
		if !filepath.IsAbs(dest) {
			parent, perr := filepath.EvalSymlinks(filepath.Dir(path))
			if perr != nil {
				parent = filepath.Dir(path)
			}
			dest = filepath.Join(parent, dest)
		}
	}
	target, targetRel, found := tools.Owner(w.resolved, filepath.Clean(dest))
	if !found || !target.Includes(targetRel) {
		w.problem(rel, "link points outside the synced paths: "+raw)
		return
	}
	fi, err := os.Stat(path)
	w.add(Entry{
		Root:        w.root.Name,
		Rel:         rel,
		Kind:        envelope.KindLink,
		Target:      target.Name + "/" + targetRel,
		TargetIsDir: err == nil && fi.IsDir(),
	})
}

// Project returns the synced form of a structured file's portable content
// and its fingerprint.
func Project(format string, portable []byte) (content []byte, fingerprint string, err error) {
	f, ok := structured.Get(format)
	if !ok {
		return nil, "", fmt.Errorf("unknown format %q", format)
	}
	items, err := f.Project(portable)
	if err != nil {
		return nil, "", err
	}
	return f.Render(items), structured.Fingerprint(items), nil
}

// managedCopy records a directory that stands in for a link on a machine
// that cannot create links. Its content is the target's, so it is not read.
func (w *walker) managedCopy(rel, target string) {
	if !w.root.Includes(rel) {
		return
	}
	root, targetRel, ok := strings.Cut(target, "/")
	r, known := tools.Find(w.s.Roots, root)
	if !ok || !known || !r.Includes(targetRel) {
		w.problem(rel, "managed link copy with an invalid target: "+target)
		return
	}
	w.add(Entry{Root: w.root.Name, Rel: rel, Kind: envelope.KindLink, Target: target, TargetIsDir: true})
}

func (w *walker) add(e Entry) {
	if w.seen[e.Rel] {
		return
	}
	w.seen[e.Rel] = true
	w.entries = append(w.entries, e)
}

func (w *walker) problem(rel, reason string) {
	w.problems = append(w.problems, Problem{Key: Key(w.root.Name, rel), Reason: reason})
}

// DropCaseCollisions removes entries whose keys, or directories, differ only
// in letter case (possible on Linux): they cannot coexist on macOS and
// Windows, where one would silently replace the other.
func DropCaseCollisions(entries []Entry) ([]Entry, []Problem) {
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key()
	}
	colliding := CaseCollidingKeys(keys)
	if len(colliding) == 0 {
		return entries, nil
	}
	var kept []Entry
	var problems []Problem
	for _, e := range entries {
		if colliding[e.Key()] {
			problems = append(problems, Problem{Key: e.Key(), Reason: "differs from another path only in letter case"})
			continue
		}
		kept = append(kept, e)
	}
	return kept, problems
}

// CaseCollidingKeys returns the keys that are, or lie below, a path that
// differs from another one only in letter case.
func CaseCollidingKeys(keys []string) map[string]bool {
	groups := platform.CaseCollisions(keys)
	if len(groups) == 0 {
		return nil
	}
	var paths []string
	for _, g := range groups {
		paths = append(paths, g...)
	}
	out := map[string]bool{}
	for _, k := range keys {
		for _, p := range paths {
			if k == p || strings.HasPrefix(k, p+"/") {
				out[k] = true
				break
			}
		}
	}
	return out
}
