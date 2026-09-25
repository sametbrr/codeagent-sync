package structured

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// member is one key of a JSON object with its value exactly as written.
type member struct {
	key string
	raw json.RawMessage
}

// parseObject reads a JSON object's members in order. Empty input is an
// empty object (the file does not exist yet).
func parseObject(data []byte) ([]member, error) {
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
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("bad JSON object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, member{key, raw})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errors.New("unterminated JSON object")
	}
	return out, nil
}

// canonical returns the compact JSON of raw with object keys sorted.
func canonical(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	return string(out), err
}

func membersToItems(members []member) ([]Item, error) {
	items := make([]Item, 0, len(members))
	for _, m := range members {
		value, err := canonical(m.raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.key, err)
		}
		items = append(items, Item{Name: m.key, Value: value, Text: m.raw})
	}
	return items, nil
}

// renderObject writes items as a JSON object with sorted keys, indented with
// two spaces at the given depth.
func renderObject(items []Item, prefix string) []byte {
	sorted := append([]Item(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var b bytes.Buffer
	b.WriteString("{")
	for i, it := range sorted {
		if i > 0 {
			b.WriteString(",")
		}
		key, _ := json.Marshal(it.Name)
		b.WriteString("\n" + prefix + "  ")
		b.Write(key)
		b.WriteString(": ")
		b.Write(indentValue([]byte(it.Value), prefix+"  "))
	}
	if len(sorted) > 0 {
		b.WriteString("\n" + prefix)
	}
	b.WriteString("}")
	return b.Bytes()
}

// indentValue formats a JSON value to sit at the given indentation.
func indentValue(value []byte, prefix string) []byte {
	var b bytes.Buffer
	if err := json.Indent(&b, value, prefix, "  "); err != nil {
		return value
	}
	return b.Bytes()
}

// writeMembers writes a JSON object keeping each member's raw bytes.
func writeMembers(members []member) []byte {
	var b bytes.Buffer
	b.WriteString("{")
	for i, m := range members {
		if i > 0 {
			b.WriteString(",")
		}
		key, _ := json.Marshal(m.key)
		b.WriteString("\n  ")
		b.Write(key)
		b.WriteString(": ")
		b.Write(m.raw)
	}
	if len(members) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// mergeMembers replaces, drops and adds members of an object from synced
// items, keeping the file's order and, for names in keep, its own version.
func mergeMembers(local []member, synced []Item, keep map[string]bool, prefix string) []member {
	byName := index(synced)
	seen := map[string]bool{}
	var out []member
	for _, m := range local {
		seen[m.key] = true
		switch it := byName[m.key]; {
		case keep[m.key]:
			out = append(out, m)
		case it == nil:
			// deleted on another machine
		case sameJSON(m.raw, it.Value):
			out = append(out, m) // unchanged: keep this file's formatting
		default:
			out = append(out, member{m.key, indentValue([]byte(it.Value), prefix)})
		}
	}
	for _, it := range synced {
		if !seen[it.Name] && !keep[it.Name] {
			out = append(out, member{it.Name, indentValue([]byte(it.Value), prefix)})
		}
	}
	return out
}

func sameJSON(raw []byte, value string) bool {
	own, err := canonical(raw)
	return err == nil && own == value
}

// jsonKeys syncs every top-level key of a JSON object: settings.json is
// shared as a whole, merged key by key.
type jsonKeys struct{}

func (jsonKeys) Project(file []byte) ([]Item, error) {
	members, err := parseObject(file)
	if err != nil {
		return nil, err
	}
	return membersToItems(members)
}

func (jsonKeys) Render(items []Item) []byte { return append(renderObject(items, ""), '\n') }

func (f jsonKeys) Parse(synced []byte) ([]Item, error) { return f.Project(synced) }

func (jsonKeys) Apply(file []byte, synced []Item, keep map[string]bool) ([]byte, error) {
	local, err := parseObject(file)
	if err != nil {
		return nil, err
	}
	return sameEnding(file, writeMembers(mergeMembers(local, synced, keep, "  "))), nil
}

func (jsonKeys) Hold(Item, func(string) bool) string { return "" }

// claudeMCP syncs only the mcpServers of .claude.json, server by server; the
// rest of that file is Claude Code's own state for this machine.
type claudeMCP struct{}

const mcpServersKey = "mcpServers"

func (claudeMCP) Project(file []byte) ([]Item, error) {
	members, err := parseObject(file)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.key == mcpServersKey {
			servers, err := parseObject(m.raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", mcpServersKey, err)
			}
			return membersToItems(servers)
		}
	}
	return nil, nil
}

func (claudeMCP) Render(items []Item) []byte {
	key, _ := json.Marshal(mcpServersKey)
	return []byte("{\n  " + string(key) + ": " + string(renderObject(items, "  ")) + "\n}\n")
}

func (f claudeMCP) Parse(synced []byte) ([]Item, error) { return f.Project(synced) }

func (claudeMCP) Apply(file []byte, synced []Item, keep map[string]bool) ([]byte, error) {
	members, err := parseObject(file)
	if err != nil {
		return nil, err
	}
	var servers []member
	at := -1
	for i, m := range members {
		if m.key == mcpServersKey {
			at = i
			if servers, err = parseObject(m.raw); err != nil {
				return nil, fmt.Errorf("%s: %w", mcpServersKey, err)
			}
		}
	}
	merged := mergeMembers(servers, synced, keep, "    ")
	var b bytes.Buffer
	b.WriteString("{")
	for i, m := range merged {
		if i > 0 {
			b.WriteString(",")
		}
		key, _ := json.Marshal(m.key)
		b.WriteString("\n    ")
		b.Write(key)
		b.WriteString(": ")
		b.Write(m.raw)
	}
	if len(merged) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("}")
	value := member{mcpServersKey, b.Bytes()}
	if at >= 0 {
		members[at] = value
	} else {
		members = append(members, value)
	}
	return sameEnding(file, writeMembers(members)), nil
}

// sameEnding keeps the file's habit of ending with a newline or not
// (Claude Code writes .claude.json without one).
func sameEnding(file, out []byte) []byte {
	if len(bytes.TrimSpace(file)) > 0 && !bytes.HasSuffix(file, []byte("\n")) {
		return bytes.TrimSuffix(out, []byte("\n"))
	}
	return out
}

// Hold holds a server whose command is an absolute path missing here (a
// Homebrew binary on a Linux machine, an app bundle on Windows).
func (claudeMCP) Hold(it Item, exists func(string) bool) string {
	var server struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(it.Value), &server) != nil {
		return ""
	}
	return missingCommand(server.Command, exists)
}

func missingCommand(command string, exists func(string) bool) string {
	if !isAbsPath(command) || exists(command) {
		return ""
	}
	return "its command " + command + " is not on this machine"
}

// isAbsPath accepts POSIX and Windows absolute paths whatever this
// machine's platform is: the command came from another machine.
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') || filepath.IsAbs(p)
}
