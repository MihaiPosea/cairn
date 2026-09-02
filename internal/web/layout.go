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
//
// Longest path is correct but its raw numbers are not usable as labels. On
// excalidraw it reaches 648 — not because anything is 648 imports deep, but
// because that is the longest chain that can be strung together across the
// repo, and most files pile up near the end of it. A column headed "630" tells
// a reader nothing. So the final step replaces each depth by its rank among the
// depths that actually occur, collapsing the empty levels between them. Rank is
// monotonic in depth, so every edge still points forward — the property the
// longest path was chosen for survives — and the labels become 0, 1, 2, 3.
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
		return compact(depth)
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
	return compact(depth)
}

// compact renumbers depths to their rank, so the levels that exist are
// consecutive. Order is preserved exactly: depth[a] < depth[b] implies
// rank[a] < rank[b], and equal depths stay equal.
func compact(depth map[string]int) map[string]int {
	seen := make(map[int]bool, len(depth))
	for _, d := range depth {
		seen[d] = true
	}
	levels := make([]int, 0, len(seen))
	for d := range seen {
		levels = append(levels, d)
	}
	sort.Ints(levels)

	rank := make(map[int]int, len(levels))
	for i, d := range levels {
		rank[d] = i
	}
	for id, d := range depth {
		depth[id] = rank[d]
	}
	return depth
}
