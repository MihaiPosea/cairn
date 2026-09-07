package graph

import (
	"errors"
	"testing"
)

// build a small file graph from a map of file -> files it imports
func build(t *testing.T, order []string, imports map[string][]string) *Graph {
	t.Helper()
	g := New()
	for _, f := range order {
		g.AddNode(&Node{ID: NodeID(File, f), Kind: File, Path: f})
	}
	for _, f := range order {
		for _, dep := range imports[f] {
			if err := g.AddEdge(Edge{
				From:      NodeID(File, f),
				To:        NodeID(File, dep),
				Kind:      Import,
				Specifier: "./" + dep,
				Line:      1,
			}); err != nil {
				t.Fatalf("AddEdge: %v", err)
			}
		}
	}
	return g
}

// page.tsx and about.tsx both import Button.tsx, which imports utils.ts
var (
	diamondOrder   = []string{"app/page.tsx", "app/about.tsx", "components/Button.tsx", "lib/utils.ts"}
	diamondImports = map[string][]string{
		"app/page.tsx":          {"components/Button.tsx"},
		"app/about.tsx":         {"components/Button.tsx"},
		"components/Button.tsx": {"lib/utils.ts"},
	}
)

// Everything a node depends on must appear before it.
func TestTopoOrderRespectsDependencies(t *testing.T) {
	g := build(t, diamondOrder, diamondImports)
	order, err := g.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	if len(order) != len(g.Nodes) {
		t.Fatalf("got %d nodes, want %d", len(order), len(g.Nodes))
	}
	pos := map[string]int{}
	for i, n := range order {
		for _, e := range g.Dependencies(n.ID) {
			if _, seen := pos[e.To]; !seen {
				t.Errorf("%s at position %d comes before its dependency %s", n.ID, i, e.To)
			}
		}
		pos[n.ID] = i
	}
}

// The same graph must always produce the same order. Ties break by insertion order.
func TestTopoOrderIsDeterministic(t *testing.T) {
	want := []string{
		"file:lib/utils.ts",
		"file:components/Button.tsx",
		"file:app/page.tsx",
		"file:app/about.tsx",
	}
	for run := 0; run < 20; run++ {
		g := build(t, diamondOrder, diamondImports)
		order, err := g.TopoOrder()
		if err != nil {
			t.Fatalf("TopoOrder: %v", err)
		}
		for i, n := range order {
			if n.ID != want[i] {
				t.Fatalf("run %d: position %d = %s, want %s", run, i, n.ID, want[i])
			}
		}
	}
}

// A cycle must come back as a *CycleError naming the real loop.
func TestTopoOrderNamesTheCycle(t *testing.T) {
	g := build(t,
		[]string{"a.ts", "b.ts", "c.ts", "lonely.ts"},
		map[string][]string{
			"a.ts": {"b.ts"},
			"b.ts": {"c.ts"},
			"c.ts": {"a.ts"},
		})

	_, err := g.TopoOrder()
	if err == nil {
		t.Fatal("expected a cycle error, got nil")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CycleError, got %T: %v", err, err)
	}
	if len(ce.Path) < 3 {
		t.Fatalf("cycle path %v is too short to describe a loop", ce.Path)
	}
	if ce.Path[0] != ce.Path[len(ce.Path)-1] {
		t.Errorf("cycle path %v should start and end with the same node", ce.Path)
	}
	// every hop in the reported path must be a real edge
	for i := 0; i < len(ce.Path)-1; i++ {
		from, to := ce.Path[i], ce.Path[i+1]
		found := false
		for _, e := range g.Dependencies(from) {
			if e.To == to {
				found = true
			}
		}
		if !found {
			t.Errorf("cycle claims %s → %s but no such edge exists", from, to)
		}
	}
}

func TestDependentsIsTheReverseDirection(t *testing.T) {
	g := build(t, diamondOrder, diamondImports)

	deps := g.Dependencies(NodeID(File, "app/page.tsx"))
	if len(deps) != 1 || deps[0].To != "file:components/Button.tsx" {
		t.Fatalf("Dependencies(page.tsx) = %v", deps)
	}

	dependents := g.Dependents(NodeID(File, "components/Button.tsx"))
	if len(dependents) != 2 {
		t.Fatalf("Button.tsx should have 2 dependents, got %d", len(dependents))
	}
}

func TestAddEdgeRejectsUnknownNodes(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "file:a.ts", Kind: File, Path: "a.ts"})
	if err := g.AddEdge(Edge{From: "file:a.ts", To: "file:ghost.ts"}); err == nil {
		t.Fatal("expected an error for an edge to a node that does not exist")
	}
}

func TestStatsCountsByKind(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: NodeID(File, "a.ts"), Kind: File, Path: "a.ts"})
	g.AddNode(&Node{ID: NodeID(Package, "next@15.5.9"), Kind: Package, Name: "next", Version: "15.5.9"})
	g.AddNode(&Node{ID: NodeID(Builtin, "node:fs"), Kind: Builtin, Name: "node:fs"})
	g.AddNode(&Node{ID: NodeID(Unresolved, "@/gone"), Kind: Unresolved, Name: "@/gone"})

	s := g.Stats()
	for kind, want := range map[Kind]int{File: 1, Package: 1, Builtin: 1, Unresolved: 1} {
		if s[kind] != want {
			t.Errorf("Stats()[%v] = %d, want %d", kind, s[kind], want)
		}
	}
}

// AddNode must be idempotent - the same file is reached from many places.
func TestAddNodeIsIdempotent(t *testing.T) {
	g := New()
	a := g.AddNode(&Node{ID: NodeID(File, "a.ts"), Kind: File, Path: "a.ts"})
	b := g.AddNode(&Node{ID: NodeID(File, "a.ts"), Kind: File, Path: "a.ts"})
	if a != b {
		t.Error("re-adding an existing ID should return the existing node")
	}
	if len(g.Order) != 1 {
		t.Errorf("Order has %d entries, want 1", len(g.Order))
	}
}

// A duplicate edge must not create a permanent deficit that looks like a cycle.
func TestTopoOrderHandlesDuplicateEdges(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: NodeID(File, "a.ts"), Kind: File, Path: "a.ts"})
	g.AddNode(&Node{ID: NodeID(File, "b.ts"), Kind: File, Path: "b.ts"})
	// The same file imported on two lines produces two edges.
	for _, line := range []int{3, 7} {
		if err := g.AddEdge(Edge{From: NodeID(File, "a.ts"), To: NodeID(File, "b.ts"), Kind: Import, Line: line}); err != nil {
			t.Fatal(err)
		}
	}
	order, err := g.TopoOrder()
	if err != nil {
		t.Fatalf("duplicate edges should not look like a cycle: %v", err)
	}
	if len(order) != 2 || order[0].ID != NodeID(File, "b.ts") {
		t.Errorf("order = %v, want b.ts then a.ts", order)
	}
}

// The reported cycle must be stable across runs.
func TestCycleErrorIsDeterministic(t *testing.T) {
	var first string
	for run := 0; run < 20; run++ {
		g := build(t,
			[]string{"a.ts", "b.ts", "c.ts", "d.ts"},
			map[string][]string{
				"a.ts": {"b.ts"},
				"b.ts": {"c.ts"},
				"c.ts": {"a.ts"},
				"d.ts": {"a.ts"},
			})
		_, err := g.TopoOrder()
		if err == nil {
			t.Fatal("expected a cycle")
		}
		if run == 0 {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("run %d reported %q, first run reported %q", run, err.Error(), first)
		}
	}
	t.Logf("stable cycle report: %s", first)
}

// A cycle deep inside a long chain must not overflow the stack.
func TestFindCycleSurvivesADeepChain(t *testing.T) {
	const depth = 40000
	g := New()
	id := func(i int) string { return NodeID(File, "f"+itoa(i)+".ts") }
	for i := 0; i < depth; i++ {
		g.AddNode(&Node{ID: id(i), Kind: File, Path: "f" + itoa(i) + ".ts"})
	}
	for i := 0; i < depth-1; i++ {
		g.AddEdge(Edge{From: id(i), To: id(i + 1), Kind: Import})
	}
	g.AddEdge(Edge{From: id(depth - 1), To: id(0), Kind: Import}) // close the loop

	_, err := g.TopoOrder()
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("expected a CycleError, got %v", err)
	}
	if len(ce.Path) != depth+1 {
		t.Errorf("cycle path has %d nodes, want %d", len(ce.Path), depth+1)
	}
}
