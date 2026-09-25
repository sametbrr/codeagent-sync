package structured

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// codexFixture mirrors the shape of a real config.toml.
const codexFixture = `# Codex settings
model = "gpt-5.5"
model_reasoning_effort = "high"
notify = [
  "/Users/ad/bin/notify", # the notifier
  "--quiet",
]

[features]
multi_agent = true
js_repl = false

[projects."/Users/ad/Projects/a"]
trust_level = "trusted"

[marketplaces.codex-warp]
last_updated = "2026-09-20T10:00:00Z"
last_revision = "abc123"
source_type = "git"
source = "https://github.com/x/warp"

[plugins."warp@codex-warp"]
enabled = true

[hooks.state]

[hooks.state."warp@codex-warp:hooks/hooks.json:stop:0:0"]
trusted_hash = "sha256:aaa"

[mcp_servers.openaiDeveloperDocs]
url = "https://developers.openai.com/mcp"

[mcp_servers.uisight]
command = "/opt/homebrew/bin/uisight-mcp"
startup_timeout_sec = 120

[mcp_servers.uisight.tools.inspect]
approval_mode = "approve"
`

func names(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

func mustProject(t *testing.T, f Format, file string) []Item {
	t.Helper()
	items, err := f.Project([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestCodexProjectLeavesMachineStateOut(t *testing.T) {
	items := mustProject(t, codexConfig{}, codexFixture)
	want := []string{"model", "model_reasoning_effort", "notify", "features", "marketplaces.codex-warp",
		`plugins."warp@codex-warp"`, "mcp_servers.openaiDeveloperDocs", "mcp_servers.uisight"}
	if got := names(items); !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %q\nwant    %q", got, want)
	}
	for _, it := range items {
		if strings.Contains(string(it.Text), "last_revision") || strings.Contains(string(it.Text), "trusted_hash") {
			t.Errorf("%s carries machine state:\n%s", it.Name, it.Text)
		}
	}
	if !strings.Contains(string(items[0].Text), "# Codex settings") {
		t.Error("the file's leading comment was not kept with the first item")
	}
}

// Applying a file's own synced form must not change a single byte.
func TestCodexApplyOfOwnProjectionIsIdentity(t *testing.T) {
	f := codexConfig{}
	out, err := f.Apply([]byte(codexFixture), mustProject(t, f, codexFixture), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != codexFixture {
		t.Errorf("file changed:\n%s", out)
	}
}

func TestCodexFingerprintIgnoresFormatting(t *testing.T) {
	f := codexConfig{}
	reformatted := strings.NewReplacer(
		"startup_timeout_sec = 120", "startup_timeout_sec   =   120.0",
		"model = \"gpt-5.5\"", "model = 'gpt-5.5'",
		"trust_level = \"trusted\"", "trust_level = \"untrusted\"", // machine state: ignored
	).Replace(codexFixture)
	if Fingerprint(mustProject(t, f, codexFixture)) != Fingerprint(mustProject(t, f, reformatted)) {
		t.Error("formatting or machine-only differences changed the fingerprint")
	}
	changed := strings.Replace(codexFixture, `model = "gpt-5.5"`, `model = "gpt-6"`, 1)
	if Fingerprint(mustProject(t, f, codexFixture)) == Fingerprint(mustProject(t, f, changed)) {
		t.Error("a real change kept the fingerprint")
	}
}

func TestCodexApplyTakesRemoteChangesAndKeepsLocalState(t *testing.T) {
	f := codexConfig{}
	other := `model = "gpt-6"
model_reasoning_effort = "high"
notify = ["/Users/ad/bin/notify", "--quiet"]
service_tier = "fast"

[features]
multi_agent = true
js_repl = false

[marketplaces.codex-warp]
source_type = "git"
source = "https://github.com/x/warp"

[mcp_servers.openaiDeveloperDocs]
url = "https://developers.openai.com/mcp"

[mcp_servers.uisight]
command = "/opt/homebrew/bin/uisight-mcp"
startup_timeout_sec = 120

[mcp_servers.codegraph]
command = "codegraph"
args = ["serve", "--mcp"]
`
	out, err := f.Apply([]byte(codexFixture), mustProject(t, f, other), nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("invalid TOML: %v\n%s", err, out)
	}
	s := string(out)
	for _, want := range []string{
		`model = "gpt-6"`, `service_tier = "fast"`, "[mcp_servers.codegraph]", // remote changes
		`[projects."/Users/ad/Projects/a"]`, `trusted_hash = "sha256:aaa"`, // this machine's state
		`last_revision = "abc123"`, "[mcp_servers.uisight.tools.inspect]",
		"# Codex settings", "# the notifier",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("merged file lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "warp@codex-warp\"]\nenabled") {
		t.Errorf("the plugin removed on the other machine is still there:\n%s", s)
	}
	// A new top-level key must land above the first table, not inside one.
	if doc["service_tier"] != "fast" {
		t.Errorf("service_tier landed inside a table: %v", doc["service_tier"])
	}
	// The merged file projects to exactly the other machine's items.
	if Fingerprint(mustProject(t, f, s)) != Fingerprint(mustProject(t, f, other)) {
		t.Error("the merged file does not say what the synced form says")
	}
}

func TestCodexHeldServers(t *testing.T) {
	f := codexConfig{}
	items := mustProject(t, f, codexFixture+"\n[mcp_servers.uisight.env]\nDEBUG = \"1\"\n")
	held := HeldNames(f, items, func(p string) bool { return p != "/opt/homebrew/bin/uisight-mcp" })
	if held["mcp_servers.uisight"] == "" || held["mcp_servers.uisight.env"] == "" || len(held) != 2 {
		t.Fatalf("held = %v", held)
	}

	// A machine that holds the server does not get it written.
	bare := "model = \"gpt-5.5\"\n"
	out, err := f.Apply([]byte(bare), items, map[string]bool{"mcp_servers.uisight": true, "mcp_servers.uisight.env": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "uisight") {
		t.Errorf("held server written:\n%s", out)
	}
	if !strings.Contains(string(out), "[mcp_servers.openaiDeveloperDocs]") {
		t.Errorf("other servers missing:\n%s", out)
	}
}

func TestSettingsJSON(t *testing.T) {
	f := jsonKeys{}
	local := `{
  "model": "opus",
  "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "a"}]}]},
  "effortLevel": "high"
}
`
	items := mustProject(t, f, local)
	if got := names(items); !reflect.DeepEqual(got, []string{"model", "hooks", "effortLevel"}) {
		t.Fatalf("items = %q", got)
	}
	reformatted := `{"effortLevel":"high","hooks":{"SessionStart":[{"hooks":[{"command":"a","type":"command"}]}]},"model":"opus"}`
	if Fingerprint(items) != Fingerprint(mustProject(t, f, reformatted)) {
		t.Error("key order or whitespace changed the fingerprint")
	}

	remote := `{"model": "sonnet", "effortLevel": "high", "statusLine": {"type": "command"}}`
	out, err := f.Apply([]byte(local), mustProject(t, f, remote), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "model": "sonnet",
  "effortLevel": "high",
  "statusLine": {
    "type": "command"
  }
}
`
	if string(out) != want {
		t.Errorf("merged settings.json:\n%s\nwant\n%s", out, want)
	}
}

func TestClaudeMCPOnlyTouchesServers(t *testing.T) {
	f := claudeMCP{}
	local := `{
  "numStartups": 42,
  "projects": {"/Users/ad/x": {"allowedTools": []}},
  "mcpServers": {
    "codegraph": {"type": "stdio", "command": "codegraph", "args": ["serve", "--mcp"]},
    "uisight": {"type": "stdio", "command": "/opt/homebrew/bin/uisight-mcp"}
  },
  "oauthAccount": {"emailAddress": "someone@example.com"}
}
`
	items := mustProject(t, f, local)
	if got := names(items); !reflect.DeepEqual(got, []string{"codegraph", "uisight"}) {
		t.Fatalf("items = %q", got)
	}
	synced := f.Render(items)
	if strings.Contains(string(synced), "oauthAccount") || strings.Contains(string(synced), "numStartups") {
		t.Fatalf("synced form carries machine state:\n%s", synced)
	}
	parsed, err := f.Parse(synced)
	if err != nil || Fingerprint(parsed) != Fingerprint(items) {
		t.Fatalf("Parse(Render) = %v, %v", names(parsed), err)
	}

	remote := `{"mcpServers": {"codegraph": {"type": "stdio", "command": "codegraph", "args": ["serve", "--mcp"]},
		"markitdown": {"type": "stdio", "command": "markitdown-mcp"}}}`
	out, err := f.Apply([]byte(local), mustProject(t, f, remote), map[string]bool{"uisight": true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"numStartups": 42`, `"oauthAccount": {"emailAddress": "someone@example.com"}`, `"markitdown"`, `"uisight"`} {
		if !strings.Contains(s, want) {
			t.Errorf("result lacks %s:\n%s", want, s)
		}
	}
	if !strings.HasPrefix(s, "{\n  \"numStartups\": 42,\n  \"projects\"") {
		t.Errorf("member order changed:\n%s", s)
	}

	held := HeldNames(f, items, func(p string) bool { return false })
	if len(held) != 1 || held["uisight"] == "" {
		t.Errorf("held = %v; only the server with an absolute command should be held", held)
	}
}

func TestMerge3(t *testing.T) {
	item := func(name, value string) Item { return Item{Name: name, Value: value, Text: []byte(value)} }
	base := []Item{item("a", "1"), item("b", "1"), item("c", "1"), item("d", "1")}
	local := []Item{item("a", "2"), item("b", "1"), item("c", "3"), item("e", "new-here")}
	remote := []Item{item("a", "2"), item("b", "5"), item("c", "4"), item("d", "1"), item("f", "new-there")}

	merged, conflicts := Merge3(base, local, remote, false)
	got := map[string]string{}
	for _, it := range merged {
		got[it.Name] = it.Value
	}
	want := map[string]string{
		"a": "2",         // same change on both sides
		"b": "5",         // changed remotely
		"c": "3",         // conflict: this machine's kept
		"e": "new-here",  // added here
		"f": "new-there", // added there; d was deleted here
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(conflicts, []string{"c"}) {
		t.Errorf("merged = %v, conflicts = %v", got, conflicts)
	}

	merged, conflicts = Merge3(nil, local, remote, true)
	for _, it := range merged {
		if it.Name == "c" && it.Value != "4" {
			t.Errorf("preferRemote kept %s for c", it.Value)
		}
	}
	if len(conflicts) != 0 {
		t.Errorf("preferRemote reported conflicts %v", conflicts)
	}
}
