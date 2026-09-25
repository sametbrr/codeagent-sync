// Package inventory records how the agent tools are installed on a machine
// — their versions, how they were installed, the programs their MCP servers
// start — so that another machine can be set up the same way. Every machine
// writes its own file, machines/<name>.yaml in the state directory, which
// syncs; nothing secret goes in it.
package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"

	"github.com/sametbrr/codeagent-sync/internal/platform"
)

// Dir is the directory of the machine files, inside the state directory.
const Dir = "machines"

// Machine is what one machine has.
type Machine struct {
	Name          string    `yaml:"machine" json:"machine"`
	OS            string    `yaml:"os" json:"os"`
	Arch          string    `yaml:"arch" json:"arch"`
	Updated       time.Time `yaml:"updated" json:"updated"`
	CodeagentSync string    `yaml:"codeagent_sync,omitempty" json:"codeagent_sync,omitempty"`
	Tools         []Program `yaml:"tools" json:"tools"`
	Programs      []Program `yaml:"programs,omitempty" json:"programs,omitempty"`
}

// Program is an installed program: an agent tool, or a program an MCP
// server starts.
type Program struct {
	Name      string   `yaml:"name" json:"name"`
	Version   string   `yaml:"version,omitempty" json:"version,omitempty"`
	Path      string   `yaml:"path,omitempty" json:"path,omitempty"`
	Found     bool     `yaml:"found" json:"found"`
	Installer string   `yaml:"installer,omitempty" json:"installer,omitempty"` // native, npm, brew, brew-cask, pipx, uv, app
	Install   string   `yaml:"install,omitempty" json:"install,omitempty"`     // the command that installs it
	For       []string `yaml:"for,omitempty" json:"for,omitempty"`             // the MCP servers that start it
}

// Collect describes this machine.
func Collect(ctx context.Context, d platform.Dirs, name, version string) Machine {
	m := Machine{Name: name, OS: runtime.GOOS, Arch: runtime.GOARCH, Updated: time.Now().UTC(), CodeagentSync: version}
	for _, t := range []struct{ name, versionPattern string }{
		{"claude", `(\d+\.\d+\.\d+)`},
		{"codex", `(\d+\.\d+\.\d+)`},
	} {
		p := locate(t.name)
		if p.Found {
			p.Version = runVersion(ctx, p.Path, t.versionPattern)
		}
		m.Tools = append(m.Tools, p)
	}
	m.Programs = mcpPrograms(d)
	return m
}

// locate finds a program by name or path and tells how it was installed.
func locate(nameOrPath string) Program {
	p := Program{Name: filepath.Base(nameOrPath)}
	path := nameOrPath
	if !filepath.IsAbs(path) {
		found, err := exec.LookPath(nameOrPath)
		if err != nil {
			return p
		}
		path = found
	} else if _, err := os.Stat(path); err != nil {
		p.Path = path
		return p
	}
	p.Path, p.Found = path, true
	real := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		real = r
	}
	p.Installer, p.Install = installer(p.Name, filepath.ToSlash(real))
	return p
}

var (
	nodeModules = regexp.MustCompile(`/node_modules/((?:@[^/]+/)?[^/]+)/`)
	brewCellar  = regexp.MustCompile(`/Cellar/([^/]+)/`)
	brewCask    = regexp.MustCompile(`/Caskroom/([^/]+)/`)
	pipxVenv    = regexp.MustCompile(`/pipx/venvs/([^/]+)/`)
	uvTool      = regexp.MustCompile(`/uv/tools/([^/]+)/`)
	pipUser     = regexp.MustCompile(`/(?:Library/)?Python/[0-9.]+/bin/|/\.local/bin/`)
)

// installer tells how a program was installed from where it really lives.
func installer(name, real string) (kind, command string) {
	switch {
	case name == "claude" && strings.Contains(real, "/.local/share/claude/"):
		if runtime.GOOS == "windows" {
			return "native", "irm https://claude.ai/install.ps1 | iex"
		}
		return "native", "curl -fsSL https://claude.ai/install.sh | bash"
	case nodeModules.MatchString(real):
		return "npm", "npm install -g " + nodeModules.FindStringSubmatch(real)[1]
	case brewCask.MatchString(real):
		return "brew-cask", "brew install --cask " + brewCask.FindStringSubmatch(real)[1]
	case brewCellar.MatchString(real):
		return "brew", "brew install " + brewCellar.FindStringSubmatch(real)[1]
	case pipxVenv.MatchString(real):
		return "pipx", "pipx install " + pipxVenv.FindStringSubmatch(real)[1]
	case uvTool.MatchString(real):
		return "uv", "uv tool install " + uvTool.FindStringSubmatch(real)[1]
	case strings.Contains(real, ".app/Contents/"):
		return "app", ""
	case pipUser.MatchString(real) && isPythonScript(real):
		// The package name is not recorded next to its script; it is
		// usually the program's own.
		return "pip", "python3 -m pip install --user " + name
	}
	return "", ""
}

// isPythonScript reports whether a program starts with a python shebang.
func isPythonScript(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 128)
	n, _ := f.Read(head)
	return strings.HasPrefix(string(head[:n]), "#!") && strings.Contains(string(head[:n]), "python")
}

var versionTimeout = 10 * time.Second

func runVersion(ctx context.Context, path, pattern string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = append(os.Environ(), "CODEAGENT_SYNC_NO_HOOKS=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(pattern).FindStringSubmatch(string(out)); m != nil {
		return m[1]
	}
	return ""
}

// mcpPrograms lists the programs the MCP servers of both tools start.
func mcpPrograms(d platform.Dirs) []Program {
	uses := map[string][]string{} // command -> servers
	if data, err := os.ReadFile(d.ClaudeJSON); err == nil {
		var doc struct {
			MCP map[string]struct {
				Command string `json:"command"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for name, s := range doc.MCP {
				if s.Command != "" {
					uses[s.Command] = append(uses[s.Command], "claude:"+name)
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(d.Codex, "config.toml")); err == nil {
		var doc struct {
			MCP map[string]struct {
				Command string `toml:"command"`
			} `toml:"mcp_servers"`
		}
		if toml.Unmarshal(data, &doc) == nil {
			for name, s := range doc.MCP {
				if s.Command != "" {
					uses[s.Command] = append(uses[s.Command], "codex:"+name)
				}
			}
		}
	}
	var out []Program
	for command, servers := range uses {
		if !filepath.IsAbs(command) && strings.ContainsAny(command, `/\`) {
			continue // relative to a working directory: part of an app
		}
		p := locate(command)
		sort.Strings(servers)
		p.For = servers
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Save writes a machine's file.
func Save(stateDir string, m Machine) error {
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	dir := filepath.Join(stateDir, Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	header := []byte("# How the agent tools are installed on this machine, written by codeagent-sync.\n")
	return platform.WriteFileAtomic(filepath.Join(dir, FileName(m.Name)), append(header, data...), 0o644)
}

// FileName is a machine's file name.
func FileName(machine string) string {
	return regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(machine, "_") + ".yaml"
}

// Age returns how long ago a machine's file was written, or a very long
// time when it was not.
func Age(stateDir, machine string) time.Duration {
	fi, err := os.Stat(filepath.Join(stateDir, Dir, FileName(machine)))
	if err != nil {
		return 1 << 62
	}
	return time.Since(fi.ModTime())
}

// Load reads every machine's file.
func Load(stateDir string) ([]Machine, error) {
	entries, err := os.ReadDir(filepath.Join(stateDir, Dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Machine
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(stateDir, Dir, e.Name()))
		if err != nil {
			continue
		}
		var m Machine
		if yaml.Unmarshal(data, &m) == nil && m.Name != "" {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Missing is something another machine has that this one lacks, or has in
// another version.
type Missing struct {
	Program
	On       string `json:"on"`             // the machine that has it
	OnOS     string `json:"on_os"`          // its system
	Here     string `json:"here,omitempty"` // this machine's version, when only the version differs
	SameKind bool   `json:"same_os"`        // the install command fits this system
	Tool     bool   `json:"tool"`           // an agent tool, not an MCP program
}

// Compare lists what the other machines have that this one lacks, and the
// agent tools whose version differs.
func Compare(here Machine, others []Machine) []Missing {
	have := map[string]Program{}
	for _, p := range append(append([]Program{}, here.Tools...), here.Programs...) {
		if p.Found {
			have[p.Name] = p
		}
	}
	seen := map[string]bool{}
	var out []Missing
	for _, m := range others {
		if m.Name == here.Name {
			continue
		}
		for i, list := range [][]Program{m.Tools, m.Programs} {
			for _, p := range list {
				if !p.Found || seen[p.Name] {
					continue
				}
				mine, ok := have[p.Name]
				switch {
				case !ok:
				case i == 0 && p.Version != "" && mine.Version != "" && p.Version != mine.Version:
				default:
					continue
				}
				seen[p.Name] = true
				out = append(out, Missing{Program: p, On: m.Name, OnOS: m.OS, Here: mine.Version, SameKind: m.OS == here.OS, Tool: i == 0})
			}
		}
	}
	return out
}
