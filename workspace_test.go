package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func callOK(t *testing.T, w *workspaceConnection, p packet) packet {
	t.Helper()
	r, err := w.call(p)
	if err != nil {
		t.Fatalf("%s: %v", p.Type, err)
	}
	if p.Type == "end" {
		waitCatalog(t, w, "ended session removed", func(c catalog) bool {
			for _, s := range c.Sessions {
				if s.ID == p.SessionID {
					return false
				}
			}
			return true
		})
	}
	return r
}
func waitCatalog(t *testing.T, w *workspaceConnection, condition string, check func(catalog) bool) catalog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, ok := w.snapshot()
		if !ok {
			t.Fatalf("workspace disconnected: %s", condition)
		}
		if check(c) {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("catalog timeout %s: %+v", condition, c)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func attachID(t *testing.T, cfg daemonConfig, w *workspaceConnection, id string) *connection {
	t.Helper()
	x, err := attachIDTest(cfg, w, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.detach)
	return x
}
func newHomeFixture(t *testing.T) (daemonConfig, *workspaceConnection) {
	t.Helper()
	x, _ := testSession(t)
	cfg := configFor(x)
	x.detach()
	return cfg, x.control
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func TestProjectsIdentityCancellationAndUnavailableDirectories(t *testing.T) {
	cfg, w := newHomeFixture(t)
	_ = cfg
	base := t.TempDir()
	root := filepath.Join(base, "spaces Žąsis 日本語", "same")
	other := filepath.Join(base, "different", "same")
	for _, path := range []string{root, other} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	first := callOK(t, w, packet{Type: "open-project", Path: root}).ProjectID
	duplicate := callOK(t, w, packet{Type: "open-project", Path: alias}).ProjectID
	second := callOK(t, w, packet{Type: "open-project", Path: other}).ProjectID
	cat, _ := w.snapshot()
	if first != duplicate || first == second || len(cat.Projects) != 2 || len(cat.Sessions) != 1 {
		t.Fatalf("identity or implicit create: %+v", cat)
	}
	if cat.Projects[0].Name != cat.Projects[1].Name {
		t.Fatal("fixture basenames differ")
	}
	a := application{workspace: w, selected: cat.SelectedSession, project: cat.SelectedProject, generation: 4}
	a.openPickedProjects(nil)
	after, _ := w.snapshot()
	if a.generation != 4 || a.project != second || after.Revision != cat.Revision {
		t.Fatal("picker cancellation changed workspace")
	}
	if _, err := w.call(packet{Type: "open-project", Path: filepath.Join(base, "missing")}); err == nil {
		t.Fatal("missing directory accepted")
	}
	noAccess := filepath.Join(base, "no-access")
	os.Mkdir(noAccess, 0000)
	defer os.Chmod(noAccess, 0700)
	if _, err := w.call(packet{Type: "open-project", Path: noAccess}); err == nil {
		t.Fatal("inaccessible directory accepted")
	}
	os.RemoveAll(other)
	if _, err := w.call(packet{Type: "create", ProjectID: second}); err == nil {
		t.Fatal("missing project silently changed cwd")
	}
	os.MkdirAll(other, 0700)
	if _, err := w.call(packet{Type: "create", ProjectID: second}); err == nil {
		t.Fatal("replacement directory identity accepted")
	}
	cat, _ = w.snapshot()
	if len(cat.Sessions) != 1 {
		t.Fatal("invalid creation made a shell")
	}
	callOK(t, w, packet{Type: "remove-project", ProjectID: second})
	if _, err := os.Stat(other); err != nil {
		t.Fatal("remove project deleted files")
	}
}

func TestIndependentProjectsAndHomeAcrossGUIReopening(t *testing.T) {
	cfg, w := newHomeFixture(t)
	cat, _ := w.snapshot()
	homeID := cat.Sessions[0].ID
	home := cat.Sessions[0].InitialDirectory
	base := t.TempDir()
	var projectIDs, ids, dirs []string
	for _, name := range []string{"project one Žąsis", "project two 日本語"} {
		dir := filepath.Join(base, name)
		os.Mkdir(dir, 0700)
		p := callOK(t, w, packet{Type: "open-project", Path: dir}).ProjectID
		r := callOK(t, w, packet{Type: "create", ProjectID: p})
		projectIDs = append(projectIDs, p)
		ids = append(ids, r.SessionID)
		canonical, _ := filepath.EvalSymlinks(dir)
		dirs = append(dirs, canonical)
	}
	ids = append(ids, homeID)
	dirs = append(dirs, home)
	pids := map[int]bool{}
	for i, id := range ids {
		x := attachID(t, cfg, w, id)
		tt := viewFor(x)
		waitText(t, tt, x.term, "runnel-test>")
		pid := x.status().PID
		if pids[pid] {
			t.Fatal("shared shell PID")
		}
		pids[pid] = true
		command(tt, fmt.Sprintf(`export ISOLATED=value%d; sleep 60 & JOB=$!; printf '\nIDENTITY=%%s|%%s|%%s|%%s|%%s\n' $$ "$PWD" "$HOME" "$(/bin/pwd -P)" "$ISOLATED"; (sleep .2; printf '\nOUTPUT_%d\n') &`, i, i))
		physical, _ := filepath.EvalSymlinks(dirs[i])
		waitText(t, tt, x.term, fmt.Sprintf("\nIDENTITY=%d|%s|%s|%s|value%d\n", pid, dirs[i], home, physical, i))
		if i == 0 {
			command(tt, "cd /")
			command(tt, `printf '\nMOVED=%s\n' "$PWD"`)
			waitText(t, tt, x.term, "\nMOVED=/\n")
		}
		x.detach()
	}
	cat, _ = w.snapshot()
	if cat.Sessions[1].ProjectID != projectIDs[0] {
		t.Fatal("cd changed membership")
	}
	callOK(t, w, packet{Type: "select", SessionID: ids[0]})
	// Reopening the GUI means a fresh catalog connection and presentation.
	w.close()
	raw, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := catalogConnection(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	cat, _ = reopened.snapshot()
	if len(cat.Sessions) != 3 || cat.SelectedSession != ids[0] {
		t.Fatalf("reopen lost catalog/selection: %+v", cat)
	}
	for i, id := range ids {
		x := attachID(t, cfg, reopened, id)
		tt := viewFor(x)
		waitText(t, tt, x.term, fmt.Sprintf("\nOUTPUT_%d\n", i))
		command(tt, `kill -0 $JOB && printf '\nRETAINED=%s|%s|%s\n' $$ "$ISOLATED" "$PWD"`)
		cwd := dirs[i]
		if i == 0 {
			cwd = "/"
		}
		waitText(t, tt, x.term, fmt.Sprintf("|value%d|%s\n", i, cwd))
		if !pids[x.status().PID] {
			t.Fatal("shell restarted")
		}
		x.detach()
	}
	if _, err := reopened.call(packet{Type: "remove-project", ProjectID: projectIDs[0]}); err == nil {
		t.Fatal("removed project containing retained session")
	}
}

func TestBackgroundExitRenameFallbackAndFinalSession(t *testing.T) {
	cfg, w := newHomeFixture(t)
	cat, _ := w.snapshot()
	first := cat.Sessions[0].ID
	second := callOK(t, w, packet{Type: "create"}).SessionID
	third := callOK(t, w, packet{Type: "create"}).SessionID
	callOK(t, w, packet{Type: "rename", SessionID: first, Name: "Build Žąsis"})
	x := attachID(t, cfg, w, first)
	tt := viewFor(x)
	command(tt, `printf '\033]2;untrusted title\007'; printf '\nEXIT_SCHEDULED\n'; sleep .2; exit 37`)
	waitText(t, tt, x.term, "\nEXIT_SCHEDULED\n")
	x.detach()
	y := attachID(t, cfg, w, second)
	yt := viewFor(y)
	waitCatalog(t, w, "background status 37", func(c catalog) bool {
		for _, s := range c.Sessions {
			if s.ID == first {
				return s.State == "exited" && s.Panes[0].Code != nil && *s.Panes[0].Code == 37 && s.Name == "Build Žąsis"
			}
		}
		return false
	})
	command(yt, `printf '\nOTHER_USABLE\n'`)
	waitText(t, yt, y.term, "\nOTHER_USABLE\n")
	callOK(t, w, packet{Type: "select", SessionID: third})
	callOK(t, w, packet{Type: "end", SessionID: third})
	cat = waitCatalog(t, w, "deterministic oldest fallback", func(c catalog) bool { return c.SelectedSession == first && len(c.Sessions) == 2 })
	for _, id := range []string{first, second} {
		callOK(t, w, packet{Type: "end", SessionID: id})
	}
	waitCatalog(t, w, "empty workspace", func(c catalog) bool { return len(c.Sessions) == 0 && c.SelectedSession == "" })
	if _, err := attachIDTest(cfg, w, first); err == nil {
		t.Fatal("attach recreated ended shell")
	}
	if _, err := w.call(packet{Type: "end", SessionID: first}); err == nil {
		t.Fatal("ended ID accepted")
	}
	newID := callOK(t, w, packet{Type: "create"}).SessionID
	if newID == first || newID == second || newID == third {
		t.Fatal("recycled session ID")
	}
	z := attachID(t, cfg, w, newID)
	zt := viewFor(z)
	waitText(t, zt, z.term, "runnel-test>")
}

func TestEndIgnoringChildWhileOtherSessionRemainsUsable(t *testing.T) {
	cfg, w := newHomeFixture(t)
	cat, _ := w.snapshot()
	first := cat.Sessions[0].ID
	x := attachID(t, cfg, w, first)
	tt := viewFor(x)
	marker := filepath.Join(t.TempDir(), "child")
	command(tt, `sh -c 'trap "" HUP; echo $$ > `+shellQuote(marker)+`; exec sleep 60' &`)
	waitFor(t, tt, x.term, "child PID", func() bool { b, _ := os.ReadFile(marker); return len(b) > 0 })
	b, _ := os.ReadFile(marker)
	child, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	second := callOK(t, w, packet{Type: "create"}).SessionID
	y := attachID(t, cfg, w, second)
	yt := viewFor(y)
	pid := y.status().PID
	endResult := make(chan error, 1)
	go func() { _, err := w.call(packet{Type: "end", SessionID: first}); endResult <- err }()
	command(yt, `printf '\nCONCURRENTLY_USABLE=%s\n' $$`)
	waitText(t, yt, y.term, fmt.Sprintf("\nCONCURRENTLY_USABLE=%d\n", pid))
	select {
	case err := <-endResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("End Session blocked")
	}
	waitCatalog(t, w, "end cleanup completes", func(c catalog) bool {
		for _, s := range c.Sessions {
			if s.ID == first {
				return false
			}
		}
		return true
	})
	if syscall.Kill(child, 0) != syscall.ESRCH {
		t.Fatalf("ignoring child %d remains", child)
	}
	command(yt, `printf '\nAFTER_END=%s\n' $$`)
	waitText(t, yt, y.term, fmt.Sprintf("\nAFTER_END=%d\n", pid))
	cat, _ = w.snapshot()
	if len(cat.Sessions) != 1 || cat.Sessions[0].ID != second || cat.Sessions[0].State != "running" {
		t.Fatalf("late events damaged survivor: %+v", cat)
	}
}

func TestRapidSwitchTargetingAndDelayedResults(t *testing.T) {
	cfg, w := newHomeFixture(t)
	cat, _ := w.snapshot()
	first := cat.Sessions[0].ID
	second := callOK(t, w, packet{Type: "create"}).SessionID
	x := attachID(t, cfg, w, first)
	xt := viewFor(x)
	command(xt, `printf '\nFIRST_HISTORY\n'; (for n in {1..30}; do print "noise-$n"; sleep .01; done) &`)
	waitText(t, xt, x.term, "\nFIRST_HISTORY\n")
	oldGeneration := nextTestGeneration(w)
	for i := 0; i < 12; i++ {
		id := first
		if i%2 == 0 {
			id = second
		}
		z := attachID(t, cfg, w, id)
		z.ResizePixels(90+i, 28, 9, 17)
		z.detach()
	}
	y := attachID(t, cfg, w, second)
	yt := viewFor(y)
	command(yt, `printf '\nSECOND_HISTORY\n'`)
	waitText(t, yt, y.term, "\nSECOND_HISTORY\n")
	// A delayed older handshake must be rejected before it can displace y.
	raw, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stale, err := attach(raw, first, x.paneID, w.owner, oldGeneration); err == nil {
		stale.detach()
		t.Fatal("stale attachment accepted")
	}
	a := application{workspace: w, selected: second, generation: 100, panes: map[string]*connection{y.paneID: y}}
	a.installAttachment(attachmentRequest{workspace: w, id: first, generation: 99}, x, nil)
	a.installAttachment(attachmentRequest{workspace: w, id: first, generation: 99}, nil, fmt.Errorf("delayed failure"))
	if a.panes[y.paneID] != y || a.message != "" {
		t.Fatal("late result replaced current selection")
	}
	command(yt, `printf '\nRIGHT_INPUT=%s\n' $$`)
	waitText(t, yt, y.term, fmt.Sprintf("\nRIGHT_INPUT=%d\n", y.status().PID))
	yt.Key(ui.Cmd, ui.KeyK)
	waitFor(t, yt, y.term, "targeted clear", func() bool { return !strings.Contains(y.term.Text(), "SECOND_HISTORY") })
	back := attachID(t, cfg, w, first)
	bt := viewFor(back)
	waitText(t, bt, back.term, "\nFIRST_HISTORY\n")
	if strings.Contains(back.term.Text(), "RIGHT_INPUT=") {
		t.Fatal("input crossed sessions")
	}
	callOK(t, w, packet{Type: "end", SessionID: second})
	command(bt, `printf '\nFIRST_STILL_HERE\n'`)
	waitText(t, bt, back.term, "\nFIRST_STILL_HERE\n")
}

func TestVimWhileUsingAnotherSessionAndReopening(t *testing.T) {
	cfg, w := newHomeFixture(t)
	cat, _ := w.snapshot()
	first := cat.Sessions[0].ID
	x := attachID(t, cfg, w, first)
	tt := viewFor(x)
	command(tt, `/usr/bin/vi -u NONE -i NONE -n`)
	tt.Type("iUNSAVED_STAGE3 — Žąsis 日本語")
	tt.Key(0, ui.KeyEscape)
	waitText(t, tt, x.term, "UNSAVED_STAGE3")
	marker := filepath.Join(t.TempDir(), "vim")
	command(tt, ":call writefile([string(getpid())], "+shellQuote(marker)+")")
	waitFor(t, tt, x.term, "Vim PID", func() bool { b, _ := os.ReadFile(marker); return len(b) > 0 })
	original, _ := os.ReadFile(marker)
	second := callOK(t, w, packet{Type: "create"}).SessionID
	y := attachID(t, cfg, w, second)
	yt := viewFor(y)
	command(yt, `printf '\nWORKING_ELSEWHERE\n'`)
	waitText(t, yt, y.term, "\nWORKING_ELSEWHERE\n")
	w.close()
	raw, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	next, err := catalogConnection(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer next.close()
	z := attachID(t, cfg, next, first)
	zt := viewFor(z)
	waitText(t, zt, z.term, "UNSAVED_STAGE3")
	zt.SetSize(1100, 700)
	waitFor(t, zt, z.term, "Vim resize", func() bool { c, _ := z.term.Size(); return c > 100 })
	os.Remove(marker)
	command(zt, ":call writefile([string(getpid()),getline(1)], "+shellQuote(marker)+")")
	waitFor(t, zt, z.term, "same unsaved buffer", func() bool { b, _ := os.ReadFile(marker); return strings.Contains(string(b), "UNSAVED_STAGE3") })
	content, _ := os.ReadFile(marker)
	if !strings.HasPrefix(string(content), string(original)) {
		t.Fatal("Vim process changed")
	}
	command(zt, ":q!")
	waitText(t, zt, z.term, "runnel-test>")
}

func TestRegistryLimitsAndUnknownAttach(t *testing.T) {
	cfg, w := newHomeFixture(t)
	if _, err := attachIDTest(cfg, w, "unknown"); err == nil {
		t.Fatal("unknown attach created shell")
	}
	cat, _ := w.snapshot()
	if len(cat.Sessions) != 1 {
		t.Fatal("attach changed registry")
	}
	for i := 1; i < maxSessions; i++ {
		callOK(t, w, packet{Type: "create"})
	}
	if _, err := w.call(packet{Type: "create"}); err == nil {
		t.Fatal("unbounded retained sessions")
	}
	cat, _ = w.snapshot()
	if len(cat.Sessions) != maxSessions {
		t.Fatal("incorrect registry bound")
	}
	for _, name := range []string{"", strings.Repeat("x", 129), "control\nname"} {
		if _, err := w.call(packet{Type: "rename", SessionID: cat.Sessions[0].ID, Name: name}); err == nil {
			t.Fatal("invalid rename accepted")
		}
	}
	callOK(t, w, packet{Type: "end", SessionID: cat.Sessions[0].ID})
	callOK(t, w, packet{Type: "create"})
}

func TestNoisySessionDoesNotStarveAnotherAndCatalogStaysAvailable(t *testing.T) {
	cfg, w := newHomeFixture(t)
	cat, _ := w.snapshot()
	first := cat.Sessions[0].ID
	second := callOK(t, w, packet{Type: "create"}).SessionID
	x := attachID(t, cfg, w, first)
	tt := viewFor(x)
	marker := filepath.Join(t.TempDir(), "flood-complete")
	started := marker + "-started"
	command(tt, `python3 -c 'import sys,pathlib; pathlib.Path(`+strconv.Quote(started)+`).write_text("started"); sys.stdout.write(("x"*8000+"\n")*1200); sys.stdout.flush(); pathlib.Path(`+strconv.Quote(marker)+`).write_text("done")'`)
	waitFor(t, tt, x.term, "flood started", func() bool { b, _ := os.ReadFile(started); return string(b) == "started" })
	y := attachID(t, cfg, w, second)
	yt := viewFor(y)
	start := time.Now()
	callOK(t, w, packet{Type: "rename", SessionID: first, Name: "Noisy"})
	command(yt, `printf '\nINTERACTION_NOT_STARVED\n'`)
	waitText(t, yt, y.term, "\nINTERACTION_NOT_STARVED\n")
	if time.Since(start) > 3*time.Second {
		t.Fatal("background output starved interaction")
	}
	waitFor(t, yt, y.term, "unselected flood finishes", func() bool { b, _ := os.ReadFile(marker); return string(b) == "done" })
}

func TestSidebarSelectionSurvivesRebuildAndProgrammaticChanges(t *testing.T) {
	_, w := newHomeFixture(t)
	second := callOK(t, w, packet{Type: "create"}).SessionID
	cat, _ := w.snapshot()
	first := cat.Sessions[0].ID
	a := application{workspace: w, cat: cat, selected: first, sidebarSelection: "session:" + first, operations: make(chan func(), 64), attachments: make(chan attachmentRequest, 1)}
	tt := ui.NewTester(a.build, 1000, 650)
	if err := tt.Click("Terminal 2 · running"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != second {
		t.Fatalf("sidebar click lost across build: %s", a.selected)
	}
	if err := tt.Click("Home"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != "" || a.project != "" {
		t.Fatal("group click did not leave empty view")
	}
	a.choose(second, "", false)
	tt.Frame()
	tt.Frame()
	if a.selected != second || a.sidebarSelection != "session:"+second {
		t.Fatal("stale sidebar change replaced programmatic selection")
	}
}

func TestWorkspaceActionQueueSaturationDoesNotDisableCreation(t *testing.T) {
	_, w := newHomeFixture(t)
	a := application{workspace: w, operations: make(chan func(), 1)}
	a.operations <- func() {}
	for range 2 {
		a.newSession()
		if a.creating || a.message == "" {
			t.Fatal("rejected create left creation disabled")
		}
	}
	a.connect(false)
	if a.loading {
		t.Fatal("rejected reconnect left reconnect disabled")
	}
	cat, _ := w.snapshot()
	if len(cat.Sessions) != 1 {
		t.Fatal("saturated action created a duplicate shell")
	}
}

func TestSwitchFlushesAcceptedMutationsAndRejectsLateCallbacks(t *testing.T) {
	cfg, w := newHomeFixture(t)
	cat, _ := w.snapshot()
	first := cat.Sessions[0].ID
	second := callOK(t, w, packet{Type: "create"}).SessionID
	x := attachID(t, cfg, w, first)
	marker := filepath.Join(t.TempDir(), "before-switch")
	if _, err := x.Write([]byte("export BEFORE_SWITCH=retained; echo $$ > " + shellQuote(marker) + "\r")); err != nil {
		t.Fatal(err)
	}
	if err := x.ResizePixels(111, 37, 9, 17); err != nil {
		t.Fatal(err)
	}
	select {
	case <-x.retire():
	case <-time.After(4 * time.Second):
		t.Fatal("switch flush timed out")
	}
	if _, err := x.Write([]byte("LATE_CALLBACK\r")); err == nil {
		t.Fatal("retired view accepted input")
	}
	y := attachID(t, cfg, w, second)
	yt := viewFor(y)
	command(yt, `printf '\nOTHER_VALUE=%s\n' "${BEFORE_SWITCH-unset}"`)
	waitText(t, yt, y.term, "\nOTHER_VALUE=unset\n")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, _ := os.ReadFile(marker); strings.TrimSpace(string(b)) == strconv.Itoa(x.status().PID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted input lost during switch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	z := attachID(t, cfg, w, first)
	for {
		cols, rows := z.term.Size()
		if cols == 111 && rows == 37 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted resize lost during switch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	zt := viewFor(z)
	command(zt, `printf '\nFIRST_VALUE=%s\n' "$BEFORE_SWITCH"`)
	waitText(t, zt, z.term, "\nFIRST_VALUE=retained\n")
}
