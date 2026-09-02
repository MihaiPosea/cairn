// Package graph holds the dependency graph: nodes (files, packages, builtins,
// unresolved specifiers) and the edges between them.
//
// It is deliberately ignorant of where the graph came from. Parsing lives in
// internal/lang, resolution in internal/resolve, packages in internal/pkgs.
// This package only knows about nodes, edges, and the algorithms over them.
package graph

import (
	"fmt"
	"sort"
	"strings"
)

// Kind is what a node represents.
type Kind uint8

const (
	// File is a source file inside the scanned repo.
	File Kind = iota
	// Package is an installed dependency, identified by name and version.
	Package
	// Builtin is a runtime builtin such as "node:fs" — real, but not a file
	// and not a package.
	Builtin
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
	case Unresolved:
		return "unresolved"
	}
	return "unknown"
}

// Node is one thing in the graph.
//
// ID is the stable identity used everywhere — the index, the CLI, the UI —
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
// — traversal order, tie-breaking, output ordering — is resolved against it, so
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
// ends up in the graph. Re-adding an existing ID is a no-op, not an error —
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

// Dependencies returns the edges leaving id — what id depends on.
func (g *Graph) Dependencies(id string) []Edge { return g.out[id] }

// Dependents returns the edges arriving at id — what depends on id.
// This is the direction blast radius walks.
func (g *Graph) Dependents(id string) []Edge { return g.in[id] }

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
// ─── YOUR TASK ──────────────────────────────────────────────────────────────
//
// Implement Kahn's algorithm. Not depth-first search — Kahn's, because the
// same counting structure is what a concurrent scheduler needs, and because
// it makes the "what's left over is a cycle" step fall out naturally.
//
//  1. Count each node's unmet dependencies (its "in-degree" in dependency
//     terms): how many edges leave it via g.Dependencies.
//
//  2. Collect every node with a count of zero into a ready list, walking
//     g.Order so the result is deterministic.
//
//  3. Repeatedly: take the first ready node, append it to the result, then for
//     each node that depends on it (g.Dependents) decrement that node's count.
//     Any node reaching zero joins the ready list, inserted so the list stays
//     in g.Order sequence.
//
//  4. When the ready list empties: if you emitted every node, you're done. If
//     nodes remain, each is stuck waiting on something — they are in, or
//     downstream of, a cycle.
//
// ─── THE PART WORTH DOING PROPERLY ──────────────────────────────────────────
//
// Step 4 tells you a cycle exists but not what it is, and "cycle detected" is
// a useless error. To name the loop, walk the remaining nodes depth-first,
// keeping a stack of the path you took. When you reach a node already on the
// current stack, the loop is the slice of the stack from that node onward,
// plus that node again at the end. Return it as a *CycleError.
//
// ─── A NOTE FOR LATER ───────────────────────────────────────────────────────
//
// Treating a cycle as an error is right for a build graph, where a cycle means
// nothing can start. It is *wrong* for an import graph — JavaScript permits
// circular imports and real repos are full of them. At M4 this gets replaced
// by condensing each strongly-connected component into a single node and
// ordering the condensation, so cycles become a finding rather than a failure.
// Build the erroring version first; you need it to understand why the other
// one is necessary.
//
// Test with: go test ./internal/graph/
func (g *Graph) TopoOrder() ([]*Node, error) {
	return nil, fmt.Errorf("TopoOrder: not implemented yet — this is your task")
}
