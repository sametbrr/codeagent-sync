package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/sametbrr/codeagent-sync/internal/crypto"
	"github.com/sametbrr/codeagent-sync/internal/envelope"
	"github.com/sametbrr/codeagent-sync/internal/homepath"
	"github.com/sametbrr/codeagent-sync/internal/platform"
	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/storage/memstore"
	"github.com/sametbrr/codeagent-sync/internal/tools"
)

// world is a store shared by several simulated machines.
type world struct {
	t      *testing.T
	store  *memstore.Store
	cipher envelope.Cipher
}

func newWorld(t *testing.T) *world {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the simulated machines use POSIX symlinks")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, store: memstore.New(), cipher: crypto.NewEncryptorFromIdentity(id)}
}

type machine struct {
	t    *testing.T
	home string
	eng  *Engine
}

// machine returns a machine with Claude Code and Codex installed (their
// directories exist).
func (w *world) machine(os platform.OS) *machine {
	w.t.Helper()
	m := w.bareMachine(os)
	for _, dir := range []string{".claude", ".codex"} {
		if err := mkdir(m.path(dir)); err != nil {
			w.t.Fatal(err)
		}
	}
	return m
}

// bareMachine returns a machine with no agent tool installed.
func (w *world) bareMachine(os platform.OS) *machine {
	w.t.Helper()
	home, err := filepath.EvalSymlinks(w.t.TempDir())
	if err != nil {
		w.t.Fatal(err)
	}
	return w.machineAt(home, w.store, os)
}

func mkdir(p string) error { return os.MkdirAll(p, 0o755) }

func (w *world) machineAt(home string, store storage.ObjectStore, platformOS platform.OS) *machine {
	dirs := platform.ResolveDirs(home, func(string) string { return "" })
	return &machine{t: w.t, home: home, eng: &Engine{
		Store:       store,
		Cipher:      w.cipher,
		Roots:       tools.Roots(dirs),
		Mapper:      homepath.New(home, platform.Current()),
		OS:          platformOS,
		StateDir:    dirs.State,
		LockTimeout: 200 * time.Millisecond,
	}}
}

func (m *machine) path(rel string) string { return filepath.Join(m.home, filepath.FromSlash(rel)) }

func (m *machine) write(rel, content string) { m.writeMode(rel, content, 0o644) }

func (m *machine) writeMode(rel, content string, perm os.FileMode) {
	m.t.Helper()
	p := m.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) link(rel, target string) {
	m.t.Helper()
	p := m.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) remove(rel string) {
	m.t.Helper()
	if err := os.RemoveAll(m.path(rel)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) read(rel string) string {
	m.t.Helper()
	data, err := os.ReadFile(m.path(rel))
	if err != nil {
		m.t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func (m *machine) exists(rel string) bool {
	_, err := os.Lstat(m.path(rel))
	return err == nil
}

func (m *machine) sync(opts Options) *Result {
	m.t.Helper()
	res, err := m.eng.Run(context.Background(), opts)
	if err != nil {
		m.t.Fatalf("sync: %v", err)
	}
	return res
}

// quiet syncs and fails the test if the run changed or reported anything.
func (m *machine) quiet() {
	m.t.Helper()
	res := m.sync(Options{})
	if len(res.Actions)+len(res.Pending)+len(res.Conflicts)+len(res.Problems) != 0 {
		m.t.Fatalf("expected a no-op sync, got actions %v pending %v conflicts %v problems %v",
			kinds(res.Actions), kinds(res.Pending), kinds(res.Conflicts), res.Problems)
	}
}

func kinds(actions []*Action) []string {
	var out []string
	for _, a := range actions {
		out = append(out, string(a.Kind)+" "+a.Key)
	}
	return out
}

func find(actions []*Action, kind ActionKind, key string) *Action {
	for _, a := range actions {
		if a.Kind == kind && a.Key == key {
			return a
		}
	}
	return nil
}

const (
	skillKey = "v1/agents/skills/foo/SKILL.md"
	skillRel = ".agents/skills/foo/SKILL.md"
)

// seeded returns two machines that share a synced skill and CLAUDE.md.
func seeded(t *testing.T) (*world, *machine, *machine) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(skillRel, "v1")
	a.write(".claude/CLAUDE.md", "rules")
	a.sync(Options{AdoptLocal: true})
	b := w.machine(platform.Current())
	b.sync(Options{})
	return w, a, b
}

func TestFirstSyncWaitsForConfirmation(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(skillRel, "v1")
	a.write(".claude/CLAUDE.md", "rules")

	res := a.sync(Options{})
	if len(res.Pending) != 2 || len(res.Actions) != 0 || len(w.store.Keys()) != 0 {
		t.Fatalf("first sync without confirmation: pending %v actions %v remote %v",
			kinds(res.Pending), kinds(res.Actions), w.store.Keys())
	}

	res = a.sync(Options{AdoptLocal: true})
	if len(res.Actions) != 2 || len(w.store.Keys()) != 2 {
		t.Fatalf("confirmed first sync: actions %v remote %v", kinds(res.Actions), w.store.Keys())
	}
	a.quiet()
}

func TestAMachineThatStartedEmptyUploadsWhatItGetsLater(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.sync(Options{})
	a.write(skillRel, "new")
	if res := a.sync(Options{}); len(res.Pending) != 0 || find(res.Actions, Upload, "v1/agents/skills/foo/SKILL.md") == nil {
		t.Fatalf("pending %v actions %v", kinds(res.Pending), kinds(res.Actions))
	}
	a.quiet()
}

func TestPullDoesNotSettleTheFirstSync(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(skillRel, "old")
	a.sync(Options{Mode: PullOnly})
	if res := a.sync(Options{}); len(res.Pending) != 1 || len(w.store.Keys()) != 0 {
		t.Fatalf("after a pull: pending %v remote %v", kinds(res.Pending), w.store.Keys())
	}
}

func TestDeclinedFirstUploadStaysWaiting(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(skillRel, "old")
	a.sync(Options{})
	a.write(".claude/CLAUDE.md", "rules")
	if res := a.sync(Options{}); len(res.Pending) != 2 || len(w.store.Keys()) != 0 {
		t.Fatalf("pending %v remote %v", kinds(res.Pending), w.store.Keys())
	}
}

func TestNewMachineReceivesEverything(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.writeMode(".agents/skills/foo/run.sh", "#!/bin/sh\n", 0o755)
	a.write(skillRel, "skill")
	a.link(".claude/skills/foo", "../../.agents/skills/foo")
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(a.path(skillRel), mtime, mtime)
	a.sync(Options{AdoptLocal: true})

	b := w.machine(platform.Current())
	res := b.sync(Options{})
	if len(res.Actions) != 3 {
		t.Fatalf("new machine actions = %v", kinds(res.Actions))
	}
	if got := b.read(skillRel); got != "skill" {
		t.Errorf("SKILL.md = %q", got)
	}
	if fi, _ := os.Stat(b.path(".agents/skills/foo/run.sh")); fi == nil || fi.Mode().Perm()&0o111 == 0 {
		t.Error("run.sh lost its execute bit")
	}
	if fi, _ := os.Stat(b.path(skillRel)); fi == nil || !fi.ModTime().Equal(mtime) {
		t.Errorf("modification time not preserved: %v", fi)
	}
	target, err := os.Readlink(b.path(".claude/skills/foo"))
	if err != nil || target != filepath.FromSlash("../../.agents/skills/foo") {
		t.Errorf("link = %q, %v; want a relative link to the shared skill", target, err)
	}
	if got := b.read(".claude/skills/foo/SKILL.md"); got != "skill" {
		t.Errorf("SKILL.md through the link = %q", got)
	}
	b.quiet()
	a.quiet()
}

func TestEditsAndDeletionsPropagate(t *testing.T) {
	_, a, b := seeded(t)

	a.write(skillRel, "v2")
	a.sync(Options{})
	b.sync(Options{})
	if got := b.read(skillRel); got != "v2" {
		t.Fatalf("edit did not arrive: %q", got)
	}

	a.remove(".agents/skills/foo")
	res := a.sync(Options{})
	if find(res.Actions, UploadDelete, skillKey) == nil {
		t.Fatalf("deletion not recorded: %v", kinds(res.Actions))
	}
	b.sync(Options{})
	if b.exists(".agents/skills/foo") {
		t.Error("the deleted skill's directory is still there")
	}
	if !b.exists(".agents/skills") && !b.exists(".agents") {
		t.Log("parent directories were pruned up to the root")
	}
	a.quiet()
	b.quiet()
}

func TestConcurrentEditsConflict(t *testing.T) {
	_, a, b := seeded(t)
	a.write(skillRel, "from a")
	b.write(skillRel, "from b")
	a.sync(Options{})

	res := b.sync(Options{})
	if res.ExitCode() != 2 || find(res.Conflicts, Conflict, skillKey) == nil {
		t.Fatalf("expected a conflict, got %v (exit %d)", kinds(res.Conflicts), res.ExitCode())
	}
	if got := b.read(skillRel); got != "from b" {
		t.Errorf("b's version was replaced: %q", got)
	}
	copies, err := b.eng.Conflicts()
	if err != nil || len(copies) != 1 {
		t.Fatalf("conflict copies = %+v, %v", copies, err)
	}
	if data, _ := os.ReadFile(copies[0].Path); string(data) != "from a" {
		t.Errorf("saved copy = %q, want a's version", data)
	}
	saved := copies[0].Saved

	// The conflict persists until resolved, but the copy is saved once.
	if res := b.sync(Options{}); len(res.Conflicts) != 1 {
		t.Errorf("conflict disappeared: %v", kinds(res.Conflicts))
	}
	copies, _ = b.eng.Conflicts()
	if len(copies) != 1 || !copies[0].Saved.Equal(saved) {
		t.Errorf("the same remote version was saved again: %+v", copies)
	}
}

func TestEditWinsOverDelete(t *testing.T) {
	_, a, b := seeded(t)
	a.remove(skillRel)
	a.sync(Options{})
	b.write(skillRel, "edited on b")

	res := b.sync(Options{})
	if up := find(res.Actions, Upload, skillKey); up == nil || up.Note == "" {
		t.Fatalf("b should keep and upload its edit: %v", kinds(res.Actions))
	}
	a.sync(Options{})
	if got := a.read(skillRel); got != "edited on b" {
		t.Errorf("a did not get the file back: %q", got)
	}
}

func TestDeletedHereChangedThereIsRestored(t *testing.T) {
	_, a, b := seeded(t)
	a.write(skillRel, "edited on a")
	a.sync(Options{})
	b.remove(skillRel)

	res := b.sync(Options{})
	if dl := find(res.Actions, Download, skillKey); dl == nil || dl.Note == "" {
		t.Fatalf("b should restore the edited file: %v", kinds(res.Actions))
	}
	if got := b.read(skillRel); got != "edited on a" {
		t.Errorf("restored content = %q", got)
	}
}

func TestHomeDirectoryIsTranslated(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(".claude/CLAUDE.md", "wiki at "+a.home+"/wiki; hooks use $HOME\n")
	a.sync(Options{AdoptLocal: true})

	b := w.machine(platform.Current())
	b.sync(Options{})
	if got, want := b.read(".claude/CLAUDE.md"), "wiki at "+b.home+"/wiki; hooks use $HOME\n"; got != want {
		t.Errorf("CLAUDE.md on b = %q, want %q", got, want)
	}
	b.quiet()
}

// A write that loses a race against another machine must not overwrite it:
// the sync runs again, sees the other version and reports a conflict.
func TestLostRaceBecomesAConflict(t *testing.T) {
	w, a, b := seeded(t)
	b.write(skillRel, "from b")

	raced := false
	w.store.BeforePut = func(key string) {
		if key != skillKey || raced {
			return
		}
		raced = true
		w.store.BeforePut = nil
		a.write(skillRel, "from a, just in time")
		a.sync(Options{})
	}

	res := b.sync(Options{})
	if !raced {
		t.Fatal("the race was not staged")
	}
	if find(res.Conflicts, Conflict, skillKey) == nil {
		t.Fatalf("expected a conflict after the lost race, got actions %v conflicts %v", kinds(res.Actions), kinds(res.Conflicts))
	}
	data, _, err := w.store.Get(context.Background(), skillKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, content, _ := envelope.Open(w.cipher, skillKey, data); string(content) != "from a, just in time" {
		t.Errorf("remote = %q; a's write was lost", content)
	}
}

func TestFirstSyncAdoptsTheSharedVersion(t *testing.T) {
	w, a, _ := seeded(t)
	_ = a
	c := w.machine(platform.Current())
	c.write(skillRel, "c's old copy")
	c.write(".claude/look-again/only-here.md", "local note")

	res := c.sync(Options{})
	if got := c.read(skillRel); got != "v1" {
		t.Errorf("c kept its own version on the first sync: %q", got)
	}
	copies, _ := c.eng.Conflicts()
	if len(copies) != 1 || copies[0].Side != sideLocal {
		t.Fatalf("c's replaced copy was not kept: %+v", copies)
	}
	if data, _ := os.ReadFile(copies[0].Path); string(data) != "c's old copy" {
		t.Errorf("kept copy = %q", data)
	}
	if find(res.Pending, Pending, "v1/claude/look-again/only-here.md") == nil {
		t.Errorf("the local-only note should wait for confirmation: %v", kinds(res.Pending))
	}
}

func TestUndoRestoresAndTheNextSyncRevertsEverywhere(t *testing.T) {
	_, a, b := seeded(t)
	a.write(skillRel, "v2")
	a.sync(Options{})
	res := b.sync(Options{})
	if res.BackupID == "" || b.read(skillRel) != "v2" {
		t.Fatalf("b did not take v2 with a backup: %+v", res)
	}

	info, restored, err := b.eng.Undo("")
	if err != nil || info.ID != res.BackupID || info.What != "sync" || restored != 1 {
		t.Fatalf("Undo = %+v, %d, %v", info, restored, err)
	}
	if got := b.read(skillRel); got != "v1" {
		t.Errorf("after undo = %q, want v1", got)
	}
	// Undo works back through the runs: the next one would take back b's
	// first sync. It must not offer the run just undone again.
	if older, err := b.eng.findBackup(""); err != nil || older.ID == info.ID {
		t.Errorf("next undo would restore %v, %v", older, err)
	}
	if _, _, err := b.eng.Undo(info.ID); err == nil {
		t.Error("the same change was undone twice")
	}
	list, err := b.eng.Backups()
	if err != nil || len(list) == 0 || list[0].ID != info.ID || !list[0].Undone {
		t.Errorf("Backups = %+v, %v", list, err)
	}

	b.sync(Options{})
	a.sync(Options{})
	if got := a.read(skillRel); got != "v1" {
		t.Errorf("a = %q; the undo should have reached it", got)
	}
}

func TestInterruptedRunIsReported(t *testing.T) {
	_, _, b := seeded(t)
	if err := writeJournal(b.eng.StateDir, "20260925-120000"); err != nil {
		t.Fatal(err)
	}
	res := b.sync(Options{})
	if len(res.Notices) != 1 || !strings.Contains(res.Notices[0], "20260925-120000") {
		t.Errorf("notices = %q", res.Notices)
	}
	if _, interrupted := interruptedRun(b.eng.StateDir); interrupted {
		t.Error("the journal survived a completed run")
	}
}

// A machine where a skill is a real directory must not write into the
// shared copy through the other machine's link, nor replace its directory.
func TestLinkAgainstDirectoryIsALayoutConflict(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(skillRel, "shared")
	a.link(".claude/skills/foo", "../../.agents/skills/foo")
	a.sync(Options{AdoptLocal: true})

	c := w.machine(platform.Current())
	c.write(".claude/skills/foo/SKILL.md", "c's own copy")
	res := c.sync(Options{AdoptLocal: true})

	problems := map[string]bool{}
	for _, p := range res.Problems {
		problems[p.Key] = true
	}
	if !problems["v1/claude/skills/foo"] || !problems["v1/claude/skills/foo/SKILL.md"] {
		t.Fatalf("problems = %+v, want the link and the file below it", res.Problems)
	}
	if got := c.read(".claude/skills/foo/SKILL.md"); got != "c's own copy" {
		t.Errorf("c's directory was changed: %q", got)
	}
	if fi, _ := os.Lstat(c.path(".claude/skills/foo")); fi == nil || !fi.IsDir() {
		t.Error("c's directory was replaced")
	}
}

// Regression: when the last shared link goes, pruning must not take the
// Claude app's own skills (skills/synced, which is excluded from syncing)
// with the now "empty" skills directory.
func TestPruningKeepsFilesThatAreNotSynced(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(skillRel, "shared")
	a.link(".claude/skills/foo", "../../.agents/skills/foo")
	a.sync(Options{AdoptLocal: true})

	b := w.machine(platform.Current())
	b.write(".claude/skills/synced/app-skill/SKILL.md", "managed by the Claude app")
	b.sync(Options{})

	a.remove(".claude/skills/foo")
	a.sync(Options{})
	b.sync(Options{})
	if b.exists(".claude/skills/foo") {
		t.Error("the link was not removed")
	}
	if got := b.read(".claude/skills/synced/app-skill/SKILL.md"); got != "managed by the Claude app" {
		t.Errorf("app-managed skill = %q", got)
	}
}

// etaglessStore hides ETags from Put, like WebDAV servers that do not report
// them; List still provides them.
type etaglessStore struct{ *memstore.Store }

func (s etaglessStore) Put(ctx context.Context, key string, data []byte, pre storage.Precondition) (string, error) {
	_, err := s.Store.Put(ctx, key, data, pre)
	return "", err
}

func TestUnknownETagsCauseNoConflicts(t *testing.T) {
	w := newWorld(t)
	store := etaglessStore{w.store}
	homeA, _ := filepath.EvalSymlinks(t.TempDir())
	a := w.machineAt(homeA, store, platform.Current())
	a.write(skillRel, "v1")
	a.sync(Options{AdoptLocal: true})

	res := a.sync(Options{})
	if len(res.Conflicts) != 0 || find(res.Actions, Record, skillKey) == nil {
		t.Fatalf("second sync: actions %v conflicts %v", kinds(res.Actions), kinds(res.Conflicts))
	}
	a.quiet()
}

// Windows cannot see the execute bit: a script edited there keeps it.
func TestExecuteBitSurvivesAWindowsMachine(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.writeMode(".agents/skills/foo/run.sh", "#!/bin/sh\necho 1\n", 0o755)
	a.sync(Options{AdoptLocal: true})

	win := w.machine(platform.Windows)
	win.sync(Options{})
	win.writeMode(".agents/skills/foo/run.sh", "#!/bin/sh\necho 2\n", 0o644)
	win.sync(Options{})

	a.sync(Options{})
	fi, err := os.Stat(a.path(".agents/skills/foo/run.sh"))
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("run.sh lost its execute bit after an edit on Windows: %v", fi)
	}
	if got := a.read(".agents/skills/foo/run.sh"); got != "#!/bin/sh\necho 2\n" {
		t.Errorf("content = %q", got)
	}
}

func TestNamesInvalidOnThisPlatformAreSkipped(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(".agents/skills/foo/CON.md", "a reserved name on Windows")
	a.write(skillRel, "fine")
	a.sync(Options{AdoptLocal: true})

	win := w.machine(platform.Windows)
	res := win.sync(Options{})
	if win.exists(".agents/skills/foo/CON.md") {
		t.Error("a name Windows reserves was written")
	}
	if len(res.Problems) != 1 || res.Problems[0].Key != "v1/agents/skills/foo/CON.md" {
		t.Errorf("problems = %+v", res.Problems)
	}
	if got := win.read(skillRel); got != "fine" {
		t.Errorf("SKILL.md = %q", got)
	}
}

func TestPullOnlyAndPushOnly(t *testing.T) {
	_, a, b := seeded(t)
	a.write(skillRel, "v2")
	b.write(".claude/CLAUDE.md", "rules v2")

	a.sync(Options{Mode: PushOnly})
	res := b.sync(Options{Mode: PullOnly})
	if find(res.Actions, Download, skillKey) == nil || find(res.Actions, Upload, "v1/claude/CLAUDE.md") != nil {
		t.Errorf("pull-only run = %v", kinds(res.Actions))
	}
	res = b.sync(Options{Mode: PushOnly})
	if find(res.Actions, Upload, "v1/claude/CLAUDE.md") == nil {
		t.Errorf("push-only run = %v", kinds(res.Actions))
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	w, a, b := seeded(t)
	a.write(skillRel, "v2")
	a.sync(Options{})
	before := len(w.store.Keys())

	res := b.sync(Options{DryRun: true})
	if find(res.Actions, Download, skillKey) == nil {
		t.Fatalf("dry run planned %v", kinds(res.Actions))
	}
	if got := b.read(skillRel); got != "v1" || len(w.store.Keys()) != before {
		t.Errorf("dry run changed something: local %q", got)
	}
}

func TestSecondSyncOnTheSameMachineWaits(t *testing.T) {
	_, _, b := seeded(t)
	held, err := platform.TryLock(filepath.Join(b.eng.StateDir, lockFile))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()
	if _, err := b.eng.Run(context.Background(), Options{}); !errors.Is(err, ErrBusy) {
		t.Errorf("Run while locked = %v, want ErrBusy", err)
	}
}

func TestResolveKeepingEitherSide(t *testing.T) {
	for _, keepLocal := range []bool{true, false} {
		_, a, b := seeded(t)
		a.write(skillRel, "from a")
		a.sync(Options{})
		b.write(skillRel, "from b")
		if res := b.sync(Options{}); len(res.Conflicts) != 1 {
			t.Fatalf("no conflict to resolve: %v", kinds(res.Conflicts))
		}

		if err := b.eng.Resolve(context.Background(), skillKey, keepLocal); err != nil {
			t.Fatalf("Resolve(keepLocal=%v): %v", keepLocal, err)
		}
		want := "from a"
		if keepLocal {
			want = "from b"
		}
		b.quiet()
		a.sync(Options{})
		if got, gotA := b.read(skillRel), a.read(skillRel); got != want || gotA != want {
			t.Errorf("keepLocal=%v: b has %q, a has %q, want %q on both", keepLocal, got, gotA, want)
		}
		if copies, _ := b.eng.Conflicts(); len(copies) != 0 {
			t.Errorf("keepLocal=%v: conflict copies left: %+v", keepLocal, copies)
		}
		if err := b.eng.Resolve(context.Background(), skillKey, keepLocal); !errors.Is(err, ErrNoConflict) {
			t.Errorf("resolving again = %v, want ErrNoConflict", err)
		}
	}
}

// A tool that is not installed on a machine is left alone there: its files
// are not written, and their absence is not a deletion.
func TestToolsThatAreNotInstalledAreLeftAlone(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(".codex/AGENTS.md", "codex rules")
	a.write(skillRel, "shared")
	a.sync(Options{AdoptLocal: true})

	b := w.bareMachine(platform.Current())
	if err := mkdir(b.path(".claude")); err != nil {
		t.Fatal(err)
	}
	b.sync(Options{})
	if b.exists(".codex") {
		t.Error("files of Codex, which is not installed on b, were written")
	}
	if got := b.read(skillRel); got != "shared" {
		t.Errorf("shared skill = %q", got)
	}
	b.quiet()

	a.sync(Options{})
	if got := a.read(".codex/AGENTS.md"); got != "codex rules" {
		t.Errorf("a's Codex file = %q; b must not delete it", got)
	}
}

// On a machine whose older sync tool copied a shared skill into a plain
// directory, the directory becomes the shared link when it holds nothing
// but the same files.
func TestOldCopyOfASharedSkillBecomesTheLink(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(skillRel, "skill")
	a.write(".agents/skills/foo/reference.md", "newer file")
	a.link(".claude/skills/foo", "../../.agents/skills/foo")
	a.sync(Options{AdoptLocal: true})

	c := w.machine(platform.Current())
	c.write(".claude/skills/foo/SKILL.md", "skill") // an older, identical subset
	res := c.sync(Options{AdoptLocal: true})
	if len(res.Problems) != 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if fi, err := os.Lstat(c.path(".claude/skills/foo")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the old directory was not replaced by the link: %v", fi)
	}
	if got := c.read(".claude/skills/foo/reference.md"); got != "newer file" {
		t.Errorf("through the link: %q", got)
	}
	if find(res.Actions, Upload, "v1/claude/skills/foo/SKILL.md") != nil {
		t.Error("the old copy's file was uploaded")
	}
	c.quiet()
}

func failingLinks(string, string, bool) (platform.LinkKind, error) {
	return "", platform.ErrLinkUnsupported
}

// Where links cannot be made, a managed copy stands in for the link; it
// follows its target and never overwrites edits made inside it.
func TestManagedCopyWhereLinksCannotBeMade(t *testing.T) {
	w := newWorld(t)
	a := w.machine(platform.Current())
	a.write(skillRel, "v1")
	a.link(".claude/skills/foo", "../../.agents/skills/foo")
	a.sync(Options{AdoptLocal: true})

	win := w.machine(platform.Current())
	win.eng.CreateLink = failingLinks
	win.sync(Options{})
	if fi, err := os.Lstat(win.path(".claude/skills/foo")); err != nil || !fi.IsDir() {
		t.Fatalf("no managed copy: %v, %v", fi, err)
	}
	if got := win.read(".claude/skills/foo/SKILL.md"); got != "v1" {
		t.Errorf("copy = %q", got)
	}
	win.quiet()

	a.write(skillRel, "v2")
	a.sync(Options{})
	win.sync(Options{})
	if got := win.read(".claude/skills/foo/SKILL.md"); got != "v2" {
		t.Errorf("the copy did not follow its target: %q", got)
	}

	win.write(".claude/skills/foo/SKILL.md", "edited in the copy")
	res := win.sync(Options{})
	if len(res.Problems) != 1 {
		t.Errorf("an edit inside the copy should be reported: %+v", res.Problems)
	}
	a.write(skillRel, "v3")
	a.sync(Options{})
	win.sync(Options{})
	if got := win.read(".claude/skills/foo/SKILL.md"); got != "edited in the copy" {
		t.Errorf("the edit inside the copy was overwritten: %q", got)
	}
}
