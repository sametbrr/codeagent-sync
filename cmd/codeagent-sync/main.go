// Command codeagent-sync keeps the configuration of coding agents (Claude
// Code, Codex) — instructions, settings, skills, agents, hooks, MCP servers,
// plugin lists — the same on every machine, end-to-end encrypted in a
// storage bucket.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	_ "github.com/sametbrr/codeagent-sync/internal/storage/gcs"
	_ "github.com/sametbrr/codeagent-sync/internal/storage/r2"
	_ "github.com/sametbrr/codeagent-sync/internal/storage/s3"
	_ "github.com/sametbrr/codeagent-sync/internal/storage/webdav"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// exitError ends the program with a code after its message was printed.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// run executes the command line and returns the exit code: 0 on success,
// 1 on an error, 2 when conflicts need attention.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{in: stdin, out: stdout, errOut: stderr}
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()

	var ee exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	default:
		fmt.Fprintf(stderr, "%serror:%s %v\n", colorRed, colorReset, err)
		return 1
	}
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "codeagent-sync",
		Short: "Keep your coding agents' configuration the same on every machine",
		Long: `codeagent-sync keeps the configuration of Claude Code and Codex —
instructions, settings, skills, agents, hooks, MCP servers and plugin lists —
the same on every machine, and helps share what works in both tools.
Everything is encrypted before it leaves the machine.

Start with: codeagent-sync init`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			initColors(a.out)
		},
	}
	root.PersistentFlags().BoolVarP(&a.quiet, "quiet", "q", false, "print nothing unless something needs attention (for hooks)")
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "print the result as JSON")

	root.AddCommand(
		a.initCmd(),
		a.joinCodeCmd(),
		a.syncCmd("sync", "Download changes from your other machines and upload this machine's", modeBoth),
		a.syncCmd("pull", "Only download changes from your other machines", modePull),
		a.syncCmd("push", "Only upload this machine's changes", modePush),
		a.statusCmd(),
		a.pathsCmd(),
		a.machinesCmd(),
		a.conflictsCmd(),
		a.undoCmd(),
		a.scanCmd(),
		a.shareCmd(),
		a.unshareCmd(),
		a.markCmd(),
		a.autoCmd(),
		a.doctorCmd(),
		a.hookCmd(),
		a.updateCmd(),
	)
	return root
}
