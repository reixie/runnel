package main

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestNestedLayoutInvariantsAndDeterministicRemoval(t *testing.T) {
	tree := leaf("a")
	original := tree
	leafID := tree.ID
	var err error
	tree, err = splitLayout(tree, "a", "b", "right", .5, 0)
	if err != nil {
		t.Fatal(err)
	}
	rootID := tree.ID
	tree, err = splitLayout(tree, "a", "c", "down", .4, 0)
	if err != nil {
		t.Fatal(err)
	}
	tree, err = splitLayout(tree, "b", "d", "down", .6, 0)
	if err != nil {
		t.Fatal(err)
	}
	panes := []paneInfo{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	if err := validateLayout(tree, panes, "c"); err != nil {
		t.Fatal(err)
	}
	if tree.First.First.ID != leafID || tree.ID != rootID || original.PaneID != "a" {
		t.Fatal("existing node identity mutated")
	}
	if !reflect.DeepEqual(tree.leaves(), []string{"a", "c", "b", "d"}) {
		t.Fatal(tree.leaves())
	}
	// Every removal permutation must preserve exact ownership and valid focus.
	var permutations func([]string, []string)
	permutations = func(prefix, remaining []string) {
		if len(remaining) > 0 {
			for i, id := range remaining {
				rest := append([]string(nil), remaining[:i]...)
				rest = append(rest, remaining[i+1:]...)
				permutations(append(append([]string(nil), prefix...), id), rest)
			}
			return
		}
		current, focus := tree, "c"
		for _, id := range prefix {
			next, nextFocus, err := removeLayout(current, id, focus)
			if err != nil {
				t.Fatal(err)
			}
			current, focus = next, nextFocus
			if current == nil {
				if focus != "" {
					t.Fatal("final focus retained")
				}
				continue
			}
			var retained []paneInfo
			for _, pid := range current.leaves() {
				retained = append(retained, paneInfo{ID: pid})
			}
			if err := validateLayout(current, retained, focus); err != nil {
				t.Fatalf("removal order %v: %v", prefix, err)
			}
		}
	}
	permutations(nil, tree.leaves())
	before, _ := json.Marshal(tree)
	for _, ratio := range []float64{0, .099, .901, 1, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := resizeLayout(tree, rootID, ratio); err == nil {
			t.Fatalf("accepted ratio %v", ratio)
		}
		if _, err := splitLayout(original, "a", "b", "right", ratio, 0); err == nil {
			t.Fatalf("split ratio %v", ratio)
		}
	}
	for _, target := range []string{"missing", leafID} {
		if _, err := resizeLayout(tree, target, .5); err == nil {
			t.Fatal("accepted non-split")
		}
	}
	if _, err := splitLayout(tree, "missing", "x", "right", .5, 0); err == nil {
		t.Fatal("unknown split")
	}
	if _, err := splitLayout(tree, "a", "x", "diagonal", .5, 0); err == nil {
		t.Fatal("unknown axis")
	}
	if _, _, err := removeLayout(tree, "missing", "c"); err == nil {
		t.Fatal("unknown removal")
	}
	after, _ := json.Marshal(tree)
	if string(before) != string(after) {
		t.Fatal("failed mutation changed tree")
	}
	resized, err := resizeLayout(tree, rootID, .72)
	if err != nil || resized.Ratio != .72 || tree.Ratio != .5 {
		t.Fatal("immutable ratio update")
	}
	// Nonfocused removal preserves focus; collapse keeps the sibling node ID.
	removed, focus, err := removeLayout(tree, "a", "c")
	if err != nil || focus != "c" || removed.First.ID != tree.First.Second.ID {
		t.Fatal("collapse changed sibling/focus")
	}
	removed, focus, err = removeLayout(removed, "c", focus)
	if err != nil || focus != "b" || removed.ID != tree.Second.ID {
		t.Fatal("focused removal should pick next visual leaf")
	}
	removed, focus, _ = removeLayout(removed, "d", "d")
	if focus != "b" || removed.PaneID != "b" {
		t.Fatal("last visual removal should pick previous")
	}
	removed, focus, _ = removeLayout(removed, "b", focus)
	if removed != nil || focus != "" {
		t.Fatal("final leaf remains")
	}
}
func TestRejectMalformedLayoutsAndDepth(t *testing.T) {
	a, b := leaf("a"), leaf("b")
	for _, tree := range []*layoutNode{
		nil, {}, {ID: "x", Axis: "right", Ratio: .5, First: a},
		{ID: "x", Axis: "right", Ratio: .5, First: a, Second: a},
		{ID: "x", Axis: "right", Ratio: .5, First: leaf("a"), Second: leaf("a")},
		{ID: "x", Axis: "right", Ratio: math.NaN(), First: a, Second: b},
		{ID: "x", PaneID: "a", First: b}, {ID: "x", PaneID: "missing"},
	} {
		if validateLayout(tree, []paneInfo{{ID: "a"}, {ID: "b"}}, "a") == nil {
			t.Fatal("malformed layout accepted")
		}
	}
	tree := leaf("a")
	for i := 0; i < maxLayoutDepth; i++ {
		var err error
		tree, err = splitLayout(tree, "a", string(rune('b'+i)), "right", .5, 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := splitLayout(tree, "a", "extra", "down", .5, 0); err == nil {
		t.Fatal("unbounded depth")
	}
	if validateLayout(leaf("a"), []paneInfo{{ID: "a"}}, "missing") == nil {
		t.Fatal("missing focus")
	}
}
