package scan

import "testing"

func TestClusterGroupsByRealCause(t *testing.T) {
	items := []Unresolvable{
		{File: "a.ts", Specifier: "../../../dist/core/x"},
		{File: "b.ts", Specifier: "../../../dist/core/y"},
		{File: "c.ts", Specifier: "../../dist/core/z"},
		{File: "d.ts", Specifier: "@/styles/base/one"},
		{File: "e.ts", Specifier: "@/styles/base/two"},
		{File: "f.ts", Specifier: "./missing"},
	}
	clusters := ClusterUnresolved(items)

	if len(clusters) == 0 {
		t.Fatal("expected clusters")
	}
	// Leading ../ must not swallow the meaningful segment.
	for _, c := range clusters {
		if c.Prefix == "../../…" || c.Prefix == "../…" {
			t.Errorf("relative prefix hid the cause: %q", c.Prefix)
		}
	}
	var dist int
	for _, c := range clusters {
		if c.Category == "points into a build output — run the repo's build first" {
			dist += c.Count
		}
	}
	if dist != 3 {
		t.Errorf("build-output imports counted as %d, want 3", dist)
	}
}

func TestUnresolvedSummaryAggregatesByCategory(t *testing.T) {
	res := &Result{}
	// Same cause, many different prefixes: no single group dominates, but the
	// category does.
	for _, p := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		res.Unresolved = append(res.Unresolved, Unresolvable{
			File: p + ".ts", Specifier: "../../" + p + "/dist/thing",
		})
	}
	got := res.UnresolvedSummary()
	t.Logf("summary: %s", got)
	if got == "" {
		t.Error("a dominant category must produce a summary even when no single group does")
	}
}

func TestNoSummaryWhenCausesAreMixed(t *testing.T) {
	res := &Result{Unresolved: []Unresolvable{
		{File: "a.ts", Specifier: "../dist/x"},
		{File: "b.ts", Specifier: "./missing"},
		{File: "c.ts", Specifier: "not-a-package name"},
	}}
	if got := res.UnresolvedSummary(); got != "" {
		t.Errorf("mixed causes should produce no summary, got %q", got)
	}
}
