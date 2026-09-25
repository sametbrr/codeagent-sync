// Package hooks puts codeagent-sync's own hooks into Claude Code's
// settings.json and Codex's hooks.json, and takes them out again. Every other
// hook, and everything else in the files, stays as it is.
//
// The hooks sync in the background when a session starts and after every
// answer, and hand what the user should hear about (a conflict, something to
// share) to the agent with the next prompt.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Tool names the file format.
type Tool string

const (
	Claude Tool = "claude" // settings.json: the program and its arguments apart
	Codex  Tool = "codex"  // hooks.json: a shell command, and one for Windows
)

// Hook is one of codeagent-sync's hooks.
type Hook struct {
	Event   string
	Matcher string
	Args    []string // after the program
	Async   bool
	Timeout int // seconds
}

// Ours are the hooks auto mode adds, in both tools.
var Ours = []Hook{
	{Event: "SessionStart", Matcher: "startup|resume", Args: []string{"hook", "sync"}, Async: true, Timeout: 120},
	{Event: "Stop", Args: []string{"hook", "sync"}, Async: true, Timeout: 120},
	{Event: "UserPromptSubmit", Args: []string{"hook", "notify"}, Timeout: 10},
}

// Program is how the hooks start codeagent-sync.
type Program struct {
	// Path is the absolute path of the program, without ".exe". Claude Code
	// starts it directly; codeagent-sync writes the home directory in it
	// portably, so other machines get their own.
	Path string
	// HomeRel is Path relative to the home directory, with slashes, or ""
	// when the program lies elsewhere.
	HomeRel string
}

// unsafeInPath are characters a shell would read in a quoted path.
const unsafeInPath = "\"'`$%!\n\r"

// NewProgram describes the program at exe for a machine with home. The
// hooks name it inside shell commands, so its path must hold no quote, $,
// %, ! or backquote.
func NewProgram(exe, home string) (Program, error) {
	if strings.ContainsAny(exe, unsafeInPath) {
		return Program{}, fmt.Errorf("the hooks cannot start %s: its path holds a quote, $, %%, ! or backquote", exe)
	}
	exe = strings.TrimSuffix(exe, ".exe")
	p := Program{Path: exe}
	if rel, err := filepath.Rel(home, exe); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		p.HomeRel = filepath.ToSlash(rel)
	}
	return p, nil
}

// posix is the command a POSIX shell runs.
func (p Program) posix(args []string) string {
	prog := "'" + strings.ReplaceAll(p.Path, "'", `'\''`) + "'"
	if p.HomeRel != "" {
		prog = `"$HOME/` + p.HomeRel + `"`
	}
	return prog + " " + strings.Join(args, " ")
}

// windows is the command for Windows. Codex runs it in the session's shell,
// PowerShell or cmd.exe; `cmd /d /c "…" args` reads the same in both, and a
// path under the home directory is written out, so that codeagent-sync
// translates it for each machine as it does every other path.
func (p Program) windows(args []string) string {
	prog := "codeagent-sync.exe"
	if p.HomeRel != "" {
		prog = `"` + p.Path + `.exe"`
	}
	return "cmd /d /c " + prog + " " + strings.Join(args, " ")
}

// handler is a hook handler, with its fields in the order the tools write them.
type handler struct {
	Type           string   `json:"type"`
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	CommandWindows string   `json:"commandWindows,omitempty"`
	Async          bool     `json:"async,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
}

type group struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []handler `json:"hooks"`
}

func (h Hook) group(tool Tool, p Program) group {
	hd := handler{Type: "command", Async: h.Async, Timeout: h.Timeout}
	if tool == Claude {
		hd.Command, hd.Args = p.Path, h.Args
	} else {
		hd.Command, hd.CommandWindows = p.posix(h.Args), p.windows(h.Args)
	}
	return group{Matcher: h.Matcher, Hooks: []handler{hd}}
}

// ourCommand matches the shell command of one of codeagent-sync's hooks.
var ourCommand = regexp.MustCompile(`(^|[/\\"'])codeagent-sync(\.exe)?["']? hook [a-z]+$`)

// IsOurs reports whether a hook handler runs codeagent-sync's hook command.
func IsOurs(h map[string]any) bool {
	command, _ := h["command"].(string)
	if args, ok := h["args"].([]any); ok {
		base := path.Base(strings.ReplaceAll(command, `\`, "/"))
		return len(args) > 0 && args[0] == "hook" && strings.TrimSuffix(base, ".exe") == "codeagent-sync"
	}
	return ourCommand.MatchString(command)
}

// Install returns file with codeagent-sync's hooks, replacing any it had.
func Install(file []byte, tool Tool, p Program) ([]byte, error) {
	return edit(file, func(events []member) ([]member, error) {
		events, err := without(events)
		if err != nil {
			return nil, err
		}
		for _, h := range Ours {
			g, err := encode(h.group(tool, p))
			if err != nil {
				return nil, err
			}
			i := find(events, h.Event)
			if i < 0 {
				events = append(events, member{h.Event, json.RawMessage("[]")})
				i = len(events) - 1
			}
			var groups []json.RawMessage
			if err := json.Unmarshal(events[i].raw, &groups); err != nil {
				return nil, fmt.Errorf("hooks.%s: %w", h.Event, err)
			}
			events[i].raw = array(append(groups, g))
		}
		return events, nil
	})
}

// Remove returns file without codeagent-sync's hooks.
func Remove(file []byte) ([]byte, error) { return edit(file, without) }

// Installed returns the events codeagent-sync has a hook for in file.
func Installed(file []byte) ([]string, error) {
	events, err := hookEvents(file)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ev := range events {
		groups, err := decodeGroups(ev)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if g.ours > 0 {
				out = append(out, ev.key)
				break
			}
		}
	}
	return out, nil
}

// member is a key of a JSON object with its value as written.
type member struct {
	key string
	raw json.RawMessage
}

func find(ms []member, key string) int {
	for i, m := range ms {
		if m.key == key {
			return i
		}
	}
	return -1
}

func members(data []byte) ([]member, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, member{tok.(string), raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

// object writes members as a JSON object, their values as they are.
func object(ms []member) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := encode(m.key)
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.raw)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// array writes values as a JSON array, as they are.
func array(raws []json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, r := range raws {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(r)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// encode writes v as JSON without escaping <, > and &, as the tools do.
func encode(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func hookEvents(file []byte) ([]member, error) {
	top, err := members(file)
	if err != nil {
		return nil, err
	}
	if i := find(top, "hooks"); i >= 0 {
		return members(top[i].raw)
	}
	return nil, nil
}

// edit rewrites the "hooks" object of a JSON file, formatted with two
// spaces as both tools write it.
func edit(file []byte, change func([]member) ([]member, error)) ([]byte, error) {
	top, err := members(file)
	if err != nil {
		return nil, err
	}
	i := find(top, "hooks")
	var events []member
	if i >= 0 {
		if events, err = members(top[i].raw); err != nil {
			return nil, fmt.Errorf("hooks: %w", err)
		}
	}
	if events, err = change(events); err != nil {
		return nil, err
	}
	switch {
	case len(events) == 0 && i >= 0:
		top = append(top[:i], top[i+1:]...)
	case len(events) > 0 && i >= 0:
		top[i].raw = object(events)
	case len(events) > 0:
		top = append(top, member{"hooks", object(events)})
	}
	var compact, out bytes.Buffer
	if err := json.Compact(&compact, object(top)); err != nil {
		return nil, err
	}
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// decodedGroup is a matcher group with its handlers as written.
type decodedGroup struct {
	raw      json.RawMessage
	fields   []member
	handlers []json.RawMessage
	ours     int
}

func decodeGroups(ev member) ([]decodedGroup, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(ev.raw, &raws); err != nil {
		return nil, fmt.Errorf("hooks.%s: %w", ev.key, err)
	}
	out := make([]decodedGroup, 0, len(raws))
	for _, raw := range raws {
		g := decodedGroup{raw: raw}
		fields, err := members(raw)
		if err != nil {
			return nil, fmt.Errorf("hooks.%s: %w", ev.key, err)
		}
		g.fields = fields
		if i := find(fields, "hooks"); i >= 0 {
			if err := json.Unmarshal(fields[i].raw, &g.handlers); err != nil {
				return nil, fmt.Errorf("hooks.%s: %w", ev.key, err)
			}
		}
		for _, h := range g.handlers {
			var m map[string]any
			if json.Unmarshal(h, &m) == nil && IsOurs(m) {
				g.ours++
			}
		}
		out = append(out, g)
	}
	return out, nil
}

// without drops codeagent-sync's handlers, then the groups and events left
// empty by that.
func without(events []member) ([]member, error) {
	var out []member
	for _, ev := range events {
		groups, err := decodeGroups(ev)
		if err != nil {
			return nil, err
		}
		changed := false
		var kept []json.RawMessage
		for _, g := range groups {
			if g.ours == 0 {
				kept = append(kept, g.raw)
				continue
			}
			changed = true
			if g.ours == len(g.handlers) {
				continue
			}
			var rest []json.RawMessage
			for _, h := range g.handlers {
				var m map[string]any
				if json.Unmarshal(h, &m) != nil || !IsOurs(m) {
					rest = append(rest, h)
				}
			}
			g.fields[find(g.fields, "hooks")].raw = array(rest)
			kept = append(kept, object(g.fields))
		}
		switch {
		case !changed:
			out = append(out, ev)
		case len(kept) > 0:
			out = append(out, member{ev.key, array(kept)})
		}
	}
	return out, nil
}

// programPart is the program at the start of a hook's shell command.
var programPart = regexp.MustCompile(`^(?:"([^"]+)"|'([^']+)'|(\S+)) hook `)

// Programs returns the programs codeagent-sync's hooks in file start, with
// $HOME and %USERPROFILE% written out as home.
func Programs(file []byte, home string) ([]string, error) {
	events, err := hookEvents(file)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		for _, v := range []string{"$HOME", "%USERPROFILE%"} {
			if strings.HasPrefix(p, v) {
				p = filepath.Join(home, filepath.FromSlash(strings.ReplaceAll(p[len(v):], `\`, "/")))
			}
		}
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, ev := range events {
		groups, err := decodeGroups(ev)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			for _, raw := range g.handlers {
				var h map[string]any
				if json.Unmarshal(raw, &h) != nil || !IsOurs(h) {
					continue
				}
				command, _ := h["command"].(string)
				if _, ok := h["args"]; ok {
					add(command)
				} else if m := programPart.FindStringSubmatch(command); m != nil {
					add(m[1] + m[2] + m[3])
				}
			}
		}
	}
	return out, nil
}
