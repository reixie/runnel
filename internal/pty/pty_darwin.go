//go:build darwin

// Package pty is the macOS-only PTY boundary. Opening, sizing and spawning
// follow MyGO v0.6.3 plugins/terminal/internal/pty (MIT). That package is
// internal: its PID, wait status and cleanup cannot be used by a daemon.
package pty

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

type PTY struct {
	master    *os.File
	cmd       *exec.Cmd
	tty       uint32
	root      identity
	done      chan struct{}
	code      int
	closeOnce sync.Once
	input     chan []byte
	stop      chan struct{}
}

func Start(shell, home string, env []string) (*PTY, error) {
	if err := loadProc(); err != nil {
		return nil, err
	}
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*PTY, error) { m.Close(); return nil, err }
	var name [128]byte
	for _, step := range []struct {
		req uintptr
		arg unsafe.Pointer
	}{
		{0x20007454, nil}, {0x20007452, nil}, {0x40807453, unsafe.Pointer(&name[0])},
	} {
		if err := ioctl(m, step.req, step.arg); err != nil {
			return fail(err)
		}
	}
	n := bytes.IndexByte(name[:], 0)
	if n < 0 {
		return fail(fmt.Errorf("unterminated PTY name"))
	}
	slave, err := os.OpenFile(string(name[:n]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	defer slave.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(slave.Fd()), &stat); err != nil {
		return fail(err)
	}
	p := &PTY{master: m, tty: uint32(stat.Rdev), done: make(chan struct{}), input: make(chan []byte, 64), stop: make(chan struct{})}
	if err := p.ResizePixels(80, 24, 8, 16); err != nil {
		return fail(err)
	}
	p.cmd = &exec.Cmd{Path: shell, Args: []string{shell, "-l"}, Dir: home, Env: env, Stdin: slave, Stdout: slave, Stderr: slave,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}}
	if err := p.cmd.Start(); err != nil {
		return fail(err)
	}
	var tracked bool
	p.root, tracked = procIdentity(int32(p.PID()))
	if !tracked {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		m.Close()
		return nil, fmt.Errorf("cannot track the owned shell with libproc")
	}
	go func() {
		err := p.cmd.Wait()
		if e, ok := errors.AsType[*exec.ExitError](err); ok {
			p.code = e.ExitCode()
			if ws, ok := e.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				p.code = 128 + int(ws.Signal())
			}
		} else if err != nil {
			p.code = -1
		}
		close(p.done)
	}()
	go func() {
		for {
			select {
			case <-p.stop:
				return
			case b := <-p.input:
				if _, err := p.master.Write(b); err != nil {
					return
				}
			}
		}
	}()
	return p, nil
}
func (p *PTY) PID() int              { return p.cmd.Process.Pid }
func (p *PTY) Done() <-chan struct{} { return p.done }
func (p *PTY) Code() int             { <-p.done; return p.code }
func (p *PTY) Read(b []byte) (int, error) {
	n, e := p.master.Read(b)
	if n == 0 && (errors.Is(e, syscall.EIO) || errors.Is(e, os.ErrClosed)) {
		e = io.EOF
	}
	return n, e
}

// Never block the emulator (including terminal query replies) on an unreading
// child. Saturation is reported to the GUI, which disconnects; it is not exit.
func (p *PTY) Write(b []byte) (int, error) {
	if len(b) > 32<<10 {
		return 0, fmt.Errorf("PTY input too large")
	}
	select {
	case <-p.stop:
		return 0, io.ErrClosedPipe
	default:
	}
	select {
	case p.input <- bytes.Clone(b):
		return len(b), nil
	default:
		return 0, fmt.Errorf("PTY input queue full")
	}
}
func (p *PTY) ResizePixels(cols, rows, w, h int) error {
	ws := struct{ Row, Col, X, Y uint16 }{uint16(rows), uint16(cols), uint16(cols * w), uint16(rows * h)}
	return ioctl(p.master, syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}
func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	c, e := f.SyscallConn()
	if e != nil {
		return e
	}
	var errno syscall.Errno
	e = c.Control(func(fd uintptr) { _, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)) })
	if e != nil {
		return e
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// libproc identities include start timestamps: a PID recycled during cleanup
// must never be signalled. Only processes with this private controlling PTY
// are owned; a process that deliberately leaves the terminal is outside scope.
type identity struct {
	Flags, Status, XStatus, PID, PPID, UID, GID, RUID, RGID, SVUID, SVGID, Reserved uint32
	Comm                                                                            [16]byte
	Name                                                                            [32]byte
	NFiles, PGID, JobC, TTY, TPGID                                                  uint32
	Nice                                                                            int32
	StartSec, StartUSec                                                             uint64
}

var procAPI struct {
	once sync.Once
	list func(uint32, uint32, unsafe.Pointer, int32) int32
	info func(int32, int32, uint64, unsafe.Pointer, int32) int32
	err  error
}

func loadProc() error {
	procAPI.once.Do(func() {
		h, e := purego.Dlopen("/usr/lib/libproc.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if e != nil {
			procAPI.err = e
			return
		}
		purego.RegisterLibFunc(&procAPI.list, h, "proc_listpids")
		purego.RegisterLibFunc(&procAPI.info, h, "proc_pidinfo")
	})
	return procAPI.err
}
func procIdentity(pid int32) (identity, bool) {
	var i identity
	if loadProc() != nil {
		return i, false
	}
	n := procAPI.info(pid, 3, 0, unsafe.Pointer(&i), int32(unsafe.Sizeof(i)))
	return i, n == int32(unsafe.Sizeof(i))
}
func (p *PTY) owned() []identity {
	if loadProc() != nil {
		return nil
	}
	var ids [16384]int32
	n := procAPI.list(3, p.tty, unsafe.Pointer(&ids[0]), int32(unsafe.Sizeof(ids)))
	var out []identity
	for _, pid := range ids[:max(0, min(int(n)/4, len(ids)))] {
		if i, ok := procIdentity(pid); ok && i.TTY == p.tty && i.UID == uint32(os.Getuid()) {
			out = append(out, i)
		}
	}
	return out
}
func sameProcess(i identity) bool {
	now, ok := procIdentity(int32(i.PID))
	return ok && now.StartSec == i.StartSec && now.StartUSec == i.StartUSec && now.Status != 5 // SZOMB
}
func (p *PTY) Close() error {
	var result error
	p.closeOnce.Do(func() {
		owned := p.owned()
		// The session leader is always ours even if it has already lost its tty.
		if p.root.PID != 0 {
			owned = append(owned, p.root)
		}
		close(p.stop)
		for _, i := range owned {
			if sameProcess(i) {
				_ = syscall.Kill(int(i.PID), syscall.SIGHUP)
				_ = syscall.Kill(int(i.PID), syscall.SIGCONT)
			}
		}
		result = p.master.Close()
		deadline := time.Now().Add(2 * time.Second)
		grace := time.Now().Add(400 * time.Millisecond)
		for {
			alive := false
			for _, i := range owned {
				if sameProcess(i) {
					alive = true
					if time.Now().After(grace) {
						_ = syscall.Kill(int(i.PID), syscall.SIGKILL)
					}
				}
			}
			if !alive {
				break
			}
			if time.Now().After(deadline) {
				result = fmt.Errorf("owned PTY processes did not exit within two seconds")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		select {
		case <-p.done:
		case <-time.After(500 * time.Millisecond):
			result = fmt.Errorf("shell was not reaped")
		}
	})
	return result
}
