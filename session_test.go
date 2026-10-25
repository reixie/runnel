package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// These files belong only to a temporary test account home, never the user's.
func testSession(t *testing.T) (*connection, *ui.Tester) {
	t.Helper()
	home := t.TempDir()
	for name, content := range map[string]string{
		".zprofile": "export RUNNEL_PROFILE=loaded\n",
		".zshrc":    "PS1='runnel-test> '\nRPS1=''\nHISTFILE=/dev/null\nunsetopt PROMPT_SP\n",
	} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := makeProfile("tester", home, "/bin/zsh", []string{"ZDOTDIR=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, done := testDaemon(t, p)
	w := testWorkspace(t, cfg)
	if _, err := w.call(packet{Type: "create"}); err != nil {
		t.Fatal(err)
	}
	s, err := attachTest(cfg, w)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.detach)
	testOwners.Store(s, done)
	testConfigs.Store(s, cfg)
	v := terminalView{conn: s, term: s.term, closeWindow: func() {}}
	tt := ui.NewTester(v.build, 800, 480)
	waitText(t, tt, s.term, "runnel-test>")
	return s, tt
}

func waitFor(t *testing.T, tt *ui.Tester, term *terminal.Terminal, condition string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s\n%s", condition, term.Text())
		}
		time.Sleep(10 * time.Millisecond) // PTY I/O uses the real clock.
		tt.Frame()
	}
	tt.Frame()
}

func waitText(t *testing.T, tt *ui.Tester, term *terminal.Terminal, text string) {
	t.Helper()
	waitFor(t, tt, term, text, func() bool { return strings.Contains(term.Text(), text) })
}

func command(tt *ui.Tester, text string) { tt.Type(text); tt.Key(0, ui.KeyEnter) }

func TestLoginPTYAndInput(t *testing.T) {
	s, tt := testSession(t)
	command(tt, `printf '\nLOGIN=%s INTERACTIVE=%s PROFILE=%s\n' $options[login] $options[interactive] "$RUNNEL_PROFILE"; /bin/pwd; /usr/bin/tty`)
	waitText(t, tt, s.term, "\nLOGIN=on INTERACTIVE=on PROFILE=loaded\n")
	waitText(t, tt, s.term, "\n/dev/ttys")
	command(tt, `[[ "$PWD" == "$HOME" ]] && printf '\n%s\n' 'HOME_OK'`)
	waitText(t, tt, s.term, "\nHOME_OK\n")
}

func TestViewResizeSignalsPTY(t *testing.T) {
	s, tt := testSession(t)
	for _, size := range [][2]int{{640, 360}, {1050, 650}} {
		beforeC, beforeR := s.term.Size()
		tt.SetSize(size[0], size[1])
		waitFor(t, tt, s.term, "resize acknowledgement", func() bool { c, r := s.term.Size(); return c != beforeC || r != beforeR })
		cols, rows := s.term.Size()
		command(tt, `printf '\nSIZE:'; stty size`)
		waitText(t, tt, s.term, fmt.Sprintf("\nSIZE:%d %d\n", rows, cols))
	}
}

func TestFullScreenVimResizeAndReturn(t *testing.T) {
	s, tt := testSession(t)
	file := filepath.Join(t.TempDir(), "tui.txt")
	var text strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&text, "row-%03d — Žąsis 日本語\n", i)
	}
	if err := os.WriteFile(file, []byte(text.String()), 0600); err != nil {
		t.Fatal(err)
	}
	command(tt, "/usr/bin/vi -u NONE -i NONE -n --cmd 'set shortmess+=F' '"+file+"'")
	waitFor(t, tt, s.term, "Vim alternate screen", func() bool {
		return strings.Contains(s.term.Text(), "row-001") && !strings.Contains(s.term.Text(), "runnel-test>")
	})
	beforeC, beforeR := s.term.Size()
	tt.SetSize(1000, 600)
	waitFor(t, tt, s.term, "Vim resize acknowledgement", func() bool { c, r := s.term.Size(); return c != beforeC || r != beforeR })
	cols, rows := s.term.Size()
	command(tt, `:echo 'GRID=' . &lines . 'x' . &columns`)
	waitText(t, tt, s.term, fmt.Sprintf("GRID=%dx%d", rows, cols))
	command(tt, ":q!")
	waitText(t, tt, s.term, "runnel-test>")
	command(tt, `printf '\n%s\n' 'returned-from-vim'`)
	waitText(t, tt, s.term, "\nreturned-from-vim\n")
}

func TestControlKeysAndJobControl(t *testing.T) {
	s, tt := testSession(t)
	command(tt, `sh -c 'printf "\nJOB_%s\n" STARTED; exec sleep 30'`)
	waitText(t, tt, s.term, "\nJOB_STARTED")
	tt.Key(ui.Ctrl, ui.KeyZ)
	waitText(t, tt, s.term, "suspended")
	command(tt, "fg")
	waitText(t, tt, s.term, "continued")
	tt.Key(ui.Ctrl, ui.KeyC)
	command(tt, `printf '\nINTERRUPTED=%s\n' "$?"`)
	waitText(t, tt, s.term, "\nINTERRUPTED=130\n")
	tt.Key(ui.Ctrl, ui.KeyD)
	waitFor(t, tt, s.term, "Ctrl+D exits shell", func() bool {
		select {
		case <-shellDone(s):
			return true
		default:
			return false
		}
	})
	if !tt.HasText("Shell exited (status 0).") {
		t.Fatalf("missing exit state: %v", tt.Texts())
	}
}

func TestUnicodeClipboardAndMultilinePaste(t *testing.T) {
	s, tt := testSession(t)
	// A bracketed multiline paste stays in the editor until Enter.
	tt.SetClipboard("printf '\\n%s\\n' 'Žąsis 日本語 👩🏽‍💻'\nprintf '\\n%s\\n' 'paste-finished'")
	tt.Key(ui.Cmd, ui.KeyV)
	if strings.Contains(s.term.Text(), "\npaste-finished\n") {
		t.Fatal("paste executed before Enter")
	}
	tt.Key(0, ui.KeyEnter)
	waitText(t, tt, s.term, "\nŽąsis 日本語 👩🏽‍💻\n")
	waitText(t, tt, s.term, "\npaste-finished\n")
	tt.Command("selectAll")
	tt.Command("copy")
	if !strings.Contains(tt.Clipboard(), "Žąsis 日本語 👩🏽‍💻") {
		t.Fatalf("copy lost Unicode: %q", tt.Clipboard())
	}
	// Scroll and return to input: the terminal must retain keyboard focus.
	command(tt, `for n in {1..150}; do print "scroll-$n"; done`)
	waitText(t, tt, s.term, "\nscroll-150\n")
	tt.Scroll(100, 100, 0, -250)
	tt.Key(ui.Shift, ui.KeyPageUp)
	command(tt, `printf '\n%s\n' 'after-scroll'`)
	waitText(t, tt, s.term, "\nafter-scroll\n")
}

func TestExitAndCloseButton(t *testing.T) {
	s, tt := testSession(t)
	closed := false
	v := terminalView{conn: s, term: s.term, closeWindow: func() { closed = true }}
	tt = ui.NewTester(v.build, 800, 480)
	command(tt, "exit 7")
	waitFor(t, tt, s.term, "exit status", func() bool {
		select {
		case <-shellDone(s):
			return true
		default:
			return false
		}
	})
	if !tt.HasText("Shell exited (status 7).") {
		t.Fatalf("missing exit status: %v", tt.Texts())
	}
	if err := tt.Click("Close Window"); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("close button did not call owner")
	}
}

func TestDeferredStartFailure(t *testing.T) {
	cfg, _ := testDaemon(t, profile{shell: "/bin/zsh", home: "/missing-runnel", env: os.Environ()})
	w := testWorkspace(t, cfg)
	if _, err := w.call(packet{Type: "create"}); err == nil {
		t.Fatal("accepted invalid shell directory")
	}
	cat, _ := w.snapshot()
	if len(cat.Sessions) != 0 {
		t.Fatal("failed create retained a session")
	}
}

func TestCloseReapsHUPIgnoringProcess(t *testing.T) {
	s, tt := testSession(t)
	command(tt, `trap '' HUP; printf '\nOWNED_PID=%s\n' $$; exec /bin/sleep 30`)
	re := regexp.MustCompile(`\nOWNED_PID=(\d+)\n`)
	waitFor(t, tt, s.term, "owned PID", func() bool { return re.MatchString(s.term.Text()) })
	pid, _ := strconv.Atoi(re.FindStringSubmatch(s.term.Text())[1])
	start := time.Now()
	if err := endAndWait(s); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("cleanup exceeded deadline")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("owned process %d remains: %v", pid, err)
	}
	if err := endAndWait(s); err != nil {
		t.Fatal(err)
	}
}

func TestCloseCleansForegroundAndBackgroundJobs(t *testing.T) {
	s, tt := testSession(t)
	command(tt, `sleep 60 & printf '\nBACKGROUND=%s\n' $!; sh -c 'printf "\nFOREGROUND=%s\n" $$; exec sleep 60'`)
	re := regexp.MustCompile(`\n(?:BACKGROUND|FOREGROUND)=(\d+)`)
	waitFor(t, tt, s.term, "both owned jobs", func() bool { return len(re.FindAllStringSubmatch(s.term.Text(), -1)) == 2 })
	var pids []int
	for _, match := range re.FindAllStringSubmatch(s.term.Text(), -1) {
		pid, _ := strconv.Atoi(match[1])
		pids = append(pids, pid)
	}
	if err := endAndWait(s); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for _, pid := range pids {
		for syscall.Kill(pid, 0) != syscall.ESRCH {
			if time.Now().After(deadline) {
				t.Fatalf("owned job %d remains after close", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCloseBeforeFirstFrame(t *testing.T) {
	s, _ := testSession(t)
	s.detach()
	s.detach()
}

var testOwners sync.Map
var testConfigs sync.Map

func shellDone(s *connection) <-chan struct{} {
	done := make(chan struct{})
	if s.status().Exited {
		close(done)
	}
	return done
}
func endAndWait(s *connection) error {
	if s.status().Ended {
		return nil
	}
	if err := s.end(); err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(4 * time.Second):
		return fmt.Errorf("end timed out")
	}
}

var testWorkspaces sync.Map
var testGenerations sync.Map

func testWorkspace(t *testing.T, cfg daemonConfig) *workspaceConnection {
	t.Helper()
	if value, ok := testWorkspaces.Load(cfg.dir); ok {
		return value.(*workspaceConnection)
	}
	c, err := dialDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w, err := catalogConnection(c)
	if err != nil {
		t.Fatal(err)
	}
	testWorkspaces.Store(cfg.dir, w)
	t.Cleanup(func() { w.close(); testWorkspaces.Delete(cfg.dir); testGenerations.Delete(w) })
	return w
}
func nextTestGeneration(w *workspaceConnection) uint64 {
	v, _ := testGenerations.LoadOrStore(w, &atomic.Uint64{})
	return v.(*atomic.Uint64).Add(1)
}
func attachTest(cfg daemonConfig, w *workspaceConnection) (*connection, error) {
	cat, _ := w.snapshot()
	return attachIDTest(cfg, w, cat.SelectedSession)
}
func attachIDTest(cfg daemonConfig, w *workspaceConnection, id string) (*connection, error) {
	raw, err := dialDaemon(cfg)
	if err != nil {
		return nil, err
	}
	paneID := "unknown"
	cat, _ := w.snapshot()
	for _, s := range cat.Sessions {
		if s.ID == id {
			paneID = s.FocusedPane
		}
	}
	x, err := attach(raw, id, paneID, w.owner, nextTestGeneration(w))
	if err == nil {
		x.control = w
	}
	return x, err
}

func testDaemon(t *testing.T, p profile) (daemonConfig, chan error) {
	t.Helper()
	dir, err := os.MkdirTemp("/private/tmp", "rt-")
	if err != nil {
		t.Fatal(err)
	}
	cfg := daemonConfig{dir: dir, profile: func() (profile, error) { return p, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go func() { done <- serveDaemon(ctx, cfg, ready) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("daemon shutdown timed out")
		}
		os.RemoveAll(dir)
	})
	if err := <-ready; err != nil {
		t.Fatal(err)
	}

	return cfg, done
}
func reattachTest(t *testing.T, cfg daemonConfig) *connection {
	t.Helper()
	x, err := attachTest(cfg, testWorkspace(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.detach)
	return x
}
