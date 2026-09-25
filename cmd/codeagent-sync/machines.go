package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/inventory"
	"github.com/sametbrr/codeagent-sync/internal/platform"
)

// machineRefresh is how often a sync rewrites this machine's file.
const machineRefresh = 12 * time.Hour

// recordMachine writes this machine's file: always when force, otherwise
// when it is older than machineRefresh. It needs the machine's name, which
// the first sync gives it.
func recordMachine(ctx context.Context, dirs platform.Dirs, force bool) (inventory.Machine, bool) {
	_, name, err := newEngine(dirs, nil, nil).LastSync()
	if err != nil || name == "" {
		return inventory.Machine{}, false
	}
	if !force && inventory.Age(dirs.State, name) < machineRefresh {
		return inventory.Machine{}, false
	}
	m := inventory.Collect(ctx, dirs, name, version)
	if inventory.Save(dirs.State, m) != nil {
		return m, false
	}
	return m, true
}

// missingHere compares this machine with the others.
func missingHere(ctx context.Context, dirs platform.Dirs) (inventory.Machine, []inventory.Machine, []inventory.Missing) {
	here, _ := recordMachine(ctx, dirs, true)
	all, _ := inventory.Load(dirs.State)
	return here, all, inventory.Compare(here, all)
}

func installHint(m inventory.Missing) string {
	switch {
	case m.Install != "" && m.SameKind:
		return m.Install
	case m.Install != "":
		return m.Install + " (as on " + m.OnOS + ")"
	}
	return "install it as on " + m.On
}

func (a *app) machinesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "machines",
		Short: "Show how Claude Code, Codex and the MCP programs are installed on each machine",
		Long: `Show how Claude Code, Codex and the programs MCP servers start are installed
on each machine — versions and install commands — and what this machine
lacks that another one has. Every machine keeps its own file in
~/.codeagent-sync/machines/, which syncs; syncs refresh it twice a day.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs, err := platform.LocalDirs()
			if err != nil {
				return err
			}
			here, all, missing := missingHere(runContext(cmd), dirs)
			if a.jsonOut {
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"machines": all, "missing_here": missing})
			}
			if here.Name == "" {
				a.info("This machine gets its name with its first sync (codeagent-sync init).")
			}
			for _, m := range all {
				mark := ""
				if m.Name == here.Name {
					mark = " (this machine)"
				}
				a.printf("%s%s%s%s  %s/%s, updated %s ago\n", colorBold, m.Name, colorReset, mark, m.OS, m.Arch, ago(time.Since(m.Updated)))
				for _, p := range append(append([]inventory.Program{}, m.Tools...), m.Programs...) {
					state := p.Version
					if !p.Found {
						state = "not installed"
					}
					if p.Installer != "" {
						state = strings.TrimSpace(state + " via " + p.Installer)
					}
					uses := ""
					if len(p.For) > 0 {
						uses = colorDim + " for " + strings.Join(p.For, ", ") + colorReset
					}
					a.printf("    %-16s %s%s\n", p.Name, strings.TrimSpace(state+uses), "")
				}
			}
			if len(missing) == 0 {
				if len(all) > 1 {
					a.success("This machine has everything the others have.")
				}
				return nil
			}
			a.printf("\n%sMissing or different here:%s\n", colorBold, colorReset)
			for _, m := range missing {
				if m.Here != "" {
					a.printf("    %-16s %s here, %s on %s\n", m.Name, m.Here, m.Version, m.On)
					continue
				}
				a.printf("    %-16s %s\n", m.Name, installHint(m))
			}
			return nil
		},
	}
}

// machineFindings reports for doctor what this machine lacks.
func machineFindings(ctx context.Context, dirs platform.Dirs) []finding {
	_, _, missing := missingHere(ctx, dirs)
	var out []finding
	for _, m := range missing {
		if m.Here != "" {
			out = append(out, finding{"machines", "ok", fmt.Sprintf("%s is %s here, %s on %s", m.Name, m.Here, m.Version, m.On)})
			continue
		}
		out = append(out, finding{"machines", "warn", fmt.Sprintf("%s is on %s but not here: %s", m.Name, m.On, installHint(m))})
	}
	return out
}
