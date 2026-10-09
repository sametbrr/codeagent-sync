package engine

import (
	"path/filepath"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/projects"
)

// withProject adds a project root to a machine, checked out at rel.
func (m *machine) withProject(identity, rel string, tracked ...string) {
	m.eng.Roots = append(m.eng.Roots, projects.Roots([]projects.Project{
		{Identity: identity, Dir: m.path(rel), Tracked: tracked},
	})...)
}

func TestProjectConfigFollowsTheProjectToAnotherPath(t *testing.T) {
	const id = "workspace:bitbucket.org/x/api+bitbucket.org/x/web"
	key := "v1/" + projects.RootName(id) + "/.mcp.json"

	w := newWorld(t)
	a := w.machine(platform.Current())
	a.withProject(id, "Projects/shop")
	a.write("Projects/shop/.mcp.json", `{"mcpServers":{"jira":{"type":"http","url":"https://mcp.example.com"}}}`)
	a.write("Projects/shop/.claude/settings.local.json", `{"dir":"`+a.path("Projects/shop")+`"}`)
	a.write("Projects/shop/.claude/settings.json", "tracked by git on b")
	a.write("Projects/shop/src/main.go", "package main")
	a.sync(Options{AdoptLocal: true})

	b := w.machine(platform.Current())
	b.withProject(id, "work/shop-checkout", ".claude/settings.json")
	b.write("work/shop-checkout/.claude/settings.json", "b's own, committed")
	res := b.sync(Options{})
	if find(res.Actions, Download, key) == nil {
		t.Fatalf("project .mcp.json not downloaded: %v", kinds(res.Actions))
	}
	if got := b.read("work/shop-checkout/.mcp.json"); got != a.read("Projects/shop/.mcp.json") {
		t.Errorf(".mcp.json = %q", got)
	}
	if got, want := b.read("work/shop-checkout/.claude/settings.local.json"), `{"dir":"`+filepath.Join(b.home, "Projects", "shop")+`"}`; got != want {
		t.Errorf("home directory not translated: %q, want %q", got, want)
	}
	if got := b.read("work/shop-checkout/.claude/settings.json"); got != "b's own, committed" {
		t.Errorf("a file git tracks was overwritten: %q", got)
	}
	if b.exists("work/shop-checkout/src/main.go") {
		t.Error("project source synced")
	}
	b.quiet()

	b.write("work/shop-checkout/.mcp.json", `{"mcpServers":{}}`)
	b.sync(Options{})
	a.sync(Options{})
	if got := a.read("Projects/shop/.mcp.json"); got != `{"mcpServers":{}}` {
		t.Errorf("edit did not come back: %q", got)
	}

	// A machine without the project leaves its entries alone.
	c := w.machine(platform.Current())
	c.sync(Options{})
	if c.exists("Projects/shop/.mcp.json") {
		t.Error("a machine without the project received its files")
	}
	c.quiet()
	a.quiet()
	b.quiet()
}
