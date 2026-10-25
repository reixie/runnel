package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// A catalog connection survives terminal switches. Its requests are serialized
// by a worker, never the UI thread; a lost create response is never retried.
type workspaceConnection struct {
	daemonPID int
	c         net.Conn
	owner     string
	mu        sync.Mutex
	rpc       sync.Mutex
	cat       catalog
	connected bool
	onChange  func()
	out       chan packet
	result    chan packet
	done      chan struct{}
	once      sync.Once
	next      uint64
}

func negotiate(c net.Conn, hello packet) (packet, error) {
	c.SetDeadline(time.Now().Add(ioTimeout))
	if err := writePacket(c, hello); err != nil {
		c.Close()
		return packet{}, err
	}
	p, err := readPacket(c)
	if err == nil && (p.Type != "welcome" || p.Version != protocolVersion) {
		err = fmt.Errorf("Runnel: %s", p.Error)
	}
	if err != nil {
		c.Close()
		return packet{}, err
	}
	c.SetDeadline(time.Time{})
	return p, nil
}
func openWorkspace(ctx context.Context, cfg daemonConfig, start bool) (*workspaceConnection, error) {
	var c net.Conn
	var err error
	if start {
		c, err = connectOrStart(ctx, cfg)
	} else {
		c, err = dialDaemon(cfg)
	}
	if err != nil {
		return nil, err
	}
	return catalogConnection(c)
}
func catalogConnection(c net.Conn) (*workspaceConnection, error) {
	p, err := negotiate(c, packet{Type: "hello", Version: protocolVersion, Role: "catalog"})
	if err != nil {
		return nil, err
	}
	if p.Catalog == nil || p.Owner == "" {
		c.Close()
		return nil, fmt.Errorf("missing workspace catalog")
	}
	w := &workspaceConnection{c: c, owner: p.Owner, daemonPID: p.PID, cat: *p.Catalog, connected: true, out: make(chan packet, 64), result: make(chan packet, 1), done: make(chan struct{})}
	go w.reader()
	go w.writer()
	return w, nil
}
func (w *workspaceConnection) snapshot() (catalog, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cat, w.connected
}
func (w *workspaceConnection) notify() {
	w.mu.Lock()
	f := w.onChange
	w.mu.Unlock()
	if f != nil {
		f()
	}
}
func (w *workspaceConnection) setNotifier(f func()) {
	w.mu.Lock()
	w.onChange = f
	w.mu.Unlock()
	w.notify()
}
func (w *workspaceConnection) close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.connected = false
		w.mu.Unlock()
		close(w.done)
		w.c.Close()
		w.notify()
	})
}
func (w *workspaceConnection) enqueue(p packet) error {
	select {
	case <-w.done:
		return fmt.Errorf("workspace disconnected")
	default:
	}
	select {
	case w.out <- p:
		return nil
	default:
		w.close()
		return fmt.Errorf("workspace request queue full")
	}
}
func (w *workspaceConnection) writer() {
	for {
		select {
		case <-w.done:
			return
		case p := <-w.out:
			w.c.SetWriteDeadline(time.Now().Add(ioTimeout))
			if writePacket(w.c, p) != nil {
				w.close()
				return
			}
		}
	}
}
func (w *workspaceConnection) reader() {
	defer w.close()
	for {
		w.c.SetReadDeadline(time.Now().Add(3 * heartbeat))
		p, err := readPacket(w.c)
		if err != nil {
			return
		}
		switch p.Type {
		case "catalog":
			if p.Catalog == nil {
				return
			}
			w.mu.Lock()
			if p.Catalog.Revision >= w.cat.Revision {
				w.cat = *p.Catalog
			}
			w.mu.Unlock()
			w.notify()
		case "result":
			if p.Catalog != nil {
				w.mu.Lock()
				if p.Catalog.Revision >= w.cat.Revision {
					w.cat = *p.Catalog
				}
				w.mu.Unlock()
				w.notify()
			}
			select {
			case w.result <- p:
			default:
				return
			}
		case "ping":
			if w.enqueue(packet{Type: "pong"}) != nil {
				return
			}
		default:
			return
		}
	}
}
func (w *workspaceConnection) call(p packet) (packet, error) {
	w.rpc.Lock()
	defer w.rpc.Unlock()
	w.next++
	p.Request = w.next
	if err := w.enqueue(p); err != nil {
		return packet{}, err
	}
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	finish := func(r packet) (packet, error) {
		if r.Request != p.Request {
			w.close()
			return r, fmt.Errorf("out-of-order workspace response")
		}
		if r.Error != "" {
			return r, fmt.Errorf("%s", r.Error)
		}
		return r, nil
	}
	select {
	case r := <-w.result:
		return finish(r)
	case <-w.done:
		// A terminal shutdown can close transport immediately after its result.
		// The reader enqueues that result before closing done; preserve the reply.
		select {
		case r := <-w.result:
			return finish(r)
		default:
		}
		return packet{}, fmt.Errorf("workspace disconnected; action outcome may be unknown. Reconnect to reconcile the catalog before trying again")
	case <-timer.C:
		w.close()
		return packet{}, fmt.Errorf("workspace request timed out; reconnect to reconcile the catalog before trying again")
	}
}
