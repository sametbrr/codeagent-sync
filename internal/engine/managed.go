package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// A managed copy stands in for a directory link where links cannot be made
// (Windows without Developer Mode): the target's files are copied, and a
// marker records the target and the hash of what was copied. The scanner
// treats the copy as the link, and every sync refreshes it from the target.
// Edits inside a copy are never overwritten: the copy's hash no longer
// matches the marker, which is reported instead.

func writeManagedCopy(p, targetAbs, target string) error {
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	if _, err := os.Stat(targetAbs); err != nil {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return err
		}
	} else if err := copyTree(targetAbs, p); err != nil {
		return err
	}
	return writeMarker(p, target)
}

func writeMarker(dir, target string) error {
	hash, err := treeHash(dir)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, tools.LinkMarker), []byte(target+"\n"+hash+"\n"), 0o644)
}

// readMarker returns a managed copy's target and the hash it was made with.
func readMarker(dir string) (target, hash string, ok bool) {
	data, err := os.ReadFile(filepath.Join(dir, tools.LinkMarker))
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return strings.TrimSpace(lines[0]), "", true
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), true
}

// treeHash hashes a directory's files, their paths and contents, leaving out
// the marker and litter.
func treeHash(dir string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == tools.LinkMarker || tools.IsLitter(d.Name()) {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel + "\x00" + envelope.Hash(data) + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// refreshManagedCopies brings every managed copy up to date with its target.
func (e *Engine) refreshManagedCopies(locals []entry.Entry, res *Result) {
	for _, l := range locals {
		if l.Kind != envelope.KindLink {
			continue
		}
		root, ok := tools.Find(e.Roots, l.Root)
		if !ok {
			continue
		}
		p := filepath.Join(root.Dir, filepath.FromSlash(l.Rel))
		target, made, isCopy := readMarker(p)
		if !isCopy {
			continue
		}
		current, err := treeHash(p)
		if err != nil {
			continue
		}
		if made != "" && current != made {
			res.Problems = append(res.Problems, entry.Problem{Key: l.Key(),
				Reason: "edited inside a copy that stands in for a link; edit " + target + " instead (the copy is not refreshed until then)"})
			continue
		}
		targetRoot, targetRel, _ := strings.Cut(target, "/")
		tr, ok := tools.Find(e.Roots, targetRoot)
		if !ok {
			continue
		}
		targetAbs := filepath.Join(tr.Dir, filepath.FromSlash(targetRel))
		want, err := treeHash(targetAbs)
		if err != nil || want == current {
			continue
		}
		if err := writeManagedCopy(p, targetAbs, target); err != nil {
			res.Problems = append(res.Problems, entry.Problem{Key: l.Key(), Reason: "refresh the copy: " + err.Error()})
		}
	}
}
