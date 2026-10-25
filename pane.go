package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/reixie/runnel/internal/pty"
)

// The daemon alone owns these. The terminal never has a View or a GUI loop.
type pane struct {
	term *terminal.Terminal
	proc *pty.PTY
	conn *queryConn
	once sync.Once
	err  error
}

// MyGO answers terminal queries into this connection; output is fed by the
// daemon's ordered event loop. In particular, it never labels PTY EOF as a
// transport disconnect, nor fabricates a shell status for a remote terminal.
type queryConn struct {
	proc *pty.PTY
	done chan struct{}
	once sync.Once
}

func (c *queryConn) Read([]byte) (int, error)    { <-c.done; return 0, io.EOF }
func (c *queryConn) Write(b []byte) (int, error) { return c.proc.Write(b) }
func (c *queryConn) Close() error                { c.once.Do(func() { close(c.done) }); return nil }

func newPane(p profile, dir string) (*pane, error) {
	if _, _, err := directory(dir); err != nil {
		return nil, err
	}
	if err := terminal.Load(); err != nil {
		return nil, err
	}
	env := slices.DeleteFunc(slices.Clone(p.env), func(e string) bool { return strings.HasPrefix(e, "PWD=") })
	env = append(env, "PWD="+dir)
	proc, err := pty.Start(p.shell, dir, env)
	if err != nil {
		return nil, err
	}
	conn := &queryConn{proc: proc, done: make(chan struct{})}
	term, err := terminal.New(terminal.Options{Conn: conn, ResizeByConn: true, Scrollback: 512 << 10, ImageLimit: -1, Theme: terminal.DarkTheme()})
	if err != nil {
		proc.Close()
		return nil, err
	}
	return &pane{term: term, proc: proc, conn: conn}, nil
}
func (s *pane) close() error {
	s.once.Do(func() {
		s.err = s.proc.Close()
		if e := s.term.Close(); e != nil && s.err == nil {
			s.err = fmt.Errorf("close terminal: %w", e)
		}
	})
	return s.err
}
