package web

import (
	"math"
	"sort"

	"github.com/MihaiPosea/cairn/internal/graph"
)

// layer assigns each node a depth so the graph can be drawn in columns.
//
// Layered, not force-directed. Every dependency visualiser that reaches for a
// force-directed layout produces the same hairball, and a hairball is what
// people mean when they say these tools are useless. Columns by depth mean
// edges mostly point one way and the eye can follow them.
//
// Depth is the *longest* path from an entry point, not the shortest. A file
// reached both directly and through five hops belongs at depth five, or its
// edges point backwards and the layout stops reading left to right.
func layers(g *graph.Graph, ids []string) map[string]int {
	inSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		inSet[id] = true
	}

	depth := make(map[string]int, len(ids))
	for _, id := range ids {
		depth[id] = 0
	}

	// TopoOrder gives dependencies before dependents, so a single pass in
	// reverse computes longest paths exactly. If the graph has a cycle it
	// returns an error, and we fall back to relaxation.
	if order, err := g.TopoOrder(); err == nil {
		for i := len(order) - 1; i >= 0; i-- {
			id := order[i].ID
			if !inSet[id] {
				continue
			}
			for _, e := range g.Dependencies(id) {
				if !inSet[e.To] {
					continue
				}
				if d := depth[id] + 1; d > depth[e.To] {
					depth[e.To] = d
				}
			}
		}
		return depth
	}

	// Cyclic graph: relax repeatedly, bounded so a cycle cannot spin forever.
	// The bound is why this is safe — inside a cycle there is no correct depth,
	// only a consistent one.
	const maxPasses = 64
	for pass := 0; pass < maxPasses; pass++ {
		changed := false
		for _, id := range ids {
			for _, e := range g.Dependencies(id) {
				if !inSet[e.To] {
					continue
				}
				if d := depth[id] + 1; d > depth[e.To] {
					depth[e.To] = d
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	return depth
}

// position turns depths into x/y coordinates.
//
// A layer is wrapped into sub-columns rather than run as one tall stack.
// Excalidraw puts 200-odd entry points at depth 0 — every test file, every
// config — and a single column of them is 6,000 pixels tall beside an empty
// canvas. Nobody scrolls that; they conclude the tool is broken.
//
// Wrapping keeps each layer roughly square, so the whole graph stays in an
// aspect ratio a screen can hold.
func position(depth map[string]int, ids []string, label func(string) string) (map[string][2]int, int, int) {
	byLayer := map[int][]string{}
	maxDepth := 0
	for _, id := range ids {
		d := depth[id]
		byLayer[d] = append(byLayer[d], id)
		if d > maxDepth {
			maxDepth = d
		}
	}
	for _, list := range byLayer {
		sort.Slice(list, func(i, j int) bool { return label(list[i]) < label(list[j]) })
	}

	const (
		colWidth  = 240
		rowHeight = 26
		gutter    = 40
		aspect    = 1.7 // target width:height, near a screen's
	)

	// Choose how tall a sub-column may be so the whole graph lands near the
	// target aspect ratio.
	//
	// A fixed cap is wrong in both directions: too low and a 1,000-node graph
	// becomes a 5,000-pixel-wide ribbon; too high and one layer is a mile-long
	// column again.
	//
	// Solved by measuring rather than predicting. A closed form has to guess at
	// gutters and at how unevenly nodes are spread across layers — the first
	// attempt was out by 50% on a real repo. Laying the graph out is cheap, so
	// it is laid out a few times and the closest result kept.
	maxRows := 24
	best, bestErr := maxRows, math.Inf(1)
	for attempt := 0; attempt < 24; attempt++ {
		w, h := measure(byLayer, maxDepth, maxRows, colWidth, rowHeight, gutter)
		if h == 0 {
			break
		}
		ratio := float64(w) / float64(h)
		if e := math.Abs(ratio - aspect); e < bestErr {
			best, bestErr = maxRows, e
		}
		if ratio > aspect {
			maxRows = int(float64(maxRows) * 1.35)
		} else {
			maxRows = int(float64(maxRows) / 1.2)
		}
		if maxRows < 6 {
			maxRows = 6
			break
		}
	}
	maxRows = best

	// Never taller than about two screens, whatever the aspect solver wanted.
	//
	// A layered graph reads left to right, so width is navigable and height is
	// not: a column taller than the viewport means scrolling down through boxes
	// with no way to see where the layer ends. Capping trades a wider picture
	// for one that can actually be followed.
	const maxRowsCap = 48
	if maxRows > maxRowsCap {
		maxRows = maxRowsCap
	}

	pos := make(map[string][2]int, len(ids))
	x := 0
	width, height := 0, 0

	for d := 0; d <= maxDepth; d++ {
		list := byLayer[d]
		if len(list) == 0 {
			continue
		}
		// Sub-columns needed to keep this layer under maxRows tall.
		cols := (len(list) + maxRows - 1) / maxRows
		rows := (len(list) + cols - 1) / cols

		for i, id := range list {
			c := i / rows
			r := i % rows
			pos[id] = [2]int{x + c*colWidth, r * rowHeight}
			if y := (r + 1) * rowHeight; y > height {
				height = y
			}
		}
		x += cols*colWidth + gutter
		width = x
	}
	return pos, width, height
}

// measure reports the bounds a given sub-column height would produce, without
// building the position map.
func measure(byLayer map[int][]string, maxDepth, maxRows, colWidth, rowHeight, gutter int) (int, int) {
	x, height := 0, 0
	for d := 0; d <= maxDepth; d++ {
		n := len(byLayer[d])
		if n == 0 {
			continue
		}
		cols := (n + maxRows - 1) / maxRows
		rows := (n + cols - 1) / cols
		if h := rows * rowHeight; h > height {
			height = h
		}
		x += cols*colWidth + gutter
	}
	return x, height
}
