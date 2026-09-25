package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/check"
	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/platform"
)

type syncMode int

const (
	modeBoth syncMode = iota
	modePull
	modePush
)

func (a *app) syncCmd(use, short string, mode syncMode) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, _, err := a.open()
			if err != nil {
				return err
			}
			opts := engine.Options{DryRun: dryRun, AdoptLocal: yes}
			switch mode {
			case modePull:
				opts.Mode = engine.PullOnly
			case modePush:
				opts.Mode = engine.PushOnly
			}
			res, err := eng.Run(runContext(cmd), opts)
			if errors.Is(err, engine.ErrBusy) && a.quiet {
				return nil // a hook fired while another sync runs; that one covers it
			}
			if err != nil {
				return err
			}
			return a.report(res, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "on this machine's first sync, also upload entries that exist only here")
	return cmd
}

func (a *app) statusCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what a sync would change, conflicts and problems",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkOnly {
				return a.runCheck()
			}
			eng, cfg, err := a.open()
			if err != nil {
				return err
			}
			last, machine, err := eng.LastSync()
			if err != nil {
				return err
			}
			res, err := eng.Run(runContext(cmd), engine.Options{DryRun: true})
			if err != nil {
				return err
			}
			if !a.jsonOut {
				a.printf("%sStorage:%s   %s, bucket %s\n", colorBold, colorReset, cfg.Storage.Provider, bucketName(cfg.Storage.Bucket, cfg.Storage.PathPrefix))
				if machine != "" {
					a.printf("%sMachine:%s   %s\n", colorBold, colorReset, machine)
				}
				if last.IsZero() {
					a.printf("%sLast sync:%s never\n\n", colorBold, colorReset)
				} else {
					a.printf("%sLast sync:%s %s (%s ago)\n\n", colorBold, colorReset, last.Local().Format("2006-01-02 15:04"), ago(time.Since(last)))
				}
			}
			return a.report(res, true)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "look for configurations the tools silently ignore or read differently")
	return cmd
}

// runCheck prints what package check finds.
func (a *app) runCheck() error {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return err
	}
	findings := check.Run(dirs, a.variants(dirs))
	if len(findings) == 0 {
		a.success("No problems found in the skill and agent layout.")
		return nil
	}
	for _, f := range findings {
		a.printf("%s!%s %s\n    %s\n    %s%s%s\n", colorYellow, colorReset, f.Path, f.Reason, colorDim, f.Hint, colorReset)
	}
	return nil
}

func bucketName(bucket, prefix string) string {
	if bucket == "" {
		return prefix
	}
	return bucket
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// displayPath shows a key as <root>/<rel>.
func displayPath(key string) string {
	if root, rel, ok := entry.ParseKey(key); ok {
		return root + "/" + rel
	}
	return key
}

// jsonResult is the --json form of a run.
type jsonResult struct {
	DryRun    bool          `json:"dry_run"`
	Actions   []jsonAction  `json:"actions"`
	Pending   []jsonAction  `json:"pending"`
	Conflicts []jsonAction  `json:"conflicts"`
	Problems  []jsonProblem `json:"problems"`
	Notices   []string      `json:"notices"`
	Backup    string        `json:"backup,omitempty"`
}

type jsonAction struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Note string `json:"note,omitempty"`
}

type jsonProblem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func toJSONActions(actions []*engine.Action) []jsonAction {
	out := []jsonAction{}
	for _, x := range actions {
		out = append(out, jsonAction{Kind: string(x.Kind), Path: displayPath(x.Key), Note: x.Note})
	}
	return out
}

// report prints a run's result and turns conflicts into exit code 2.
func (a *app) report(res *engine.Result, dryRun bool) error {
	if a.jsonOut {
		out := jsonResult{
			DryRun:    dryRun,
			Actions:   toJSONActions(res.Actions),
			Pending:   toJSONActions(res.Pending),
			Conflicts: toJSONActions(res.Conflicts),
			Problems:  []jsonProblem{},
			Notices:   append([]string{}, res.Notices...),
			Backup:    res.BackupID,
		}
		for _, p := range res.Problems {
			out.Problems = append(out.Problems, jsonProblem{Path: displayPath(p.Key), Reason: p.Reason})
		}
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		a.printActions(res, dryRun)
	}
	if code := res.ExitCode(); code != 0 {
		return exitError{code}
	}
	return nil
}

func (a *app) printActions(res *engine.Result, dryRun bool) {
	groups := []struct {
		kind      engine.ActionKind
		symbol    string
		done, dry string
	}{
		{engine.Download, colorCyan + "↓" + colorReset, "from your other machines", "to download from your other machines"},
		{engine.RemoveLocal, colorCyan + "✗" + colorReset, "removed here (deleted on another machine)", "to remove here (deleted on another machine)"},
		{engine.Upload, colorGreen + "↑" + colorReset, "uploaded", "to upload"},
		{engine.UploadDelete, colorGreen + "✗" + colorReset, "deletions uploaded", "deletions to upload"},
	}
	changed := 0
	for _, g := range groups {
		var paths []string
		for _, x := range res.Actions {
			if x.Kind == g.kind {
				line := displayPath(x.Key)
				if x.Note != "" {
					line += colorDim + " — " + x.Note + colorReset
				}
				paths = append(paths, line)
			}
		}
		if len(paths) == 0 {
			continue
		}
		changed += len(paths)
		sort.Strings(paths)
		title := g.done
		if dryRun {
			title = g.dry
		}
		a.printf("%s %d %s\n", g.symbol, len(paths), title)
		a.printList(paths)
	}

	if len(res.Pending) > 0 {
		var paths []string
		for _, x := range res.Pending {
			paths = append(paths, displayPath(x.Key))
		}
		sort.Strings(paths)
		a.printf("%s• %d exist only on this machine%s (first sync); upload them with: codeagent-sync sync --yes\n", colorYellow, len(paths), colorReset)
		a.printList(paths)
	}

	if len(res.Conflicts) > 0 {
		a.warn("%d changed on this machine and on another one; this machine's version is kept:", len(res.Conflicts))
		for _, x := range res.Conflicts {
			fmt.Fprintf(a.errOut, "    %s\n", displayPath(x.Key))
		}
		fmt.Fprintf(a.errOut, "  The other versions are saved; see: codeagent-sync conflicts\n")
	}
	for _, p := range res.Problems {
		a.warn("%s: %s", displayPath(p.Key), p.Reason)
	}
	for _, n := range res.Notices {
		a.warn("%s", n)
	}

	if changed == 0 && len(res.Pending) == 0 && len(res.Conflicts) == 0 {
		a.success("Everything is in sync.")
	} else if res.BackupID != "" {
		a.info("Files changed here are backed up; codeagent-sync undo restores them.")
	}
}

func (a *app) printList(paths []string) {
	const max = 15
	for i, p := range paths {
		if i == max {
			a.printf("    %s… and %d more%s\n", colorDim, len(paths)-max, colorReset)
			return
		}
		a.printf("    %s\n", p)
	}
}

// keyFromPath accepts a key (v1/claude/x), a displayed path (claude/x) or a
// path on disk, and returns the key.
func keyFromPath(eng *engine.Engine, arg string) (string, error) {
	if _, _, ok := entry.ParseKey(arg); ok {
		return arg, nil
	}
	if root, rel, ok := strings.Cut(arg, "/"); ok {
		for _, r := range eng.Roots {
			if r.Name == root && rel != "" {
				return entry.Key(root, rel), nil
			}
		}
	}
	return keyFromDiskPath(eng, arg)
}

func runContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
