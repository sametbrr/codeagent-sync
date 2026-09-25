package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Trust statuses Codex reports for a hook.
const (
	Trusted   = "trusted"
	Untrusted = "untrusted"
	Modified  = "modified" // trusted once, changed since
)

// codexEvents maps the event names of Codex's app-server to the names in
// hooks.json.
var codexEvents = map[string]string{
	"sessionStart":     "SessionStart",
	"stop":             "Stop",
	"userPromptSubmit": "UserPromptSubmit",
}

// CodexTrust asks Codex how it treats codeagent-sync's hooks on this
// machine: event -> trust status. Codex runs a hook only once the user has
// trusted it in /hooks, and again after every change to it.
func CodexTrust(ctx context.Context, home string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server")
	cmd.Dir = home
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	enc := json.NewEncoder(stdin)
	for _, msg := range []map[string]any{
		{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "codeagent-sync", "version": "1"}}},
		{"method": "initialized"},
		{"id": 2, "method": "hooks/list", "params": map[string]any{"cwds": []string{home}}},
	} {
		if err := enc.Encode(msg); err != nil {
			return nil, err
		}
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"` // set on the server's own requests and notifications
			Result *struct {
				Data []struct {
					Hooks []struct {
						EventName   string `json:"eventName"`
						Command     string `json:"command"`
						Source      string `json:"source"`
						TrustStatus string `json:"trustStatus"`
					} `json:"hooks"`
				} `json:"data"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || strings.TrimSpace(string(msg.ID)) != "2" || msg.Method != "" {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("codex hooks/list: %s", msg.Error.Message)
		}
		if msg.Result == nil {
			continue
		}
		out := map[string]string{}
		for _, entry := range msg.Result.Data {
			for _, h := range entry.Hooks {
				if h.Source != "user" || !IsOurs(map[string]any{"command": h.Command}) {
					continue
				}
				event := codexEvents[h.EventName]
				if event == "" {
					event = h.EventName
				}
				if out[event] != Untrusted && out[event] != Modified {
					out[event] = h.TrustStatus
				}
			}
		}
		return out, nil
	}
	if ctx.Err() != nil {
		return nil, errors.New("codex did not answer in time")
	}
	return nil, errors.New("codex app-server ended without answering")
}
