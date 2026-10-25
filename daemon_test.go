package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func viewFor(x *connection) *ui.Tester {
	v := &terminalView{term: x.term, conn: x, closeWindow: func() {}}
	return ui.NewTester(v.build, 800, 480)
}
func configFor(x *connection) daemonConfig { c, _ := testConfigs.Load(x); return c.(daemonConfig) }

func TestDetachRetainsShellJobsHistoryAndClear(t *testing.T) {
	x, tt := testSession(t)
	cfg := configFor(x)
	pid := x.status().PID
	command(tt, `export STAGE2_VALUE=retained; sleep 60 & JOB=$!; printf '\nPID=%s JOB=%s\n' $$ $JOB; (sleep .2; printf '\n%s\n' 'detached-output') &`)
	waitText(t, tt, x.term, fmt.Sprintf("\nPID=%d JOB=", pid))
	x.detach()
	time.Sleep(300 * time.Millisecond)
	y := reattachTest(t, cfg)
	yt := viewFor(y)
	waitText(t, yt, y.term, "\ndetached-output\n")
	command(yt, `kill -0 $JOB && printf '\nRETAINED=%s PID=%s\n' "$STAGE2_VALUE" $$`)
	waitText(t, yt, y.term, fmt.Sprintf("\nRETAINED=retained PID=%d\n", pid))
	if y.status().PID != pid {
		t.Fatal("shell replaced")
	}
	command(yt, `for n in {1..100}; do print "history-$n"; done`)
	waitText(t, yt, y.term, "\nhistory-100\n")
	yt.Key(ui.Cmd, ui.KeyK)
	waitFor(t, yt, y.term, "authoritative clear", func() bool { return !strings.Contains(y.term.Text(), "history-1") })
	y.detach()
	z := reattachTest(t, cfg)
	zt := viewFor(z)
	waitText(t, zt, z.term, "runnel-test>")
	if strings.Contains(z.term.Text(), "history-1") || strings.Contains(z.term.Text(), "detached-output") {
		t.Fatal("cleared history returned")
	}
}

func TestVimUnsavedBufferAcrossDetach(t *testing.T) {
	x, tt := testSession(t)
	cfg := configFor(x)
	pid := x.status().PID
	command(tt, `/usr/bin/vi -u NONE -i NONE -n`)
	tt.Type("iUNSAVED Žąsis 日本語")
	tt.Key(0, ui.KeyEscape)
	waitText(t, tt, x.term, "UNSAVED")
	vimPIDFile := filepath.Join(t.TempDir(), "vim-pid")
	command(tt, fmt.Sprintf(":call writefile([string(getpid())], '%s')", vimPIDFile))
	waitFor(t, tt, x.term, "Vim PID written", func() bool { b, _ := os.ReadFile(vimPIDFile); return len(b) > 0 })
	originalVimPID, _ := os.ReadFile(vimPIDFile)
	x.detach()
	y := reattachTest(t, cfg)
	yt := viewFor(y)
	waitText(t, yt, y.term, "UNSAVED")
	beforeC, beforeR := y.term.Size()
	yt.SetSize(1100, 640)
	waitFor(t, yt, y.term, "resized attached Vim", func() bool { c, r := y.term.Size(); return c != beforeC || r != beforeR })
	cols, rows := y.term.Size()
	yt.Key(0, ui.KeyEscape)
	command(yt, fmt.Sprintf(":call writefile([string(getpid())], '%s')", vimPIDFile))
	command(yt, ":echo 'VIM-READY'")
	waitText(t, yt, y.term, "VIM-READY")
	attachedVimPID, _ := os.ReadFile(vimPIDFile)
	if !bytes.Equal(originalVimPID, attachedVimPID) {
		t.Fatal("Vim process changed")
	}
	yt.Key(0, ui.KeyEscape)
	command(yt, fmt.Sprintf(":echo 'PID=' . getpid() . ' GRID=' . &lines . 'x' . &columns"))
	waitText(t, yt, y.term, fmt.Sprintf("GRID=%dx%d", rows, cols))
	command(yt, `:q!`)
	waitText(t, yt, y.term, "runnel-test>")
	command(yt, `printf '\nSHELL=%s\n' $$`)
	waitText(t, yt, y.term, fmt.Sprintf("\nSHELL=%d\n", pid))
}

func TestDisconnectIsNotShellExitAndExitedSessionReattaches(t *testing.T) {
	x, tt := testSession(t)
	cfg := configFor(x)
	pid := x.status().PID
	// Closing the raw transport simulates a broken connection, not a PTY EOF.
	x.c.Close()
	waitFor(t, tt, x.term, "disconnected state", func() bool { return !x.status().Connected })
	if x.status().Exited || !tt.HasText("Disconnected. The session may still be running.") {
		t.Fatal("connection loss reported as exit")
	}
	y := reattachTest(t, cfg)
	yt := viewFor(y)
	command(yt, "exit 23")
	waitFor(t, yt, y.term, "actual exit 23", func() bool { return y.status().Exited })
	if y.status().Code != 23 || !y.status().Connected {
		t.Fatalf("bad shell exit: %+v", y.status())
	}
	y.detach()
	z := reattachTest(t, cfg)
	zt := viewFor(z)
	waitFor(t, zt, z.term, "retained exit", func() bool { return z.status().Exited })
	if z.status().Code != 23 || z.status().PID != pid {
		t.Fatalf("exited session replaced: %+v", z.status())
	}
}

func TestProtocolMismatchDoesNotDisturbSession(t *testing.T) {
	x, tt := testSession(t)
	cfg := configFor(x)
	c, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	if err := writePacket(c, packet{Type: "hello", Version: protocolVersion + 1}); err != nil {
		t.Fatal(err)
	}
	p, err := readPacket(c)
	if err != nil || p.Type != "error" {
		t.Fatalf("mismatch: %+v %v", p, err)
	}
	command(tt, `printf '\n%s\n' 'still-original-session'`)
	waitText(t, tt, x.term, "\nstill-original-session\n")
	if !x.status().Connected {
		t.Fatal("mismatch detached compatible GUI")
	}
}

func TestFramingBoundsAndResizeValidation(t *testing.T) {
	for _, n := range []uint32{0, maxPacket + 1, 0xffffffff} {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], n)
		if _, err := readPacket(bytes.NewReader(h[:])); err == nil {
			t.Fatalf("accepted %d", n)
		}
	}
	for _, p := range []packet{{Cols: 100000, Rows: 24, CellW: 8, CellH: 16}, {Cols: 80, Rows: 24, CellW: 0, CellH: 16}} {
		if p.validSize() {
			t.Fatal("accepted invalid grid")
		}
	}
}

func TestStaleSocketAndLiveSocketSafety(t *testing.T) {
	dir, err := os.MkdirTemp("/private/tmp", "rt-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg := daemonConfig{dir: dir}
	path := socketPath(cfg)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	live, lock, err := listenDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	defer lock.Close()
	if _, _, err := listenDaemon(cfg); err == nil {
		t.Fatal("second listener acquired live lock")
	}
	c, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal("live socket was disturbed:", err)
	}
	c.Close()
}

func TestSlowClientDoesNotBlockShell(t *testing.T) {
	x, tt := testSession(t)
	cfg := configFor(x)
	// A substantial initial snapshot must already exceed the slow socket's
	// buffer. No per-row timers or hundreds of spawned sleeps are involved.
	command(tt, `python3 -c 'import sys; sys.stdout.write(("p"*8000+"\n")*180); sys.stdout.flush()'; printf '\nPREPARED\n'`)
	waitText(t, tt, x.term, "\nPREPARED\n")
	marker := filepath.Join(t.TempDir(), "produced")
	c, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.(*net.UnixConn).SetReadBuffer(1024)
	c.SetDeadline(time.Now().Add(4 * time.Second))
	if err := writePacket(c, packet{Type: "hello", Version: protocolVersion, Role: "terminal", SessionID: x.id, PaneID: x.paneID, Owner: x.control.owner, Generation: nextTestGeneration(x.control)}); err != nil {
		t.Fatal(err)
	}
	if p, e := readPacket(c); e != nil || p.Type != "welcome" {
		t.Fatalf("attach slow peer: %+v %v", p, e)
	}
	attached := time.Now()
	script := fmt.Sprintf(`python3 -c 'import sys,pathlib; sys.stdout.write(("x"*8000+"\n")*180); sys.stdout.flush(); pathlib.Path("%s").write_text("done")' &`+"\r", marker)
	if err := writePacket(c, packet{Type: "input", SessionID: x.id, PaneID: x.paneID, Data: []byte(script)}); err != nil {
		t.Fatal(err)
	}
	controlStart := time.Now()
	callOK(t, x.control, packet{Type: "rename", SessionID: x.id, PaneID: x.paneID, Name: "Stalled stream"})
	second := callOK(t, x.control, packet{Type: "create"}).SessionID
	if time.Since(controlStart) > ioTimeout {
		t.Fatal("stalled terminal blocked workspace control")
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		if b, _ := os.ReadFile(marker); string(b) == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slow GUI stalled output from shell %d", x.status().PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Do not resume reading before the server's bounded write has expired.
	// Observe EOF before any replacement attachment could close this peer.
	if remaining := ioTimeout + 250*time.Millisecond - time.Since(attached); remaining > 0 {
		time.Sleep(remaining)
	}
	c.SetReadDeadline(time.Now().Add(4 * time.Second))
	buf := make([]byte, 65536)
	for {
		_, e := c.Read(buf)
		if e != nil {
			if e, ok := e.(net.Error); ok && e.Timeout() {
				t.Fatal("slow peer was not disconnected by bounded output")
			}
			break
		}
	}
	y := attachID(t, cfg, x.control, second)
	yt := viewFor(y)
	command(yt, `printf '\n%s\n' 'after-slow-client'`)
	waitText(t, yt, y.term, "\nafter-slow-client\n")
	original := attachID(t, cfg, x.control, x.id)
	ot := viewFor(original)
	command(ot, `printf '\nORIGINAL_AFTER_STALL=%s\n' $$`)
	waitText(t, ot, original.term, fmt.Sprintf("\nORIGINAL_AFTER_STALL=%d\n", x.status().PID))

}

func TestHeadlessQueriesPixelsAndTheme(t *testing.T) {
	x, tt := testSession(t)
	if err := x.ResizePixels(80, 24, 9, 17); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, x.term, "pixel resize acknowledgement", func() bool { c, r := x.term.Size(); return c == 80 && r == 24 })
	if err := x.UseTheme(terminal.LightTheme()); err != nil {
		t.Fatal(err)
	}
	// The child reads raw reports, so the GUI cannot accidentally answer them.
	command(tt, `python3 -c 'import os,tty,termios,select; f=0; old=termios.tcgetattr(f); tty.setraw(f); os.write(1,b"\x1b[14t\x1b[16t\x1b[6n\x1b]11;?\x1b\\\x1b[?996n"); r=b""; exec("while select.select([f],[],[],.3)[0]: r+=os.read(f,4096)"); termios.tcsetattr(f,termios.TCSANOW,old); print("\r\nQUERY="+repr(r)+"\r")'`)
	waitText(t, tt, x.term, "QUERY=")
	waitFor(t, tt, x.term, "query replies", func() bool { return strings.Contains(x.term.Text(), "rgb:") && strings.Contains(x.term.Text(), "997;") })
	for _, reply := range []string{`[4;408;720t`, `[6;17;9t`, `[?997;2n`} {
		if !strings.Contains(x.term.Text(), reply) {
			t.Fatalf("missing query reply %q: %s", reply, x.term.Text())
		}
	}
	color := terminal.LightTheme().Background
	rgb := fmt.Sprintf("rgb:%04x/%04x/%04x", uint16(color.R)*257, uint16(color.G)*257, uint16(color.B)*257)
	if !strings.Contains(x.term.Text(), rgb) {
		t.Fatalf("missing propagated theme %s", rgb)
	}
}

func TestConcurrentPackagedStartup(t *testing.T) {
	// This launches the exact bundled binary, without a GUI event loop, in a
	// private test directory. Never use or terminate the real Runnel endpoint.
	bundle, err := filepath.Abs("dist/darwin-" + runtime.GOARCH + "/Runnel.app/Contents/MacOS/Runnel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); err != nil {
		t.Skip("build packaged app first")
	}
	dir, err := os.MkdirTemp("/private/tmp", "rt-process-")
	if err != nil {
		t.Fatal(err)
	}
	cfg := daemonConfig{dir: dir, executable: bundle}
	t.Setenv("MYGO_GHOSTTY_VT", "/missing-inherited-library")
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := connectOrStart(ctx, cfg)
			if err == nil {
				c.Close()
			} else {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	w := testWorkspace(t, cfg)
	cat, _ := w.snapshot()
	if len(cat.Sessions) != 0 {
		t.Fatal("startup implicitly created shells")
	}
	if _, err := w.call(packet{Type: "create"}); err != nil {
		t.Fatal(err)
	}
	x := reattachTest(t, cfg)
	pid := x.status().PID
	x.detach()
	y := reattachTest(t, cfg)
	if y.status().PID != pid {
		t.Fatal("duplicate shell")
	}
	y.detach()
	t.Cleanup(func() {
		w.call(packet{Type: "end", SessionID: x.id})
		syscall.Kill(w.daemonPID, syscall.SIGTERM)
		deadline := time.Now().Add(4 * time.Second)
		for {
			if _, err := os.Stat(socketPath(cfg)); os.IsNotExist(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Error("test daemon shutdown timed out")
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		os.RemoveAll(dir)
	})

	// Confirm a second candidate cannot replace the retained daemon.
	contender := exec.Command(bundle, "--internal-daemon", dir)
	if err := contender.Run(); err == nil {
		t.Error("contender unexpectedly acquired daemon ownership")
	}
}

func TestEndSessionKillsIgnoringBackgroundChildAndAllowsFreshSession(t *testing.T) {
	x, tt := testSession(t)
	cfg := configFor(x)
	original := x.status().PID
	command(tt, `sh -c 'trap "" HUP; printf "\nIGNORING=%s\n" $$; exec sleep 60' &`)
	re := regexp.MustCompile(`\nIGNORING=(\d+)`)
	waitFor(t, tt, x.term, "ignoring background child", func() bool { return re.MatchString(x.term.Text()) })
	child, _ := strconv.Atoi(re.FindStringSubmatch(x.term.Text())[1])
	if err := endAndWait(x); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, x.term, "End Session acknowledgement", func() bool { return x.status().Ended })
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(child, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			t.Fatalf("owned ignoring child %d remains", child)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := x.control.call(packet{Type: "create"}); err != nil {
		t.Fatal(err)
	}
	y := reattachTest(t, cfg)
	yt := viewFor(y)
	waitText(t, yt, y.term, "runnel-test>")
	if y.status().PID == original {
		t.Fatal("End Session reused shell")
	}
}

func TestInputBackpressureDisconnectsWithoutEndingSession(t *testing.T) {
	x, tt := testSession(t)
	cfg := configFor(x)
	pid := x.status().PID
	command(tt, `stty -echo; printf '\nINPUT_BLOCKED\n'; sleep 60`)
	waitText(t, tt, x.term, "\nINPUT_BLOCKED")
	block := bytes.Repeat([]byte("x"), maxInput)
	for range 300 {
		if _, err := x.Write(block); err != nil {
			break
		}
	}
	waitFor(t, tt, x.term, "input backpressure disconnect", func() bool { return !x.status().Connected })
	if x.status().Exited {
		t.Fatal("input backpressure was shell exit")
	}
	y := reattachTest(t, cfg)
	if y.status().PID != pid {
		t.Fatal("backpressure replaced shell")
	}
	if err := y.end(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-y.done:
	case <-time.After(4 * time.Second):
		t.Fatal("End Session blocked on PTY input")
	}
}
