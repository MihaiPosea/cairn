package affected

import (
	"testing"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/scan"
)

func graphOf(t *testing.T, order []string, imports map[string][]string) *scan.Result {
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

// A deleted file is no longer in the graph, so its impact cannot be computed.
// Bailing is the only safe answer.
func TestDeletedFileBails(t *testing.T) {
	res := graphOf(t,
		[]string{"app/page.tsx", "lib/utils.ts"},
		map[string][]string{"app/page.tsx": {"lib/utils.ts"}})

	got := Compute(res, []string{"lib/deleted.ts"})
	t.Logf("deleted -> bail=%q unknown=%v", got.Bail, got.Unknown)
	if got.Bail == "" {
		t.Error("BUG: a deleted file must bail - whatever imported it is now broken and unfindable")
	}
}

// A rename is a delete plus an add. The new path is in the graph; the old one
// is not, so it must still bail.
func TestRenameBails(t *testing.T) {
	res := graphOf(t,
		[]string{"app/page.tsx", "lib/renamed.ts"},
		map[string][]string{"app/page.tsx": {"lib/renamed.ts"}})

	got := Compute(res, []string{"lib/original.ts", "lib/renamed.ts"})
	t.Logf("rename -> bail=%q unknown=%v", got.Bail, got.Unknown)
	if got.Bail == "" {
		t.Error("BUG: the disappeared half of a rename must bail")
	}
}

// A non-code file that is genuinely irrelevant still bails, because cairn
// cannot know it is irrelevant. Better slow than wrong.
func TestUnrelatedNonCodeFileStillBails(t *testing.T) {
	res := graphOf(t, []string{"app/page.tsx"}, nil)
	got := Compute(res, []string{"README.md"})
	t.Logf("README -> bail=%q", got.Bail)
	if got.Bail == "" {
		t.Error("a file cairn cannot see must bail; it has no way to know README.md is harmless")
	}
}

// Reduction figures must never exceed 100% or go negative.
func TestReductionStaysInRange(t *testing.T) {
	res := graphOf(t,
		[]string{"a.ts", "b.ts", "c.ts"},
		map[string][]string{"a.ts": {"b.ts"}, "b.ts": {"c.ts"}})

	for _, changed := range [][]string{{}, {"c.ts"}, {"a.ts"}, {"a.ts", "b.ts", "c.ts"}} {
		got := Compute(res, changed)
		r, tr := got.Reduction(), got.TestReduction()
		t.Logf("changed=%v -> affected=%d reduction=%.2f testReduction=%.2f", changed, len(got.Affected), r, tr)
		if r < 0 || r > 1 || tr < 0 || tr > 1 {
			t.Errorf("out of range: reduction=%.3f testReduction=%.3f", r, tr)
		}
	}
}
