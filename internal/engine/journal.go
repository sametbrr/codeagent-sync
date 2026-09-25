package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

// The journal marks a sync that is changing files on disk. It holds the ID
// of the run's backup and is removed when the run completes; finding it at
// the start of a sync means the previous one was interrupted. Nothing is
// lost by that — the next sync reconciles by content — but the user may want
// to undo what the interrupted run already changed.
const journalFile = "journal"

func writeJournal(stateDir, backupID string) error {
	return platform.WriteFileAtomic(filepath.Join(stateDir, journalFile), []byte(backupID+"\n"), 0o600)
}

func clearJournal(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, journalFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func interruptedRun(stateDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(stateDir, journalFile))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// Interrupted reports a sync that was interrupted while it changed files on
// disk, with the ID of its backup.
func (e *Engine) Interrupted() (backupID string, ok bool) { return interruptedRun(e.StateDir) }
