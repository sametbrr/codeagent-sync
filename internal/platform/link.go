package platform

import (
	"errors"
	"os"
	"path/filepath"
)

// LinkKind says how a link is materialized on this machine.
type LinkKind string

const (
	// LinkSymlink is a relative symbolic link (macOS, Linux, and Windows with
	// Developer Mode or admin rights).
	LinkSymlink LinkKind = "symlink"
	// LinkJunction is an NTFS directory junction, the Windows fallback that
	// needs no privilege but stores an absolute target.
	LinkJunction LinkKind = "junction"
)

// ErrLinkUnsupported means no link could be created at all; the caller falls
// back to a managed copy.
var ErrLinkUnsupported = errors.New("links are not supported here")

// CreateLink creates a link at linkPath pointing to relTarget, a
// slash-separated path relative to linkPath's parent directory, for example
// "../../.agents/skills/foo". isDir tells whether the target is a directory,
// which decides whether a junction is a possible fallback. Missing parent
// directories are created; an existing entry at linkPath is an error.
func CreateLink(linkPath, relTarget string, isDir bool) (LinkKind, error) {
	return createLink(linkPath, relTarget, isDir)
}

// ReadLink reports whether linkPath is a link. If it is, target is its
// destination as a slash-separated path relative to linkPath's parent
// directory, or an absolute path if the link points outside of that form.
func ReadLink(linkPath string) (target string, kind LinkKind, ok bool, err error) {
	return readLink(linkPath)
}

// LinkTarget returns the file p leads to when p is a symlink (a settings
// file kept in a dotfiles repository, say), so that writing it keeps the
// link; otherwise, or when the link is broken, p itself.
func LinkTarget(p string) string {
	if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
