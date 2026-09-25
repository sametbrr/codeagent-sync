package platform

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("locked by another process")

const lockPollInterval = 50 * time.Millisecond

// FileLock is an exclusive lock shared by processes on one machine, backed by
// an OS file lock. The OS releases it when the holding process exits, so a
// crashed run never leaves a stale lock behind.
type FileLock struct {
	f *os.File
}

// TryLock acquires the lock at path without waiting. It returns ErrLocked if
// another process holds it.
func TryLock(path string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &FileLock{f: f}, nil
}

// Lock acquires the lock at path, waiting up to timeout for another process
// to release it.
func Lock(path string, timeout time.Duration) (*FileLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		l, err := TryLock(path)
		if !errors.Is(err, ErrLocked) {
			return l, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(lockPollInterval)
	}
}

// Unlock releases the lock. It is safe to call more than once.
func (l *FileLock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
