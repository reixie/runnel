package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
)

type daemonConfig struct {
	dir        string
	executable string
	profile    func() (profile, error)
}

func defaultConfig() daemonConfig {
	dir := filepath.Join("/private/tmp", "runnel-"+strconv.Itoa(os.Getuid()))
	if v := os.Getenv("RUNNEL_INTERNAL_RUNTIME"); v != "" {
		dir = v
	}
	return daemonConfig{dir: dir, profile: func() (profile, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return loginProfile(ctx, os.Environ())
	}}
}
func privateDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("runtime directory must be absolute")
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	i, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok || !i.IsDir() || i.Mode().Perm() != 0700 || st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("unsafe Runnel runtime directory: %s (requires owner-only 0700)", dir)
	}
	if len(filepath.Join(dir, "session.sock")) > 100 {
		return fmt.Errorf("runtime socket path is too long")
	}
	return nil
}
func socketPath(cfg daemonConfig) string { return filepath.Join(cfg.dir, "session.sock") }
func dialDaemon(cfg daemonConfig) (net.Conn, error) {
	i, err := os.Lstat(socketPath(cfg))
	if err != nil {
		return nil, err
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok || i.Mode()&os.ModeSocket == 0 || i.Mode().Perm() != 0600 || st.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("unsafe Runnel socket")
	}
	return net.DialTimeout("unix", socketPath(cfg), ioTimeout)
}

// Each candidate takes the same kernel lock before binding or creating a
// shell. The lock file is permanent; unlinking a locked file breaks exclusion.
func listenDaemon(cfg daemonConfig) (*net.UnixListener, *os.File, error) {
	if err := privateDir(cfg.dir); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(filepath.Join(cfg.dir, "daemon.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, nil, err
	}
	fail := func(e error) (*net.UnixListener, *os.File, error) { f.Close(); return nil, nil, e }
	i, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 || st.Uid != uint32(os.Getuid()) {
		return fail(fmt.Errorf("unsafe daemon lock"))
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(err)
	}
	// A responding socket is never removed, even if its owner uses a different
	// protocol or does not participate in our lock convention.
	if c, e := net.DialTimeout("unix", socketPath(cfg), 300*time.Millisecond); e == nil {
		c.Close()
		return fail(fmt.Errorf("a daemon already listens at %s", socketPath(cfg)))
	} else if !errors.Is(e, syscall.ENOENT) && !errors.Is(e, syscall.ECONNREFUSED) {
		return fail(e)
	}
	if i, e := os.Lstat(socketPath(cfg)); e == nil {
		st, ok := i.Sys().(*syscall.Stat_t)
		if !ok || i.Mode()&os.ModeSocket == 0 || st.Uid != uint32(os.Getuid()) {
			return fail(fmt.Errorf("refusing to remove unsafe stale socket"))
		}
		if e := os.Remove(socketPath(cfg)); e != nil {
			return fail(e)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return fail(e)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath(cfg), Net: "unix"})
	if err != nil {
		return fail(err)
	}
	l.SetUnlinkOnClose(false)
	if err := os.Chmod(socketPath(cfg), 0600); err != nil {
		l.Close()
		os.Remove(socketPath(cfg))
		return fail(err)
	}
	return l, f, nil
}

func connectOrStart(ctx context.Context, cfg daemonConfig) (net.Conn, error) {
	if err := privateDir(cfg.dir); err != nil {
		return nil, err
	}
	if c, e := dialDaemon(cfg); e == nil {
		return c, nil
	} else if !errors.Is(e, os.ErrNotExist) && !errors.Is(e, syscall.ECONNREFUSED) {
		return nil, e
	}
	executable, err := os.Executable()
	if cfg.executable != "" {
		executable = cfg.executable
		err = nil
	}
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, "--internal-daemon", cfg.dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// No inherited terminal or GUI descriptors. No log file that can grow forever.
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	for {
		if c, e := dialDaemon(cfg); e == nil {
			return c, nil
		} else if !errors.Is(e, os.ErrNotExist) && !errors.Is(e, syscall.ECONNREFUSED) {
			return nil, e
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("daemon startup: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

type peer struct {
	c                 net.Conn
	sessionID, paneID string
	out               chan packet
	catalog           chan packet
	done              chan struct{}
	once              sync.Once
	mirror            *terminal.Mirror // actor only
	seq               uint64
}

func newPeer(c net.Conn) *peer {
	return &peer{c: c, out: make(chan packet, 2), catalog: make(chan packet, 1), done: make(chan struct{})}
}
func (p *peer) close() { p.once.Do(func() { close(p.done); p.c.Close() }) }
func (p *peer) send(m packet) bool {
	select {
	case <-p.done:
		return false
	default:
	}
	select {
	case p.out <- m:
		return true
	default:
		p.close()
		return false
	}
}

// Catalogs are replaceable snapshots; bursts of independent pane exits must
// not disconnect a healthy catalog reader. Correlated results stay FIFO.
func (p *peer) sendCatalog(m packet) {
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case <-p.catalog:
	default:
	}
	select {
	case p.catalog <- m:
	default:
	}
}
func (p *peer) writer() {
	defer p.close()
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		var m packet
		select {
		case <-p.done:
			return
		case m = <-p.out:
		case m = <-p.catalog:
		case <-tick.C:
			m = packet{Type: "ping", SessionID: p.sessionID, PaneID: p.paneID}
		}
		p.c.SetWriteDeadline(time.Now().Add(ioTimeout))
		if err := writePacket(p.c, m); err != nil {
			return
		}
		if m.ack != nil {
			close(m.ack)
		}
	}
}
