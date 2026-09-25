//go:build !windows

package platform

import (
	"os"
	"path/filepath"
)

func createLink(linkPath, relTarget string, _ bool) (LinkKind, error) {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return "", err
	}
	if err := os.Symlink(filepath.FromSlash(relTarget), linkPath); err != nil {
		return "", err
	}
	return LinkSymlink, nil
}

func readLink(linkPath string) (string, LinkKind, bool, error) {
	fi, err := os.Lstat(linkPath)
	if err != nil {
		return "", "", false, err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return "", "", false, nil
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		return "", "", false, err
	}
	return filepath.ToSlash(target), LinkSymlink, true, nil
}
