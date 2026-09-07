// Package graph holds the dependency graph: nodes (files, packages, builtins,
// unresolved specifiers) and the edges between them.
//
// It is deliberately ignorant of where the graph came from. Parsing lives in
// internal/lang, resolution in internal/resolve, packages in internal/pkgs.
// This package only knows about nodes, edges, and the algorithms over them.
package graph

import (
	"container/heap"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind is what a node represents.
type Kind uint8

const (
	// File is a source file inside the scanned repo.
	File Kind = iota
	// Package is an installed dependency, identified by name and version.
	Package
	// Builtin is a runtime builtin such as "node:fs" - real, but not a file
	// and not a package.
	Builtin
	// Virtual is a module a framework or bundler synthesises: astro:content,
	// virtual:uno.css, $app/stores, #imports.
	//
	// Kept apart from Builtin because they answer different questions. A
	// builtin tells you the code touches the platform; a virtual module tells
	// you the code is coupled to a specific framework's build step, which is
	// usually the more interesting fact.
	Virtual
	// Unresolved is a specifier we could not resolve to anything. These are
	// kept as nodes on purpose: silently dropping them would hide exactly the
	// bugs we care about. The unresolved rate is the tool's honesty metric.
	Unresolved
)

func (k Kind) String() string {
	switch k {
	case File:
		return "file"
	case Package:
		return "pkg"
	case Builtin:
		return "builtin"
	case Virtual:
		return "virtual"
	case Unresolved:
		return "unresolved"
	}
	return "unknown"
}

// Node is one thing in the graph.
//
// ID is the stable identity used everywhere - the index, the CLI, the UI -
// and is always "<kind>:<path-or-name>", e.g.
//
//	file:app/page.tsx
//	pkg:next@15.5.9
//	builtin:node:fs
//	unresolved:@/components/Missing
type Node struct {
	ID      string
	Kind    Kind
	Path    string // repo-relative, for File
	Name    string // for Package, Builtin, Unresolved
	Version string // for Package
	Bytes   int64  // installed size, for Package
}

// NodeID builds the canonical ID for a node.
func NodeID(k Kind, nameOrPath string) string {
	return k.String() + ":" + nameOrPath
}

// EdgeKind distinguishes edges that behave differently at runtime.
//
// The distinction matters: a type-only import vanishes when the code is
// compiled, so it must not keep a file alive for dead-code purposes and must
// not count toward the cost of an import. Conflating these is the most common
// way dependency tools produce confidently wrong answers.
type EdgeKind uint8

const (
	// Import is a normal runtime import.
	Import EdgeKind = iota
	// TypeOnly is erased at compile time (`import type { X } from ...`).
	TypeOnly
	// Dynamic is import() with a literal specifier.
	Dynamic
	// Requires is a package depending on another package.
	Requires
)

func (e EdgeKind) String() string {
	switch e {
	case Import:
		return "import"
	case TypeOnly:
		return "type-only"
	case Dynamic:
		return "dynamic"
	case Requires:
		return "requires"
	}
	return "unknown"
}

// Edge is a directed dependency: From depends on To.
//
// Specifier and Line are kept so every answer can point back at the exact line
// of code that caused it. "utils.ts is reachable" is not useful; "app/page.tsx
// line 3 imports './utils'" is.
type Edge struct {
	From      string
	To        string
	Kind      EdgeKind
	Specifier string // the raw text in the source, e.g. "@/components/Button"
	Line      int
}

// Graph is a directed graph of Nodes and Edges.
//
// Order records insertion order. Everything that could otherwise be arbitrary
// - traversal order, tie-breaking, output ordering - is resolved against it, so
// two scans of an unchanged repo produce byte-identical output. Without that,
// nothing here is testable.
type Graph struct {
	Nodes map[string]*Node
	Order []string
	out   map[string][]Edge
	in    map[string][]Edge
}

func New() *Graph {
	return &Graph{
		Nodes: map[string]*Node{},
		out:   map[string][]Edge{},
		in:    map[string][]Edge{},
	}
}

// AddNode inserts n if its ID is not already present, and returns the node that
// ends up in the graph. Re-adding an existing ID is a no-op, not an error -
// the same file is reached from many places.
func (g *Graph) AddNode(n *Node) *Node {
	if existing, ok := g.Nodes[n.ID]; ok {
		return existing
	}
	g.Nodes[n.ID] = n
	g.Order = append(g.Order, n.ID)
	return n
}

// AddEdge records that e.From depends on e.To. Both endpoints must already
// exist as nodes.
func (g *Graph) AddEdge(e Edge) error {
	if _, ok := g.Nodes[e.From]; !ok {
		return fmt.Errorf("edge from unknown node %q", e.From)
	}
	if _, ok := g.Nodes[e.To]; !ok {
		return fmt.Errorf("edge to unknown node %q", e.To)
	}
	g.out[e.From] = append(g.out[e.From], e)
	g.in[e.To] = append(g.in[e.To], e)
	return nil
}

// Dependencies returns the edges leaving id - what id depends on.
func (g *Graph) Dependencies(id string) []Edge { return g.out[id] }

// Dependents returns the edges arriving at id - what depends on id.
// This is the direction blast radius walks.
func (g *Graph) Dependents(id string) []Edge { return g.in[id] }

// EdgeCount returns the number of edges, optionally restricted to those whose
// target is of the given kinds.
func (g *Graph) EdgeCount(toKinds ...Kind) int {
	want := map[Kind]bool{}
	for _, k := range toKinds {
		want[k] = true
	}
	n := 0
	for _, id := range g.Order {
		for _, e := range g.out[id] {
			if len(want) == 0 || want[g.Nodes[e.To].Kind] {
				n++
			}
		}
	}
	return n
}

// IDs returns every node ID in insertion order.
func (g *Graph) IDs() []string { return g.Order }

// Stats counts nodes by kind. The Unresolved count is printed on every scan.
func (g *Graph) Stats() map[Kind]int {
	s := map[Kind]int{}
	for _, id := range g.Order {
		s[g.Nodes[id].Kind]++
	}
	return s
}

// CycleError reports a dependency cycle, naming the exact loop.
//
// Path is the loop with the starting node repeated at the end, e.g.
// ["file:a.ts", "file:b.ts", "file:a.ts"], so Error prints a → b → a.
type CycleError struct {
	Path []string
}

func (e *CycleError) Error() string {
	return "dependency cycle: " + strings.Join(e.Path, " → ")
}

// SortedIDs returns node IDs sorted lexically. Useful for stable test output
// where insertion order isn't the property under test.
func (g *Graph) SortedIDs() []string {
	out := append([]string(nil), g.Order...)
	sort.Strings(out)
	return out
}

// TopoOrder returns every node in an order where each node appears after
// everything it depends on. If the graph contains a cycle it returns a
// *CycleError naming the loop.
//
// Kahn's algorithm rather than depth-first search: the counting structure here
// is exactly what a concurrent scheduler needs, and "whatever is left over is
// in a cycle" falls out of it for free.
//
// Note that treating a cycle as an error is right for a build graph, where a
// cycle means nothing can start. It is wrong for an import graph - JavaScript
// permits circular imports and real repos are full of them - which is why
// query.Cycles reports them as findings instead. Both exist on purpose.
func (g *Graph) TopoOrder() ([]*Node, error) {
	// Distinct targets, not edge count. A file that imports the same module on
	// two lines produces two edges, and counting both would leave a permanent
	// deficit that looks exactly like a cycle.
	deps := make(map[string]map[string]bool, len(g.Nodes))
	dependents := make(map[string]map[string]bool, len(g.Nodes))
	for _, id := range g.Order {
		for _, e := range g.out[id] {
			if deps[id] == nil {
				deps[id] = map[string]bool{}
			}
			if dependents[e.To] == nil {
				dependents[e.To] = map[string]bool{}
			}
			deps[id][e.To] = true
			dependents[e.To][id] = true
		}
	}

	// Position in insertion order, so ties break deterministically.
	pos := make(map[string]int, len(g.Order))
	for i, id := range g.Order {
		pos[id] = i
	}

	remaining := make(map[string]int, len(g.Order))
	ready := &posHeap{pos: pos}
	for _, id := range g.Order {
		n := len(deps[id])
		remaining[id] = n
		if n == 0 {
			heap.Push(ready, id)
		}
	}

	out := make([]*Node, 0, len(g.Order))
	for ready.Len() > 0 {
		id := heap.Pop(ready).(string)
		out = append(out, g.Nodes[id])
		delete(remaining, id)

		for dep := range dependents[id] {
			remaining[dep]--
			if remaining[dep] == 0 {
				heap.Push(ready, dep)
			}
		}
	}

	if len(out) != len(g.Order) {
		return nil, &CycleError{Path: findCycle(g, remaining)}
	}
	return out, nil
}

// findCycle names a loop among the nodes Kahn's algorithm could not drain.
//
// Kahn's tells you a cycle exists but not what it is, and "cycle detected" is
// a useless error message. Every stuck node is in or downstream of a cycle, so
// a depth-first walk restricted to stuck nodes, carrying the path it took,
// finds one: reaching a node already on the current path closes the loop.
//
// Iterative rather than recursive - a deep chain would overflow the stack, and
// this runs on repos with tens of thousands of files.
func findCycle(g *Graph, stuck map[string]int) []string {
	inStuck := func(id string) bool { _, ok := stuck[id]; return ok }

	// Walk in insertion order so the reported cycle is deterministic.
	for _, start := range g.Order {
		if !inStuck(start) {
			continue
		}

		type frame struct {
			node string
			edge int
		}
		var path []string
		onPath := map[string]bool{}
		stack := []frame{{node: start}}
		path = append(path, start)
		onPath[start] = true

		for len(stack) > 0 {
			f := &stack[len(stack)-1]
			edges := g.out[f.node]

			descended := false
			for f.edge < len(edges) {
				to := edges[f.edge].To
				f.edge++
				if !inStuck(to) {
					continue
				}
				if onPath[to] {
					// Found it: the loop is the tail of the path from `to`.
					for i, id := range path {
						if id == to {
							return append(append([]string{}, path[i:]...), to)
						}
					}
				}
				if visitedThisWalk(path, to) {
					continue
				}
				stack = append(stack, frame{node: to})
				path = append(path, to)
				onPath[to] = true
				descended = true
				break
			}
			if descended {
				continue
			}
			onPath[f.node] = false
			path = path[:len(path)-1]
			stack = stack[:len(stack)-1]
		}
	}
	// Unreachable for a genuinely stuck graph, but never return an empty loop.
	return []string{"<cycle among " + itoa(len(stuck)) + " nodes>"}
}

func visitedThisWalk(path []string, id string) bool {
	for _, p := range path {
		if p == id {
			return true
		}
	}
	return false
}

// posHeap pops node IDs in insertion order, which is what makes two runs of
// TopoOrder over an unchanged graph produce identical output.
type posHeap struct {
	ids []string
	pos map[string]int
}

func (h *posHeap) Len() int           { return len(h.ids) }
func (h *posHeap) Less(i, j int) bool { return h.pos[h.ids[i]] < h.pos[h.ids[j]] }
func (h *posHeap) Swap(i, j int)      { h.ids[i], h.ids[j] = h.ids[j], h.ids[i] }
func (h *posHeap) Push(x any)         { h.ids = append(h.ids, x.(string)) }
func (h *posHeap) Pop() any {
	old := h.ids
	n := len(old)
	x := old[n-1]
	h.ids = old[:n-1]
	return x
}

func itoa(i int) string { return strconv.Itoa(i) }
