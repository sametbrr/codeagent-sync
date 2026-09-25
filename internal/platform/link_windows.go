//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// createLink tries a relative symlink first. Creating one needs Developer
// Mode or admin rights; without them a directory falls back to a junction,
// which works for any user but must store an absolute target.
func createLink(linkPath, relTarget string, isDir bool) (LinkKind, error) {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return "", err
	}
	target := filepath.FromSlash(relTarget)
	symErr := os.Symlink(target, linkPath)
	if symErr == nil {
		return LinkSymlink, nil
	}
	if !isDir {
		return "", fmt.Errorf("%w: %v", ErrLinkUnsupported, symErr)
	}
	abs := filepath.Join(filepath.Dir(linkPath), target)
	out, err := exec.Command("cmd", "/c", "mklink", "/J", linkPath, abs).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: symlink: %v; junction: %v: %s",
			ErrLinkUnsupported, symErr, err, strings.TrimSpace(string(out)))
	}
	return LinkJunction, nil
}

// readLink recognizes both symlinks and junctions. Since Go 1.23 junctions
// (mount points) report ModeIrregular rather than ModeSymlink.
func readLink(linkPath string) (string, LinkKind, bool, error) {
	fi, err := os.Lstat(linkPath)
	if err != nil {
		return "", "", false, err
	}
	mode := fi.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(linkPath)
		if err != nil {
			return "", "", false, err
		}
		return filepath.ToSlash(target), LinkSymlink, true, nil
	case mode&os.ModeIrregular != 0:
		target, err := os.Readlink(linkPath)
		if err != nil {
			// Some other kind of reparse point: not a link we manage.
			return "", "", false, nil
		}
		rel, err := filepath.Rel(filepath.Dir(linkPath), target)
		if err != nil {
			return filepath.ToSlash(target), LinkJunction, true, nil
		}
		return filepath.ToSlash(rel), LinkJunction, true, nil
	}
	return "", "", false, nil
}
