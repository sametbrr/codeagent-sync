package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

func (a *app) pathsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "paths",
		Short: "Show and change what is synced (include and exclude rules)",
		Long: `Show and change what is synced. Beyond the defaults, rules add paths
(include) or leave paths out (exclude), written as <root>/<pattern>:

  claude/plans/**                   a directory under ~/.claude
  codex/agents/old-*.toml           files under ~/.codex
  claude-state/.claude.json         Claude Code's MCP servers
  claude/settings.json#permissions  one setting (items only after #)
  codex/config.toml#mcp_servers.*   Codex's MCP servers

Rules live in ~/.codeagent-sync/sync.yaml, which syncs so every machine
follows them, and with --local in sync.local.yaml, for this machine only.
A path left out is not deleted anywhere: it stops syncing. An item left out
keeps each machine's own version. Credentials, sessions and history are
never synced.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return a.pathsList() },
	}
	var local bool
	change := func(use, short string, edit func(*tools.Rules, string) (string, error)) *cobra.Command {
		c := &cobra.Command{
			Use:   use + " <pattern>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				dirs, err := platform.LocalDirs()
				if err != nil {
					return err
				}
				shared, loc, err := tools.LoadRules(dirs.State)
				if err != nil {
					return err
				}
				rules, name, where := &shared, tools.SharedRulesFile, "every machine (after the next sync)"
				if local {
					rules, name, where = &loc, tools.LocalRulesFile, "this machine"
				}
				msg, err := edit(rules, strings.TrimSpace(args[0]))
				if err != nil {
					return err
				}
				if err := newEngine(dirs, nil, nil).WithLock(func() error { return tools.SaveRules(dirs.State, name, *rules) }); err != nil {
					return err
				}
				a.success("%s, for %s.", msg, where)
				return nil
			},
		}
		c.Flags().BoolVar(&local, "local", false, "for this machine only (sync.local.yaml)")
		return c
	}
	roots := func() []tools.Root {
		dirs, _ := platform.LocalDirs()
		return tools.Roots(dirs)
	}
	cmd.AddCommand(
		&cobra.Command{Use: "list", Short: "Show what is synced", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error { return a.pathsList() }},
		change("include", "Sync a path too", func(r *tools.Rules, p string) (string, error) {
			if err := tools.CheckRule(p, true, roots()); err != nil {
				return "", err
			}
			r.Exclude = slices.DeleteFunc(r.Exclude, func(x string) bool { return x == p })
			if !slices.Contains(r.Include, p) {
				r.Include = append(r.Include, p)
			}
			return "Included " + p, nil
		}),
		change("exclude", "Leave a path, or items of a settings file, out", func(r *tools.Rules, p string) (string, error) {
			if err := tools.CheckRule(p, false, roots()); err != nil {
				return "", err
			}
			r.Include = slices.DeleteFunc(r.Include, func(x string) bool { return x == p })
			if !slices.Contains(r.Exclude, p) {
				r.Exclude = append(r.Exclude, p)
			}
			return "Excluded " + p, nil
		}),
		change("reset", "Remove a rule", func(r *tools.Rules, p string) (string, error) {
			n := len(r.Include) + len(r.Exclude)
			r.Include = slices.DeleteFunc(r.Include, func(x string) bool { return x == p })
			r.Exclude = slices.DeleteFunc(r.Exclude, func(x string) bool { return x == p })
			if len(r.Include)+len(r.Exclude) == n {
				return "", fmt.Errorf("there is no rule %s here (see codeagent-sync paths)", p)
			}
			return "Removed the rule " + p, nil
		}),
	)
	return cmd
}

// pathsState is the --json form of paths.
type pathsState struct {
	Roots []pathsRoot `json:"roots"`
	Rules struct {
		Shared tools.Rules `json:"shared"`
		Local  tools.Rules `json:"local"`
	} `json:"rules"`
	Problems []string `json:"problems,omitempty"`
}

type pathsRoot struct {
	Name    string              `json:"name"`
	Dir     string              `json:"dir"`
	Include []string            `json:"include"`
	Exclude []string            `json:"exclude"`
	Hidden  map[string][]string `json:"hidden_items,omitempty"`
}

func (a *app) pathsList() error {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return err
	}
	var st pathsState
	st.Rules.Shared, st.Rules.Local, err = tools.LoadRules(dirs.State)
	if err != nil {
		return err
	}
	roots, problems := syncRoots(dirs)
	for _, p := range problems {
		st.Problems = append(st.Problems, p.Error())
	}
	for _, r := range roots {
		st.Roots = append(st.Roots, pathsRoot{Name: r.Name, Dir: r.Dir, Include: r.Include, Exclude: r.Exclude, Hidden: r.Hidden})
	}
	if a.jsonOut {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	for _, r := range st.Roots {
		a.printf("%s%s%s  %s\n", colorBold, r.Name, colorReset, homeRelative(r.Dir, dirs.Home))
		a.printf("    include: %s\n", strings.Join(r.Include, ", "))
		if ex := withoutLitter(r.Exclude); len(ex) > 0 {
			a.printf("    %sexclude: %s%s\n", colorDim, strings.Join(ex, ", "), colorReset)
		}
		for rel, items := range r.Hidden {
			a.printf("    kept per machine: %s#%s\n", rel, strings.Join(items, ", #"))
		}
	}
	for _, set := range []struct {
		name  string
		rules tools.Rules
	}{{"~/.codeagent-sync/" + tools.SharedRulesFile + " (every machine)", st.Rules.Shared}, {"~/.codeagent-sync/" + tools.LocalRulesFile + " (this machine)", st.Rules.Local}} {
		if len(set.rules.Include)+len(set.rules.Exclude) == 0 {
			continue
		}
		a.printf("\n%sRules in %s:%s\n", colorBold, set.name, colorReset)
		for _, p := range set.rules.Include {
			a.printf("    + %s\n", p)
		}
		for _, p := range set.rules.Exclude {
			a.printf("    - %s\n", p)
		}
	}
	for _, p := range st.Problems {
		a.warn("skipped rule: %s", p)
	}
	a.printf("\nChange with: codeagent-sync paths include|exclude|reset <pattern> [--local]\n")
	return nil
}

// withoutLitter drops the patterns every root excludes (OS and editor
// litter), which only clutter the list.
func withoutLitter(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		if !strings.HasPrefix(p, "**/") {
			out = append(out, p)
		}
	}
	return out
}
