// Package platform isolates everything that differs between macOS, Linux and
// Windows: where the agent tools keep their files, how files are replaced
// atomically, how processes lock each other out, which file names are legal
// and how directory links are created.
//
// Decisions that must hold for every platform take an OS value instead of
// reading runtime.GOOS, so Windows and Linux behavior is exercised by tests
// running on any machine.
package platform

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// OS names an operating-system family.
type OS string

const (
	Darwin  OS = "darwin"
	Linux   OS = "linux"
	Windows OS = "windows"
)

// Current returns the OS family of the running process. Unix systems other
// than macOS behave like Linux for everything this package decides.
func Current() OS {
	switch runtime.GOOS {
	case "darwin":
		return Darwin
	case "windows":
		return Windows
	default:
		return Linux
	}
}

// CaseInsensitive reports whether the platform's default file system treats
// names that differ only in letter case as the same file.
func (o OS) CaseInsensitive() bool { return o == Darwin || o == Windows }

// TracksExecBit reports whether the file system records the execute
// permission. On Windows it does not, so a synced file's execute bit must be
// carried over from the remote copy instead of being read from disk.
func (o OS) TracksExecBit() bool { return o != Windows }

// Dirs holds the directories and files the agent tools use on one machine.
type Dirs struct {
	Home       string // the user's home directory
	Claude     string // Claude Code config dir: $CLAUDE_CONFIG_DIR or ~/.claude
	ClaudeJSON string // Claude Code global state file, holds user-scope MCP servers
	Codex      string // Codex home: $CODEX_HOME or ~/.codex
	Agents     string // shared agent dir read by Codex and `npx skills`: ~/.agents
	State      string // codeagent-sync's own config and state: ~/.codeagent-sync
}

// ResolveDirs computes Dirs for the given home directory. getenv is normally
// os.Getenv; tests pass a stub.
func ResolveDirs(home string, getenv func(string) string) Dirs {
	d := Dirs{
		Home:       home,
		Claude:     filepath.Join(home, ".claude"),
		ClaudeJSON: filepath.Join(home, ".claude.json"),
		Codex:      filepath.Join(home, ".codex"),
		Agents:     filepath.Join(home, ".agents"),
		State:      filepath.Join(home, ".codeagent-sync"),
	}
	if v := getenv("CLAUDE_CONFIG_DIR"); v != "" {
		d.Claude = expandHome(v, home)
		// With a custom config dir, Claude Code keeps its global state file
		// inside that dir instead of next to it in the home directory.
		d.ClaudeJSON = filepath.Join(d.Claude, ".claude.json")
	}
	if v := getenv("CODEX_HOME"); v != "" {
		d.Codex = expandHome(v, home)
	}
	return d
}

// LocalDirs resolves Dirs for the current user and environment.
func LocalDirs() (Dirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, err
	}
	if home == "" {
		return Dirs{}, errors.New("home directory is not set")
	}
	return ResolveDirs(home, os.Getenv), nil
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return filepath.Clean(p)
}

// Executable reports whether mode has any execute bit set.
func Executable(mode fs.FileMode) bool { return mode.Perm()&0o111 != 0 }

// FilePerm returns the permission bits for a synced file.
func FilePerm(executable bool) fs.FileMode {
	if executable {
		return 0o755
	}
	return 0o644
}
