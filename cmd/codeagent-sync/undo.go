package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/platform"
)

func (a *app) undoCmd() *cobra.Command {
	var list, yes bool
	cmd := &cobra.Command{
		Use:   "undo [id]",
		Short: "Restore the files a sync, share or other change touched on this machine",
		Long: `Restore the files a change touched on this machine, from the backup taken
before it: by default the most recent change not undone yet, or the one with
the given ID. It shows what it will restore and asks first. Syncs that ran in
the background (automatic sync) are changes too; --list shows them all.

Nothing changes remotely until the next sync, which uploads the restored
versions and so reverts the change on your other machines too.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs, err := platform.LocalDirs()
			if err != nil {
				return err
			}
			eng := newEngine(dirs, nil, nil)
			backups, err := eng.Backups()
			if err != nil {
				return err
			}
			if list {
				return a.listBackups(backups)
			}

			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			target, later, err := pickBackup(backups, id)
			if err != nil {
				return err
			}
			a.printf("%s%s%s (%s, %s):\n", colorBold, target.What, colorReset, target.ID, target.Time.Local().Format("2006-01-02 15:04"))
			var shown []string
			for _, p := range target.Paths {
				shown = append(shown, homeRelative(p, dirs.Home))
			}
			a.printList(shown)
			if clobbered := overlapping(target, later); len(clobbered) > 0 {
				a.warn("Later changes to these paths are lost as well:")
				for _, b := range clobbered {
					a.warn("  %s (%s)", b.What, b.ID)
				}
			}
			ok, err := a.confirm("Restore these paths as they were before this change?", true, yes)
			if errors.Is(err, errNotInteractive) {
				return errors.New("undo needs a confirmation; run it in a terminal or pass --yes")
			}
			if err != nil || !ok {
				return err
			}
			info, restored, err := eng.Undo(target.ID)
			if err != nil {
				return err
			}
			a.success("Restored %d path(s) from before %s.", restored, info.What)
			a.info("The next sync uploads them, which reverts that change on your other machines too.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "list the changes that can be undone")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// pickBackup returns the change to undo, and the changes made after it
// that are not undone.
func pickBackup(backups []engine.BackupInfo, id string) (target engine.BackupInfo, later []engine.BackupInfo, err error) {
	for _, b := range backups { // newest first
		switch {
		case b.Undone:
		case id == "" || b.ID == id:
			return b, later, nil
		default:
			later = append(later, b)
		}
	}
	for _, b := range backups {
		if b.ID == id {
			return target, nil, fmt.Errorf("%s is undone already", id)
		}
	}
	if id != "" {
		return target, nil, fmt.Errorf("there is no backup %s (see codeagent-sync undo --list)", id)
	}
	return target, nil, errors.New("there is nothing to undo")
}

// overlapping returns the later changes that touched a path target touched.
func overlapping(target engine.BackupInfo, later []engine.BackupInfo) []engine.BackupInfo {
	paths := map[string]bool{}
	for _, p := range target.Paths {
		paths[p] = true
	}
	var out []engine.BackupInfo
	for _, b := range later {
		for _, p := range b.Paths {
			if paths[p] {
				out = append(out, b)
				break
			}
		}
	}
	return out
}

func (a *app) listBackups(backups []engine.BackupInfo) error {
	if a.jsonOut {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(backups)
	}
	if len(backups) == 0 {
		a.success("There is nothing to undo.")
		return nil
	}
	for _, b := range backups {
		state := ""
		if b.Undone {
			state = colorDim + " (undone)" + colorReset
		}
		a.printf("%s  %-16s  %s, %d path(s)%s\n", b.ID, b.Time.Local().Format("2006-01-02 15:04"), b.What, len(b.Paths), state)
	}
	a.printf("\nUndo one with: codeagent-sync undo <id>\n")
	return nil
}
