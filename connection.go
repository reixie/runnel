package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
)

type connectionState struct {
	Connected, Exited, Ended bool
	Code, PID                int
	Error                    string
}

// connection belongs to the GUI. Its Close only detaches. It never owns a PTY.
type connection struct {
	sendMu     sync.Mutex
	sealed     bool
	retired    chan struct{}
	flushed    chan struct{}
	retireOnce sync.Once
	flushOnce  sync.Once
	id         string
	paneID     string
	control    *workspaceConnection
	onState    func()
	c          net.Conn
	term       *terminal.Terminal
	ready      chan struct{}
	out        chan packet
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	state      connectionState
	pending    *bytes.Reader // used only by MyGO's Conn reader
	seq        uint64
}

func attach(c net.Conn, id, paneID, owner string, generation uint64) (*connection, error) {
	welcome, err := negotiate(c, packet{Type: "hello", Version: protocolVersion, Role: "terminal", SessionID: id, PaneID: paneID, Owner: owner, Generation: generation})
	if err != nil {
		return nil, err
	}
	if welcome.SessionID != id || welcome.PaneID != paneID {
		c.Close()
		return nil, fmt.Errorf("attachment session mismatch")
	}
	x := &connection{retired: make(chan struct{}), flushed: make(chan struct{}), id: id, paneID: paneID, c: c, ready: make(chan struct{}), out: make(chan packet, 64), done: make(chan struct{}), state: connectionState{Connected: true, PID: welcome.PID}}
	go x.writer()
	term, err := terminal.New(terminal.Options{Conn: x, ResizeByConn: true, Scrollback: 512 << 10, ImageLimit: -1,
		OnClear: func(terminal.Clear) bool { x.enqueue(packet{Type: "clear"}); return true }})
	if err != nil {
		x.Close()
		return nil, err
	}
	x.term = term
	close(x.ready)
	return x, nil
}
func (x *connection) status() connectionState { x.mu.Lock(); defer x.mu.Unlock(); return x.state }
func (x *connection) notify() {
	x.mu.Lock()
	f := x.onState
	x.mu.Unlock()
	if f != nil {
		f()
	}
}
func (x *connection) setNotifier(f func()) { x.mu.Lock(); x.onState = f; x.mu.Unlock(); x.notify() }
func (x *connection) fail(err error) {
	x.mu.Lock()
	x.state.Connected = false
	if err != nil && !x.state.Ended {
		x.state.Error = err.Error()
	}
	x.mu.Unlock()
	x.notify()
	x.Close()
}
func (x *connection) Close() error { x.once.Do(func() { close(x.done); x.c.Close() }); return nil }
func (x *connection) enqueue(m packet) error {
	x.sendMu.Lock()
	if x.sealed {
		x.sendMu.Unlock()
		return io.ErrClosedPipe
	}
	m.SessionID = x.id
	m.PaneID = x.paneID
	select {
	case <-x.done:
		x.sendMu.Unlock()
		return io.ErrClosedPipe
	default:
	}
	select {
	case x.out <- m:
		x.sendMu.Unlock()
		return nil
	default:
	}
	x.sendMu.Unlock()
	err := fmt.Errorf("connection input queue full")
	x.fail(err)
	return err
}

// Seal against late view callbacks, then acknowledge all already accepted
// mutations on the daemon's FIFO before a replacement attachment can displace
// this peer. The caller never waits on the UI thread.
func (x *connection) retire() <-chan struct{} {
	x.retireOnce.Do(func() {
		x.sendMu.Lock()
		x.sealed = true
		x.sendMu.Unlock()
		go func() {
			defer close(x.retired)
			defer x.detach()
			timer := time.NewTimer(ioTimeout)
			defer timer.Stop()
			select {
			case x.out <- packet{Type: "flush", SessionID: x.id, PaneID: x.paneID}:
			case <-x.done:
				return
			case <-timer.C:
				return
			}
			select {
			case <-x.flushed:
			case <-x.done:
			case <-timer.C:
			}
		}()
	})
	return x.retired
}

func (x *connection) writer() {
	for {
		select {
		case <-x.done:
			return
		case m := <-x.out:
			x.c.SetWriteDeadline(time.Now().Add(ioTimeout))
			if err := writePacket(x.c, m); err != nil {
				x.fail(err)
				return
			}
		}
	}
}
func (x *connection) Write(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		size := min(len(b), maxInput)
		if err := x.enqueue(packet{Type: "input", Data: bytes.Clone(b[:size])}); err != nil {
			return n, err
		}
		n += size
		b = b[size:]
	}
	return n, nil
}
func (x *connection) ResizePixels(c, r, w, h int) error {
	p := packet{Type: "resize", Cols: c, Rows: r, CellW: w, CellH: h}
	if !p.validSize() {
		p.Cols = max(2, min(c, 500))
		p.Rows = max(2, min(r, 300))
		p.CellW = max(1, min(w, 128))
		p.CellH = max(1, min(h, 128))
	}
	return x.enqueue(p)
}
func (x *connection) UseTheme(t *terminal.Theme) error {
	if t == nil {
		return nil
	}
	return x.enqueue(packet{Type: "theme", Theme: t})
}
func (x *connection) Read(b []byte) (int, error) {
	<-x.ready
	for {
		if x.pending != nil && x.pending.Len() > 0 {
			return x.pending.Read(b)
		}
		x.c.SetReadDeadline(time.Now().Add(3 * heartbeat))
		m, err := readPacket(x.c)
		if err != nil {
			x.fail(err)
			return 0, err
		}
		switch m.Type {
		case "frame":
			if (m.SessionID != x.id || m.PaneID != x.paneID) || !m.validSize() || m.Seq != x.seq+1 {
				err := fmt.Errorf("invalid or out-of-order terminal frame")
				x.fail(err)
				return 0, err
			}
			x.seq = m.Seq
			// MyGO calls Read again only after feeding all bytes returned before.
			// Apply each frame's size before any of its bytes, never ahead of a
			// preceding frame. View resize merely requests a daemon resize.
			// Hold presentation while MyGO's 64 KiB reader consumes a complete
			// frame. In particular, a large initial history must never paint half
			// restored. Begin before resize, and release after the final frame byte.
			x.term.Feed([]byte("\x1b[?2026h"))
			x.term.ResizePixels(m.Cols, m.Rows, m.CellW, m.CellH)
			x.mu.Lock()
			changed := x.state.Exited != m.Exited || x.state.Code != m.Code
			x.state.Exited = m.Exited
			x.state.Code = m.Code
			x.state.PID = m.PID
			x.mu.Unlock()
			if len(m.Data) == 0 {
				m.Data = []byte("\x1b[0m")
			}
			if changed {
				x.notify()
			}
			x.pending = bytes.NewReader(append(m.Data, []byte("\x1b[?2026l")...))
		case "flushed":
			if m.SessionID != x.id || m.PaneID != x.paneID {
				x.fail(fmt.Errorf("flush session mismatch"))
				return 0, io.ErrUnexpectedEOF
			}
			x.flushOnce.Do(func() { close(x.flushed) })
		case "ping":
			if m.SessionID != x.id || m.PaneID != x.paneID {
				x.fail(fmt.Errorf("heartbeat pane mismatch"))
				return 0, io.ErrUnexpectedEOF
			}
			x.enqueue(packet{Type: "pong"})
		case "ended":
			if m.SessionID != x.id || m.PaneID != x.paneID {
				x.fail(fmt.Errorf("ended session mismatch"))
				return 0, io.ErrUnexpectedEOF
			}
			x.mu.Lock()
			x.state.Ended = true
			x.state.Connected = false
			x.mu.Unlock()
			x.notify()
			x.Close()
			return 0, io.EOF
		case "error":
			err := fmt.Errorf("daemon: %s", m.Error)
			x.fail(err)
			return 0, err
		default:
			err := fmt.Errorf("unexpected daemon packet %q", m.Type)
			x.fail(err)
			return 0, err
		}
	}
}
func (x *connection) detach() { x.Close(); _ = x.term.Close() }
func (x *connection) end() error {
	if x.control == nil {
		return fmt.Errorf("workspace control unavailable")
	}
	_, err := x.control.call(packet{Type: "end", SessionID: x.id, PaneID: x.paneID})
	return err
}
