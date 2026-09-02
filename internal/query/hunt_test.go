package query

import (
	"testing"

	"github.com/MihaiPosea/cairn/internal/graph"
)

func fg(t *testing.T, order []string, edges map[string][]string) *graph.Graph {
	t.Helper()
	g := graph.New()
	for _, f := range order {
		g.AddNode(&graph.Node{ID: graph.NodeID(graph.File, f), Kind: graph.File, Path: f})
	}
	for from, tos := range edges {
		for _, to := range tos {
			if err := g.AddEdge(graph.Edge{
				From: graph.NodeID(graph.File, from),
				To:   graph.NodeID(graph.File, to),
				Kind: graph.Import,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return g
}

// SUSPECT: a library repo with no framework conventions. If nothing is
// recognised as an entry point, everything looks dead.
func TestLibraryRepoWithNoConventions(t *testing.T) {
	g := fg(t,
		[]string{"src/client.ts", "src/helpers.ts", "src/types.ts"},
		map[string][]string{"src/client.ts": {"src/helpers.ts", "src/types.ts"}})

	// With no manifest and no conventions, it must refuse rather than declare
	// the whole repo dead.
	rep := DeadFilesWith(g, nil, false)
	t.Logf("no manifest -> bail=%q dead=%d", rep.Bail, len(rep.Files))
	if rep.Bail == "" {
		t.Errorf("BUG: with no entry points it must refuse, not report %d dead files", len(rep.Files))
	}

	// With package.json declaring the entry, only the genuinely unreached file
	// is dead.
	rep2 := DeadFilesWith(g, []string{"src/client.ts"}, false)
	t.Logf("with manifest -> entries=%d dead=%d", len(rep2.Entries), len(rep2.Files))
	if rep2.Bail != "" {
		t.Errorf("a declared entry point should let the analysis run, got bail=%q", rep2.Bail)
	}
	if len(rep2.Files) != 0 {
		t.Errorf("everything is reachable from src/client.ts, got dead=%v", rep2.Files)
	}
}

// SUSPECT: a file that imports itself.
func TestSelfImport(t *testing.T) {
	g := fg(t, []string{"a.ts"}, map[string][]string{"a.ts": {"a.ts"}})

	b := BlastRadius(g, graph.NodeID(graph.File, "a.ts"))
	t.Logf("self-import blast=%v", SortedKeys(b.Affected))
	if len(b.Affected) != 0 {
		t.Errorf("BUG: a file should not be in its own blast radius, got %v", SortedKeys(b.Affected))
	}

	cycles := Cycles(g, AllEdges)
	t.Logf("self-import cycles=%d", len(cycles))
}

// SUSPECT: circular dependencies between packages, which npm permits.
func TestCircularPackageCostTerminates(t *testing.T) {
	g := graph.New()
	for _, n := range []string{"a", "b", "c"} {
		g.AddNode(&graph.Node{ID: graph.NodeID(graph.Package, n), Kind: graph.Package, Name: n, Bytes: 100})
	}
	pid := func(n string) string { return graph.NodeID(graph.Package, n) }
	g.AddEdge(graph.Edge{From: pid("a"), To: pid("b"), Kind: graph.Requires})
	g.AddEdge(graph.Edge{From: pid("b"), To: pid("c"), Kind: graph.Requires})
	g.AddEdge(graph.Edge{From: pid("c"), To: pid("a"), Kind: graph.Requires})

	c := PackageCost(g, "a")
	t.Logf("circular cost=%d packages, %d bytes", len(c.Packages), c.Bytes)
	if len(c.Packages) != 3 || c.Bytes != 300 {
		t.Errorf("BUG: circular package deps miscounted: %d packages, %d bytes", len(c.Packages), c.Bytes)
	}
}

// SUSPECT: a package and a file sharing a name.
func TestFileAndPackageSameName(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: graph.NodeID(graph.File, "utils"), Kind: graph.File, Path: "utils"})
	g.AddNode(&graph.Node{ID: graph.NodeID(graph.Package, "utils"), Kind: graph.Package, Name: "utils"})

	w := WhyPackage(g, "utils", nil)
	t.Logf("why utils -> %+v", w)
	if w == nil {
		t.Error("BUG: a package sharing a name with a file was not found")
	}
}

// SUSPECT: an empty graph must not panic.
func TestEmptyGraph(t *testing.T) {
	g := graph.New()
	if got := DeadFiles(g, false); len(got) != 0 {
		t.Errorf("empty graph should have no dead files, got %v", got)
	}
	if got := Cycles(g, AllEdges); len(got) != 0 {
		t.Errorf("empty graph should have no cycles, got %v", got)
	}
	if got := WhyPackage(g, "nope", nil); got != nil {
		t.Errorf("missing package should return nil, got %v", got)
	}
	if got := PackageCost(g, "nope"); got != nil {
		t.Errorf("missing package should return nil, got %v", got)
	}
}

// SUSPECT: blast radius on a package rather than a file.
func TestBlastOnAPackage(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: graph.NodeID(graph.File, "a.ts"), Kind: graph.File, Path: "a.ts"})
	g.AddNode(&graph.Node{ID: graph.NodeID(graph.Package, "react"), Kind: graph.Package, Name: "react"})
	g.AddEdge(graph.Edge{From: graph.NodeID(graph.File, "a.ts"), To: graph.NodeID(graph.Package, "react"), Kind: graph.Import})

	b := BlastRadius(g, graph.NodeID(graph.Package, "react"))
	t.Logf("blast on package -> %v", SortedKeys(b.Affected))
	if len(b.Affected) != 1 {
		t.Errorf("removing react should affect a.ts, got %v", SortedKeys(b.Affected))
	}
}
