package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/engine"
	"github.com/sametbrr/codeagent-sync/internal/entry"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

func (a *app) conflictsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conflicts",
		Short: "List the versions set aside because of conflicts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, _, err := a.open()
			if err != nil {
				return err
			}
			copies, err := eng.Conflicts()
			if err != nil {
				return err
			}
			if a.jsonOut {
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				return enc.Encode(copies)
			}
			if len(copies) == 0 {
				a.success("No conflicts.")
				return nil
			}
			for _, c := range copies {
				switch c.Side {
				case "remote":
					who := c.Machine
					if who == "" {
						who = "another machine"
					}
					a.printf("%s%s%s\n  kept: this machine's version\n  other version (from %s): %s\n",
						colorBold, displayPath(c.Key), colorReset, who, c.Path)
				default:
					a.printf("%s%s%s\n  replaced by the synced version on this machine's first sync\n  this machine's old version: %s\n",
						colorBold, displayPath(c.Key), colorReset, c.Path)
				}
			}
			a.printf("\nResolve with: codeagent-sync conflicts resolve <path> --keep local|remote\n")
			return nil
		},
	}
	cmd.AddCommand(a.resolveCmd())
	return cmd
}

func (a *app) resolveCmd() *cobra.Command {
	var keep string
	cmd := &cobra.Command{
		Use:   "resolve <path>",
		Short: "Settle a conflict by keeping this machine's version (local) or the other one (remote)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if keep != "local" && keep != "remote" {
				return errors.New("--keep must be local or remote")
			}
			eng, _, err := a.open()
			if err != nil {
				return err
			}
			key, err := keyFromPath(eng, args[0])
			if err != nil {
				return err
			}
			err = eng.Resolve(runContext(cmd), key, keep == "local")
			if errors.Is(err, engine.ErrNoConflict) {
				a.success("%s has no conflict any more.", displayPath(key))
				return nil
			}
			if err != nil {
				return err
			}
			if keep == "local" {
				a.success("Kept this machine's version of %s; your other machines get it on their next sync.", displayPath(key))
			} else {
				a.success("Took the other version of %s (the replaced one is backed up; undo restores it).", displayPath(key))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&keep, "keep", "", "local or remote")
	_ = cmd.MarkFlagRequired("keep")
	return cmd
}

// keyFromDiskPath maps a path on disk to its key.
func keyFromDiskPath(eng *engine.Engine, p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err != nil {
		// A deleted file can still be in conflict; its parent tells the root.
		abs = filepath.Clean(abs)
	}
	root, rel, ok := tools.Owner(eng.Roots, abs)
	if !ok || !root.Includes(rel) {
		return "", fmt.Errorf("%s is not a synced path", p)
	}
	return entry.Key(root.Name, rel), nil
}
