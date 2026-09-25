package structured

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// codexConfig syncs Codex's config.toml by top-level key and by table.
// Parts that belong to one machine are left out: trusted projects, approved
// hooks, approved MCP tools, the Desktop app's state, and the update
// bookkeeping of plugin marketplaces.
type codexConfig struct{}

// tomlItem is a top-level key, a [table] or a run of [[array]] tables, with
// the comments above it.
type tomlItem struct {
	path   []string
	name   string
	table  bool
	text   []byte
	fields []tomlField // the key lines of a table
}

// tomlField is one key line of a table: its byte range inside the item.
type tomlField struct {
	key        string
	start, end int
}

// excludedPath reports whether a key or table belongs to this machine only.
func excludedPath(path []string) bool {
	switch {
	case len(path) == 0:
		return false
	case path[0] == "projects", path[0] == "desktop":
		return true
	case path[0] == "hooks" && len(path) > 1 && path[1] == "state":
		return true
	case path[0] == "mcp_servers" && len(path) > 2 && path[2] == "tools":
		return true
	}
	return false
}

// excludedField reports whether a key inside a table belongs to this machine.
func excludedField(path []string, key string) bool {
	return len(path) == 2 && path[0] == "marketplaces" && (key == "last_updated" || key == "last_revision")
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlName(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		if bareKey.MatchString(p) {
			parts[i] = p
		} else {
			q, _ := json.Marshal(p)
			parts[i] = string(q)
		}
	}
	return strings.Join(parts, ".")
}

// splitTOML splits a TOML document into items. Comments above a key or
// table belong to it; comments at the top of the file belong to the first
// item.
func splitTOML(data []byte) ([]tomlItem, error) {
	p := unstable.Parser{KeepComments: true}
	p.Reset(data)

	type span struct {
		item          int // index into items
		start, rawEnd int
		key           string
	}
	var items []tomlItem
	var starts []int
	var fieldSpans []span
	pending := -1
	current := -1 // table the following keys belong to

	for p.NextExpression() {
		e := p.Expression()
		if e.Kind == unstable.Comment {
			if pending < 0 {
				pending = lineStart(data, int(e.Raw.Offset))
			}
			continue
		}
		var path []string
		first := -1
		for it := e.Key(); it.Next(); {
			n := it.Node()
			if first < 0 {
				first = int(n.Raw.Offset)
			}
			path = append(path, string(n.Data))
		}

		switch e.Kind {
		case unstable.KeyValue:
			start := lineStart(data, int(e.Raw.Offset))
			if current >= 0 {
				fieldSpans = append(fieldSpans, span{current, start, int(e.Raw.Offset + e.Raw.Length), path[0]})
				pending = -1
				continue
			}
			if pending >= 0 {
				start, pending = pending, -1
			}
			items = append(items, tomlItem{path: path, name: tomlName(path)})
			starts = append(starts, start)
		case unstable.Table, unstable.ArrayTable:
			name := tomlName(path)
			if e.Kind == unstable.ArrayTable {
				name += "[]"
				// Consecutive [[x]] blocks form one item.
				if n := len(items); n > 0 && items[n-1].name == name && current == n-1 {
					pending = -1
					continue
				}
			}
			start := lineStart(data, headerStart(data, first))
			if pending >= 0 {
				start, pending = pending, -1
			}
			items = append(items, tomlItem{path: path, name: name, table: true})
			starts = append(starts, start)
			current = len(items) - 1
		}
	}
	if err := p.Error(); err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	for i := range items {
		if seen[items[i].name] {
			return nil, fmt.Errorf("%s is defined twice", items[i].name)
		}
		seen[items[i].name] = true
		start, end := starts[i], len(data)
		if i == 0 {
			start = 0
		}
		if i+1 < len(items) {
			end = starts[i+1]
		}
		items[i].text = data[start:end]
		for _, f := range fieldSpans {
			if f.item == i {
				items[i].fields = append(items[i].fields, tomlField{key: f.key, start: f.start - start, end: lineEnd(data, f.rawEnd) - start})
			}
		}
	}
	return items, nil
}

// headerStart finds the opening bracket of a table header from its first key.
func headerStart(data []byte, keyOffset int) int {
	i := keyOffset - 1
	for i >= 0 && (data[i] == ' ' || data[i] == '\t' || data[i] == '"' || data[i] == '\'') {
		i--
	}
	for i > 0 && data[i-1] == '[' {
		i--
	}
	if i < 0 {
		return 0
	}
	return i
}

func lineStart(data []byte, offset int) int {
	return bytes.LastIndexByte(data[:offset], '\n') + 1
}

func lineEnd(data []byte, offset int) int {
	if i := bytes.IndexByte(data[offset:], '\n'); i >= 0 {
		return offset + i + 1
	}
	return len(data)
}

// projectedText is an item's text without the fields that belong to this
// machine.
func (it tomlItem) projectedText() []byte {
	var out []byte
	last := 0
	for _, f := range it.fields {
		if excludedField(it.path, f.key) {
			out = append(out, it.text[last:f.start]...)
			last = f.end
		}
	}
	return append(out, it.text[last:]...)
}

func tomlValue(text []byte) (string, error) {
	var v map[string]any
	if err := toml.Unmarshal(text, &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	return string(out), err
}

func (codexConfig) Project(file []byte) ([]Item, error) {
	all, err := splitTOML(file)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, it := range all {
		if excludedPath(it.path) {
			continue
		}
		text := it.projectedText()
		value, err := tomlValue(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", it.name, err)
		}
		items = append(items, Item{Name: it.name, Value: value, Text: withNewline(text)})
	}
	return items, nil
}

func (codexConfig) Render(items []Item) []byte {
	var root, tables []byte
	for _, it := range items {
		if isTableItem(it.Text) {
			tables = append(tables, withNewline(it.Text)...)
		} else {
			root = append(root, withNewline(it.Text)...)
		}
	}
	return append(root, tables...)
}

func (f codexConfig) Parse(synced []byte) ([]Item, error) { return f.Project(synced) }

func (codexConfig) Apply(file []byte, synced []Item, keep map[string]bool) ([]byte, error) {
	local, err := splitTOML(file)
	if err != nil {
		return nil, err
	}
	byName := index(synced)
	seen := map[string]bool{}
	var root, tables []byte
	add := func(table bool, text []byte, fresh bool) {
		if table {
			if fresh && len(tables)+len(root) > 0 && !bytes.HasSuffix(append(root, tables...), []byte("\n\n")) {
				tables = append(tables, '\n')
			}
			tables = append(tables, withNewline(text)...)
		} else {
			root = append(root, withNewline(text)...)
		}
	}
	for _, it := range local {
		seen[it.name] = true
		switch s := byName[it.name]; {
		case excludedPath(it.path), keep[it.name]:
			add(it.table, it.text, false)
		case s == nil:
			// deleted on another machine
		case it.sameValue(s.Value):
			add(it.table, it.text, false) // unchanged: keep this file's formatting
		default:
			add(isTableItem(s.Text), it.withOwnFields(it.keepComments(s.Text)), false)
		}
	}
	for _, s := range synced {
		if !seen[s.Name] && !keep[s.Name] {
			add(isTableItem(s.Text), s.Text, true)
		}
	}

	out := append(root, tables...)
	var check map[string]any
	if err := toml.Unmarshal(out, &check); err != nil {
		return nil, fmt.Errorf("the merged config.toml would be invalid: %w", err)
	}
	return out, nil
}

func (it tomlItem) sameValue(value string) bool {
	own, err := tomlValue(it.projectedText())
	return err == nil && own == value
}

// keepComments keeps the comments above this file's version of an item when
// the new version brings none of its own.
func (it tomlItem) keepComments(synced []byte) []byte {
	if len(leadingComments(synced)) > 0 {
		return synced
	}
	return append(append([]byte{}, leadingComments(it.text)...), synced...)
}

// leadingComments returns the comment and blank lines at the top of text.
func leadingComments(text []byte) []byte {
	off := 0
	for off < len(text) {
		end := lineEnd(text, off)
		line := bytes.TrimSpace(text[off:end])
		if len(line) > 0 && line[0] != '#' {
			break
		}
		off = end
	}
	if bytes.Count(text[:off], []byte("#")) == 0 {
		return nil
	}
	return text[:off]
}

// withOwnFields puts this machine's own fields of a table (the update
// bookkeeping of a marketplace) back into the table's synced text, right
// after its header.
func (it tomlItem) withOwnFields(synced []byte) []byte {
	var own []byte
	for _, f := range it.fields {
		if excludedField(it.path, f.key) {
			own = append(own, withNewline(it.text[f.start:f.end])...)
		}
	}
	if len(own) == 0 {
		return synced
	}
	at := headerLineEnd(synced)
	out := append([]byte{}, synced[:at]...)
	out = append(out, own...)
	return append(out, synced[at:]...)
}

// headerLineEnd returns the offset just after the first table header line.
func headerLineEnd(text []byte) int {
	for off := 0; off < len(text); {
		end := lineEnd(text, off)
		if bytes.HasPrefix(bytes.TrimLeft(text[off:end], " \t"), []byte("[")) {
			return end
		}
		off = end
	}
	return 0
}

// isTableItem reports whether an item's text is a table (its first line that
// is not a comment or blank opens with a bracket).
func isTableItem(text []byte) bool {
	for _, line := range bytes.Split(text, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		return line[0] == '['
	}
	return false
}

func withNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return append(append([]byte{}, b...), '\n')
	}
	return b
}

// Hold holds an MCP server whose command is an absolute path missing here;
// HeldNames extends it to the server's subtables.
func (codexConfig) Hold(it Item, exists func(string) bool) string {
	var doc struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcp_servers"`
	}
	if json.Unmarshal([]byte(it.Value), &doc) != nil || len(doc.MCPServers) != 1 {
		return ""
	}
	for _, s := range doc.MCPServers {
		return missingCommand(s.Command, exists)
	}
	return ""
}
