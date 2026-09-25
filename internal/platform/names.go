package platform

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ValidateRelPath checks that rel, a slash-separated path relative to a sync
// root, is safe to create on target: it must stay inside the root and every
// name in it must be legal on that platform.
func ValidateRelPath(rel string, target OS) error {
	if rel == "" {
		return errors.New("empty path")
	}
	if strings.ContainsRune(rel, 0) {
		return fmt.Errorf("%q contains a NUL byte", rel)
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("%q is absolute", rel)
	}
	for _, name := range strings.Split(rel, "/") {
		switch name {
		case "", ".", "..":
			return fmt.Errorf("%q has an invalid segment %q", rel, name)
		}
		if target == Windows {
			if err := validateWindowsName(name); err != nil {
				return fmt.Errorf("%q: %w", rel, err)
			}
		}
	}
	return nil
}

var windowsReservedNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

func validateWindowsName(name string) error {
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return fmt.Errorf("name %q contains %q, which Windows does not allow", name, r)
		}
	}
	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("name %q ends with a space or a dot, which Windows does not allow", name)
	}
	// Device names are reserved with any extension: "nul.txt" is NUL too.
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	if windowsReservedNames[base] ||
		(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
			base[3] >= '1' && base[3] <= '9') {
		return fmt.Errorf("name %q is a reserved device name on Windows", name)
	}
	return nil
}

// CaseCollisions returns the groups of paths (files or the directories that
// contain them) that differ only in letter case. They cannot coexist on a
// case-insensitive file system, so syncing them to macOS or Windows would make
// one silently replace the other.
func CaseCollisions(paths []string) [][]string {
	spellings := make(map[string]map[string]bool)
	add := func(p string) {
		key := strings.ToLower(p)
		if spellings[key] == nil {
			spellings[key] = make(map[string]bool)
		}
		spellings[key][p] = true
	}
	for _, p := range paths {
		for q := p; q != "." && q != "/" && q != ""; q = path.Dir(q) {
			add(q)
		}
	}

	var groups [][]string
	for _, set := range spellings {
		if len(set) < 2 {
			continue
		}
		group := make([]string, 0, len(set))
		for p := range set {
			group = append(group, p)
		}
		sort.Strings(group)
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}
