package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// A different endpoint identifies GUI ownership. No process-name matching:
// daemon and GUI intentionally share the same executable name.
func registerGUI() (func(), error) {
	cfg := defaultConfig()
	if err := privateDir(cfg.dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(cfg.dir, "gui.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("Runnel GUI is already open")
	}
	path := filepath.Join(cfg.dir, "gui.sock")
	if i, e := os.Lstat(path); e == nil {
		if i.Mode()&os.ModeSocket == 0 {
			f.Close()
			return nil, fmt.Errorf("unsafe GUI socket")
		}
		if e = os.Remove(path); e != nil {
			f.Close()
			return nil, e
		}
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		l.Close()
		f.Close()
		return nil, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.SetDeadline(time.Now().Add(ioTimeout))
			p, err := readPacketLimit(c, 4096)
			stop := err == nil && p.Type == "stop-gui" && p.Version == guiControlVersion
			if err == nil && (p.Type == "probe" || stop) {
				_ = writePacket(c, packet{Type: "gui", Version: guiControlVersion})
			}
			c.Close()
			if stop {
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			}
		}
	}()
	return func() { l.Close(); f.Close() }, nil
}
func guiControl(stop bool) error {
	cfg := defaultConfig()
	if err := privateDir(cfg.dir); err != nil {
		return err
	}
	c, err := net.DialTimeout("unix", filepath.Join(cfg.dir, "gui.sock"), time.Second)
	if err != nil {
		if stop && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)) {
			return nil
		}
		return err
	}
	c.SetDeadline(time.Now().Add(ioTimeout))
	action := "probe"
	if stop {
		action = "stop-gui"
	}
	err = writePacket(c, packet{Type: action, Version: guiControlVersion})
	if err == nil {
		var p packet
		p, err = readPacket(c)
		if err == nil && p.Type != "gui" {
			err = fmt.Errorf("unexpected GUI control response")
		}
	}
	c.Close()
	if err != nil || !stop {
		return err
	}
	f, err := os.OpenFile(filepath.Join(cfg.dir, "gui.lock"), os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("GUI did not detach within five seconds")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
