package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const maxSessions = 32 // running, exited and ending all consume a slot
const maxProjects = 64
const maxSessionPanes = 4 // fits catalog + visible panes + retiring/handshake headroom in eight sockets
const maxTotalPanes = 64  // includes exited and ending panes
const maxLayoutDepth = 3

// An empty ProjectID means Home. Membership and InitialDirectory never follow
// OSC titles, OSC 7 or the shell's changing working directory.
type sessionInfo struct {
	ID               string      `json:"id"`
	ProjectID        string      `json:"project_id,omitempty"`
	Name             string      `json:"name"`
	InitialDirectory string      `json:"initial_directory"`
	State            string      `json:"state"` // running, exited, ending
	Panes            []paneInfo  `json:"panes"`
	Layout           *layoutNode `json:"layout"`
	FocusedPane      string      `json:"focused_pane"`
	LayoutRevision   uint64      `json:"layout_revision"`
}
type paneInfo struct {
	ID    string `json:"id"`
	State string `json:"state"`
	PID   int    `json:"pid"`
	Code  *int   `json:"code,omitempty"` // only an exited pane has an exit code
}
type projectInfo struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Name string `json:"name"`
}
type project struct {
	projectInfo
	identity os.FileInfo
}
type catalog struct {
	Projects        []projectInfo `json:"projects"`
	Sessions        []sessionInfo `json:"sessions"`
	SelectedSession string        `json:"selected_session,omitempty"`
	SelectedProject string        `json:"selected_project,omitempty"`
	Revision        uint64        `json:"revision"`
}

func opaqueID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func directory(path string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", nil, fmt.Errorf("directory must be an absolute local path")
	}
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, fmt.Errorf("directory is unavailable: %s: %w", path, err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("directory is unavailable: %s", path)
	}
	if err := syscall.Access(root, 4|1); err != nil {
		return "", nil, fmt.Errorf("directory is inaccessible: %s: %w", path, err)
	}
	return filepath.Clean(root), info, nil
}
func sessionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 128 || strings.ContainsFunc(name, unicode.IsControl) {
		return "", fmt.Errorf("session name must contain 1–128 printable characters")
	}
	return name, nil
}
