package scan

import (
	"strings"
	"testing"
)

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
		if c.Category == "build output with no source equivalent — run the repo's build" {
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

// The importing file's location says more than the specifier does. A test
// fixture importing a component that deliberately does not exist is not a
// finding, and calling it one buries the findings that matter.
func TestFixturesAndTemplatesAreNamedForWhatTheyAre(t *testing.T) {
	for _, tc := range []struct {
		name, file, spec, want string
	}{
		{"svelte compiler fixture",
			"packages/svelte/tests/compiler-errors/samples/x/main.svelte", "./Component.svelte",
			"test fixtures — these imports are meant to fail"},
		{"codemod fixture",
			"packages/codemods/src/__testfixtures__/a.input.tsx", "../another/module",
			"test fixtures — these imports are meant to fail"},
		{"scaffolding template",
			"cli/template/extras/src/app/page.tsx", "./index.module.css",
			"scaffolding templates — the files appear when the template is used"},
		{"next codegen",
			"apps/web/next-env.d.ts", "./.next/types/routes.d.ts",
			"codegen output — written by a framework or generator, not committed"},
		{"prisma codegen",
			"src/db.ts", "../generated/client",
			"codegen output — written by a framework or generator, not committed"},
		{"native binary",
			"src/native/bindings.js", "./nx.win32-x64-msvc.node",
			"native binaries for other platforms"},
	} {
		got := ClusterUnresolved([]Unresolvable{{File: tc.file, Specifier: tc.spec}})
		if len(got) != 1 {
			t.Fatalf("%s: expected one cluster", tc.name)
		}
		if got[0].Category != tc.want {
			t.Errorf("%s:\n  got  %q\n  want %q", tc.name, got[0].Category, tc.want)
		}
	}
}

// A real source file importing something missing must NOT be excused as a
// fixture just because a sibling group was.
func TestRealCodeIsNotExcused(t *testing.T) {
	got := ClusterUnresolved([]Unresolvable{
		{File: "src/app/page.tsx", Specifier: "./missing-component"},
	})
	if got[0].Category != "relative path with no matching file" {
		t.Errorf("real source code got excused as %q", got[0].Category)
	}
}

// A group mixing a fixture and real code must not be labelled a fixture.
func TestMixedGroupIsNotExcused(t *testing.T) {
	got := ClusterUnresolved([]Unresolvable{
		{File: "tests/samples/a.svelte", Specifier: "./Thing"},
		{File: "src/real.ts", Specifier: "./Thing"},
	})
	if strings.Contains(got[0].Category, "test fixtures") {
		t.Errorf("a group containing real code was excused as a fixture: %q", got[0].Category)
	}
}

// A large cluster is systematic absence; a small one is a real mistake.
func TestSystematicAbsenceIsDistinguishedFromAMistake(t *testing.T) {
	var many []Unresolvable
	for i := 0; i < 12; i++ {
		many = append(many, Unresolvable{
			File: "src/app/c" + string(rune('a'+i)) + ".tsx", Specifier: "@/styles/base-nova/ui/x",
		})
	}
	if got := ClusterUnresolved(many)[0].Category; !strings.Contains(got, "entire directory tree") {
		t.Errorf("12 imports of the same absent tree got %q", got)
	}

	// Two of them is a mistake, not a generator.
	few := many[:2]
	if got := ClusterUnresolved(few)[0].Category; strings.Contains(got, "entire directory tree") {
		t.Errorf("2 imports should not be excused as codegen, got %q", got)
	}
}
