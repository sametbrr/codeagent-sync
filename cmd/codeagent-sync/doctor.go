package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/check"
	"github.com/sametbrr/codeagent-sync/internal/config"
	"github.com/sametbrr/codeagent-sync/internal/crypto"
	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/hooks"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
)

// finding is one line of doctor's report.
type finding struct {
	Check  string `json:"check"`
	Status string `json:"status"` // ok, warn or fail
	Detail string `json:"detail"`
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the setup, the storage, the key, the hooks and the layout",
		Long: `Check everything codeagent-sync depends on: the setup, the storage and the
key, conditional writes, the last sync, conflicts, MCP servers held back
here, the hooks of automatic sync (and whether Codex trusts them), and
skill layouts the tools would ignore. It writes only a few probe objects,
and removes them again.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			findings := a.doctor(runContext(cmd))
			failed := false
			for _, f := range findings {
				failed = failed || f.Status == "fail"
			}
			if a.jsonOut {
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(findings); err != nil {
					return err
				}
			} else {
				for _, f := range findings {
					switch f.Status {
					case "ok":
						a.printf("%s✓%s %-15s %s\n", colorGreen, colorReset, f.Check, f.Detail)
					case "warn":
						a.printf("%s!%s %-15s %s\n", colorYellow, colorReset, f.Check, f.Detail)
					default:
						a.printf("%s✗%s %-15s %s\n", colorRed, colorReset, f.Check, f.Detail)
					}
				}
			}
			if failed {
				return exitError{1}
			}
			return nil
		},
	}
}

func (a *app) doctor(ctx context.Context) []finding {
	var out []finding
	add := func(check, status, format string, args ...any) {
		out = append(out, finding{check, status, fmt.Sprintf(format, args...)})
	}

	dirs, err := platform.LocalDirs()
	if err != nil {
		add("machine", "fail", "%v", err)
		return out
	}
	for _, tool := range []struct{ name, bin, dir string }{{"Claude Code", "claude", dirs.Claude}, {"Codex", "codex", dirs.Codex}} {
		_, dirErr := os.Stat(tool.dir)
		_, binErr := exec.LookPath(tool.bin)
		switch {
		case dirErr == nil && binErr == nil:
			add(tool.bin, "ok", "%s is installed", tool.name)
		case dirErr == nil:
			add(tool.bin, "ok", "%s's settings are here (%s is not on PATH)", tool.name, tool.bin)
		default:
			add(tool.bin, "ok", "%s is not installed here; its settings are left alone", tool.name)
		}
	}

	cfg, err := config.Load(dirs.State)
	if err != nil {
		add("setup", "fail", "%v", err)
		return append(out, a.layoutFindings(dirs)...)
	}
	add("setup", "ok", "%s, bucket %s", cfg.Storage.Provider, bucketName(cfg.Storage.Bucket, cfg.Storage.PathPrefix))

	enc, err := crypto.NewEncryptor(cfg.KeyPath(dirs.Home))
	if err != nil {
		add("key", "fail", "%v", err)
		return out
	}
	store, err := storage.New(&cfg.Storage)
	if err != nil {
		add("storage", "fail", "%v", err)
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if ok, err := store.BucketExists(ctx); err != nil || !ok {
		if err == nil {
			err = errors.New("the bucket does not exist")
		}
		add("storage", "fail", "%v", err)
		return out
	}
	add("storage", "ok", "reachable")
	if blob, _, err := store.Get(ctx, checkKey); err != nil {
		add("key", "fail", "read the bucket's key check: %v", err)
	} else if err := crypto.VerifyKeyCheck(enc, blob); err != nil {
		add("key", "fail", "this machine's key does not open the bucket: %v", err)
	} else {
		add("key", "ok", "opens the bucket")
	}
	if probe, err := storage.Probe(ctx, store, entry.MetaPrefix+"probe/"); err != nil {
		add("writes", "fail", "the storage does not accept writes: %v", err)
	} else if !probe.ConditionalWrites() {
		add("writes", "warn", "the storage ignores conditional writes; sync one machine at a time")
	} else {
		add("writes", "ok", "conditional writes work, so machines syncing at once cannot overwrite each other")
	}

	eng := newEngine(dirs, store, enc)
	last, machine, err := eng.LastSync()
	switch {
	case err != nil:
		add("last sync", "fail", "%v", err)
	case last.IsZero():
		add("last sync", "warn", "never; run codeagent-sync sync")
	case time.Since(last) > 7*24*time.Hour:
		add("last sync", "warn", "%s ago, as %s", ago(time.Since(last)), machine)
	default:
		add("last sync", "ok", "%s ago, as %s", ago(time.Since(last)), machine)
	}
	if eng.Busy() {
		add("sync", "ok", "a sync is running now")
	} else if id, ok := eng.Interrupted(); ok { // a running sync has its journal too
		add("sync", "warn", "a sync was interrupted; the next one completes it, and codeagent-sync undo %s restores what it changed", id)
	}
	if copies, err := eng.Conflicts(); err == nil && len(copies) > 0 {
		add("conflicts", "warn", "%d set aside; see codeagent-sync conflicts", len(copies))
	} else if err == nil {
		add("conflicts", "ok", "none")
	}
	if held, err := eng.Held(); err == nil {
		for key, items := range held {
			add("held", "warn", "%s: %s not used here, as their command is not installed", displayPath(key), strings.Join(items, ", "))
		}
	}

	out = append(out, a.hookFindings(ctx, dirs)...)
	return append(out, a.layoutFindings(dirs)...)
}

// hookFindings checks automatic sync: the hooks, the program they start,
// and whether Codex runs them.
func (a *app) hookFindings(ctx context.Context, dirs platform.Dirs) []finding {
	var out []finding
	for _, st := range a.autoStates(ctx, dirs) {
		name := toolName(st.Tool)
		switch {
		case len(st.Hooks) == 0 && st.Reason != "":
			out = append(out, finding{"auto sync", "warn", name + ": " + st.Reason})
		case len(st.Hooks) == 0:
			out = append(out, finding{"auto sync", "warn", name + ": off; turn it on with codeagent-sync auto enable"})
		case len(st.Hooks) < len(hooks.Ours):
			out = append(out, finding{"auto sync", "warn", name + ": partly on; run codeagent-sync auto enable"})
		case st.Trust != nil && !allTrusted(st.Trust):
			out = append(out, finding{"auto sync", "warn", name + ": on, but not trusted yet; type /hooks in Codex and trust the codeagent-sync hooks"})
		case st.Reason != "":
			out = append(out, finding{"auto sync", "ok", name + ": on (" + st.Reason + ")"})
		default:
			out = append(out, finding{"auto sync", "ok", name + ": on"})
		}
	}
	return append(out, missingPrograms(dirs)...)
}

// missingPrograms reports hooks that start a codeagent-sync this machine
// does not have.
func missingPrograms(dirs platform.Dirs) []finding {
	var out []finding
	for _, h := range hookFiles(dirs) {
		data, err := os.ReadFile(h.path)
		if err != nil {
			continue
		}
		programs, _ := hooks.Programs(data, dirs.Home)
		for _, p := range programs {
			if !programExists(p) {
				out = append(out, finding{"auto sync", "fail", fmt.Sprintf("%s's hooks start %s, which is not installed here", h.name, p)})
			}
		}
	}
	return out
}

func programExists(p string) bool {
	if !filepath.IsAbs(p) {
		_, err := exec.LookPath(p)
		return err == nil
	}
	if _, err := os.Stat(p); err == nil {
		return true
	}
	if runtime.GOOS == "windows" {
		_, err := os.Stat(p + ".exe")
		return err == nil
	}
	return false
}

// layoutFindings reports skill and agent layouts the tools would ignore.
func (a *app) layoutFindings(dirs platform.Dirs) []finding {
	findings := check.Run(dirs, a.variants(dirs))
	if len(findings) == 0 {
		return []finding{{"layout", "ok", "skills and agents are where the tools read them"}}
	}
	var out []finding
	for _, f := range findings {
		out = append(out, finding{"layout", "warn", f.Path + ": " + f.Reason + " (" + f.Hint + ")"})
	}
	return out
}
