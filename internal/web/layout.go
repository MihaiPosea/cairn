package web

import (
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

// position turns depths into x/y coordinates, sorting within each column so
// the picture is stable between runs.
func position(depth map[string]int, ids []string, label func(string) string) map[string][2]int {
	byLayer := map[int][]string{}
	for _, id := range ids {
		byLayer[depth[id]] = append(byLayer[depth[id]], id)
	}
	for _, list := range byLayer {
		sort.Slice(list, func(i, j int) bool { return label(list[i]) < label(list[j]) })
	}

	const (
		colWidth = 260
		rowGap   = 30
	)
	pos := make(map[string][2]int, len(ids))
	for layer, list := range byLayer {
		for row, id := range list {
			pos[id] = [2]int{layer * colWidth, row * rowGap}
		}
	}
	return pos
}
