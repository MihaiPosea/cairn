package graph

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

// randomDAG builds an acyclic graph: edges only ever point from a higher index
// to a lower one, so a cycle is impossible by construction.
func randomDAG(rng *rand.Rand, n, maxOut int) *Graph {
	g := New()
	for i := 0; i < n; i++ {
		id := NodeID(File, fmt.Sprintf("f%d.ts", i))
		g.AddNode(&Node{ID: id, Kind: File, Path: fmt.Sprintf("f%d.ts", i)})
	}
	for i := 0; i < n; i++ {
		for k := 0; k < rng.Intn(maxOut+1); k++ {
			j := rng.Intn(i + 1)
			if j == i {
				continue
			}
			_ = g.AddEdge(Edge{
				From: NodeID(File, fmt.Sprintf("f%d.ts", i)),
				To:   NodeID(File, fmt.Sprintf("f%d.ts", j)),
				Kind: Import,
			})
		}
	}
	return g
}

// On any acyclic graph, TopoOrder must succeed, emit every node exactly once,
// and place each node after everything it depends on.
func TestTopoOrderPropertiesOnRandomDAGs(t *testing.T) {
	for seed := int64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewSource(seed))
		n := 1 + rng.Intn(120)
		g := randomDAG(rng, n, 5)

		order, err := g.TopoOrder()
		if err != nil {
			t.Fatalf("seed %d: acyclic graph reported a cycle: %v", seed, err)
		}
		if len(order) != len(g.Nodes) {
			t.Fatalf("seed %d: emitted %d nodes, graph has %d", seed, len(order), len(g.Nodes))
		}

		seen := map[string]int{}
		for i, node := range order {
			if _, dup := seen[node.ID]; dup {
				t.Fatalf("seed %d: %s emitted twice", seed, node.ID)
			}
			seen[node.ID] = i
		}
		for _, node := range order {
			for _, e := range g.Dependencies(node.ID) {
				if seen[e.To] > seen[node.ID] {
					t.Fatalf("seed %d: %s at %d precedes its dependency %s at %d",
						seed, node.ID, seen[node.ID], e.To, seen[e.To])
				}
			}
		}
	}
}

// Adding a back edge to an acyclic graph must always be detected, and the
// reported path must be a real walk through the graph.
func TestCycleDetectionOnRandomGraphs(t *testing.T) {
	for seed := int64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewSource(seed))
		n := 3 + rng.Intn(80)
		g := randomDAG(rng, n, 4)

		// Close a loop: a low node depends back on a high one.
		lo, hi := 0, n-1
		if err := g.AddEdge(Edge{
			From: NodeID(File, fmt.Sprintf("f%d.ts", lo)),
			To:   NodeID(File, fmt.Sprintf("f%d.ts", hi)),
			Kind: Import,
		}); err != nil {
			t.Fatal(err)
		}
		// Guarantee a path back down, so the loop really exists.
		_ = g.AddEdge(Edge{
			From: NodeID(File, fmt.Sprintf("f%d.ts", hi)),
			To:   NodeID(File, fmt.Sprintf("f%d.ts", lo)),
			Kind: Import,
		})

		_, err := g.TopoOrder()
		var ce *CycleError
		if !errors.As(err, &ce) {
			t.Fatalf("seed %d: a genuine cycle was not reported, got %v", seed, err)
		}
		if len(ce.Path) < 3 {
			t.Fatalf("seed %d: path %v is too short for a loop", seed, ce.Path)
		}
		if ce.Path[0] != ce.Path[len(ce.Path)-1] {
			t.Fatalf("seed %d: path %v does not close", seed, ce.Path)
		}
		for i := 0; i < len(ce.Path)-1; i++ {
			found := false
			for _, e := range g.Dependencies(ce.Path[i]) {
				if e.To == ce.Path[i+1] {
					found = true
				}
			}
			if !found {
				t.Fatalf("seed %d: reported hop %s -> %s is not a real edge",
					seed, ce.Path[i], ce.Path[i+1])
			}
		}
	}
}

// The same graph must always produce the same answer, cycle or not.
func TestTopoOrderIsStableAcrossRuns(t *testing.T) {
	for seed := int64(0); seed < 60; seed++ {
		rng := rand.New(rand.NewSource(seed))
		g := randomDAG(rng, 1+rng.Intn(60), 4)

		first, err := g.TopoOrder()
		if err != nil {
			t.Fatal(err)
		}
		for run := 0; run < 5; run++ {
			again, err := g.TopoOrder()
			if err != nil {
				t.Fatal(err)
			}
			for i := range first {
				if first[i].ID != again[i].ID {
					t.Fatalf("seed %d run %d: position %d changed between runs (%s vs %s)",
						seed, run, i, again[i].ID, first[i].ID)
				}
			}
		}
	}
}

// A complete graph is the worst realistic shape: every node depends on every
// earlier one. It must finish, not hang.
func TestCompleteGraphTerminates(t *testing.T) {
	const n = 400
	g := New()
	for i := 0; i < n; i++ {
		g.AddNode(&Node{ID: NodeID(File, fmt.Sprintf("f%d", i)), Kind: File})
	}
	for i := 0; i < n; i++ {
		for j := 0; j < i; j++ {
			_ = g.AddEdge(Edge{
				From: NodeID(File, fmt.Sprintf("f%d", i)),
				To:   NodeID(File, fmt.Sprintf("f%d", j)),
				Kind: Import,
			})
		}
	}
	order, err := g.TopoOrder()
	if err != nil {
		t.Fatalf("a complete DAG has a valid order, got: %v", err)
	}
	if len(order) != n {
		t.Errorf("emitted %d of %d nodes", len(order), n)
	}
}
