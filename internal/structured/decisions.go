package structured

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// decisions is codeagent-sync's registry.yaml. Every decision is an item, so
// machines that record different decisions at the same time merge instead
// of conflicting.
type decisions struct{}

// decisionsFile is the layout of registry.yaml.
type decisionsFile struct {
	Version   int               `yaml:"version"`
	Decisions map[string]string `yaml:"decisions"`
}

const decisionsHeader = "# Decisions about what codeagent-sync shares between Claude Code and Codex.\n" +
	"# Edit with: codeagent-sync mark <name> --shared|--claude-only|--codex-only|--variant|--forget\n"

// RenderDecisions writes registry.yaml, sorted, the same way every time.
func RenderDecisions(d map[string]string) []byte {
	if d == nil {
		d = map[string]string{}
	}
	var b bytes.Buffer
	b.WriteString(decisionsHeader)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	enc.Encode(decisionsFile{Version: 1, Decisions: d}) // a map of strings always encodes
	enc.Close()
	return b.Bytes()
}

// ParseDecisions reads registry.yaml; empty input has no decisions.
func ParseDecisions(file []byte) (map[string]string, error) {
	var f decisionsFile
	if err := yaml.Unmarshal(file, &f); err != nil {
		return nil, fmt.Errorf("registry.yaml: %w", err)
	}
	if f.Decisions == nil {
		f.Decisions = map[string]string{}
	}
	return f.Decisions, nil
}

func (decisions) Project(file []byte) ([]Item, error) {
	d, err := ParseDecisions(file)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(d))
	for name := range d {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]Item, 0, len(d))
	for _, name := range names {
		value, _ := json.Marshal(d[name])
		items = append(items, Item{Name: name, Value: string(value), Text: value})
	}
	return items, nil
}

func (decisions) Render(items []Item) []byte {
	d := make(map[string]string, len(items))
	for _, it := range items {
		var decision string
		if json.Unmarshal([]byte(it.Value), &decision) == nil {
			d[it.Name] = decision
		}
	}
	return RenderDecisions(d)
}

func (f decisions) Parse(synced []byte) ([]Item, error) { return f.Project(synced) }

func (f decisions) Apply(file []byte, synced []Item, keep map[string]bool) ([]byte, error) {
	local, err := f.Project(file)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range synced {
		if !keep[it.Name] {
			out = append(out, it)
		}
	}
	for _, it := range local {
		if keep[it.Name] {
			out = append(out, it)
		}
	}
	return f.Render(out), nil
}

func (decisions) Hold(Item, func(string) bool) string { return "" }
