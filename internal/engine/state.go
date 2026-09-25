package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/platform"
)

// State is what this machine knew about every entry after its last sync.
type State struct {
	Version  int       `json:"version"`
	Machine  string    `json:"machine"`
	LastSync time.Time `json:"last_sync,omitzero"`
	// Settled is set by the first sync that left nothing of this machine's
	// own waiting for confirmation; until then, entries only this machine
	// has are uploaded only when confirmed. Missing in state files written
	// before it existed.
	Settled *bool                 `json:"settled,omitempty"`
	Entries map[string]EntryState `json:"entries"`
}

// EntryState is an entry as it was at the last sync, on both sides.
type EntryState struct {
	// ETag of the remote object; empty when the backend did not report it.
	ETag string        `json:"etag,omitempty"`
	Kind envelope.Kind `json:"kind"` // file, link or tombstone

	// Files: hash of the portable content as read from this machine's disk.
	Hash string `json:"hash,omitempty"`
	Exec bool   `json:"exec,omitempty"`

	// Links.
	Target      string `json:"target,omitempty"`
	TargetIsDir bool   `json:"target_is_dir,omitempty"`

	// Structured files: items held back on this machine (MCP servers whose
	// command is not installed here).
	Held []string `json:"held,omitempty"`
}

const stateVersion = 1

// LoadState reads the state file. A missing file is a machine that has not
// synced yet.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		settled := false
		return &State{Version: stateVersion, Settled: &settled, Entries: map[string]EntryState{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("state file %s is damaged: %w", path, err)
	}
	if st.Version != stateVersion {
		return nil, fmt.Errorf("state file %s has version %d; this build understands %d", path, st.Version, stateVersion)
	}
	if st.Entries == nil {
		st.Entries = map[string]EntryState{}
	}
	return &st, nil
}

// Save writes the state file atomically, readable only by the user.
func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return platform.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

// FirstSync reports whether this machine has not settled its first sync.
func (s *State) FirstSync() bool {
	if s.Settled == nil {
		return len(s.Entries) == 0 // how state files without the field decided
	}
	return !*s.Settled
}

// ensureMachine gives the machine a name, used in object headers and
// conflict messages. It is the host name plus a random suffix, so two
// machines with the same host name can still be told apart.
func (s *State) ensureMachine() {
	if s.Machine != "" {
		return
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "machine"
	}
	host, _, _ = strings.Cut(host, ".")
	suffix := make([]byte, 2)
	rand.Read(suffix)
	s.Machine = host + "-" + hex.EncodeToString(suffix)
}

func stateOf(e *entry.Entry, etag string) EntryState {
	switch e.Kind {
	case envelope.KindLink:
		return EntryState{ETag: etag, Kind: envelope.KindLink, Target: e.Target, TargetIsDir: e.TargetIsDir}
	default:
		return EntryState{ETag: etag, Kind: envelope.KindFile, Hash: e.Hash, Exec: e.Executable}
	}
}
