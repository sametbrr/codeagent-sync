package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"

	"github.com/sametbrr/codeagent-sync/internal/compat"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/promote"
	"github.com/sametbrr/codeagent-sync/internal/registry"
)

// runTool runs a tool's own command when sharing needs one; nil runs it for
// real. Tests replace it.
var runTool func(name string, args ...string) error

// sharing is what scan, share, unshare and mark work with.
type sharing struct {
	dirs platform.Dirs
	env  compat.Env
	pr   *promote.Promoter
}

func (a *app) sharing() (*sharing, error) {
	dirs, err := platform.LocalDirs()
	if err != nil {
		return nil, err
	}
	reg, err := registry.Load(dirs.State)
	if err != nil {
		return nil, err
	}
	env := compat.Env{Dirs: dirs, Registry: reg, Exists: func(p string) bool { _, err := os.Stat(p); return err == nil }}
	pr := &promote.Promoter{Engine: newEngine(dirs, nil, nil), Dirs: dirs, Registry: reg, Run: runTool}
	return &sharing{dirs: dirs, env: env, pr: pr}, nil
}

// save writes the decisions, holding the sync lock.
func (s *sharing) save() error {
	return s.pr.Engine.WithLock(func() error { return s.env.Registry.Save(s.dirs.State) })
}

func toolOnly(d compat.Direction) string {
	if d == compat.ToCodex {
		return registry.ClaudeOnly
	}
	return registry.CodexOnly
}

var verdictSymbol = map[compat.Verdict]string{compat.OK: "✓", compat.Warn: "⚠", compat.Blocked: "✗", compat.Variant: "⇄"}

// scanItem is a candidate with the second opinion on it, if one was asked.
type scanItem struct {
	compat.Candidate
	Opinion string `json:"opinion,omitempty"`
}

// scanReport is the --json form of scan.
type scanReport struct {
	Shareable []scanItem `json:"shareable"`
	Recorded  []scanItem `json:"recorded"`
}

func (a *app) scanCmd() *cobra.Command {
	var yes, deep bool
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Find skills, MCP servers and plugin marketplaces only one tool has, and offer to share them",
		Long: `Find skills, MCP servers and plugin marketplaces that Claude Code or Codex has
and the other does not, and judge whether they work in the other tool too.

What depends on its tool (or exists as one version per tool) is recorded as
such once. Everything else is offered: share it now, not now, or keep it
with its tool for good. Decisions sync to your other machines.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.sharing()
			if err != nil {
				return err
			}
			cands, err := compat.Scan(s.env)
			if err != nil {
				return err
			}
			var report scanReport
			for _, c := range cands {
				switch c.Verdict {
				case compat.Blocked:
					s.env.Registry.Set(c.Kind, c.Name, toolOnly(c.Direction))
					report.Recorded = append(report.Recorded, scanItem{Candidate: c})
				case compat.Variant:
					s.env.Registry.Set(c.Kind, c.Name, registry.Variant)
					report.Recorded = append(report.Recorded, scanItem{Candidate: c})
				default:
					report.Shareable = append(report.Shareable, scanItem{Candidate: c})
				}
			}
			if len(report.Recorded) > 0 {
				if err := s.save(); err != nil {
					return err
				}
			}
			if a.jsonOut {
				if deep {
					for i := range report.Shareable {
						if report.Shareable[i].Kind == registry.Skill {
							report.Shareable[i].Opinion, _ = compat.SecondOpinion(runContext(cmd), report.Shareable[i].Candidate)
						}
					}
				}
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			return a.offer(cmd, s, report, yes, deep)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "share everything that can be shared, without asking")
	cmd.Flags().BoolVar(&deep, "deep", false, "also ask the other tool's CLI for a second opinion on each skill")
	return cmd
}

func (a *app) offer(cmd *cobra.Command, s *sharing, report scanReport, yes, deep bool) error {
	for _, item := range report.Recorded {
		c := item.Candidate
		stays := "stays with " + c.Direction.Tool()
		if c.Verdict == compat.Variant {
			stays = "one version per tool"
		}
		a.printf("%s %s %s — %s: %s\n", verdictSymbol[c.Verdict], c.Kind, c.Name, stays, firstText(c))
	}
	if len(report.Recorded) > 0 {
		a.info("Recorded. To have one checked again: codeagent-sync mark <name> --forget")
	}
	if len(report.Shareable) == 0 {
		a.success("Nothing new to share.")
		return nil
	}
	if len(report.Recorded) > 0 {
		a.printf("\n")
	}

	shared := 0
	for _, item := range report.Shareable {
		c := item.Candidate
		a.describe(c)
		if deep && c.Kind == registry.Skill {
			if opinion, err := compat.SecondOpinion(runContext(cmd), c); err == nil {
				a.printf("    %ssecond opinion:%s %s\n", colorBold, colorReset, opinion)
			} else {
				a.info("  no second opinion: %v", err)
			}
		}
		choice := "share"
		switch {
		case yes:
		case a.interactive():
			options := []string{"Share it", "Not now", "Keep it with " + c.Direction.Tool() + " only (don't ask again)"}
			var picked string
			if err := survey.AskOne(&survey.Select{Message: "Share " + c.Kind + " " + c.Name + " with " + c.Direction.Target() + "?", Options: options}, &picked); err != nil {
				return err
			}
			switch picked {
			case options[1]:
				choice = "later"
			case options[2]:
				choice = "never"
			}
		default:
			choice = "later"
		}

		switch choice {
		case "share":
			if err := s.pr.Share(c); err != nil {
				a.warn("%s %s: %v", c.Kind, c.Name, err)
				continue
			}
			shared++
			a.success("Shared %s %s with %s.", c.Kind, c.Name, c.Direction.Target())
		case "never":
			s.env.Registry.Set(c.Kind, c.Name, toolOnly(c.Direction))
			if err := s.save(); err != nil {
				return err
			}
			a.info("  %s %s stays with %s.", c.Kind, c.Name, c.Direction.Tool())
		}
	}
	if shared > 0 {
		a.printf("\nRun %scodeagent-sync sync%s to send the change to your other machines (undo takes it back here).\n", colorBold, colorReset)
	} else if !yes && !a.interactive() {
		a.info("Share with: codeagent-sync share <name>")
	}
	return nil
}

// toolName names a tool given as claude or codex.
func toolName(tool string) string {
	if tool == "codex" {
		return "Codex"
	}
	return "Claude Code"
}

func firstText(c compat.Candidate) string {
	if len(c.Reasons) == 0 {
		return c.Suggest
	}
	r := c.Reasons[0]
	if r.File != "" {
		return fmt.Sprintf("%s (%s:%d)", r.Text, r.File, r.Line)
	}
	return r.Text
}

func (a *app) describe(c compat.Candidate) {
	a.printf("%s %s%s %s%s: %s → %s\n", verdictSymbol[c.Verdict], colorBold, c.Kind, c.Name, colorReset, c.Direction.Tool(), c.Direction.Target())
	for _, r := range c.Reasons {
		where := ""
		if r.File != "" {
			where = fmt.Sprintf(" (%s:%d)", r.File, r.Line)
		}
		a.printf("    %s%s\n", r.Text, where)
	}
	if c.Installer != "" {
		a.printf("    %sinstalled by %s%s\n", colorDim, c.Installer, colorReset)
	}
	a.printf("    %ssharing: %s%s\n", colorDim, c.Suggest, colorReset)
}

func (a *app) shareCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "share <name>",
		Short: "Share a skill, MCP server or plugin marketplace with the other tool",
		Long:  "Share a skill, MCP server or plugin marketplace with the other tool. Use skill:NAME, mcp:NAME or plugin:NAME when a name is ambiguous.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.sharing()
			if err != nil {
				return err
			}
			kind, name := splitKind(args[0])
			cands, err := compat.Scan(s.env)
			if err != nil {
				return err
			}
			var found []compat.Candidate
			for _, c := range cands {
				if c.Name == name && (kind == "" || c.Kind == kind) {
					found = append(found, c)
				}
			}
			switch len(found) {
			case 0:
				return fmt.Errorf("%s is not something only one tool has (already shared, or decided: see codeagent-sync mark)", args[0])
			case 1:
			default:
				return fmt.Errorf("%s is ambiguous; use skill:%s, mcp:%s or plugin:%s", name, name, name, name)
			}
			c := found[0]
			a.describe(c)
			ok, err := a.confirm("Share it with "+c.Direction.Target()+"?", true, yes)
			if err != nil || !ok {
				return err
			}
			if err := s.pr.Share(c); err != nil {
				return err
			}
			a.success("Shared %s %s with %s. Run codeagent-sync sync to send it to your other machines.", c.Kind, c.Name, c.Direction.Target())
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func (a *app) unshareCmd() *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "unshare <name> --to claude|codex",
		Short: "Take something out of the shared layout, keeping it with one tool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to != "claude" && to != "codex" {
				return errors.New("--to must be claude or codex")
			}
			s, err := a.sharing()
			if err != nil {
				return err
			}
			kind, name := splitKind(args[0])
			if kind == "" {
				if kind, err = s.inferKind(name); err != nil {
					return err
				}
			}
			if err := s.pr.Unshare(kind, name, to); err != nil {
				return err
			}
			a.success("%s %s now stays with %s. Run codeagent-sync sync to send the change to your other machines.", kind, name, toolName(to))
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "claude or codex: the tool that keeps it")
	cmd.MarkFlagRequired("to")
	return cmd
}

func (a *app) markCmd() *cobra.Command {
	var sharedFlag, claudeOnly, codexOnly, variant, forget bool
	cmd := &cobra.Command{
		Use:   "mark <name>",
		Short: "Record a decision without changing any file",
		Long: `Record a decision about a skill, MCP server or plugin marketplace without
changing any file, so scan stops asking about it. Decisions sync to your
other machines. Use skill:NAME, mcp:NAME or plugin:NAME when a name is ambiguous.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			decision := ""
			count := 0
			for flag, d := range map[*bool]string{&sharedFlag: registry.Shared, &claudeOnly: registry.ClaudeOnly, &codexOnly: registry.CodexOnly, &variant: registry.Variant, &forget: ""} {
				if *flag {
					decision = d
					count++
				}
			}
			if count != 1 {
				return errors.New("give exactly one of --shared, --claude-only, --codex-only, --variant, --forget")
			}
			s, err := a.sharing()
			if err != nil {
				return err
			}
			kind, name := splitKind(args[0])
			if kind == "" {
				if kind, err = s.inferKind(name); err != nil {
					return err
				}
			}
			s.env.Registry.Set(kind, name, decision)
			if err := s.save(); err != nil {
				return err
			}
			if decision == "" {
				a.success("Forgot the decision about %s %s; scan will ask again.", kind, name)
			} else {
				a.success("Recorded: %s %s is %s.", kind, name, decision)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&sharedFlag, "shared", false, "it is shared")
	f.BoolVar(&claudeOnly, "claude-only", false, "it stays with Claude Code")
	f.BoolVar(&codexOnly, "codex-only", false, "it stays with Codex")
	f.BoolVar(&variant, "variant", false, "each tool has its own version, on purpose")
	f.BoolVar(&forget, "forget", false, "forget the decision")
	return cmd
}

// splitKind reads "skill:foo", "mcp/foo" or plain "foo".
func splitKind(arg string) (kind, name string) {
	for _, sep := range []string{":", "/"} {
		if k, n, ok := strings.Cut(arg, sep); ok && (k == registry.Skill || k == registry.MCP || k == registry.Plugin) {
			return k, n
		}
	}
	return "", arg
}

// inferKind finds what a bare name refers to.
func (s *sharing) inferKind(name string) (string, error) {
	var kinds []string
	for _, dir := range []string{filepath.Join(s.dirs.Claude, "skills", name), filepath.Join(s.dirs.Agents, "skills", name)} {
		if _, err := os.Lstat(dir); err == nil {
			kinds = append(kinds, registry.Skill)
			break
		}
	}
	if _, err1 := compat.Server(s.dirs, name, compat.ToCodex); err1 == nil {
		kinds = append(kinds, registry.MCP)
	} else if _, err2 := compat.Server(s.dirs, name, compat.ToClaude); err2 == nil {
		kinds = append(kinds, registry.MCP)
	}
	if compat.HasMarketplace(s.dirs, name) {
		kinds = append(kinds, registry.Plugin)
	}
	switch len(kinds) {
	case 0:
		return "", fmt.Errorf("no skill, MCP server or plugin marketplace is called %s", name)
	case 1:
		return kinds[0], nil
	}
	return "", fmt.Errorf("%s is ambiguous; use skill:%s, mcp:%s or plugin:%s", name, name, name, name)
}
