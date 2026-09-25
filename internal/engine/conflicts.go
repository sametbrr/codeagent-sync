package engine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/homepath"
	"github.com/sametbrr/codeagent-sync/internal/platform"
)

// Copies set aside instead of being lost live under the conflicts
// directory: "remote" holds another machine's version of an entry changed
// on both sides (this machine's version stays in place), "local" holds this
// machine's version replaced by the synced one on its first sync.
const (
	conflictsDir = "conflicts"
	conflictsIdx = "index.json"
	sideRemote   = "remote"
	sideLocal    = "local"
)

// ConflictCopy is one set-aside version.
type ConflictCopy struct {
	Key     string    `json:"key"`
	Side    string    `json:"side"`
	Path    string    `json:"path"`
	Machine string    `json:"machine,omitempty"` // who wrote the other version
	ETag    string    `json:"etag,omitempty"`
	Saved   time.Time `json:"saved"`
}

// saveConflict writes a version of an action's entry, in this machine's
// form, to the conflicts directory and records it in the index. The same
// remote version is saved only once.
func (e *Engine) saveConflict(a *Action, side string) error {
	idx, err := e.loadConflicts()
	if err != nil {
		return err
	}
	id := side + ":" + a.Key
	if side == sideRemote {
		if prev, ok := idx[id]; ok && prev.ETag == a.Remote.ETag {
			return nil
		}
	}

	p := filepath.Join(e.StateDir, conflictsDir, side, filepath.FromSlash(a.Root), filepath.FromSlash(a.Rel))
	var content []byte
	perm := platform.FilePerm(false)
	info := ConflictCopy{Key: a.Key, Side: side, Path: p, Saved: e.now().UTC()}
	if side == sideRemote {
		h := a.Remote.Header
		info.Machine, info.ETag = h.Machine, a.Remote.ETag
		if h.Kind == envelope.KindLink {
			content = []byte("link to " + h.LinkTarget + "\n")
		} else {
			content, perm = a.Remote.Content, platform.FilePerm(h.Executable)
		}
	} else if a.Local.Kind == envelope.KindLink {
		content = []byte("link to " + a.Local.Target + "\n")
	} else {
		content, perm = a.Local.Content, platform.FilePerm(a.Local.Executable)
	}
	if homepath.IsText(content) {
		content = e.Mapper.ToLocal(content)
	}
	if err := platform.WriteFileAtomic(p, content, perm); err != nil {
		return err
	}
	idx[id] = info
	return e.saveConflicts(idx)
}

// Conflicts lists the set-aside versions, oldest first.
func (e *Engine) Conflicts() ([]ConflictCopy, error) {
	idx, err := e.loadConflicts()
	if err != nil {
		return nil, err
	}
	out := make([]ConflictCopy, 0, len(idx))
	for _, c := range idx {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Saved.Before(out[j].Saved) })
	return out, nil
}

func (e *Engine) loadConflicts() (map[string]ConflictCopy, error) {
	data, err := os.ReadFile(filepath.Join(e.StateDir, conflictsDir, conflictsIdx))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]ConflictCopy{}, nil
	}
	if err != nil {
		return nil, err
	}
	idx := map[string]ConflictCopy{}
	return idx, json.Unmarshal(data, &idx)
}

func (e *Engine) saveConflicts(idx map[string]ConflictCopy) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return platform.WriteFileAtomic(filepath.Join(e.StateDir, conflictsDir, conflictsIdx), data, 0o600)
}
