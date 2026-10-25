package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func sessionFrom(t *testing.T, w *workspaceConnection, id string) sessionInfo {
	t.Helper()
	cat, _ := w.snapshot()
	for _, s := range cat.Sessions {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("missing session %s", id)
	return sessionInfo{}
}
func layoutCall(t *testing.T, w *workspaceConnection, id string, p packet) packet {
	t.Helper()
	p.SessionID = id
	p.Revision = sessionFrom(t, w, id).LayoutRevision
	return callOK(t, w, p)
}
func attachPane(t *testing.T, cfg daemonConfig, w *workspaceConnection, sid, pid string) *connection {
	t.Helper()
	raw, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	x, err := attach(raw, sid, pid, w.owner, nextTestGeneration(w))
	if err != nil {
		t.Fatal(err)
	}
	x.control = w
	t.Cleanup(x.detach)
	return x
}
func fourPanes(t *testing.T) (daemonConfig, *workspaceConnection, sessionInfo, []*connection) {
	t.Helper()
	cfg, w := newHomeFixture(t)
	root := t.TempDir()
	project := callOK(t, w, packet{Type: "open-project", Path: root}).ProjectID
	created := callOK(t, w, packet{Type: "create", ProjectID: project})
	id := created.SessionID
	a := created.PaneID
	b := layoutCall(t, w, id, packet{Type: "split", PaneID: a, Axis: "right", Ratio: .5}).PaneID
	layoutCall(t, w, id, packet{Type: "split", PaneID: a, Axis: "down", Ratio: .5})
	layoutCall(t, w, id, packet{Type: "split", PaneID: b, Axis: "down", Ratio: .5})
	s := sessionFrom(t, w, id)
	if err := validateLayout(s.Layout, s.Panes, s.FocusedPane); err != nil {
		t.Fatal(err)
	}
	var xs []*connection
	for _, pid := range s.Layout.leaves() {
		xs = append(xs, attachPane(t, cfg, w, id, pid))
	}
	return cfg, w, s, xs
}
func waitFile(t *testing.T, path string, check func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		if check(string(b)) {
			return string(b)
		}
		if time.Now().After(deadline) {
			t.Fatalf("file timeout %s: %s", path, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func send(t *testing.T, x *connection, s string) {
	t.Helper()
	if _, err := x.Write([]byte(s + "\r")); err != nil {
		t.Fatal(err)
	}
}
func drainApp(a *application) {
	for {
		select {
		case f := <-a.operations:
			f()
		default:
			return
		}
	}
}

func TestFourPaneSocketsIdentityGridsFocusPasteClearAndDividers(t *testing.T) {
	_, w, s, xs := fourPanes(t)
	pids, ttys := map[int]bool{}, map[string]bool{}
	dir := t.TempDir()
	for i, x := range xs {
		if pids[x.status().PID] || x.paneID == s.ID {
			t.Fatal("shared identity")
		}
		pids[x.status().PID] = true
		marker := filepath.Join(dir, fmt.Sprint(i))
		send(t, x, fmt.Sprintf(`export PANE_VALUE=value%d; printf '%%s\n%%s\n%%s\n%%s\n' $$ "$PANE_VALUE" "$PWD" "$HOME" > %s; tty >> %s`, i, shellQuote(marker), shellQuote(marker)))
		text := waitFile(t, marker, func(v string) bool { return strings.Contains(v, "/dev/ttys") })
		lines := strings.Split(strings.TrimSpace(text), "\n")
		if lines[0] != strconv.Itoa(x.status().PID) || lines[1] != fmt.Sprintf("value%d", i) || lines[2] != s.InitialDirectory {
			t.Fatal(text)
		}
		if ttys[lines[4]] {
			t.Fatal("shared PTY")
		}
		ttys[lines[4]] = true
	}
	a := &application{workspace: w, cat: func() catalog { c, _ := w.snapshot(); return c }(), selected: s.ID, project: s.ProjectID, sidebarSelection: "session:" + s.ID, panes: map[string]*connection{}, operations: make(chan func(), 64), focusedPane: xs[0].paneID, focusRequest: xs[0].paneID}
	for _, x := range xs {
		a.panes[x.paneID] = x
	}
	// Exercise the real recursive MyGO view and its persistent focus handles.
	tt := ui.NewTester(a.build, 1200, 800)
	pump := func() { drainApp(a); a.cat, _ = w.snapshot(); tt.Frame() }
	for _, x := range xs {
		waitFor(t, tt, x.term, "pane grid fits view", func() bool { pump(); c, r := x.term.Size(); return c < 80 && c >= 2 && r >= 2 })
	}
	for i, x := range xs {
		label := fmt.Sprintf("Pane %d · running", i+1)
		if a.focusedPane == x.paneID {
			label = "● " + label
		}
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		pump()
		command(tt, fmt.Sprintf("export TARGETED_%d=yes", i))
		pump()
		marker := filepath.Join(dir, fmt.Sprintf("target%d", i))
		tt.SetClipboard(fmt.Sprintf(`printf '%%s %%s\n' "$PANE_VALUE" "${TARGETED_%d-unset}" > %s`, i, shellQuote(marker)))
		tt.Key(ui.Cmd, ui.KeyV)
		tt.Key(0, ui.KeyEnter)
		waitFile(t, marker, func(v string) bool { return v == fmt.Sprintf("value%d yes\n", i) })
		if a.focusedPane != x.paneID {
			t.Fatal("pointer focus not remembered")
		}
		// Clear targets the focused pane; other screens retain distinct markers.
		send(t, x, fmt.Sprintf("printf '\\nHISTORY_PANE_%d\\n'", i))
		waitFor(t, tt, x.term, "history", func() bool { pump(); return strings.Contains(x.term.Text(), fmt.Sprintf("HISTORY_PANE_%d", i)) })
	}
	tt.Command("selectAll")
	tt.Command("copy")
	if !strings.Contains(tt.Clipboard(), "HISTORY_PANE_3") || strings.Contains(tt.Clipboard(), "HISTORY_PANE_0") {
		t.Fatal("Copy crossed pane focus")
	}
	a.focusPane(xs[1].paneID, true)
	a.focusRequest = xs[1].paneID
	pump()
	tt.Key(ui.Cmd, ui.KeyK)
	waitFor(t, tt, xs[1].term, "pane Clear", func() bool { pump(); return !strings.Contains(xs[1].term.Text(), "HISTORY_PANE_1") })
	for _, i := range []int{0, 2, 3} {
		if !strings.Contains(xs[i].term.Text(), fmt.Sprintf("HISTORY_PANE_%d", i)) {
			t.Fatal("Clear crossed pane")
		}
	}
	a.cyclePane(1)
	pump()
	if a.focusedPane != xs[2].paneID {
		t.Fatal("next visual pane")
	}
	a.cyclePane(-1)
	pump()
	if a.focusedPane != xs[1].paneID {
		t.Fatal("previous visual pane")
	}
	// A modal owns focus even when a sibling gets rebuilt.
	a.beginRename()
	pump()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("RENAME_ONLY")
	pump()
	late := a.panes[xs[3].paneID]
	delete(a.panes, xs[3].paneID)
	pump()
	a.installPaneAttachment(attachmentRequest{workspace: w, id: s.ID, generation: a.generation}, xs[3].paneID, late, nil)
	pump()
	tt.Type("_AFTER_ATTACH")
	pump()
	if a.name != "RENAME_ONLY_AFTER_ATTACH" {
		t.Fatalf("rename focus stolen: %q", a.name)
	}
	a.renaming = false
	// Move a real MyGO divider, then ensure every affected PTY receives its grid.

	var bounds ui.Rect
	originalBuild := a.build
	tt = ui.NewTester(func(c *ui.Context) {
		originalBuild(c)
		if state := a.splits[s.Layout.ID]; state != nil {
			bounds = state.handle.Bounds(c)
		}
	}, 1200, 800)
	// New tester is a new window, handles have per-window bindings.
	tt.Frame()
	if bounds.W <= 0 {
		tt.Frame()
	}
	dividerX := bounds.X + bounds.W/2
	dividerY := bounds.Y + bounds.H/2
	tt.Press(dividerX, dividerY)
	tt.Move(dividerX+100, dividerY)
	tt.Release(dividerX+100, dividerY)
	tt.Advance(100 * time.Millisecond)
	pump()
	state := a.splits[s.Layout.ID]
	if state.percent <= 50 {
		t.Fatalf("divider did not move: %f bounds %+v", state.percent, bounds)
	}
	for _, size := range [][2]int{{1400, 900}, {600, 450}, {1200, 800}} {
		tt.SetSize(size[0], size[1])
		pump()
		for i, x := range xs {
			marker := filepath.Join(dir, fmt.Sprintf("grid%d-%d", i, size[0]))
			send(t, x, "stty size > "+shellQuote(marker))
			waitFile(t, marker, func(v string) bool { c, r := x.term.Size(); return strings.TrimSpace(v) == fmt.Sprintf("%d %d", r, c) })
		}
	}
	for i, x := range xs {
		c, r := x.term.Size()
		if c < 2 || r < 2 || !x.status().Connected {
			t.Fatalf("invalid pane %d %dx%d", i, c, r)
		}
	}
}

func TestPaneCloseExitOwnershipRevisionsAndNonblockingCleanup(t *testing.T) {
	cfg, w, s, xs := fourPanes(t)
	dir := t.TempDir()
	childFile := filepath.Join(dir, "child")
	send(t, xs[0], `sh -c 'trap "" HUP; echo $$ > `+shellQuote(childFile)+`; exec sleep 60' &`)
	child, _ := strconv.Atoi(strings.TrimSpace(waitFile(t, childFile, func(v string) bool { return v != "" })))
	layoutCall(t, w, s.ID, packet{Type: "focus", PaneID: xs[2].paneID})
	old := sessionFrom(t, w, s.ID)
	before, _ := json.Marshal(old.Layout)
	for _, p := range []packet{
		{Type: "split", PaneID: "missing", Axis: "right", Ratio: .5},
		{Type: "close-pane", PaneID: "missing"}, {Type: "focus", PaneID: "missing"},
		{Type: "ratio", NodeID: old.Layout.ID, Ratio: 0},
		{Type: "ratio", NodeID: old.Layout.First.ID, Ratio: 1},
		{Type: "split", PaneID: xs[1].paneID, Axis: "down", Ratio: .5},
	} {
		p.SessionID = s.ID
		p.Revision = old.LayoutRevision
		if _, err := w.call(p); err == nil {
			t.Fatal("invalid mutation accepted", p.Type)
		}
	}
	if _, err := w.call(packet{Type: "focus", SessionID: s.ID, PaneID: xs[1].paneID, Revision: old.LayoutRevision - 1}); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err := w.call(packet{Type: "focus", SessionID: s.ID, PaneID: xs[1].paneID, Revision: 0}); err == nil {
		t.Fatal("zero revision accepted")
	}
	current := sessionFrom(t, w, s.ID)
	after, _ := json.Marshal(current.Layout)
	if string(before) != string(after) || old.LayoutRevision != current.LayoutRevision {
		t.Fatal("failure changed layout")
	}
	// Close a nonfocused pane; acknowledgement is initiation, cleanup is async.
	started := time.Now()
	layoutCall(t, w, s.ID, packet{Type: "close-pane", PaneID: xs[0].paneID})
	during := sessionFrom(t, w, s.ID)
	if len(during.Panes) != 4 || len(during.Layout.leaves()) != 3 || during.FocusedPane != xs[2].paneID {
		t.Fatal("close lost retained slot/focus")
	}
	if err := validateLayout(during.Layout, during.Panes, during.FocusedPane); err != nil {
		t.Fatal(err)
	}
	if _, err := w.call(packet{Type: "split", SessionID: s.ID, PaneID: xs[1].paneID, Axis: "down", Ratio: .5, Revision: during.LayoutRevision}); err == nil {
		t.Fatal("ending pane released slot early")
	}
	callOK(t, w, packet{Type: "rename", SessionID: s.ID, Name: "Cleanup remains interactive"})
	if time.Since(started) > 350*time.Millisecond {
		t.Fatal("catalog waited for cleanup")
	}
	marker := filepath.Join(dir, "survivor")
	send(t, xs[2], "echo $$ > "+shellQuote(marker))
	waitFile(t, marker, func(v string) bool { return strings.TrimSpace(v) == strconv.Itoa(xs[2].status().PID) })
	waitCatalog(t, w, "closed pane removed", func(c catalog) bool { return len(sessionFromNoFail(c, s.ID).Panes) == 3 })
	if syscall.Kill(child, 0) != syscall.ESRCH {
		t.Fatal("owned ignoring child survived")
	}
	// One broken stream does not retire any sibling or report shell exit.
	xs[1].c.Close()
	deadline := time.Now().Add(time.Second)
	for xs[1].status().Connected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if xs[1].status().Exited || !xs[2].status().Connected || !xs[3].status().Connected {
		t.Fatal("broken connection affected lifecycle/sibling")
	}
	replacement := attachPane(t, cfg, w, s.ID, xs[1].paneID)
	if replacement.status().PID != xs[1].status().PID {
		t.Fatal("broken pane restarted")
	}
	send(t, replacement, "exit 37")
	waitCatalog(t, w, "actual pane exit", func(c catalog) bool {
		q := sessionFromNoFail(c, s.ID)
		for _, p := range q.Panes {
			if p.ID == replacement.paneID {
				return p.State == "exited" && p.Code != nil && *p.Code == 37 && q.State == "running"
			}
		}
		return false
	})
	for i, x := range xs[2:] {
		send(t, x, fmt.Sprintf("exit %d", i+11))
	}
	waitCatalog(t, w, "all retained panes exited", func(c catalog) bool { return sessionFromNoFail(c, s.ID).State == "exited" })
	retained := sessionFrom(t, w, s.ID)
	for _, p := range retained.Panes {
		if p.Code == nil {
			t.Fatal("missing individual exit code")
		}
	}
	for _, pid := range retained.Layout.leaves() {
		layoutCall(t, w, s.ID, packet{Type: "close-pane", PaneID: pid})
		waitCatalog(t, w, "pane cleanup", func(c catalog) bool {
			for _, p := range sessionFromNoFail(c, s.ID).Panes {
				if p.ID == pid {
					return false
				}
			}
			return true
		})
	}
	callOK(t, w, packet{Type: "create", ProjectID: s.ProjectID})
}
func sessionFromNoFail(c catalog, id string) sessionInfo {
	for _, s := range c.Sessions {
		if s.ID == id {
			return s
		}
	}
	return sessionInfo{}
}

func TestFourPaneVimRetirementAndReopening(t *testing.T) {
	cfg, w, s, xs := fourPanes(t)
	dir := t.TempDir()
	vim := filepath.Join(dir, "vim")
	job := filepath.Join(dir, "job")
	send(t, xs[0], `/usr/bin/vi -u NONE -i NONE -n '+call setline(1, "UNSAVED_STAGE4 — Žąsis 日本語")' '+set modified' '+call writefile([string(getpid())], "`+vim+`")'`)
	original := waitFile(t, vim, func(v string) bool { return v != "" })
	send(t, xs[1], "sleep 60 & JOB=$!; echo $JOB > "+shellQuote(job))
	waitFile(t, job, func(v string) bool { return v != "" })
	layoutCall(t, w, s.ID, packet{Type: "ratio", NodeID: s.Layout.ID, Ratio: .63})
	layoutCall(t, w, s.ID, packet{Type: "focus", PaneID: xs[0].paneID})
	for i, x := range xs {
		if err := x.ResizePixels(60+i*10, 18+i*3, 8, 16); err != nil {
			t.Fatal(err)
		}
	}
	// Accepted input, resize, and Clear survive simultaneous retirement.
	input := filepath.Join(dir, "accepted")
	send(t, xs[2], "export BEFORE_SWITCH=kept; echo $$ > "+shellQuote(input))
	xs[3].enqueue(packet{Type: "clear"})
	retired := joinRetirements(nil, xs...)
	select {
	case <-retired:
	case <-time.After(4 * time.Second):
		t.Fatal("group retirement exceeded bound")
	}
	for _, x := range xs {
		if _, err := x.Write([]byte("LATE_CALLBACK\r")); err == nil {
			t.Fatal("late pane callback accepted")
		}
	}
	other := callOK(t, w, packet{Type: "create"}).SessionID
	y := attachID(t, cfg, w, other)
	send(t, y, "echo working > "+shellQuote(filepath.Join(dir, "working")))
	waitFile(t, filepath.Join(dir, "working"), func(v string) bool { return v == "working\n" })
	// Late results and failures from a retired pane cannot replace a current one.
	a := application{workspace: w, selected: other, generation: 90, panes: map[string]*connection{y.paneID: y}}
	a.installPaneAttachment(attachmentRequest{workspace: w, id: s.ID, generation: 89}, xs[0].paneID, xs[0], nil)
	a.installPaneAttachment(attachmentRequest{workspace: w, id: s.ID, generation: 89}, xs[1].paneID, nil, fmt.Errorf("late failure"))
	if len(a.panes) != 1 || a.message != "" {
		t.Fatal("late pane result changed selection")
	}
	callOK(t, w, packet{Type: "select", SessionID: s.ID})
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
	restored := sessionFrom(t, next, s.ID)
	if restored.Layout.Ratio != .63 || restored.FocusedPane != xs[0].paneID || !reflect.DeepEqual(restored.Layout.leaves(), s.Layout.leaves()) {
		t.Fatal("reopening lost layout/focus")
	}
	var zs []*connection
	for i, x := range xs {
		z := attachPane(t, cfg, next, s.ID, x.paneID)
		zs = append(zs, z)
		if z.status().PID != x.status().PID {
			t.Fatal("pane shell restarted")
		}
		if err := z.ResizePixels(65+i, 22+i, 8, 16); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(dir, "vim-restored")
	send(t, zs[0], `:call writefile([string(getpid()),string(&lines)."x".string(&columns),getline(1),string(&modified)], "`+state+`")`)
	content := waitFile(t, state, func(v string) bool { return strings.Contains(v, "UNSAVED_STAGE4") })
	if !strings.HasPrefix(content, original) || !strings.Contains(content, "22x65\n") || !strings.HasSuffix(content, "\n1\n") {
		t.Fatal(content)
	}
	send(t, zs[1], "kill -0 $JOB && echo $JOB > "+shellQuote(job+"-after"))
	afterJob := waitFile(t, job+"-after", func(v string) bool { return v != "" })
	beforeJob, _ := os.ReadFile(job)
	if afterJob != string(beforeJob) {
		t.Fatal("job replaced")
	}
	waitFile(t, input, func(v string) bool { return strings.TrimSpace(v) == strconv.Itoa(zs[2].status().PID) })
	send(t, zs[0], ":q!")
}

func TestOwnershipMismatchAndFullPaneCapacityReattachment(t *testing.T) {
	cfg, w, s, xs := fourPanes(t)
	// The attachment itself is ownership-scoped, independent of leaf position.
	raw, _ := dialDaemon(cfg)
	if _, err := attach(raw, s.ID, xs[0].paneID, "wrong-owner", nextTestGeneration(w)); err == nil {
		t.Fatal("foreign owner accepted")
	}
	other := callOK(t, w, packet{Type: "create"}).SessionID
	raw, _ = dialDaemon(cfg)
	if _, err := attach(raw, other, xs[0].paneID, w.owner, nextTestGeneration(w)); err == nil {
		t.Fatal("foreign pane accepted")
	}
	// A command may not spoof a sibling, even on a valid attached socket.
	if err := writePacket(xs[0].c, packet{Type: "clear", SessionID: s.ID, PaneID: xs[1].paneID}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for xs[0].status().Connected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if xs[0].status().Connected || !xs[1].status().Connected {
		t.Fatal("mismatch not isolated")
	}
	// Fill all 64 retained slots with four-pane sessions; no eviction.
	cat, _ := w.snapshot()
	total := 0
	for _, q := range cat.Sessions {
		total += len(q.Panes)
	}
	for total < maxTotalPanes {
		if len(sessionFrom(t, w, other).Panes) >= maxSessionPanes {
			other = callOK(t, w, packet{Type: "create"}).SessionID
			total++
			continue
		}
		q := sessionFrom(t, w, other)
		layoutCall(t, w, other, packet{Type: "split", PaneID: q.FocusedPane, Axis: "right", Ratio: .5})
		total++
	}
	if _, err := w.call(packet{Type: "create"}); err == nil {
		t.Fatal("total pane limit ignored")
	}
	send(t, xs[1], "exit 37")
	waitCatalog(t, w, "exited pane still retained at capacity", func(c catalog) bool {
		for _, p := range sessionFromNoFail(c, s.ID).Panes {
			if p.ID == xs[1].paneID {
				return p.State == "exited"
			}
		}
		return false
	})
	if _, err := w.call(packet{Type: "create"}); err == nil {
		t.Fatal("exited pane released retained capacity")
	}

	q := sessionFrom(t, w, other)
	if len(q.Panes) < 4 {
		if _, err := w.call(packet{Type: "split", SessionID: other, PaneID: q.FocusedPane, Axis: "down", Ratio: .5, Revision: q.LayoutRevision}); err == nil {
			t.Fatal("total pane split overflow")
		}
	}
	// Catalog + four attachments remain possible at retained capacity, including
	// transient retiring streams. The scheduler retires the whole previous group.
	select {
	case <-joinRetirements(nil, xs...):
	case <-time.After(4 * time.Second):
		t.Fatal("retire")
	}
	var all []*connection
	for _, pid := range s.Layout.leaves() {
		all = append(all, attachPane(t, cfg, w, s.ID, pid))
	}
	for _, x := range all {
		if !x.status().Connected {
			t.Fatal("full retained layout cannot attach")
		}
	}
	// End Session initiates all four cleanups together and leaves catalog free.
	started := time.Now()
	callOK(t, w, packet{Type: "end", SessionID: s.ID})
	if time.Since(started) > 3*time.Second {
		t.Fatal("serialized pane cleanup")
	}
	callOK(t, w, packet{Type: "create"})
}

func TestSlowPaneIsolatesSiblingsAndCatalog(t *testing.T) {
	cfg, w, s, xs := fourPanes(t)
	x := xs[0]
	dir := t.TempDir()
	prepared := filepath.Join(dir, "prepared")
	send(t, x, `python3 -c 'import sys;sys.stdout.write(("p"*8000+"\n")*180);sys.stdout.flush()'; echo yes > `+shellQuote(prepared))
	waitFile(t, prepared, func(v string) bool { return v == "yes\n" })
	c, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.(*net.UnixConn).SetReadBuffer(1024)
	c.SetDeadline(time.Now().Add(ioTimeout))
	writePacket(c, packet{Type: "hello", Version: protocolVersion, Role: "terminal", SessionID: s.ID, PaneID: x.paneID, Owner: w.owner, Generation: nextTestGeneration(w)})
	welcome, err := readPacket(c)
	if err != nil || welcome.Type != "welcome" {
		t.Fatal(welcome, err)
	}
	attached := time.Now()
	produced := filepath.Join(dir, "produced")
	script := `python3 -c 'import sys,pathlib;sys.stdout.write(("x"*8000+"\n")*1200);sys.stdout.flush();pathlib.Path("` + produced + `").write_text("done")'` + "\r"
	if err := writePacket(c, packet{Type: "input", SessionID: s.ID, PaneID: x.paneID, Data: []byte(script)}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	callOK(t, w, packet{Type: "rename", SessionID: s.ID, Name: "Slow pane"})
	for i, z := range xs[1:] {
		marker := filepath.Join(dir, fmt.Sprint(i))
		send(t, z, "echo $$ > "+shellQuote(marker))
		waitFile(t, marker, func(v string) bool { return strings.TrimSpace(v) == strconv.Itoa(z.status().PID) })
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("noisy pane starved siblings/catalog")
	}
	waitFile(t, produced, func(v string) bool { return v == "done" })
	if remaining := ioTimeout + 100*time.Millisecond - time.Since(attached); remaining > 0 {
		time.Sleep(remaining)
	}
	c.SetReadDeadline(time.Now().Add(ioTimeout))
	buf := make([]byte, 65536)
	for {
		_, err := c.Read(buf)
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("slow pane not disconnected")
			}
			break
		}
	}
	for _, z := range xs[1:] {
		if !z.status().Connected {
			t.Fatal("slow pane disconnected sibling")
		}
	}
	z := attachPane(t, cfg, w, s.ID, x.paneID)
	if z.status().PID != x.status().PID {
		t.Fatal("slow shell restarted")
	}
}

func TestFailedSplitDirectoryAndSpawnLeaveLayoutUntouched(t *testing.T) {
	cfg, w := newHomeFixture(t)
	root := filepath.Join(t.TempDir(), "project")
	os.Mkdir(root, 0700)
	project := callOK(t, w, packet{Type: "open-project", Path: root}).ProjectID
	id := callOK(t, w, packet{Type: "create", ProjectID: project}).SessionID
	before := sessionFrom(t, w, id)
	os.Rename(root, root+"-moved")
	os.Mkdir(root, 0700)
	if _, err := w.call(packet{Type: "split", SessionID: id, PaneID: before.FocusedPane, Axis: "right", Ratio: .5, Revision: before.LayoutRevision}); err == nil {
		t.Fatal("replaced initial directory accepted")
	}
	after := sessionFrom(t, w, id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed split changed retained session")
	}
	original := attachPane(t, cfg, w, id, before.FocusedPane)
	marker := filepath.Join(t.TempDir(), "alive")
	send(t, original, "echo alive > "+shellQuote(marker))
	waitFile(t, marker, func(v string) bool { return v == "alive\n" })
	// A valid directory but failing spawn also leaves the exact tree untouched.
	home := t.TempDir()
	prof, _ := makeProfile("tester", home, "/bin/zsh", []string{"ZDOTDIR=" + home})
	calls := 0
	cfg3 := daemonConfig{}
	// Replace only the injected test configuration before starting a new daemon.
	cfg3.profile = func() (profile, error) {
		calls++
		p := prof
		if calls > 1 {
			p.shell = "/missing-runnel-shell"
		}
		return p, nil
	}
	// testDaemon owns its own server; this second fixture needs the override at start.
	dir, _ := os.MkdirTemp("/private/tmp", "rt-fail-spawn-")
	defer os.RemoveAll(dir)
	cfg3.dir = dir
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go func() { done <- serveDaemon(ctx, cfg3, ready) }()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("cleanup timeout")
		}
	}()
	c, _ := dialDaemon(cfg3)
	control, err := catalogConnection(c)
	if err != nil {
		t.Fatal(err)
	}
	defer control.close()
	created := callOK(t, control, packet{Type: "create"})
	initial := sessionFrom(t, control, created.SessionID)
	if _, err := control.call(packet{Type: "split", SessionID: initial.ID, PaneID: initial.FocusedPane, Axis: "down", Ratio: .5, Revision: initial.LayoutRevision}); err == nil {
		t.Fatal("failed exec accepted")
	}
	if !reflect.DeepEqual(initial, sessionFrom(t, control, initial.ID)) {
		t.Fatal("spawn failure changed layout")
	}
}

func TestShutdownOnlyVerifiedCompatibleEmptyRuntime(t *testing.T) {
	cfg, w := newHomeFixture(t)
	if _, err := w.call(packet{Type: "shutdown-empty"}); err == nil {
		t.Fatal("shutdown live panes")
	}
	cat, _ := w.snapshot()
	for _, s := range cat.Sessions {
		callOK(t, w, packet{Type: "end", SessionID: s.ID})
	}
	// Use a separate packaged daemon, since testDaemon owns cancellation/done.
	t.Setenv("RUNNEL_INTERNAL_RUNTIME", cfg.dir)
	if err := shutdownEmptyRuntime(); err != nil {
		t.Fatal(err)
	}
	// The runtime lock/socket are removed by this same daemon's ordinary defer.
	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(socketPath(cfg))
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("empty daemon remained")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDividerCoalescingFinalRatioAndRetiredReply(t *testing.T) {
	_, w, s, _ := fourPanes(t)
	a := application{workspace: w, cat: func() catalog { c, _ := w.snapshot(); return c }(), selected: s.ID, generation: 8, operations: make(chan func(), 64), splits: map[string]*splitPresentation{}}
	state := &splitPresentation{percent: 60, dirty: true, serial: 1}
	a.splits[s.Layout.ID] = state
	a.sendRatio(s.Layout.ID, state)
	state.percent = 73
	state.serial++
	state.dirty = true
	a.sendRatio(s.Layout.ID, state)
	// Complete the earlier reply after the newer local drag has already happened.
	(<-a.operations)()
	if state.percent != 73 || !state.pending {
		t.Fatal("delayed reply restored obsolete ratio")
	}
	(<-a.operations)()
	if state.pending || state.dirty || sessionFrom(t, w, s.ID).Layout.Ratio != .73 {
		t.Fatal("final divider ratio lost")
	}
	state.percent = 64
	state.serial++
	state.dirty = true
	a.flushRatios() // Selection change must submit the final ratio before retiring.
	a.selected = "another-session"
	a.generation++
	(<-a.operations)()
	if sessionFrom(t, w, s.ID).Layout.Ratio != .64 || state.percent != 64 {
		t.Fatal("retired reply undid accepted divider mutation")
	}
}

func TestFourAttachmentsWithCatalogAndHandshakeHeadroom(t *testing.T) {
	cfg, w, s, xs := fourPanes(t)
	select {
	case <-joinRetirements(nil, xs...):
	case <-time.After(4 * time.Second):
		t.Fatal("retirement")
	}
	var handshakes []net.Conn
	for range 3 {
		c, err := dialDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		handshakes = append(handshakes, c)
		defer c.Close()
	}
	// All eight slots: one catalog, three bounded handshakes, four live panes.
	for _, id := range s.Layout.leaves() {
		x := attachPane(t, cfg, w, s.ID, id)
		if !x.status().Connected {
			t.Fatal("layout cannot attach with reserved headroom")
		}
	}
	callOK(t, w, packet{Type: "rename", SessionID: s.ID, Name: "Eight sockets"})
}

func TestEndSessionCleansFourIgnoringPanesConcurrently(t *testing.T) {
	cfg, w, s, xs := fourPanes(t)
	dir := t.TempDir()
	var children []int
	for i, x := range xs {
		marker := filepath.Join(dir, fmt.Sprint(i))
		send(t, x, `sh -c 'trap "" HUP; echo $$ > `+shellQuote(marker)+`; exec sleep 60' &`)
		pid, _ := strconv.Atoi(strings.TrimSpace(waitFile(t, marker, func(v string) bool { return v != "" })))
		children = append(children, pid)
	}
	start := time.Now()
	if _, err := w.call(packet{Type: "end", SessionID: s.ID}); err != nil {
		t.Fatal(err)
	}
	other := callOK(t, w, packet{Type: "create"}).SessionID
	y := attachID(t, cfg, w, other)
	marker := filepath.Join(dir, "other")
	send(t, y, "echo alive > "+shellQuote(marker))
	waitFile(t, marker, func(v string) bool { return v == "alive\n" })
	waitCatalog(t, w, "parallel pane cleanup", func(c catalog) bool { return sessionFromNoFail(c, s.ID).ID == "" })
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatal("pane cleanup serialized")
	}
	for _, pid := range children {
		if syscall.Kill(pid, 0) != syscall.ESRCH {
			t.Fatal("owned child survived", pid)
		}
	}
}
