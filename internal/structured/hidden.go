package structured

import (
	"path"
	"strings"
)

// MatchItem reports whether an item name matches one of the patterns: as a
// whole (path.Match; * spans dots), or as an item nested below a match
// ("mcp_servers.x" hides "mcp_servers.x.env").
func MatchItem(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok || strings.HasPrefix(name, p+".") {
			return true
		}
	}
	return false
}

// Hiding returns f writing files so that items matching the patterns keep
// the file's own version (or stay absent). The engine, for its part, never
// sends them: see Pin.
func Hiding(f Format, patterns []string) Format {
	if len(patterns) == 0 {
		return f
	}
	return hiding{f, patterns}
}

type hiding struct {
	Format
	patterns []string
}

func (h hiding) Apply(file []byte, synced []Item, keep map[string]bool) ([]byte, error) {
	all := map[string]bool{}
	for k, v := range keep {
		all[k] = v
	}
	local, _ := h.Project(file)
	for _, it := range append(local, synced...) {
		if MatchItem(h.patterns, it.Name) {
			all[it.Name] = true
		}
	}
	return h.Format.Apply(file, synced, all)
}

// Pin replaces, in this machine's items, those matching the patterns with
// their versions in base (the last synced form), and drops the ones base
// lacks: what this machine keeps to itself then never shows as a change,
// and other machines keep their versions.
func Pin(local, base []Item, patterns []string) []Item {
	if len(patterns) == 0 {
		return local
	}
	var out []Item
	for _, it := range local {
		if !MatchItem(patterns, it.Name) {
			out = append(out, it)
		}
	}
	for _, it := range base {
		if MatchItem(patterns, it.Name) {
			out = append(out, it)
		}
	}
	return out
}
