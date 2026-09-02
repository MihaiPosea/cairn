package affected

import (
	"testing"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/scan"
)

func mk(t *testing.T, order []string, imports map[string][]string) *scan.Result {
	t.Helper()
	g := graph.New()
	for _, f := range order {
		g.AddNode(&graph.Node{ID: graph.NodeID(graph.File, f), Kind: graph.File, Path: f})
	}
	for from, tos := range imports {
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
	return &scan.Result{Graph: g, FilesScanned: len(order)}
}

var (
	order = []string{
		"app/page.tsx", "components/Button.tsx", "lib/utils.ts", "lib/unrelated.ts",
		"lib/utils.test.ts", "lib/unrelated.test.ts", "components/Button.test.tsx",
	}
	imports = map[string][]string{
		"app/page.tsx":              {"components/Button.tsx"},
		"components/Button.tsx":     {"lib/utils.ts"},
		"lib/utils.test.ts":         {"lib/utils.ts"},
		"lib/unrelated.test.ts":     {"lib/unrelated.ts"},
		"components/Button.test.tsx": {"components/Button.tsx"},
	}
)

func TestOnlyAffectedTestsAreSelected(t *testing.T) {
	res := mk(t, order, imports)
	got := Compute(res, []string{"lib/utils.ts"})

	if got.Bail != "" {
		t.Fatalf("should not have bailed: %s", got.Bail)
	}
	want := map[string]bool{"lib/utils.test.ts": true, "components/Button.test.tsx": true}
	if len(got.Tests) != len(want) {
		t.Fatalf("tests = %v, want %v", got.Tests, want)
	}
	for _, tst := range got.Tests {
		if !want[tst] {
			t.Errorf("selected %s, which does not depend on the change", tst)
		}
	}
	if got.TotalTests != 3 {
		t.Errorf("TotalTests = %d, want 3", got.TotalTests)
	}
}

func TestUnrelatedChangeSelectsOnlyItsOwnTest(t *testing.T) {
	got := Compute(mk(t, order, imports), []string{"lib/unrelated.ts"})
	if len(got.Tests) != 1 || got.Tests[0] != "lib/unrelated.test.ts" {
		t.Errorf("tests = %v, want only lib/unrelated.test.ts", got.Tests)
	}
}

// An unknown changed path must force running everything. A config file can
// affect anything, and a wrong small answer is worse than a right large one.
func TestUnknownFileBails(t *testing.T) {
	got := Compute(mk(t, order, imports), []string{"tsconfig.json"})
	if got.Bail == "" {
		t.Error("a changed file that is not in the graph must bail")
	}
	if len(got.Unknown) != 1 {
		t.Errorf("Unknown = %v, want [tsconfig.json]", got.Unknown)
	}
}

// An incomplete graph must never produce a confident small answer.
func TestUnresolvedImportsBail(t *testing.T) {
	res := mk(t, order, imports)
	res.Unresolved = []scan.Unresolvable{{File: "a.ts", Specifier: "./missing"}}
	if got := Compute(res, []string{"lib/utils.ts"}); got.Bail == "" {
		t.Error("unresolved imports mean the graph is incomplete, so it must bail")
	}
}

func TestComputedImportsBail(t *testing.T) {
	res := mk(t, order, imports)
	res.Unanalyzable = []scan.Unresolvable{{File: "a.ts", Reason: "computed"}}
	if got := Compute(res, []string{"lib/utils.ts"}); got.Bail == "" {
		t.Error("computed import() means files can be loaded invisibly, so it must bail")
	}
}

func TestReductionMath(t *testing.T) {
	got := Compute(mk(t, order, imports), []string{"lib/unrelated.ts"})
	// affected: unrelated.ts + unrelated.test.ts = 2 of 7 files
	if len(got.Affected) != 2 {
		t.Fatalf("affected = %v, want 2 files", got.Affected)
	}
	if r := got.Reduction(); r < 0.71 || r > 0.72 {
		t.Errorf("reduction = %.3f, want ~0.714", r)
	}
	if r := got.TestReduction(); r < 0.66 || r > 0.67 {
		t.Errorf("test reduction = %.3f, want ~0.667", r)
	}
}
