package main

import (
	"context"
	"log"
	"sync"
	"time"
)

// Exactly one terminal/PTY pair per pane. PTY output, input, resize and
// Clear enter the same FIFO, preserving their accepted order. Each pane has
// its own bounded queue and goroutine, so a flood cannot fill another's queue
// or block the registry handling control operations.
type ownedPane struct {
	id        string
	sessionID string
	pane      *pane
	commands  chan daemonEvent
	stop      chan struct{}
	done      chan struct{}
	once      sync.Once
}

func (s *ownedPane) end() { s.once.Do(func() { close(s.stop) }) }
func (s *ownedPane) command(e daemonEvent) bool {
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.commands <- e:
		return true
	default:
		e.peer.close()
		return false
	}
}
func startOwnedPane(ctx context.Context, sessionID, id string, s *pane, emit func(daemonEvent)) *ownedPane {
	o := &ownedPane{id: id, sessionID: sessionID, pane: s, commands: make(chan daemonEvent, 64), stop: make(chan struct{}), done: make(chan struct{})}
	go o.run(ctx, emit)
	return o
}
func (o *ownedPane) run(ctx context.Context, emit func(daemonEvent)) {
	s := o.pane
	// Cancellation also releases a reader blocked behind the bounded output queue.
	readerStop := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		b := make([]byte, 32<<10)
		for {
			n, err := s.proc.Read(b)
			if n > 0 {
				select {
				case o.commands <- daemonEvent{data: append([]byte(nil), b[:n]...)}:
				case <-readerStop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		<-s.proc.Done()
		select {
		case <-drained:
		case <-time.After(100 * time.Millisecond):
		}
		if err := s.proc.Close(); err != nil {
			log.Print(err)
		}
		<-drained
		select {
		case o.commands <- daemonEvent{exit: true, code: s.proc.Code()}:
		case <-readerStop:
		}
	}()
	var active *peer
	var render *time.Timer
	var renderC <-chan time.Time
	closePeer := func() {
		if active != nil {
			active.close()
			active.mirror.Close()
			active = nil
			if render != nil {
				render.Stop()
			}
			renderC = nil
		}
	}
	schedule := func() {
		if active != nil && renderC == nil {
			render = time.NewTimer(16 * time.Millisecond)
			renderC = render.C
		}
	}
	exited := false
	code := 0
	cellW, cellH := 8, 16
	defer func() {
		close(readerStop)
		if render != nil {
			render.Stop()
		}
		if err := s.close(); err != nil {
			log.Print(err)
		}
		<-drained
		<-watcherDone
		// The writer, never this loop or the registry, waits for the socket.
		if active != nil {
			active.mirror.Close()
			ack := make(chan struct{})
			p := active
			p.send(packet{Type: "ended", SessionID: o.sessionID, PaneID: o.id, ack: ack})
			go func() {
				select {
				case <-ack:
				case <-p.done:
				case <-time.After(ioTimeout):
				}
				p.close()
			}()
		}
		emit(daemonEvent{owner: o, state: "removed"})
		close(o.done)
	}()
	handle := func(e daemonEvent) {
		p, m := e.peer, e.msg
		if m.Type == "detach" {
			if active == p {
				closePeer()
			}
			return
		}
		if m.Type == "attach" {
			mirror, err := s.term.NewMirror()
			if err != nil {
				p.send(packet{Type: "error", SessionID: o.sessionID, PaneID: o.id, Error: err.Error()})
				return
			}
			closePeer()
			active = p
			active.mirror = mirror
			p.send(packet{Type: "welcome", Version: protocolVersion, SessionID: o.sessionID, PaneID: o.id, PID: s.proc.PID()})
			schedule()
			return
		}
		if active != p || (m.SessionID != o.sessionID || m.PaneID != o.id) {
			return
		}
		select {
		case <-p.done:
			closePeer()
			return
		default:
		}
		switch m.Type {
		case "flush":
			p.send(packet{Type: "flushed", SessionID: o.sessionID, PaneID: o.id})
		case "input":
			if len(m.Data) == 0 || len(m.Data) > maxInput {
				closePeer()
				return
			}
			if !exited {
				if _, err := s.proc.Write(m.Data); err != nil {
					closePeer()
				}
			}
		case "resize":
			if !m.validSize() {
				closePeer()
				return
			}
			cellW, cellH = m.CellW, m.CellH
			s.term.ResizePixels(m.Cols, m.Rows, m.CellW, m.CellH)
			if !exited {
				_ = s.proc.ResizePixels(m.Cols, m.Rows, m.CellW, m.CellH)
			}
			schedule()
		case "theme":
			if m.Theme == nil {
				closePeer()
				return
			}
			s.term.UseTheme(m.Theme)
			schedule()
		case "clear":
			s.term.ClearScreen()
			schedule()
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.stop:
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-o.stop:
			return
		case e := <-o.commands:
			if e.peer != nil {
				handle(e)
				continue
			}
			if e.exit {
				exited = true
				code = e.code
				emit(daemonEvent{owner: o, state: "exited", code: code})
			} else {
				s.term.Feed(e.data)
			}
			schedule()
		case <-renderC:
			renderC = nil
			if active == nil {
				continue
			}
			select {
			case <-active.done:
				closePeer()
				continue
			default:
			}
			data, due := active.mirror.Frame()
			if due {
				schedule()
				continue
			}
			cols, rows := s.term.Size()
			if len(data) > 0 || exited {
				active.seq++
				if !active.send(packet{Type: "frame", SessionID: o.sessionID, PaneID: o.id, Seq: active.seq, Data: data, Cols: cols, Rows: rows, CellW: cellW, CellH: cellH, Exited: exited, Code: code, PID: s.proc.PID()}) {
					closePeer()
				}
			}
		}
	}
}
