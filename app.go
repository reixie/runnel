package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type attachmentRequest struct {
	workspace  *workspaceConnection
	id         string
	panes      []string
	generation uint64
	retired    <-chan struct{}
}

// Window state is UI-thread-only. Socket requests and terminal attachment have
// separate bounded workers; only the latest attachment result can be installed.
type application struct {
	win                       *mygo.Window
	workspace                 *workspaceConnection
	panes                     map[string]*connection
	paneHandles               map[string]*ui.Handle
	paneViewport              ui.Handle
	canvasWidth, canvasHeight float32
	splits                    map[string]*splitPresentation
	focusedPane               string
	focusRequest              string
	attachSerial              uint64
	cat                       catalog
	selected, project         string
	sidebarSelection          string
	generation                uint64
	closed                    bool
	loading                   bool
	creating                  bool
	dialog                    bool
	renaming                  bool
	renameID, name            string
	message                   string

	retirement  <-chan struct{}
	operations  chan func()
	attachments chan attachmentRequest
	stop        chan struct{}
	closeOnce   sync.Once
}

func (a *application) update(f func()) {
	if a.win != nil {
		a.win.Update(f)
	} else {
		f()
	}
}
func (a *application) open() {
	a.sidebarSelection = "group:"
	a.operations = make(chan func(), 64)
	a.attachments = make(chan attachmentRequest, 1)
	a.stop = make(chan struct{})
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "Open Project…", Accelerator: "Cmd+O", Click: func(*mygo.MenuItem, *mygo.Window) { a.openProject() }},
			{Label: "New Session", Accelerator: "Cmd+T", Click: func(*mygo.MenuItem, *mygo.Window) { a.newSession() }},
			{Label: "Rename Session…", Click: func(*mygo.MenuItem, *mygo.Window) { a.beginRename() }},
			{Label: "End Session", Click: func(*mygo.MenuItem, *mygo.Window) { a.endSession(a.selected) }},
			{Label: "Split Right", Accelerator: "Cmd+D", Click: func(*mygo.MenuItem, *mygo.Window) { a.splitPane("right") }},
			{Label: "Split Down", Accelerator: "Cmd+Shift+D", Click: func(*mygo.MenuItem, *mygo.Window) { a.splitPane("down") }},
			{Label: "Next Pane", Accelerator: "Cmd+]", Click: func(*mygo.MenuItem, *mygo.Window) { a.cyclePane(1) }},
			{Label: "Previous Pane", Accelerator: "Cmd+[", Click: func(*mygo.MenuItem, *mygo.Window) { a.cyclePane(-1) }},
			{Label: "Close Pane", Click: func(*mygo.MenuItem, *mygo.Window) { a.closePane(a.focusedPane) }},
			{Role: mygo.RoleClose},
		}}, {Role: mygo.RoleEditMenu}, {Role: mygo.RoleWindowMenu},
	}))
	a.panes = make(map[string]*connection)
	a.paneHandles = make(map[string]*ui.Handle)
	a.splits = make(map[string]*splitPresentation)
	a.win = mygo.NewWindow(mygo.WindowOptions{Title: "Runnel", Width: 1000, Height: 650, MinWidth: 640, MinHeight: 400, Content: ui.View(a.build)})
	a.win.OnClose(func(*mygo.CloseEvent) { a.close() })
	a.win.OnClosed(a.close)
	go func() {
		for {
			select {
			case <-a.stop:
				return
			case f := <-a.operations:
				f()
			}
		}
	}()
	go a.attachmentWorker()
	a.connect(true)
}
func (a *application) enqueue(f func()) bool {
	select {
	case a.operations <- f:
		return true
	default:
		a.message = "Too many pending workspace actions. Try again after they complete."
		return false
	}
}
func (a *application) connect(start bool) {
	if a.loading || a.closed {
		return
	}
	a.loading = true
	a.message = ""
	if !a.enqueue(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		w, err := openWorkspace(ctx, defaultConfig(), start)
		a.update(func() {
			a.loading = false
			if a.closed {
				if w != nil {
					w.close()
				}
				return
			}
			if err != nil {
				a.message = err.Error()
				return
			}
			if a.workspace != nil {
				a.workspace.close()
			}
			a.dropTerminal()
			a.workspace = w
			w.setNotifier(func() { a.update(func() { a.reconcile(w) }) })
			a.cat, _ = w.snapshot()
			a.project = a.cat.SelectedProject
			a.choose(a.cat.SelectedSession, a.cat.SelectedProject, false)
		})
	}) {
		a.loading = false
	}
}
func (a *application) reconcile(w *workspaceConnection) {
	if a.closed || a.workspace != w {
		return
	}
	cat, connected := w.snapshot()
	a.cat = cat
	if !connected {
		a.dropTerminal()
		return
	}
	if a.selected != "" {
		exists := false
		for _, s := range cat.Sessions {
			if s.ID == a.selected {
				exists = true
				break
			}
		}
		if !exists {
			a.choose(cat.SelectedSession, cat.SelectedProject, false)
		}
	}
	a.syncPanes()
}
func joinRetirements(previous <-chan struct{}, xs ...*connection) <-chan struct{} {
	if len(xs) == 0 {
		return previous
	} // Empty rapid selections reuse the existing barrier.
	done := make(chan struct{})
	waits := make([]<-chan struct{}, 0, len(xs)+1)
	if previous != nil {
		waits = append(waits, previous)
	}
	for _, x := range xs {
		waits = append(waits, x.retire())
	}
	go func() {
		for _, ch := range waits {
			<-ch
		}
		close(done)
	}()
	return done
}
func (a *application) dropTerminal() {
	a.flushRatios()
	a.generation++
	xs := make([]*connection, 0, len(a.panes))
	for _, x := range a.panes {
		xs = append(xs, x)
	}
	a.retirement = joinRetirements(a.retirement, xs...)
	a.panes = make(map[string]*connection)
	a.paneHandles = make(map[string]*ui.Handle)
	a.splits = make(map[string]*splitPresentation)
	a.focusedPane = ""
	a.focusRequest = ""
}
func (a *application) choose(id, project string, persist bool) {
	if a.closed {
		return
	}
	a.dropTerminal()
	a.selected = id
	a.sidebarSelection = "group:" + project
	if id != "" {
		a.sidebarSelection = "session:" + id
	}
	a.project = project
	a.message = ""
	w := a.workspace
	if w == nil {
		return
	}
	if persist {
		a.enqueue(func() { _, err := w.call(packet{Type: "select", SessionID: id, ProjectID: project}); a.report(w, err) })
	}
	if a.workspace != nil {
		a.cat, _ = a.workspace.snapshot()
	}
	a.syncPanes()
}
func (a *application) selectedInfo() *sessionInfo {
	for i := range a.cat.Sessions {
		if a.cat.Sessions[i].ID == a.selected {
			return &a.cat.Sessions[i]
		}
	}
	return nil
}
func (a *application) syncPanes() {
	s := a.selectedInfo()
	if s == nil || s.Layout == nil {
		return
	}
	if a.panes == nil {
		a.panes = make(map[string]*connection)
	}
	visible := map[string]bool{}
	missing := false
	for _, id := range s.Layout.leaves() {
		visible[id] = true
		if a.panes[id] == nil {
			missing = true
		}
	}
	for id, x := range a.panes {
		if !visible[id] {
			a.retirement = joinRetirements(a.retirement, x)
			delete(a.panes, id)
			delete(a.paneHandles, id)
		}
	}
	if !visible[a.focusedPane] {
		a.focusedPane = s.FocusedPane
		a.focusRequest = s.FocusedPane
	}
	if missing && a.attachments != nil {
		a.requestAttachment(attachmentRequest{workspace: a.workspace, id: s.ID, panes: s.Layout.leaves(), generation: a.generation, retired: a.retirement})
	}
}
func (a *application) requestAttachment(r attachmentRequest) {
	select {
	case <-a.attachments:
	default:
	}
	a.attachments <- r
}
func (a *application) attachmentWorker() {
	for {
		select {
		case <-a.stop:
			return
		case r := <-a.attachments:
			if r.retired != nil {
				select {
				case <-r.retired:
				case <-a.stop:
					return
				}
			}
			for _, pid := range r.panes {
				needed := make(chan bool, 1)
				a.update(func() {
					s := a.selectedInfo()
					valid := !a.closed && a.workspace == r.workspace && a.generation == r.generation && a.selected == r.id && a.panes[pid] == nil && s != nil
					if valid {
						valid = false
						for _, id := range s.Layout.leaves() {
							if id == pid {
								valid = true
							}
						}
					}
					needed <- valid
				})
				select {
				case yes := <-needed:
					if !yes {
						continue
					}
				case <-a.stop:
					return
				}
				raw, err := dialDaemon(defaultConfig())
				var x *connection
				if err == nil {
					a.attachSerial++
					x, err = attach(raw, r.id, pid, r.workspace.owner, a.attachSerial)
				}
				installed := make(chan bool, 1)
				a.update(func() { installed <- a.installPaneAttachment(r, pid, x, err) })
				select {
				case accepted := <-installed:
					if !accepted && x != nil {
						<-x.retire()
					}
				case <-a.stop:
					if x != nil {
						x.detach()
					}
					return
				}
			}
		}
	}
}
func (a *application) installAttachment(r attachmentRequest, x *connection, err error) { // also used by the delayed-result regression
	pid := ""
	if x != nil {
		pid = x.paneID
	}
	a.installPaneAttachment(r, pid, x, err)
}
func (a *application) installPaneAttachment(r attachmentRequest, pid string, x *connection, err error) bool {
	valid := !a.closed && a.workspace == r.workspace && a.generation == r.generation && a.selected == r.id
	if valid {
		valid = false
		latest, connected := r.workspace.snapshot()
		if connected && latest.Revision >= a.cat.Revision {
			a.cat = latest
		}
		if s := a.selectedInfo(); connected && s != nil {
			for _, id := range s.Layout.leaves() {
				if id == pid {
					valid = true
				}
			}
		}
	}
	if !valid {
		if x != nil {
			x.retire()
		}
		return false
	}
	if err != nil {
		a.message = err.Error()
		return false
	}
	x.control = r.workspace
	a.panes[pid] = x
	// A sibling's late handshake never asks for keyboard focus.
	if a.win != nil {
		x.setNotifier(a.win.Invalidate)
	}
	return true
}

func (a *application) available() bool {
	if a.workspace == nil || a.closed || a.dialog || a.renaming {
		return false
	}
	_, ok := a.workspace.snapshot()
	return ok
}
func (a *application) report(w *workspaceConnection, err error) {
	if err == nil {
		return
	}
	log.Print(err)
	a.update(func() {
		if !a.closed && a.workspace == w {
			a.message = err.Error()
		}
	})
}
func (a *application) action(p packet, done func(packet)) bool {
	w := a.workspace
	if w == nil {
		return false
	}
	return a.enqueue(func() {
		switch p.Type {
		case "split", "close-pane", "ratio", "focus":
			cat, _ := w.snapshot()
			for _, s := range cat.Sessions {
				if s.ID == p.SessionID {
					p.Revision = s.LayoutRevision
				}
			}
		}
		r, err := w.call(p)
		a.update(func() {
			if a.closed || a.workspace != w {
				return
			}
			if err != nil {
				a.message = err.Error()
			}
			if done != nil {
				done(r)
			}
		})
	})
}
func (a *application) openProject() {
	if !a.available() {
		return
	}
	a.dialog = true
	go func() {
		// Use MyGO’s directory picker with the system’s standard Open prompt.
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "Open Project", Directory: true})
		a.update(func() {
			a.dialog = false
			if a.closed {
				return
			}
			if err != nil {
				a.message = err.Error()
				return
			}
			a.openPickedProjects(paths)
		})
	}()
}

// Cancel (nil paths) is deliberately a no-op, including local selection.
func (a *application) openPickedProjects(paths []string) {
	if len(paths) == 0 {
		return
	}
	gen := a.generation
	a.action(packet{Type: "open-project", Path: paths[0]}, func(r packet) {
		if r.Error == "" && r.ProjectID != "" && a.generation == gen {
			a.choose("", r.ProjectID, false)
		}
	})
}
func (a *application) newSession() {
	if !a.available() || a.creating {
		return
	}
	a.creating = true
	gen := a.generation
	if !a.action(packet{Type: "create", ProjectID: a.project}, func(r packet) {
		a.creating = false
		if r.Error == "" && r.SessionID != "" && a.generation == gen {
			a.choose(r.SessionID, r.ProjectID, false)
		}
	}) {
		a.creating = false
	}
}
func (a *application) beginRename() {
	if !a.available() || a.selected == "" {
		return
	}
	for _, s := range a.cat.Sessions {
		if s.ID == a.selected && s.State != "ending" {
			a.renameID = s.ID
			a.name = s.Name
			a.renaming = true
			return
		}
	}
}
func (a *application) saveRename() {
	name, err := sessionName(a.name)
	if err != nil {
		a.message = err.Error()
		return
	}
	a.renaming = false
	a.action(packet{Type: "rename", SessionID: a.renameID, Name: name}, nil)
}
func (a *application) endSession(id string) {
	if a.available() && id != "" {
		a.action(packet{Type: "end", SessionID: id}, nil)
	}
}
func (a *application) close() {
	a.closeOnce.Do(func() {
		a.closed = true
		a.dropTerminal()
		if a.workspace != nil {
			w := a.workspace
			retired := a.retirement
			finished := make(chan struct{})
			finish := func() {
				defer close(finished)
				if retired != nil {
					<-retired
				}
				w.close()
				if a.stop != nil {
					close(a.stop)
				}
			}
			if !a.enqueue(finish) {
				go finish()
			}
			// Shutdown only: give already accepted mutations their bounded flush before
			// App.Run returns and exits the GUI process. No shell is terminated.
			select {
			case <-finished:
			case <-time.After(ioTimeout + 500*time.Millisecond):
				w.close()
			}
		} else if a.stop != nil {
			close(a.stop)
		}
	})
}
func (a *application) build(c *ui.Context) {
	connected := false
	if a.workspace != nil {
		_, connected = a.workspace.snapshot()
	}
	ui.Row(c).Fill().Children(func() {
		ui.Column(c).Width(210).Shrink(0).FillHeight().Children(func() {
			if ui.Sidebar(c, &a.sidebarSelection, func() {
				group := func(id, label, path string) {
					ui.SidebarItem(c, "group:"+id, nil, label).Tooltip(path)
					ui.SidebarSection(c, "Sessions", nil, func() {
						for _, s := range a.cat.Sessions {
							if s.ProjectID != id {
								continue
							}
							label := s.Name + " · " + s.State
							if s.State == "exited" {
								label = s.Name + " · exited"
							}
							ui.SidebarItem(c, "session:"+s.ID, nil, label)
						}
					})
				}
				group("", "Home", "")
				for _, p := range a.cat.Projects {
					group(p.ID, p.Name+" — "+p.Path, p.Path)
				}
			}).Grow(1).FillWidth().Changed() {
				for _, s := range a.cat.Sessions {
					if a.sidebarSelection == "session:"+s.ID && s.State != "ending" {
						a.choose(s.ID, s.ProjectID, true)
					}
				}
				if len(a.sidebarSelection) >= 6 && a.sidebarSelection[:6] == "group:" {
					a.choose("", a.sidebarSelection[6:], true)
				}
			}
			if ui.Button(c, "Open Project…").Disabled(!connected || a.dialog || a.renaming).Clicked() {
				a.openProject()
			}
			if ui.Button(c, "New Session").Disabled(!connected || a.creating || a.dialog || a.renaming).Clicked() {
				a.newSession()
			}
		})
		ui.Column(c).Grow(1).FillHeight().Children(func() {
			ui.Row(c).Wrap().Padding(8).Gap(8).Children(func() {
				if ui.Button(c, "Rename Session…").Disabled(!connected || a.selected == "").Clicked() {
					a.beginRename()
				}
				if ui.Button(c, "End Session").Disabled(!connected || a.selected == "").Clicked() {
					a.endSession(a.selected)
				}
				if a.project != "" {
					empty := true
					for _, s := range a.cat.Sessions {
						if s.ProjectID == a.project {
							empty = false
						}
					}
					if ui.Button(c, "Remove Project").Disabled(!connected || !empty).Clicked() {
						id := a.project
						a.action(packet{Type: "remove-project", ProjectID: id}, func(r packet) {
							if r.Error == "" {
								a.choose("", "", false)
							}
						})
					}
				}
			})
			if !connected {
				ui.Text(c, "Disconnected. Retained sessions may still be running.")
				if a.loading {
					ui.Text(c, "Connecting…")
				} else if ui.Button(c, "Reconnect").Clicked() {
					a.connect(false)
				}
			} else if info := a.selectedInfo(); info != nil {
				a.buildPanes(c, info)
			} else {
				ui.Text(c, "No session selected. Choose a session or create one in this group.")
			}
			if a.message != "" {
				ui.Text(c, a.message)
			}
		})
	})
	ui.Modal(c, &a.renaming, func() {
		ui.Text(c, "Rename Session")
		if ui.TextInput(c, &a.name).Label("Session name").AutoFocus().Submitted() {
			a.saveRename()
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Cancel").Clicked() {
				a.renaming = false
			}
			if ui.PrimaryButton(c, "Rename").Clicked() {
				a.saveRename()
			}
		})
	})
}

func bundledLibrary(resources string) (string, error) {
	path := filepath.Join(resources, "libghostty-vt.dylib")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("libghostty-vt is missing from this app. Rebuild with make build. (%s)", path)
	}
	return path, nil
}

// Derive bundle resources from the executable in both roles, without App.Path
// or App.IsPackaged: the daemon never starts the GUI backend. Always override
// an inherited MYGO_GHOSTTY_VT, and never allow the plugin downloader fallback.
func loadBundledLibrary() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if filepath.Base(filepath.Dir(exe)) != "MacOS" {
		path := os.Getenv("MYGO_GHOSTTY_VT")
		info, e := os.Stat(path)
		if e != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("an explicit local MYGO_GHOSTTY_VT library is required outside the app bundle")
		}
		return nil
	}
	path, err := bundledLibrary(filepath.Join(filepath.Dir(exe), "..", "Resources"))
	if err != nil {
		return err
	}
	return os.Setenv("MYGO_GHOSTTY_VT", path)
}
