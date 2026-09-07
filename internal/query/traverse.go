// Package query answers questions about a built graph.
//
// Everything here is a traversal. The value is not the algorithms - they are
// textbook - it is being careful about which edges count for which question.
// A type-only import breaks a compile but not a runtime; a dynamic import keeps
// a file alive but cannot be followed statically. Getting those distinctions
// wrong is how a tool tells you to delete a file you need.
package query

import (
	"sort"

	"github.com/MihaiPosea/cairn/internal/graph"
)

// EdgeFilter decides whether an edge counts for a particular question.
type EdgeFilter func(graph.Edge) bool

// AllEdges follows everything.
func AllEdges(graph.Edge) bool { return true }

// RuntimeOnly skips imports that vanish when the code compiles.
//
// Use it for questions about what actually ships. Do not use it for "is this
// file dead" - a file that only anything imports for its types is still needed
// to compile, and deleting it breaks the build.
func RuntimeOnly(e graph.Edge) bool { return e.Kind != graph.TypeOnly }

// PackageEdges follows only package-to-package dependencies.
func PackageEdges(e graph.Edge) bool { return e.Kind == graph.Requires }

// Reachable returns every node reachable from the given roots, including the
// roots themselves.
//
// Breadth-first rather than depth-first so that Depth is the true shortest
// distance, which is what "how far is this from an entry point" should mean.
func Reachable(g *graph.Graph, roots []string, follow EdgeFilter) map[string]int {
	seen := map[string]int{}
	queue := make([]string, 0, len(roots))
	for _, r := range roots {
		if _, ok := g.Nodes[r]; !ok {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = 0
		queue = append(queue, r)
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.Dependencies(cur) {
			if !follow(e) {
				continue
			}
			if _, ok := seen[e.To]; ok {
				continue
			}
			seen[e.To] = seen[cur] + 1
			queue = append(queue, e.To)
		}
	}
	return seen
}

// ReachableFrom walks the graph backwards: everything that depends on the given
// node, transitively. This is what a blast radius is.
func ReachableFrom(g *graph.Graph, target string, follow EdgeFilter) map[string]int {
	seen := map[string]int{target: 0}
	queue := []string{target}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.Dependents(cur) {
			if !follow(e) {
				continue
			}
			if _, ok := seen[e.From]; ok {
				continue
			}
			seen[e.From] = seen[cur] + 1
			queue = append(queue, e.From)
		}
	}
	return seen
}

// ShortestPath returns the shortest chain of node IDs from any of roots to
// target, or nil if there is none.
//
// This is what answers "why is this package here" - and the answer has to be a
// path, not a yes. "You depend on left-pad" is useless; "app/page.tsx imports
// a, which requires b, which requires left-pad" is actionable.
func ShortestPath(g *graph.Graph, roots []string, target string, follow EdgeFilter) []string {
	if _, ok := g.Nodes[target]; !ok {
		return nil
	}
	prev := map[string]string{}
	seen := map[string]bool{}
	var queue []string

	for _, r := range roots {
		if _, ok := g.Nodes[r]; !ok || seen[r] {
			continue
		}
		seen[r] = true
		queue = append(queue, r)
		if r == target {
			return []string{r}
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.Dependencies(cur) {
			if !follow(e) || seen[e.To] {
				continue
			}
			seen[e.To] = true
			prev[e.To] = cur
			if e.To == target {
				return rebuild(prev, target)
			}
			queue = append(queue, e.To)
		}
	}
	return nil
}

func rebuild(prev map[string]string, target string) []string {
	var path []string
	for at := target; ; {
		path = append(path, at)
		p, ok := prev[at]
		if !ok {
			break
		}
		at = p
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// SortedKeys returns a map's keys in a stable order.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
