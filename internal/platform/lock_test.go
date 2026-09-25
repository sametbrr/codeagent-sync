package platform

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTryLockExcludesSecondHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "sync.lock")

	first, err := TryLock(p)
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	if _, err := TryLock(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock error = %v, want ErrLocked", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("second Unlock should be a no-op, got %v", err)
	}

	again, err := TryLock(p)
	if err != nil {
		t.Fatalf("TryLock after release: %v", err)
	}
	again.Unlock()
}

func TestLockTimesOut(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sync.lock")
	held, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()

	start := time.Now()
	if _, err := Lock(p, 150*time.Millisecond); !errors.Is(err, ErrLocked) {
		t.Fatalf("Lock error = %v, want ErrLocked", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("Lock gave up after %v, before the timeout", elapsed)
	}
}

func TestLockWaitsForRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sync.lock")
	held, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		held.Unlock()
	}()

	l, err := Lock(p, 5*time.Second)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	l.Unlock()
}

// TestLockAcrossProcesses runs a helper process that takes the lock, checks
// that this process is locked out, then kills the helper: the OS must release
// the lock of a process that died without unlocking.
func TestLockAcrossProcesses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sync.lock")

	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), "CODEAGENT_LOCK_HELPER="+p)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		cmd.Process.Kill()
		t.Fatalf("helper did not report the lock: %q, %v", line, err)
	}

	if _, err := TryLock(p); !errors.Is(err, ErrLocked) {
		cmd.Process.Kill()
		t.Fatalf("TryLock while helper holds the lock = %v, want ErrLocked", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()

	l, err := Lock(p, 5*time.Second)
	if err != nil {
		t.Fatalf("lock of a killed process was not released: %v", err)
	}
	l.Unlock()
}

func TestLockHelperProcess(t *testing.T) {
	p := os.Getenv("CODEAGENT_LOCK_HELPER")
	if p == "" {
		t.Skip("only runs as a helper process")
	}
	if _, err := TryLock(p); err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	fmt.Println("locked")
	io.Copy(io.Discard, os.Stdin) // hold the lock until killed
	os.Exit(0)
}
