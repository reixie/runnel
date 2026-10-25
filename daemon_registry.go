package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type daemonEvent struct {
	data  []byte
	exit  bool
	peer  *peer
	msg   packet
	owner *ownedPane
	state string
	code  int
}
type paneEntry struct {
	info    paneInfo
	runtime *ownedPane
}
type sessionEntry struct {
	info     sessionInfo
	panes    map[string]*paneEntry
	order    []string
	identity os.FileInfo
	ending   bool
}

func (s *sessionEntry) paneInfos() []paneInfo {
	out := make([]paneInfo, 0, len(s.panes))
	for _, id := range s.order {
		if p := s.panes[id]; p != nil {
			out = append(out, p.info)
		}
	}
	return out
}
func (s *sessionEntry) refresh() {
	s.info.State = "exited"
	for _, p := range s.panes {
		if p.info.State == "ending" {
			s.info.State = "ending"
		}
	}
	for _, p := range s.panes {
		if p.info.State == "running" {
			s.info.State = "running"
		}
	}
	if s.ending {
		s.info.State = "ending"
	}
}

// Only this loop owns the catalog. PTY output never enters its queue: each
// pane has its own ordered emulator loop and bounded output queue.
func serveDaemon(ctx context.Context, cfg daemonConfig, ready chan<- error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, lock, err := listenDaemon(cfg)
	if ready != nil {
		ready <- err
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	defer os.Remove(socketPath(cfg))
	defer listener.Close()
	events := make(chan daemonEvent, 64)
	emit := func(e daemonEvent) {
		select {
		case events <- e:
		case <-ctx.Done():
		}
	}
	slots := make(chan struct{}, 8)
	var handlers sync.WaitGroup
	var peers sync.Map
	acceptDone := make(chan struct{})
	go func() { <-ctx.Done(); listener.Close() }()
	go func() {
		defer close(acceptDone)
		for {
			// Reserve before accept: retiring handlers release their slots before a
			// queued replacement handshake is admitted. The kernel backlog supplies
			// bounded waiting instead of transiently rejecting a valid four-pane GUI.
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			c, err := listener.Accept()
			if err != nil {
				<-slots
				return
			}

			peers.Store(c, true)
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer peers.Delete(c)
				defer func() { <-slots }()
				p := newPeer(c)
				defer p.close()
				c.SetDeadline(time.Now().Add(ioTimeout))
				hello, err := readPacketLimit(c, 4096)
				if err != nil {
					return
				}
				if hello.Type != "hello" || hello.Version != protocolVersion {
					_ = writePacket(c, packet{Type: "error", Version: protocolVersion, Error: "Incompatible Runnel protocol. The existing daemon and sessions were left running. Use the compatible app, save and end every session, then explicitly shut down that verified empty runtime before upgrading."})
					return
				}
				c.SetDeadline(time.Time{})
				if hello.Role == "terminal" {
					p.sessionID = hello.SessionID
					p.paneID = hello.PaneID
				}
				go p.writer()
				hello.Type = "connect"
				emit(daemonEvent{peer: p, msg: hello})
				defer emit(daemonEvent{peer: p, msg: packet{Type: "detach"}})
				for {
					c.SetReadDeadline(time.Now().Add(3 * heartbeat))
					m, err := readPacketLimit(c, 64<<10)
					if err != nil {
						return
					}
					emit(daemonEvent{peer: p, msg: m})
				}
			}()
		}
	}()
	entries := make(map[string]*sessionEntry)
	var order []string
	var projects []project
	var control *peer
	active := make(map[string]*peer)
	var activeID, owner string
	var generation uint64
	var nextName uint64
	cat := catalog{}
	snapshot := func() catalog {
		c := cat
		c.Projects = make([]projectInfo, 0, len(projects))
		c.Sessions = make([]sessionInfo, 0, len(order))
		for _, p := range projects {
			c.Projects = append(c.Projects, p.projectInfo)
		}
		for _, id := range order {
			if s := entries[id]; s != nil {
				info := s.info
				info.Panes = s.paneInfos()
				c.Sessions = append(c.Sessions, info)
			}
		}
		return c
	}
	publish := func() {
		cat.Revision++
		if control != nil {
			c := snapshot()
			control.sendCatalog(packet{Type: "catalog", Catalog: &c})
		}
	}
	reply := func(p *peer, m packet, err error) {
		c := snapshot()
		r := packet{Catalog: &c, Type: "result", Request: m.Request, SessionID: m.SessionID, ProjectID: m.ProjectID, PaneID: m.PaneID}
		if err != nil {
			r.Error = err.Error()
		}
		p.send(r)
	}
	closePaneStream := func(sid, pid string) {
		if p := active[pid]; p != nil {
			if s := entries[sid]; s != nil {
				if pane := s.panes[pid]; pane != nil {
					pane.runtime.command(daemonEvent{peer: p, msg: packet{Type: "detach", SessionID: sid, PaneID: pid}})
				}
			}
			p.close()
			delete(active, pid)
		}
	}
	closeActive := func() {
		for pid := range active {
			closePaneStream(activeID, pid)
		}
		activeID = ""
	}
	retainedPanes := func() int {
		n := 0
		for _, s := range entries {
			n += len(s.panes)
		}
		return n
	}
	findProject := func(id string) *project {
		for i := range projects {
			if projects[i].ID == id {
				return &projects[i]
			}
		}
		return nil
	}
	fallback := func() {
		if entries[cat.SelectedSession] != nil {
			return
		}
		cat.SelectedSession = ""
		// Oldest retained session in the same group, then oldest anywhere.
		for _, id := range order {
			if s := entries[id]; s != nil && !s.ending && s.info.ProjectID == cat.SelectedProject {
				cat.SelectedSession = id
				return
			}
		}
		for _, id := range order {
			if s := entries[id]; s != nil && !s.ending {
				cat.SelectedSession = id
				cat.SelectedProject = s.info.ProjectID
				return
			}
		}
	}
	defer func() {
		cancel()
		listener.Close()
		<-acceptDone
		peers.Range(func(k, v any) bool { k.(net.Conn).Close(); return true })
		for _, s := range entries {
			for _, pane := range s.panes {
				pane.runtime.end()
			}
		}
		for _, s := range entries {
			for _, pane := range s.panes {
				<-pane.runtime.done
			}
		}
		handlers.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-events:
			if e.owner != nil {
				s := entries[e.owner.sessionID]
				if s == nil {
					continue
				}
				pane := s.panes[e.owner.id]
				if pane == nil || pane.runtime != e.owner {
					continue
				}
				if e.state == "removed" {
					delete(s.panes, e.owner.id)
					for i, id := range s.order {
						if id == e.owner.id {
							s.order = append(s.order[:i], s.order[i+1:]...)
							break
						}
					}
					delete(active, e.owner.id)

					if len(s.panes) == 0 {
						delete(entries, s.info.ID)
						for i, id := range order {
							if id == s.info.ID {
								order = append(order[:i], order[i+1:]...)
								break
							}
						}
						if cat.SelectedSession == s.info.ID {
							fallback()
						}

					}
				} else if pane.info.State != "ending" {
					pane.info.State = e.state
					code := e.code
					pane.info.Code = &code
				}
				s.refresh()
				publish()
				continue
			}
			p, m := e.peer, e.msg
			if m.Type == "detach" {
				if control == p {
					control = nil
					closeActive()
					owner = ""
				}
				for pid, stream := range active {
					if stream == p {
						closePaneStream(activeID, pid)
					}
				}
				continue
			}
			if m.Type == "connect" {
				switch m.Role {
				case "catalog":
					if control != nil {
						control.close()
					}
					closeActive()
					control = p
					owner = opaqueID()
					generation = 0
					c := snapshot()
					p.send(packet{Type: "welcome", Version: protocolVersion, Owner: owner, Catalog: &c, PID: os.Getpid()})
				case "terminal":
					s := entries[m.SessionID]
					if control == nil || m.Owner != owner || m.Generation == 0 || m.Generation <= generation {
						p.send(packet{Type: "error", SessionID: m.SessionID, PaneID: m.PaneID, Error: "stale terminal attachment"})
						continue
					}
					if s == nil || s.ending || s.panes[m.PaneID] == nil || s.panes[m.PaneID].info.State == "ending" {
						p.send(packet{Type: "error", SessionID: m.SessionID, PaneID: m.PaneID, Error: "unknown or ended session/pane"})
						continue
					}
					generation = m.Generation
					if activeID != m.SessionID {
						closeActive()
					}
					closePaneStream(m.SessionID, m.PaneID)
					activeID = m.SessionID
					active[m.PaneID] = p
					if !s.panes[m.PaneID].runtime.command(daemonEvent{peer: p, msg: packet{Type: "attach", SessionID: m.SessionID, PaneID: m.PaneID}}) {
						closePaneStream(m.SessionID, m.PaneID)
					}
				default:
					p.send(packet{Type: "error", Error: "hello requires catalog or terminal role"})
				}
				continue
			}
			if m.Type == "pong" {
				continue
			}
			var attached string
			for id, stream := range active {
				if stream == p {
					attached = id
					break
				}
			}
			if attached != "" {
				if m.SessionID != activeID || m.PaneID != attached {
					closePaneStream(activeID, attached)
					continue
				}
				if s := entries[activeID]; s != nil && !s.ending {
					if pane := s.panes[attached]; pane != nil && pane.info.State != "ending" {
						switch m.Type {
						case "input", "resize", "theme", "clear", "flush":
							if !pane.runtime.command(e) {
								closePaneStream(activeID, attached)
							}
						default:
							p.send(packet{Type: "error", SessionID: activeID, PaneID: attached, Error: "use the catalog connection for workspace actions"})
						}
					}
				}
				continue
			}
			if p != control {
				p.close()
				continue
			}
			var actionErr error
			changed := false
			switch m.Type {
			case "list":
				c := snapshot()
				p.sendCatalog(packet{Type: "catalog", Catalog: &c})
			case "open-project":
				var path string
				var info os.FileInfo
				path, info, actionErr = directory(m.Path)
				if actionErr == nil {
					var found *project
					for i := range projects {
						if os.SameFile(projects[i].identity, info) {
							found = &projects[i]
							break
						}
					}
					if found == nil {
						if len(projects) >= maxProjects {
							actionErr = fmt.Errorf("project limit reached (%d)", maxProjects)
						} else {
							projects = append(projects, project{projectInfo: projectInfo{ID: opaqueID(), Path: path, Name: filepath.Base(path)}, identity: info})
							found = &projects[len(projects)-1]
						}
					}
					if found != nil {
						found.Path = path
						found.Name = filepath.Base(path)
						m.ProjectID = found.ID
						cat.SelectedProject = found.ID
						cat.SelectedSession = ""
						changed = true
					}
				}
			case "remove-project":
				if findProject(m.ProjectID) == nil {
					actionErr = fmt.Errorf("unknown project")
				} else {
					for _, s := range entries {
						if s.info.ProjectID == m.ProjectID {
							actionErr = fmt.Errorf("project still has retained sessions")
							break
						}
					}
					if actionErr == nil {
						for i, p := range projects {
							if p.ID == m.ProjectID {
								projects = append(projects[:i], projects[i+1:]...)
								break
							}
						}
						if cat.SelectedProject == m.ProjectID {
							cat.SelectedProject = ""
							cat.SelectedSession = ""
						}
						changed = true
					}
				}
			case "create":
				if len(entries) >= maxSessions || retainedPanes() >= maxTotalPanes {
					actionErr = fmt.Errorf("session limit reached (%d); end a retained session first", maxSessions)
					break
				}
				var prof profile
				prof, actionErr = cfg.profile()
				if actionErr != nil {
					break
				}
				dir := prof.home
				if m.ProjectID != "" {
					project := findProject(m.ProjectID)
					if project == nil {
						actionErr = fmt.Errorf("unknown project")
						break
					}
					var info os.FileInfo
					dir, info, actionErr = directory(project.Path)
					if actionErr != nil {
						break
					}
					if !os.SameFile(project.identity, info) {
						actionErr = fmt.Errorf("project directory identity changed; reopen the folder")
						break
					}
				}
				_, identity, err := directory(dir)
				if err != nil {
					actionErr = err
					break
				}
				var terminal *pane
				terminal, actionErr = newPane(prof, dir)
				if actionErr == nil {
					id, pid := opaqueID(), opaqueID()
					nextName++
					owned := startOwnedPane(ctx, id, pid, terminal, emit)
					entries[id] = &sessionEntry{info: sessionInfo{ID: id, ProjectID: m.ProjectID, Name: fmt.Sprintf("Terminal %d", nextName), InitialDirectory: dir, State: "running", Layout: leaf(pid), FocusedPane: pid, LayoutRevision: 1}, panes: map[string]*paneEntry{pid: {info: paneInfo{ID: pid, State: "running", PID: terminal.proc.PID()}, runtime: owned}}, order: []string{pid}, identity: identity}
					order = append(order, id)
					m.SessionID = id
					m.PaneID = pid
					cat.SelectedSession = id
					cat.SelectedProject = m.ProjectID
					changed = true
				}
			case "select":
				if m.SessionID != "" {
					s := entries[m.SessionID]
					if s == nil || s.ending {
						actionErr = fmt.Errorf("unknown or ended session")
					} else {
						cat.SelectedSession = m.SessionID
						cat.SelectedProject = s.info.ProjectID
						changed = true
					}
				} else if m.ProjectID != "" && findProject(m.ProjectID) == nil {
					actionErr = fmt.Errorf("unknown project")
				} else {
					cat.SelectedSession = ""
					cat.SelectedProject = m.ProjectID
					changed = true
				}
			case "rename":
				s := entries[m.SessionID]
				if s == nil || s.ending {
					actionErr = fmt.Errorf("unknown or ended session")
				} else {
					var name string
					name, actionErr = sessionName(m.Name)
					if actionErr == nil {
						s.info.Name = name
						changed = true
					}
				}
			case "split", "close-pane", "focus", "ratio":
				s := entries[m.SessionID]
				if s == nil || s.ending {
					actionErr = fmt.Errorf("unknown or ended session")
					break
				}
				if m.Revision == 0 || m.Revision != s.info.LayoutRevision {
					actionErr = fmt.Errorf("stale layout revision")
					break
				}
				switch m.Type {
				case "split":
					if len(s.panes) >= maxSessionPanes || retainedPanes() >= maxTotalPanes {
						actionErr = fmt.Errorf("retained pane limit reached (%d per session, %d total)", maxSessionPanes, maxTotalPanes)
						break
					}
					id := opaqueID()
					var tree *layoutNode
					tree, actionErr = splitLayout(s.info.Layout, m.PaneID, id, m.Axis, m.Ratio, 0)
					if actionErr != nil {
						break
					}
					var prof profile
					prof, actionErr = cfg.profile()
					if actionErr != nil {
						break
					}
					var identity os.FileInfo
					_, identity, actionErr = directory(s.info.InitialDirectory)
					if actionErr != nil {
						break
					}
					if !os.SameFile(identity, s.identity) {
						actionErr = fmt.Errorf("initial directory identity changed")
						break
					}
					var terminal *pane
					terminal, actionErr = newPane(prof, s.info.InitialDirectory)
					if actionErr != nil {
						break
					}
					// Commit only after both resources exist. No fallible work follows.
					owned := startOwnedPane(ctx, s.info.ID, id, terminal, emit)
					s.panes[id] = &paneEntry{info: paneInfo{ID: id, State: "running", PID: terminal.proc.PID()}, runtime: owned}
					s.order = append(s.order, id)
					s.info.Layout = tree
					s.info.FocusedPane = id
					m.PaneID = id
				case "ratio":
					s.info.Layout, actionErr = func() (*layoutNode, error) {
						tree, err := resizeLayout(s.info.Layout, m.NodeID, m.Ratio)
						if err != nil {
							return s.info.Layout, err
						}
						return tree, nil
					}()
				case "focus":
					found := false
					for _, id := range s.info.Layout.leaves() {
						if id == m.PaneID {
							found = true
						}
					}
					if !found {
						actionErr = fmt.Errorf("unknown pane")
						break
					}
					s.info.FocusedPane = m.PaneID
				case "close-pane":
					var tree *layoutNode
					var focus string
					tree, focus, actionErr = removeLayout(s.info.Layout, m.PaneID, s.info.FocusedPane)
					if actionErr != nil {
						break
					}
					pane := s.panes[m.PaneID]
					if tree == nil { // Final pane uses the same whole-session lifecycle.
						s.ending = true
						s.info.Layout = nil
						s.info.FocusedPane = ""
					} else {
						s.info.Layout = tree
						s.info.FocusedPane = focus
					}
					pane.info.State = "ending"
					pane.runtime.end()
					s.info.LayoutRevision++
					s.refresh()
					publish()
					reply(p, m, nil)
					continue
				}
				if actionErr == nil {
					s.info.LayoutRevision++
					s.refresh()
					changed = true
				}
			case "end":
				s := entries[m.SessionID]
				if s == nil || s.ending {
					actionErr = fmt.Errorf("unknown or ended session")
				} else {
					s.ending = true
					s.info.Layout = nil
					s.info.FocusedPane = ""
					s.info.LayoutRevision++
					s.info.State = "ending"
					for _, pane := range s.panes {
						pane.info.State = "ending"
						pane.runtime.end()
					}
					publish()
					reply(p, m, nil)
					continue
				}
			case "shutdown-empty":
				if len(entries) != 0 {
					actionErr = fmt.Errorf("runtime still has retained sessions")
				} else {
					ack := make(chan struct{})
					p.send(packet{Type: "result", Request: m.Request, ack: ack})
					select {
					case <-ack:
					case <-p.done:
					case <-time.After(ioTimeout):
					}
					return nil
				}
			default:
				actionErr = fmt.Errorf("unknown workspace action %q", m.Type)
			}
			if changed {
				publish()
			}
			reply(p, m, actionErr)
		}
	}
}
