// Package structured syncs files that several machines, and the tools
// themselves, edit at once — Claude Code's settings.json and .claude.json,
// Codex's config.toml — item by item instead of as whole files.
//
// A file splits into items that merge independently (a top-level setting,
// one MCP server, one TOML table). Items compare by meaning, not by
// formatting, and writing a file back changes only the items that changed:
// everything else, comments included, stays byte for byte. Parts that belong
// to one machine (trusted projects, approved hooks, the rest of
// .claude.json) are never part of the synced form.
package structured

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Item is one independently merged part of a structured file.
type Item struct {
	// Name identifies the item on every machine.
	Name string
	// Value is the item's canonical JSON, for comparisons.
	Value string
	// Text is the item as written in the synced form.
	Text []byte
}

// Format knows one kind of structured file.
type Format interface {
	// Project returns the synced items of a file on disk, in file order.
	Project(file []byte) ([]Item, error)
	// Render returns the synced form of items: what is uploaded.
	Render(items []Item) []byte
	// Parse returns the items of a synced form.
	Parse(synced []byte) ([]Item, error)
	// Apply writes the synced items into a file on disk, keeping everything
	// that is not synced. For the names in keep, the file's own version stays
	// (or stays absent).
	Apply(file []byte, synced []Item, keep map[string]bool) ([]byte, error)
	// Hold returns why an item cannot be used on this machine (a command at
	// an absolute path that does not exist here), or "".
	Hold(it Item, exists func(path string) bool) string
}

// Format names used by the roots.
const (
	JSONKeys    = "json-keys"    // every top-level key of a JSON object (settings.json)
	ClaudeMCP   = "claude-mcp"   // the mcpServers of .claude.json
	CodexConfig = "codex-config" // Codex's config.toml
	Decisions   = "decisions"    // codeagent-sync's registry.yaml
)

// Get returns the format with the given name.
func Get(name string) (Format, bool) {
	switch name {
	case JSONKeys:
		return jsonKeys{}, true
	case ClaudeMCP:
		return claudeMCP{}, true
	case CodexConfig:
		return codexConfig{}, true
	case Decisions:
		return decisions{}, true
	}
	return nil, false
}

// Fingerprint identifies what items say, whatever their order and
// formatting.
func Fingerprint(items []Item) string {
	lines := make([]string, len(items))
	for i, it := range items {
		lines[i] = it.Name + "\x00" + it.Value
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Merge3 merges this machine's and the remote items against the items both
// had at the last sync (base). An item changed on one side only takes that
// side; changed identically on both, it is kept; changed differently, it is
// a conflict — resolved to the remote version when preferRemote is set,
// otherwise reported with this machine's version kept. The merged items
// follow this machine's order, with remote additions at the end.
func Merge3(base, local, remote []Item, preferRemote bool) (merged []Item, conflicts []string) {
	b, l, r := index(base), index(local), index(remote)
	take := func(it *Item) {
		if it != nil {
			merged = append(merged, *it)
		}
	}
	resolve := func(name string) *Item {
		bi, li, ri := b[name], l[name], r[name]
		switch {
		case same(li, ri):
			return li
		case same(li, bi):
			return ri
		case same(ri, bi):
			return li
		case preferRemote:
			return ri
		}
		conflicts = append(conflicts, name)
		return li
	}
	for _, it := range local {
		take(resolve(it.Name))
	}
	for _, it := range remote {
		if l[it.Name] == nil {
			take(resolve(it.Name))
		}
	}
	return merged, conflicts
}

func index(items []Item) map[string]*Item {
	m := make(map[string]*Item, len(items))
	for i := range items {
		m[items[i].Name] = &items[i]
	}
	return m
}

func same(a, b *Item) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Value == b.Value
}

// HeldNames returns the items that cannot be used here, with the reason,
// including the items nested below a held one (a held TOML table's
// subtables).
func HeldNames(f Format, items []Item, exists func(string) bool) map[string]string {
	held := map[string]string{}
	for _, it := range items {
		if reason := f.Hold(it, exists); reason != "" {
			held[it.Name] = reason
		}
	}
	for _, it := range items {
		for name, reason := range held {
			if strings.HasPrefix(it.Name, name+".") {
				held[it.Name] = reason
			}
		}
	}
	return held
}
