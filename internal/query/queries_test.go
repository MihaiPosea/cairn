package query

import (
	"testing"

	"github.com/MihaiPosea/cairn/internal/graph"
)

// files builds a graph from file -> imports, with edge kinds.
func files(t *testing.T, order []string, edges map[string][]graph.Edge) *graph.Graph {
	t.Helper()
	g := graph.New()
	for _, f := range order {
		g.AddNode(&graph.Node{ID: graph.NodeID(graph.File, f), Kind: graph.File, Path: f})
	}
	for from, list := range edges {
		for _, e := range list {
			e.From = graph.NodeID(graph.File, from)
			if _, ok := g.Nodes[e.To]; !ok {
				t.Fatalf("edge target %q not in graph", e.To)
			}
			if err := g.AddEdge(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	return g
}

func fid(p string) string { return graph.NodeID(graph.File, p) }

func TestBlastRadiusIsTransitiveAndIncludesTypeOnly(t *testing.T) {
	g := files(t,
		[]string{"app/page.tsx", "components/Button.tsx", "lib/utils.ts", "lib/types.ts", "unrelated.ts"},
		map[string][]graph.Edge{
			"app/page.tsx":          {{To: fid("components/Button.tsx"), Kind: graph.Import}},
			"components/Button.tsx": {{To: fid("lib/utils.ts"), Kind: graph.Import}, {To: fid("lib/types.ts"), Kind: graph.TypeOnly}},
		})

	b := BlastRadius(g, fid("lib/utils.ts"))
	if len(b.Affected) != 2 {
		t.Fatalf("affected = %v, want Button and page", SortedKeys(b.Affected))
	}
	if len(b.Direct) != 1 || b.Direct[0] != fid("components/Button.tsx") {
		t.Errorf("direct = %v, want just Button.tsx", b.Direct)
	}

	// Changing a type breaks the compile of whoever imports it.
	bt := BlastRadius(g, fid("lib/types.ts"))
	if _, ok := bt.Affected[fid("components/Button.tsx")]; !ok {
		t.Error("a type-only importer must appear in the blast radius - changing a type breaks its compile")
	}
}

func TestEntryPointsRecogniseFrameworkConventions(t *testing.T) {
	g := files(t, []string{
		"app/page.tsx", "app/layout.tsx", "app/api/x/route.ts",
		"pages/about.tsx", "middleware.ts", "next.config.mjs",
		"components/Button.tsx", "lib/utils.ts", "next-env.d.ts",
		"scripts/seed.ts", "lib/utils.test.ts",
	}, nil)

	got := map[string]string{}
	for _, e := range EntryPoints(g) {
		got[g.Nodes[e.File].Path] = e.Reason
	}
	for _, want := range []string{
		"app/page.tsx", "app/layout.tsx", "app/api/x/route.ts", "pages/about.tsx",
		"middleware.ts", "next.config.mjs", "next-env.d.ts", "scripts/seed.ts",
		"lib/utils.test.ts",
	} {
		if got[want] == "" {
			t.Errorf("%s should be an entry point", want)
		}
	}
	for _, notEntry := range []string{"components/Button.tsx", "lib/utils.ts"} {
		if got[notEntry] != "" {
			t.Errorf("%s should not be an entry point (it is reached by imports, not loaded directly)", notEntry)
		}
	}
}

func TestDeadFilesRespectsEntryPointsAndTypeOnlyEdges(t *testing.T) {
	g := files(t,
		[]string{"app/page.tsx", "components/Button.tsx", "lib/types.ts", "orphan.ts"},
		map[string][]graph.Edge{
			"app/page.tsx":          {{To: fid("components/Button.tsx"), Kind: graph.Import}},
			"components/Button.tsx": {{To: fid("lib/types.ts"), Kind: graph.TypeOnly}},
		})

	dead := DeadFiles(g, false)
	if len(dead) != 1 || dead[0].File != fid("orphan.ts") {
		t.Fatalf("dead = %v, want only orphan.ts", dead)
	}
}

// A file reachable only through a type-only import is not dead: deleting it
// breaks the build.
func TestTypeOnlyImportKeepsAFileAlive(t *testing.T) {
	g := files(t,
		[]string{"app/page.tsx", "lib/types.ts"},
		map[string][]graph.Edge{
			"app/page.tsx": {{To: fid("lib/types.ts"), Kind: graph.TypeOnly}},
		})
	if dead := DeadFiles(g, false); len(dead) != 0 {
		t.Errorf("got %v, want nothing dead", dead)
	}
}

func TestDeadFilesLowersConfidenceWithComputedImports(t *testing.T) {
	g := files(t, []string{"app/page.tsx", "orphan.ts"}, nil)
	dead := DeadFiles(g, true)
	if len(dead) != 1 {
		t.Fatalf("want 1 dead file, got %d", len(dead))
	}
	if dead[0].Why == "no entry point reaches it" {
		t.Error("confidence should be lowered when the repo has computed import() calls")
	}
}

func TestWhyPackageReturnsAPathNotAYesNo(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: fid("app/page.tsx"), Kind: graph.File, Path: "app/page.tsx"})
	for _, p := range []string{"a", "b", "left-pad"} {
		g.AddNode(&graph.Node{ID: graph.NodeID(graph.Package, p), Kind: graph.Package, Name: p})
	}
	g.AddEdge(graph.Edge{From: fid("app/page.tsx"), To: graph.NodeID(graph.Package, "a"), Kind: graph.Import})
	g.AddEdge(graph.Edge{From: graph.NodeID(graph.Package, "a"), To: graph.NodeID(graph.Package, "b"), Kind: graph.Requires})
	g.AddEdge(graph.Edge{From: graph.NodeID(graph.Package, "b"), To: graph.NodeID(graph.Package, "left-pad"), Kind: graph.Requires})

	w := WhyPackage(g, "left-pad", nil)
	if w == nil || len(w.Path) != 4 {
		t.Fatalf("want a 4-hop path from the file to left-pad, got %v", w)
	}
	if w.Direct {
		t.Error("left-pad is not directly imported")
	}
	if w.Path[0] != fid("app/page.tsx") || w.Path[3] != graph.NodeID(graph.Package, "left-pad") {
		t.Errorf("path should run file -> ... -> left-pad, got %v", w.Path)
	}
}

func TestPackageCostIsTransitive(t *testing.T) {
	g := graph.New()
	add := func(name string, bytes int64) {
		g.AddNode(&graph.Node{ID: graph.NodeID(graph.Package, name), Kind: graph.Package, Name: name, Bytes: bytes})
	}
	add("big", 1000)
	add("dep1", 500)
	add("dep2", 250)
	add("unrelated", 9999)
	g.AddEdge(graph.Edge{From: graph.NodeID(graph.Package, "big"), To: graph.NodeID(graph.Package, "dep1"), Kind: graph.Requires})
	g.AddEdge(graph.Edge{From: graph.NodeID(graph.Package, "dep1"), To: graph.NodeID(graph.Package, "dep2"), Kind: graph.Requires})

	c := PackageCost(g, "big")
	if len(c.Packages) != 3 {
		t.Fatalf("packages = %v, want big + dep1 + dep2", c.Packages)
	}
	if c.Bytes != 1750 {
		t.Errorf("bytes = %d, want 1750", c.Bytes)
	}
	if !c.Measured {
		t.Error("Measured should be true when sizes are present")
	}
}

func TestCyclesFindsRealLoopsOnly(t *testing.T) {
	g := files(t,
		[]string{"a.ts", "b.ts", "c.ts", "straight.ts", "leaf.ts"},
		map[string][]graph.Edge{
			"a.ts":        {{To: fid("b.ts"), Kind: graph.Import}},
			"b.ts":        {{To: fid("c.ts"), Kind: graph.Import}},
			"c.ts":        {{To: fid("a.ts"), Kind: graph.Import}},
			"straight.ts": {{To: fid("leaf.ts"), Kind: graph.Import}},
		})

	cycles := Cycles(g, AllEdges)
	if len(cycles) != 1 {
		t.Fatalf("got %d cycles, want 1: %v", len(cycles), cycles)
	}
	if len(cycles[0].Nodes) != 3 {
		t.Errorf("cycle = %v, want a.ts b.ts c.ts", cycles[0].Nodes)
	}
}

// Tarjan must be iterative: a deep chain would blow a recursive stack, and
// crashing on a big repo is the one failure this tool cannot afford.
func TestCyclesSurvivesADeepChain(t *testing.T) {
	const depth = 60000
	g := graph.New()
	name := func(i int) string { return graph.NodeID(graph.File, "f"+itoa(i)+".ts") }
	for i := 0; i < depth; i++ {
		g.AddNode(&graph.Node{ID: name(i), Kind: graph.File, Path: "f" + itoa(i) + ".ts"})
	}
	for i := 0; i < depth-1; i++ {
		g.AddEdge(graph.Edge{From: name(i), To: name(i + 1), Kind: graph.Import})
	}
	if cycles := Cycles(g, AllEdges); len(cycles) != 0 {
		t.Errorf("a straight chain has no cycles, got %d", len(cycles))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
