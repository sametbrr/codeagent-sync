//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly || windows)

package platform

import (
	"errors"
	"os"
)

func lockFile(*os.File) error   { return errors.New("file locking is not supported on this platform") }
func unlockFile(*os.File) error { return nil }
