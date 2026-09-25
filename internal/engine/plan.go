package engine

import (
	"path"
	"sort"
	"strings"

	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
)

// ActionKind is what a sync does with one entry.
type ActionKind string

const (
	Download     ActionKind = "download"      // write the remote entry here
	RemoveLocal  ActionKind = "remove"        // delete the entry here: it was deleted elsewhere
	Upload       ActionKind = "upload"        // store this machine's entry remotely
	UploadDelete ActionKind = "upload-delete" // record remotely that the entry was deleted here
	Record       ActionKind = "record"        // both sides already agree; only the state changes
	Forget       ActionKind = "forget"        // gone on both sides; dropped from the state
	Conflict     ActionKind = "conflict"      // changed on both sides; this machine's version is kept
	Pending      ActionKind = "pending"       // only on this machine at its first sync; needs confirmation
	Merge        ActionKind = "merge"         // structured file changed on both sides: merged, written here and uploaded
)

// Action is one planned change.
type Action struct {
	Kind ActionKind
	Key  string
	Root string
	Rel  string

	Local  *entry.Entry // the entry on this machine, if any
	Remote *remoteEntry // the remote entry, when it had to be read
	// IfMatch is the remote ETag an upload is conditioned on; empty means
	// the object must not exist yet.
	IfMatch string
	// KeepLocalCopy saves this machine's version to the conflicts directory
	// before a download replaces it.
	KeepLocalCopy bool
	// Merged is the merged synced form of a structured file (Merge).
	Merged []byte
	// ReplaceDir lets a downloaded link replace a directory that holds the
	// same files as the link's target (a copy made by an older sync tool).
	ReplaceDir bool
	// Note explains an unusual decision.
	Note string

	// The state of a merged structured file once it is uploaded.
	mergedHash string
	mergedHeld []string
}

type remoteEntry struct {
	Header  envelope.Header
	Content []byte
	ETag    string
	// Fingerprint compares with entry.Entry.Hash: the content hash, or for a
	// structured file the fingerprint of its items.
	Fingerprint string
}

// needsFetch reports whether a listed remote object must be read to decide:
// it is new to this machine, or changed since the last sync, or its ETag was
// never known.
func needsFetch(info storage.ObjectInfo, s *EntryState) bool {
	return s == nil || s.ETag == "" || s.ETag != info.ETag
}

// decide chooses the action for one key from the entry on this machine (l),
// the listed remote object (info), the state of the last sync (s) and the
// remote entry, read when needsFetch said so (r). nil means nothing to do.
func decide(key string, l *entry.Entry, info *storage.ObjectInfo, s *EntryState, r *remoteEntry, firstSync, adoptLocal bool, os platform.OS) *Action {
	a := &Action{Key: key, Local: l, Remote: r}
	localSame := localUnchanged(l, s, os)
	remoteSame := remoteUnchanged(info, s)

	switch {
	case localSame && remoteSame:
		return nil

	case localSame: // only the remote side changed
		switch {
		case info == nil && s.Kind == envelope.KindTombstone:
			a.Kind = Forget
		case info == nil:
			a.Kind, a.Note = Upload, "the remote copy was missing; uploaded again"
		case r.Header.Kind == envelope.KindTombstone && l == nil:
			a.Kind = Record
		case r.Header.Kind == envelope.KindTombstone:
			a.Kind = RemoveLocal
		case sameContent(l, r, os):
			a.Kind = Record
		default:
			a.Kind = Download
		}

	case remoteSame: // only this machine changed
		switch {
		case s == nil && firstSync && !adoptLocal:
			a.Kind = Pending
		case s == nil:
			a.Kind = Upload
		case l == nil:
			a.Kind, a.IfMatch = UploadDelete, s.ETag
		default:
			a.Kind, a.IfMatch = Upload, s.ETag
		}

	default: // both sides changed, or the remote ETag was never known
		switch {
		case info == nil && l == nil:
			a.Kind = Forget
		case info == nil:
			a.Kind, a.Note = Upload, "the remote copy was missing; uploaded again"
		case sameContent(l, r, os):
			a.Kind = Record
		case s == nil && r.Header.Kind == envelope.KindTombstone:
			if firstSync && !adoptLocal {
				a.Kind, a.Note = Pending, "deleted on another machine"
			} else {
				a.Kind, a.IfMatch, a.Note = Upload, r.ETag, "deleted on another machine; kept because it exists here"
			}
		case s == nil:
			a.Kind, a.KeepLocalCopy, a.Note = Download, true, "differs from the synced version; this machine's copy was saved to the conflicts directory"
		case l == nil:
			a.Kind, a.Note = Download, "deleted here but changed on another machine; restored"
		case r.Header.Kind == envelope.KindTombstone:
			a.Kind, a.IfMatch, a.Note = Upload, r.ETag, "deleted on another machine but changed here; kept"
		default:
			a.Kind, a.IfMatch = Conflict, r.ETag
		}
	}
	return a
}

// localUnchanged reports whether the entry on disk is what the last sync
// left there. Where the file system has no execute bit, only content counts.
func localUnchanged(l *entry.Entry, s *EntryState, os platform.OS) bool {
	if s == nil || s.Kind == envelope.KindTombstone {
		return l == nil
	}
	if l == nil || l.Kind != s.Kind {
		return false
	}
	if l.Kind == envelope.KindLink {
		return l.Target == s.Target
	}
	return l.Hash == s.Hash && (!os.TracksExecBit() || l.Executable == s.Exec)
}

// remoteUnchanged reports whether the remote object is the one the last
// sync saw. An unknown ETag counts as changed, so the object gets read.
func remoteUnchanged(info *storage.ObjectInfo, s *EntryState) bool {
	if s == nil {
		return info == nil
	}
	return info != nil && s.ETag != "" && info.ETag == s.ETag
}

// sameContent reports whether the entry on disk and the remote entry hold
// the same thing.
func sameContent(l *entry.Entry, r *remoteEntry, os platform.OS) bool {
	if l == nil {
		return r.Header.Kind == envelope.KindTombstone
	}
	if l.Kind != r.Header.Kind {
		return false
	}
	if l.Kind == envelope.KindLink {
		return l.Target == r.Header.LinkTarget
	}
	return l.Hash == r.Fingerprint && (!os.TracksExecBit() || l.Executable == r.Header.Executable)
}

// layoutConflicts finds entries that would end up below a link or a file on
// this machine: a path is a directory on one machine but a link or a file on
// another. Writing through a link would change the linked entry instead, so
// both the outer entry and the entries below it are left alone. It returns
// the keys to cancel, each with the outer key that explains it.
func layoutConflicts(actions map[string]*Action, local map[string]*entry.Entry) map[string]string {
	final := map[string]envelope.Kind{}
	for k, l := range local {
		final[k] = l.Kind
	}
	for k, a := range actions {
		switch a.Kind {
		case Download, Merge:
			if a.Remote.Header.Kind == envelope.KindTombstone {
				delete(final, k)
			} else {
				final[k] = a.Remote.Header.Kind
			}
		case RemoveLocal:
			delete(final, k)
		}
	}

	cancel := map[string]string{}
	for k := range final {
		for p := path.Dir(k); strings.Count(p, "/") >= 2; p = path.Dir(p) {
			if _, occupied := final[p]; occupied {
				cancel[k] = p
				cancel[p] = p
			}
		}
	}
	return cancel
}

// sortForApply orders actions so that the disk is always consistent:
// removals first, deepest paths first (a directory empties before a link can
// replace it), then files, then links (their targets may have just arrived).
func sortForApply(actions []*Action) {
	rank := func(a *Action) int {
		switch {
		case a.Kind == RemoveLocal:
			return 0
		case (a.Kind == Download || a.Kind == Merge) && a.Remote.Header.Kind == envelope.KindFile:
			return 1
		case a.Kind == Download:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(actions, func(i, j int) bool {
		ri, rj := rank(actions[i]), rank(actions[j])
		if ri != rj {
			return ri < rj
		}
		if ri == 0 {
			return actions[i].Key > actions[j].Key
		}
		return actions[i].Key < actions[j].Key
	})
}
