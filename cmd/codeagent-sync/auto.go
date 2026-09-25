package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/config"
	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/hooks"
	"github.com/sametbrr/codeagent-sync/internal/platform"
)

const hookSyncTimeout = 100 * time.Second

var (
	// hookMinInterval skips a background sync this soon after the last one.
	hookMinInterval = 15 * time.Second
	// executable and codexTrust are replaced by tests.
	executable = os.Executable
	codexTrust = hooks.CodexTrust
)

// hookFile is where a tool keeps its hooks.
type hookFile struct {
	tool hooks.Tool
	name string // for messages
	path string
	dir  string // the tool's directory: no directory, no tool
}

func hookFiles(d platform.Dirs) []hookFile {
	return []hookFile{
		{hooks.Claude, "Claude Code", filepath.Join(d.Claude, "settings.json"), d.Claude},
		{hooks.Codex, "Codex", filepath.Join(d.Codex, "hooks.json"), d.Codex},
	}
}

func (h hookFile) installed() bool {
	fi, err := os.Stat(h.dir)
	return err == nil && fi.IsDir()
}

func (a *app) autoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auto",
		Short: "Sync automatically, through hooks in Claude Code and Codex",
		Long: `Sync automatically: hooks in Claude Code and Codex sync in the background
when a session starts and after every answer, and pass what you should hear
about (a conflict, something new that could be shared) to the agent with
your next message.

The hooks are part of the synced configuration, so they reach your other
machines too. Codex runs new hooks only after you trust them in /hooks.`,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "enable",
		Short: "Add the hooks",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return a.autoEnable(runContext(cmd)) },
	}, &cobra.Command{
		Use:   "disable",
		Short: "Remove the hooks",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return a.autoDisable() },
	}, &cobra.Command{
		Use:   "status",
		Short: "Show whether the hooks are in place, and whether Codex trusts them",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return a.autoStatus(runContext(cmd)) },
	})
	return cmd
}

// program returns how hooks start this codeagent-sync: through the name
// PATH finds for it when that is the same file (a package manager's link
// outlives upgrades), otherwise by the path it runs from.
func program(home string) (hooks.Program, error) {
	exe, err := executable()
	if err != nil {
		return hooks.Program{}, err
	}
	if strings.Contains(exe, "go-build") {
		return hooks.Program{}, fmt.Errorf("%s is a temporary build; install codeagent-sync (for example at ~/.local/bin) and run that", exe)
	}
	if onPath, err := exec.LookPath("codeagent-sync"); err == nil && filepath.IsAbs(onPath) && sameFile(onPath, exe) {
		exe = onPath
	}
	if name := strings.TrimSuffix(filepath.Base(exe), ".exe"); name != "codeagent-sync" {
		return hooks.Program{}, fmt.Errorf("the hooks look for a program named codeagent-sync; rename %s", exe)
	}
	return hooks.NewProgram(exe, home)
}

func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// editHooks changes the hook files of the installed tools as one undoable
// step. A hook file that is a symlink (into a dotfiles repository, say) is
// written through the link.
func editHooks(dirs platform.Dirs, what string, change func(hookFile, []byte) ([]byte, error)) (changed []string, err error) {
	eng := newEngine(dirs, nil, nil)
	err = eng.WithLock(func() error {
		var backup *engine.Backup // started with the first change, so undo skips no-ops
		defer func() {
			if backup != nil {
				_ = backup.Close()
			}
		}()
		for _, h := range hookFiles(dirs) {
			if !h.installed() {
				continue
			}
			h.path = platform.LinkTarget(h.path)
			current, err := os.ReadFile(h.path)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			out, err := change(h, current)
			if err != nil {
				return fmt.Errorf("%s: %w", h.path, err)
			}
			if bytes.Equal(out, current) {
				continue
			}
			if backup == nil {
				if backup, err = eng.StartBackup(what); err != nil {
					return err
				}
			}
			if err := backup.Save(h.path); err != nil {
				return err
			}
			mode := fs.FileMode(0o644)
			if fi, err := os.Stat(h.path); err == nil {
				mode = fi.Mode().Perm()
			}
			if err := platform.WriteFileAtomic(h.path, out, mode); err != nil {
				return err
			}
			changed = append(changed, h.name)
		}
		return nil
	})
	return changed, err
}

func (a *app) autoEnable(ctx context.Context) error {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return err
	}
	if _, err := config.Load(dirs.State); err != nil {
		return err
	}
	prog, err := program(dirs.Home)
	if err != nil {
		return err
	}
	var tools []string
	changed, err := editHooks(dirs, "auto enable", func(h hookFile, file []byte) ([]byte, error) {
		tools = append(tools, h.name)
		if inPlace(file, prog, dirs.Home) {
			return file, nil // rewriting would move the hooks, and Codex would ask to trust them again
		}
		return hooks.Install(file, h.tool, prog)
	})
	if err != nil {
		return err
	}
	if len(tools) == 0 {
		return errors.New("neither Claude Code nor Codex is installed here")
	}
	if len(changed) == 0 {
		a.success("Automatic sync is already on for %s.", strings.Join(tools, " and "))
	} else {
		a.success("Automatic sync is on for %s.", strings.Join(tools, " and "))
		a.info("They sync in the background when a session starts and after every answer;")
		a.info("conflicts and new things to share reach the agent with your next message.")
	}
	where := prog.Path
	if prog.HomeRel != "" {
		where = "~/" + prog.HomeRel
	}
	a.info("The hooks reach your other machines with the next sync; install codeagent-sync at %s there too.", where)
	if _, err := os.Stat(dirs.Codex); err == nil {
		a.codexTrustAdvice(ctx, dirs.Home)
	}
	return nil
}

// inPlace reports whether file has all of codeagent-sync's hooks, starting
// prog.
func inPlace(file []byte, prog hooks.Program, home string) bool {
	events, err := hooks.Installed(file)
	if err != nil || len(events) != len(hooks.Ours) {
		return false
	}
	programs, err := hooks.Programs(file, home)
	return err == nil && len(programs) == 1 && filepath.Clean(programs[0]) == filepath.Clean(prog.Path)
}

// codexTrustAdvice tells the user to trust the hooks in Codex when Codex
// does not run them yet.
func (a *app) codexTrustAdvice(ctx context.Context, home string) {
	trust, err := codexTrust(ctx, home)
	if err == nil && allTrusted(trust) {
		return
	}
	a.warn("Codex runs new hooks only after you trust them: in Codex, type /hooks and trust")
	a.warn("the codeagent-sync hooks. Do this once on each machine (and after they change).")
}

func allTrusted(trust map[string]string) bool {
	if len(trust) < len(hooks.Ours) {
		return false
	}
	for _, status := range trust {
		if status != hooks.Trusted {
			return false
		}
	}
	return true
}

func (a *app) autoDisable() error {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return err
	}
	changed, err := editHooks(dirs, "auto disable", func(h hookFile, file []byte) ([]byte, error) {
		if !bytes.Contains(file, []byte("codeagent-sync")) {
			return file, nil
		}
		return hooks.Remove(file)
	})
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		a.success("Automatic sync was not on.")
		return nil
	}
	a.success("Automatic sync is off for %s.", strings.Join(changed, " and "))
	a.info("Your other machines turn it off with their next sync.")
	return nil
}

// autoState is the --json form of auto status.
type autoState struct {
	Tool   string            `json:"tool"`
	Hooks  []string          `json:"hooks"`
	Trust  map[string]string `json:"trust,omitempty"`
	Reason string            `json:"reason,omitempty"`
}

func (a *app) autoStates(ctx context.Context, dirs platform.Dirs) []autoState {
	var out []autoState
	for _, h := range hookFiles(dirs) {
		if !h.installed() {
			continue
		}
		st := autoState{Tool: string(h.tool)}
		data, err := os.ReadFile(h.path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			st.Reason = err.Error()
		} else if st.Hooks, err = hooks.Installed(data); err != nil {
			st.Reason = err.Error()
		}
		if h.tool == hooks.Codex && len(st.Hooks) > 0 {
			if trust, err := codexTrust(ctx, dirs.Home); err == nil {
				st.Trust = trust
			} else {
				st.Reason = "could not ask Codex whether it trusts the hooks: " + err.Error()
			}
		}
		out = append(out, st)
	}
	return out
}

func (a *app) autoStatus(ctx context.Context) error {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return err
	}
	states := a.autoStates(ctx, dirs)
	if a.jsonOut {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(states)
	}
	for _, st := range states {
		name := toolName(st.Tool)
		switch {
		case st.Reason != "" && len(st.Hooks) == 0:
			a.warn("%s: %s", name, st.Reason)
		case len(st.Hooks) == 0:
			a.printf("%s: off\n", name)
		case len(st.Hooks) < len(hooks.Ours):
			a.warn("%s: partly on (%s); run codeagent-sync auto enable", name, strings.Join(st.Hooks, ", "))
		case st.Tool == string(hooks.Codex) && st.Trust != nil && !allTrusted(st.Trust):
			a.warn("%s: on, but Codex does not run the hooks until you trust them: type /hooks in Codex", name)
		default:
			a.success("%s: on", name)
			if st.Reason != "" {
				a.info("  %s", st.Reason)
			}
		}
	}
	if len(states) == 0 {
		a.warn("Neither Claude Code nor Codex is installed here.")
	}
	return nil
}

// noHooksEnv, set, makes the hook commands do nothing: codeagent-sync sets
// it for the agent CLIs it starts itself, whose hooks would otherwise run
// codeagent-sync again.
const noHooksEnv = "CODEAGENT_SYNC_NO_HOOKS"

// hookCmd is what the hooks run. It never fails the agent's session: it
// always exits 0 (a tool reads 2 as "block the prompt"), and errors go to
// the hook log.
func (a *app) hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Run by the hooks of automatic sync",
		Hidden: true,
	}
	safely := func(fn func(ctx context.Context)) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			if os.Getenv(noHooksEnv) != "" {
				return nil
			}
			go io.Copy(io.Discard, a.in) // the tool writes the event; nothing in it is needed
			defer func() {
				if r := recover(); r != nil {
					if dirs, err := platform.LocalDirs(); err == nil {
						hookLog(dirs.State, fmt.Sprintf("hook %s failed: %v", cmd.Name(), r), time.Now())
					}
				}
			}()
			fn(runContext(cmd))
			return nil
		}
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "sync",
		Short: "Sync in the background and queue what the user should hear about",
		Args:  cobra.ArbitraryArgs,
		RunE:  safely(func(ctx context.Context) { a.hookSync(ctx, time.Now()) }),
	}, &cobra.Command{
		Use:   "notify",
		Short: "Print the queued notice for the agent",
		Args:  cobra.ArbitraryArgs,
		RunE: safely(func(ctx context.Context) {
			dirs, err := platform.LocalDirs()
			if err != nil {
				return
			}
			if text, err := takeNotice(dirs.State, time.Now()); err == nil && text != "" {
				fmt.Fprint(a.out, text)
			}
		}),
	})
	return cmd
}

func (a *app) hookSync(ctx context.Context, now time.Time) {
	eng, _, err := a.open()
	if errors.Is(err, config.ErrNotInitialized) {
		return
	}
	dirs, derr := platform.LocalDirs()
	if derr != nil {
		return
	}
	if err != nil {
		hookLog(dirs.State, "error: "+err.Error(), now)
		return
	}
	eng.LockTimeout = time.Millisecond // another sync is running: it covers this one

	var res *engine.Result
	var syncErr error
	if last, _, _ := eng.LastSync(); now.Sub(last) >= hookMinInterval {
		ctx, cancel := context.WithTimeout(ctx, hookSyncTimeout)
		res, syncErr = eng.Run(ctx, engine.Options{Background: true})
		cancel()
		switch {
		case errors.Is(syncErr, engine.ErrBusy):
			return
		case syncErr != nil:
			hookLog(dirs.State, "sync failed: "+syncErr.Error(), now)
		default:
			hookLog(dirs.State, "sync: "+summary(res), now)
		}
	}
	if res != nil {
		recordMachine(ctx, dirs, false)
	}
	s, err := a.sharing()
	if err != nil {
		return
	}
	if err := queueNotice(dirs.State, gatherNotice(s, eng, res, syncErr, now), res != nil, now); err != nil {
		hookLog(dirs.State, "notice: "+err.Error(), now)
	}
}

// summary counts what a sync did, for the hook log.
func summary(res *engine.Result) string {
	counts := map[engine.ActionKind]int{}
	for _, x := range res.Actions {
		counts[x.Kind]++
	}
	parts := []string{}
	for _, k := range []engine.ActionKind{engine.Download, engine.RemoveLocal, engine.Upload, engine.UploadDelete, engine.Merge} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
	}
	if n := len(res.Conflicts); n > 0 {
		parts = append(parts, fmt.Sprintf("%d conflicts", n))
	}
	if len(parts) == 0 {
		return "nothing to do"
	}
	return strings.Join(parts, ", ")
}
