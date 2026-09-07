package scan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/query"
)

// fixture writes a small but realistic Next.js-shaped repo.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"package.json": `{"name":"fix","dependencies":{"react":"^19.0.0","unused-dep":"^1.0.0"}}`,
		"tsconfig.json": `{
  // path alias, Next.js style
  "compilerOptions": { "paths": { "@/*": ["./*"] } },
}`,
		"app/page.tsx": `import { Button } from "@/components/Button";
import type { Config } from "@/lib/types";
import { helper } from "./helper";
import React from "react";
export default function Page() { return <Button />; }`,
		"app/helper.ts":         `export const helper = 1;`,
		"app/layout.tsx":        `import "./globals.css";`,
		"app/globals.css":       `body { margin: 0 }`,
		"components/Button.tsx": `import { fmt } from "@/lib/utils"; export const Button = () => null;`,
		"lib/utils.ts":          `import fs from "node:fs"; export const fmt = () => fs;`,
		"lib/types.ts":          `export type Config = { a: number };`,
		"lib/orphan.ts":         `export const nobody = 1;`,

		// installed packages, no lockfile - exercises the node_modules fallback
		"node_modules/react/package.json":      `{"name":"react","version":"19.0.0","dependencies":{"scheduler":"^0.25.0"}}`,
		"node_modules/scheduler/package.json":  `{"name":"scheduler","version":"0.25.0"}`,
		"node_modules/unused-dep/package.json": `{"name":"unused-dep","version":"1.0.0"}`,
	}
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestEndToEnd(t *testing.T) {
	res, err := Run(fixture(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.FilesScanned != 7 {
		t.Errorf("files scanned = %d, want 7", res.FilesScanned)
	}
	if !res.AliasesLoaded {
		t.Error("tsconfig aliases should have loaded")
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("unresolved should be empty, got %v", res.Unresolved)
	}

	// node_modules must never enter the file graph.
	for _, id := range res.Graph.IDs() {
		if n := res.Graph.Nodes[id]; n.Kind == graph.File && filepath.HasPrefix(n.Path, "node_modules") {
			t.Errorf("node_modules leaked into the file graph: %s", n.Path)
		}
	}

	// The css import resolves to a real file node.
	if _, ok := res.Graph.Nodes[graph.NodeID(graph.File, "app/globals.css")]; !ok {
		t.Error("globals.css should be a file node")
	}
	// node:fs is a builtin, not a package.
	if n, ok := res.Graph.Nodes[graph.NodeID(graph.Builtin, "node:fs")]; !ok || n.Kind != graph.Builtin {
		t.Error("node:fs should be a builtin node")
	}
}

func TestEndToEndPackagesAndJoin(t *testing.T) {
	res, err := Run(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Packages == nil {
		t.Fatal("packages should have loaded from node_modules")
	}
	if len(res.Packages.Packages) != 3 {
		t.Errorf("installed = %d, want 3", len(res.Packages.Packages))
	}
	if res.Join == nil {
		t.Fatal("join report missing")
	}
	if len(res.Join.UnusedDeclared) != 1 || res.Join.UnusedDeclared[0] != "unused-dep" {
		t.Errorf("unused declared = %v, want [unused-dep]", res.Join.UnusedDeclared)
	}
	if len(res.Join.ImportedNotDeclared) != 0 {
		t.Errorf("imported-not-declared = %v, want none", res.Join.ImportedNotDeclared)
	}
}

func TestEndToEndQueries(t *testing.T) {
	res, err := Run(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	g := res.Graph

	// lib/utils.ts is imported by Button, which is imported by page.
	b := query.BlastRadius(g, graph.NodeID(graph.File, "lib/utils.ts"))
	if len(b.Affected) != 2 {
		t.Errorf("blast radius = %v, want Button.tsx and app/page.tsx", query.SortedKeys(b.Affected))
	}

	// Only lib/orphan.ts is unreachable. lib/types.ts is reached by a
	// type-only import and must NOT be reported dead.
	dead := query.DeadFiles(g, false)
	if len(dead) != 1 || dead[0].File != graph.NodeID(graph.File, "lib/orphan.ts") {
		t.Errorf("dead = %v, want only lib/orphan.ts", dead)
	}

	// scheduler is nobody's direct import; it rides in behind react, which
	// app/page.tsx does import. So the chain starts at your own code.
	w := query.WhyPackage(g, "scheduler", []string{"react", "unused-dep"})
	if w == nil || len(w.Path) != 3 {
		t.Fatalf("why scheduler = %+v, want page.tsx -> react -> scheduler", w)
	}
	if w.From != "your code" || w.Direct {
		t.Errorf("scheduler should be reachable from your code but not imported directly, got %+v", w)
	}

	c := query.PackageCost(g, "react")
	if len(c.Packages) != 2 {
		t.Errorf("react cost = %v, want react + scheduler", c.Packages)
	}
}

// Two scans of an unchanged repo must produce identical output.
func TestScanIsDeterministic(t *testing.T) {
	root := fixture(t)
	first, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		next, err := Run(root)
		if err != nil {
			t.Fatal(err)
		}
		a, b := first.Graph.IDs(), next.Graph.IDs()
		if len(a) != len(b) {
			t.Fatalf("run %d produced %d nodes, first produced %d", i, len(b), len(a))
		}
		for j := range a {
			if a[j] != b[j] {
				t.Fatalf("run %d differs at node %d: %s vs %s", i, j, b[j], a[j])
			}
		}
	}
}

// A warm cache must produce exactly the same graph as a cold one.
func TestCacheDoesNotChangeResults(t *testing.T) {
	root := fixture(t)

	cold, err := RunWith(root, Options{NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	// First cached run populates; second reads it back.
	if _, err := Run(root); err != nil {
		t.Fatal(err)
	}
	warm, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}

	if warm.CacheHits == 0 {
		t.Error("second run should have hit the cache")
	}
	if warm.CacheMisses != 0 {
		t.Errorf("nothing changed, so there should be no misses, got %d", warm.CacheMisses)
	}
	if warm.ImportsFound != cold.ImportsFound {
		t.Errorf("cached run found %d imports, uncached found %d", warm.ImportsFound, cold.ImportsFound)
	}

	a, b := cold.Graph.IDs(), warm.Graph.IDs()
	if len(a) != len(b) {
		t.Fatalf("cached graph has %d nodes, uncached has %d", len(b), len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("node %d differs: cached %s, uncached %s", i, b[i], a[i])
		}
	}
}

// Editing a file must invalidate exactly that file.
func TestEditInvalidatesOnlyThatFile(t *testing.T) {
	root := fixture(t)
	if _, err := Run(root); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(root, "lib", "orphan.ts")
	if err := os.WriteFile(target, []byte(`import "./utils"; export const nobody = 2;`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheMisses != 1 {
		t.Errorf("one edited file should cause exactly 1 cache miss, got %d", res.CacheMisses)
	}
	if res.CacheHits != res.FilesScanned-1 {
		t.Errorf("hits=%d, want %d (every other file)", res.CacheHits, res.FilesScanned-1)
	}
}

// "build", "dist" and "out" are names for generated output and also perfectly
// ordinary names for source directories. Skipping them wherever they appear
// silently loses real code, and silently is the operative word: a directory
// that is never descended into leaves no trace in any count or warning.
//
// Found by sweeping 34 repositories - astro keeps a hundred lines of
// hand-written TypeScript in packages/astro/src/core/build/, and every one of
// them was invisible.
func TestSourceDirectoriesNamedLikeOutputAreStillScanned(t *testing.T) {
	root := t.TempDir()
	write := func(p, body string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("pkg/package.json", `{"name":"pkg"}`)
	// Real source that happens to live under directories named like output.
	write("pkg/src/index.ts", `import "./build/analyzer"; import "./out/writer";`)
	write("pkg/src/build/analyzer.ts", `export const analyze = 1;`)
	write("pkg/src/out/writer.ts", `export const write = 1;`)
	// Actual build output, beside the manifest it was built from.
	write("pkg/dist/index.js", `export const generated = 1;`)
	write("pkg/build/bundle.js", `export const bundled = 1;`)

	res, err := RunWith(root, Options{SkipPackages: true, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, n := range res.Graph.Nodes {
		seen[n.Path] = true
	}
	for _, want := range []string{
		"pkg/src/index.ts", "pkg/src/build/analyzer.ts", "pkg/src/out/writer.ts",
	} {
		if !seen[want] {
			t.Errorf("%s is source and was not scanned", want)
		}
	}
	for _, notWant := range []string{"pkg/dist/index.js", "pkg/build/bundle.js"} {
		if seen[notWant] {
			t.Errorf("%s is build output beside its package.json and should be skipped", notWant)
		}
	}
	// And the imports into those directories must resolve, or the files are
	// present but disconnected, which is barely better than missing.
	if res.UnresolvedRate() > 0 {
		t.Errorf("imports into src/build and src/out should resolve, rate is %.2f%%: %v",
			res.UnresolvedRate()*100, res.Unresolved)
	}
}
