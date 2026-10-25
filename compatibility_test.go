package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStage2EndpointIsNotReplaced(t *testing.T) { testOldEndpoint(t, 2) }
func TestStage3EndpointIsNotReplaced(t *testing.T) { testOldEndpoint(t, 3) }
func testOldEndpoint(t *testing.T, version int) {
	dir, err := os.MkdirTemp("/private/tmp", "rt-v2-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg := daemonConfig{dir: dir, executable: "/must-not-start-a-replacement"}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath(cfg), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	os.Chmod(socketPath(cfg), 0600)
	observed := make(chan packet, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(ioTimeout))
		p, _ := readPacket(c)
		observed <- p
		writePacket(c, packet{Type: "error", Version: version, Error: "Incompatible Runnel protocol; existing session left running"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if w, err := openWorkspace(ctx, cfg, true); err == nil {
		w.close()
		t.Fatal("accepted Stage 2 daemon")
	} else if !strings.Contains(err.Error(), "Incompatible") {
		t.Fatal(err)
	}
	select {
	case p := <-observed:
		if p.Version != protocolVersion {
			t.Fatal("wrong negotiation")
		}
	case <-time.After(ioTimeout):
		t.Fatal("no negotiation")
	}
	c, err := net.DialTimeout("unix", socketPath(cfg), time.Second)
	if err != nil {
		t.Fatal("live Stage 2 socket disturbed", err)
	}
	c.Close()
}

func TestGUIControlKeepsItsStage2Version(t *testing.T) {
	dir, err := os.MkdirTemp("/private/tmp", "rt-gui-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("RUNNEL_INTERNAL_RUNTIME", dir)
	os.WriteFile(filepath.Join(dir, "gui.lock"), nil, 0600)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "gui.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	observed := make(chan packet, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(ioTimeout))
		p, _ := readPacket(c)
		observed <- p
		writePacket(c, packet{Type: "gui", Version: 2})
	}()
	// This is a test server; no real process receives stop-gui or a signal.
	if err := guiControl(true); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-observed:
		if p.Version != 2 || p.Type != "stop-gui" {
			t.Fatalf("GUI compatibility changed: %+v", p)
		}
	case <-time.After(ioTimeout):
		t.Fatal("no control request")
	}
}
