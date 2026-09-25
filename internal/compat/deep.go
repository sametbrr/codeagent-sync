package compat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sametbrr/codeagent-sync/internal/registry"
)

// opinionTimeout bounds how long a second opinion may take.
const opinionTimeout = 3 * time.Minute

// SecondOpinion asks an agent CLI whether a skill would work in the other
// tool: that tool's own CLI when it is installed, otherwise the other one.
// The CLI runs read-only in an empty temporary directory and gets the skill's
// text in the prompt, so it has nothing to act on.
func SecondOpinion(ctx context.Context, c Candidate) (string, error) {
	if c.Kind != registry.Skill {
		return "", errors.New("a second opinion is only given for skills")
	}
	text, err := os.ReadFile(filepath.Join(c.Path, "SKILL.md"))
	if err != nil {
		return "", err
	}
	if n := 12000; len(text) > n {
		for !utf8.RuneStart(text[n]) {
			n--
		}
		text = append(text[:n], "\n[…cut]"...)
	}
	var findings []string
	for _, r := range c.Reasons {
		findings = append(findings, r.Text)
	}
	if len(findings) == 0 {
		findings = []string{"none"}
	}
	prompt := fmt.Sprintf("An agent skill written for %s may be shared with %s. Judge only from the text below; "+
		"do not run tools or commands.\nStatic check findings: %s.\n"+
		"Answer in at most three sentences: will it work in %s as it is, what would need changing, and should it be shared.\n\n--- SKILL.md ---\n%s",
		c.Direction.Tool(), c.Direction.Target(), strings.Join(findings, "; "), c.Direction.Target(), text)

	order := []string{"claude", "codex"}
	if c.Direction == ToCodex {
		order = []string{"codex", "claude"}
	}
	for _, cli := range order {
		if _, err := exec.LookPath(cli); err != nil {
			continue
		}
		return ask(ctx, cli, prompt)
	}
	return "", errors.New("neither claude nor codex is installed")
}

// ask runs one CLI with the prompt on stdin, in an empty directory, with
// nothing it could act with: no tools, no MCP servers, no user settings or
// hooks. The skill text could be written to steer it, so it must not even
// read files (the storage credentials are files too).
func ask(ctx context.Context, cli, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, opinionTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "codeagent-sync-opinion-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	var cmd *exec.Cmd
	if cli == "codex" {
		cmd = exec.CommandContext(ctx, "codex", "exec", "--ignore-user-config", "--disable", "shell_tool",
			"--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral", "--color", "never", "-")
	} else {
		cmd = exec.CommandContext(ctx, "claude", "-p", "--safe-mode", "--tools", "", "--strict-mcp-config", "--no-session-persistence")
	}
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	// Hooks the CLI still runs must not run codeagent-sync's (which would,
	// among other things, hand this run the notice meant for the user).
	cmd.Env = append(os.Environ(), "CODEAGENT_SYNC_NO_HOOKS=1")
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s gave no answer within %s", cli, opinionTimeout)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			lines := strings.Split(strings.TrimSpace(string(exit.Stderr)), "\n")
			return "", fmt.Errorf("%s: %s", cli, lines[len(lines)-1])
		}
		return "", fmt.Errorf("%s: %w", cli, err)
	}
	return strings.TrimSpace(string(out)) + " (" + cli + ")", nil
}
