package main

import (
	"fmt"
	"math"
)

// Trees are immutable values. Mutations path-copy, so failed operations and
// queued catalog snapshots cannot observe a partially changed layout.
type layoutNode struct {
	ID     string      `json:"id"`
	PaneID string      `json:"pane_id,omitempty"`
	Axis   string      `json:"axis,omitempty"` // right: first left; down: first above
	Ratio  float64     `json:"ratio,omitempty"`
	First  *layoutNode `json:"first,omitempty"`
	Second *layoutNode `json:"second,omitempty"`
}

func leaf(id string) *layoutNode { return &layoutNode{ID: opaqueID(), PaneID: id} }
func validRatio(r float64) bool  { return !math.IsNaN(r) && !math.IsInf(r, 0) && r >= .1 && r <= .9 }
func (n *layoutNode) leaves() []string {
	if n == nil {
		return nil
	}
	if n.PaneID != "" {
		return []string{n.PaneID}
	}
	return append(n.First.leaves(), n.Second.leaves()...)
}
func (n *layoutNode) node(id string) *layoutNode {
	if n == nil || n.ID == id {
		return n
	}
	if x := n.First.node(id); x != nil {
		return x
	}
	return n.Second.node(id)
}
func validateLayout(n *layoutNode, panes []paneInfo, focus string) error {
	ids, leaves := map[string]bool{}, map[string]bool{}
	available := map[string]bool{}
	for _, p := range panes {
		if p.ID == "" || available[p.ID] {
			return fmt.Errorf("duplicate pane")
		}
		available[p.ID] = true
	}
	var walk func(*layoutNode, int) error
	walk = func(n *layoutNode, d int) error {
		if n == nil || n.ID == "" || ids[n.ID] || d > maxLayoutDepth {
			return fmt.Errorf("invalid node or tree depth")
		}
		ids[n.ID] = true
		if n.PaneID != "" {
			if n.First != nil || n.Second != nil || n.Axis != "" || n.Ratio != 0 || leaves[n.PaneID] || !available[n.PaneID] {
				return fmt.Errorf("invalid leaf")
			}
			leaves[n.PaneID] = true
			return nil
		}
		if (n.Axis != "right" && n.Axis != "down") || !validRatio(n.Ratio) {
			return fmt.Errorf("invalid split")
		}
		if err := walk(n.First, d+1); err != nil {
			return err
		}
		return walk(n.Second, d+1)
	}
	if err := walk(n, 0); err != nil {
		return err
	}
	if len(leaves) > maxSessionPanes || !leaves[focus] {
		return fmt.Errorf("invalid pane count or focus")
	}
	for _, p := range panes {
		if p.State != "ending" && !leaves[p.ID] {
			return fmt.Errorf("pane missing from layout")
		}
		if p.State == "ending" && leaves[p.ID] {
			return fmt.Errorf("ending pane in layout")
		}
	}
	return nil
}
func splitLayout(n *layoutNode, target, newID, axis string, ratio float64, depth int) (*layoutNode, error) {
	if n == nil {
		return nil, fmt.Errorf("unknown pane")
	}
	if (axis != "right" && axis != "down") || !validRatio(ratio) {
		return nil, fmt.Errorf("invalid split axis or ratio")
	}
	if n.PaneID == target {
		if depth >= maxLayoutDepth {
			return nil, fmt.Errorf("layout depth limit reached")
		}
		return &layoutNode{ID: opaqueID(), Axis: axis, Ratio: ratio, First: n, Second: leaf(newID)}, nil
	}
	if n.PaneID != "" {
		return nil, fmt.Errorf("unknown pane")
	}
	copy := *n
	if a, err := splitLayout(n.First, target, newID, axis, ratio, depth+1); err == nil {
		copy.First = a
		return &copy, nil
	}
	if b, err := splitLayout(n.Second, target, newID, axis, ratio, depth+1); err == nil {
		copy.Second = b
		return &copy, nil
	}
	return nil, fmt.Errorf("unknown pane or depth limit reached")
}
func removeLeaf(n *layoutNode, pane string) (*layoutNode, bool) {
	if n == nil {
		return nil, false
	}
	if n.PaneID != "" {
		if n.PaneID == pane {
			return nil, true
		}
		return n, false
	}
	c := *n
	if a, ok := removeLeaf(n.First, pane); ok {
		if a == nil {
			return n.Second, true
		}
		c.First = a
		return &c, true
	}
	if b, ok := removeLeaf(n.Second, pane); ok {
		if b == nil {
			return n.First, true
		}
		c.Second = b
		return &c, true
	}
	return n, false
}
func removeLayout(n *layoutNode, pane, focus string) (*layoutNode, string, error) {
	order := n.leaves()
	at := -1
	for i, id := range order {
		if id == pane {
			at = i
		}
	}
	if at < 0 {
		return n, focus, fmt.Errorf("unknown pane")
	}
	next, _ := removeLeaf(n, pane)
	if focus == pane {
		surviving := next.leaves()
		focus = ""
		if len(surviving) > 0 {
			focus = surviving[min(at, len(surviving)-1)]
		}
	}
	return next, focus, nil
}
func resizeLayout(n *layoutNode, id string, ratio float64) (*layoutNode, error) {
	if !validRatio(ratio) {
		return nil, fmt.Errorf("invalid split ratio")
	}
	if n == nil {
		return nil, fmt.Errorf("unknown split")
	}
	c := *n
	if n.ID == id {
		if n.PaneID != "" {
			return nil, fmt.Errorf("target is a leaf")
		}
		c.Ratio = ratio
		return &c, nil
	}
	if a, err := resizeLayout(n.First, id, ratio); err == nil {
		c.First = a
		return &c, nil
	}
	if b, err := resizeLayout(n.Second, id, ratio); err == nil {
		c.Second = b
		return &c, nil
	}
	return nil, fmt.Errorf("unknown split")
}
