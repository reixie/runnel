package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// Presentation state survives frames. While a drag is dirty or in flight,
// catalog acknowledgements cannot overwrite its newer local percentage.
type splitPresentation struct {
	percent        float32
	handle         ui.Handle
	dirty, pending bool
	serial         uint64
	changed        time.Time
}

func (a *application) layoutAction(p packet, done func(packet)) bool {
	if !a.available() || a.selected == "" {
		return false
	}
	p.SessionID = a.selected
	return a.action(p, done)
}
func (a *application) splitPane(axis string) {
	id, gen := a.selected, a.generation
	a.flushRatios()
	a.layoutAction(packet{Type: "split", PaneID: a.focusedPane, Axis: axis, Ratio: .5}, func(r packet) {
		if r.Error == "" && a.selected == id && a.generation == gen {
			a.cat, _ = a.workspace.snapshot()
			a.focusPane(r.PaneID, false)
			a.syncPanes()
		}
	})
}
func (a *application) closePane(pid string) {
	a.flushRatios()
	a.layoutAction(packet{Type: "close-pane", PaneID: pid}, nil)
}
func (a *application) focusPane(pid string, persist bool) {
	s := a.selectedInfo()
	if s == nil {
		return
	}
	found := false
	for _, id := range s.Layout.leaves() {
		if id == pid {
			found = true
		}
	}
	if !found {
		return
	}
	changed := a.focusedPane != pid
	a.focusedPane = pid
	if !persist {
		a.focusRequest = pid
	}
	if persist && changed {
		a.layoutAction(packet{Type: "focus", PaneID: pid}, nil)
	}
}
func (a *application) cyclePane(step int) {
	if !a.available() {
		return
	}
	s := a.selectedInfo()
	if s == nil {
		return
	}
	ids := s.Layout.leaves()
	for i, id := range ids {
		if id == a.focusedPane {
			next := ids[(i+len(ids)+step)%len(ids)]
			a.focusPane(next, true)
			a.focusRequest = next
			return
		}
	}
}
func (a *application) flushRatios() {
	for id, state := range a.splits {
		if state.dirty {
			a.sendRatio(id, state)
		}
	}
}
func (a *application) sendRatio(id string, state *splitPresentation) {
	if !state.dirty || a.workspace == nil {
		return
	}
	serial, gen, sid := state.serial, a.generation, a.selected
	ratio := float64(state.percent) / 100
	state.dirty = false
	state.pending = true
	if !a.action(packet{Type: "ratio", SessionID: sid, NodeID: id, Ratio: ratio}, func(r packet) {
		if a.closed || a.generation != gen || a.selected != sid || a.splits[id] != state {
			return
		}
		// A later drag has its own queued request. Only its callback may settle it.
		if serial != state.serial {
			return
		}
		state.pending = false
		a.cat, _ = a.workspace.snapshot()
		if r.Error != "" {
			if s := a.selectedInfo(); s != nil {
				if n := s.Layout.node(id); n != nil {
					state.percent = float32(n.Ratio * 100)
				}
			}
		}
	}) {
		state.pending = false
		state.dirty = true
	}
}
func (a *application) buildPanes(c *ui.Context, s *sessionInfo) {
	if s.Layout == nil {
		ui.Text(c, "Ending session…")
		return
	}
	if a.paneHandles == nil {
		a.paneHandles = make(map[string]*ui.Handle)
	}
	if a.splits == nil {
		a.splits = make(map[string]*splitPresentation)
	}
	// Read all committed handles before rebuilding any subtree. Reparenting a
	// stable pane changes MyGO's ancestry key, so restore only actual terminal
	// focus, never focus belonging to a dialog, button, or divider.
	for id, h := range a.paneHandles {
		if a.dialog || a.renaming {
			h.CancelFocus()
			continue
		}
		if h.Focused(c) && a.focusRequest == "" {
			a.focusPane(id, true)
			if a.focusRequest == "" {
				a.focusRequest = id
			}
		}
	}
	bounds := a.paneViewport.Bounds(c)
	minW, minH := layoutMinimum(s.Layout)
	width, height := max(minW, bounds.W), max(minH, bounds.H)
	if width != a.canvasWidth || height != a.canvasHeight {
		a.canvasWidth = width
		a.canvasHeight = height
		c.Invalidate()
	}
	ui.ScrollBoth(c).Grow(1).FillWidth().Bind(&a.paneViewport).Children(func() {
		// The split widgets keep their standard minimums. A scrollable canvas sized
		// from the viewport and tree keeps headers and two terminal rows reachable.
		ui.Box(c).Width(width).Height(height).Children(func() { a.buildTree(c, s, s.Layout) })
	})
	// Unreachable split state can never send a late drag to a collapsed node.
	for id := range a.splits {
		if s.Layout.node(id) == nil {
			delete(a.splits, id)
		}
	}
}
func (a *application) buildTree(c *ui.Context, s *sessionInfo, n *layoutNode) {
	if n == nil {
		return
	}
	if n.PaneID != "" {
		a.buildPane(c, s, n.PaneID)
		return
	}
	state := a.splits[n.ID]
	if state == nil {
		state = &splitPresentation{percent: float32(n.Ratio * 100)}
		a.splits[n.ID] = state
	}
	if !state.dirty && !state.pending {
		state.percent = float32(n.Ratio * 100)
	}
	first, second := func() { a.buildTree(c, s, n.First) }, func() { a.buildTree(c, s, n.Second) }
	var split ui.Element
	if n.Axis == "right" {
		split = ui.SplitPercent(c.Key(n.ID), &state.percent, first, second)
	} else {
		split = ui.SplitVerticalPercent(c.Key(n.ID), &state.percent, first, second)
	}
	split.Fill().Bind(&state.handle).OnChange(func() {
		state.percent = max(10, min(90, state.percent))
		state.serial++
		state.dirty = true
		state.changed = c.Now()
		c.Invalidate()
	})
	if state.dirty {
		remaining := 60*time.Millisecond - c.Now().Sub(state.changed)
		if remaining <= 0 {
			a.sendRatio(n.ID, state)
		} else {
			c.After(remaining)
		}
	}
}
func (a *application) buildPane(c *ui.Context, s *sessionInfo, id string) {
	h := a.paneHandles[id]
	if h == nil {
		h = &ui.Handle{}
		a.paneHandles[id] = h
	}
	var info paneInfo
	index := 0
	for i, pid := range s.Layout.leaves() {
		if pid == id {
			index = i + 1
		}
	}
	for _, p := range s.Panes {
		if p.ID == id {
			info = p
		}
	}
	ui.Column(c.Key(id)).Fill().MinWidth(0).MinHeight(0).Children(func() {
		ui.Row(c).Wrap().Gap(6).Padding(4).Children(func() {
			label := fmt.Sprintf("Pane %d · %s", index, info.State)
			if info.Code != nil {
				label = fmt.Sprintf("Pane %d · exited %d", index, *info.Code)
			}
			if a.focusedPane == id {
				label = "● " + label
			}
			if ui.Button(c, label).Tooltip("Focus pane").Clicked() {
				a.focusPane(id, true)
				a.focusRequest = id
			}
			if ui.Button(c, "Close Pane").Tooltip("Close only this terminal and its processes").Disabled(a.dialog || a.renaming).Clicked() {
				a.closePane(id)
			}
		})
		if x := a.panes[id]; x != nil {
			e := terminal.View(c.Key("terminal:"+id), x.term).Grow(1).FillWidth().Bind(h).Disabled(a.dialog || a.renaming)
			if !a.dialog && !a.renaming && a.focusRequest == id {
				e.Focus()
				a.focusRequest = ""
			}
			state := x.status()
			if !state.Connected {
				ui.Text(c, "Disconnected. This pane may still be running.")
				if ui.Button(c, "Reattach Pane").Clicked() {
					delete(a.panes, id)
					a.retirement = joinRetirements(a.retirement, x)
					a.syncPanes()
				}
			}
		} else {
			ui.Text(c, "Attaching pane…")
			if ui.Button(c, "Reattach Pane").Clicked() {
				a.syncPanes()
			}
		}
	})
}

func layoutMinimum(n *layoutNode) (float32, float32) {
	if n == nil || n.PaneID != "" {
		return 210, 120
	}
	aw, ah := layoutMinimum(n.First)
	bw, bh := layoutMinimum(n.Second)
	// Respect the stored ratio too: otherwise a narrow child could lose its
	// controls even while the total canvas exceeded the sum of the minima.
	share := float32(n.Ratio)
	if n.Axis == "right" {
		return max(aw/share, bw/(1-share)) + 1, max(ah, bh)
	}
	return max(aw, bw), max(ah/share, bh/(1-share)) + 1
}
