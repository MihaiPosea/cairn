package jsts

import (
	"testing"

	"github.com/MihaiPosea/cairn/internal/lang"
)

func parse(t *testing.T, path, src string) []lang.RawImport {
	t.Helper()
	imps, err := New().Parse(path, []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return imps
}

func TestExtractsEveryImportForm(t *testing.T) {
	src := `import React from "react";
import type { Foo } from "./types";
import { a } from "@/lib/utils";
import "./side-effect.css";
export { x } from "./x";
export * from "./star";
export type { T } from "./t";
const m = await import("./lazy");
const r = require("node:fs");
export const notAnImport = 1;
`
	got := parse(t, "a.ts", src)

	want := []lang.RawImport{
		{Specifier: "react", Kind: lang.Static, Line: 1},
		{Specifier: "./types", Kind: lang.TypeOnly, Line: 2},
		{Specifier: "@/lib/utils", Kind: lang.Static, Line: 3},
		{Specifier: "./side-effect.css", Kind: lang.Static, Line: 4},
		{Specifier: "./x", Kind: lang.Reexport, Line: 5},
		{Specifier: "./star", Kind: lang.Reexport, Line: 6},
		{Specifier: "./t", Kind: lang.TypeOnlyReexport, Line: 7},
		{Specifier: "./lazy", Kind: lang.Dynamic, Line: 8},
		{Specifier: "node:fs", Kind: lang.Require, Line: 9},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d imports, want %d:\n%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Specifier != want[i].Specifier || got[i].Kind != want[i].Kind || got[i].Line != want[i].Line {
			t.Errorf("import %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// The whole reason for using a real grammar instead of regexes.
func TestIgnoresImportsThatArentImports(t *testing.T) {
	src := `// import Fake from "commented-out";
/* import Also from "block-comment"; */
const s = 'import X from "inside-a-string"';
const t = ` + "`" + `import Y from "inside-a-template"` + "`" + `;
import Real from "real";
`
	got := parse(t, "a.ts", src)
	if len(got) != 1 {
		t.Fatalf("got %d imports, want 1 (only the real one): %v", len(got), got)
	}
	if got[0].Specifier != "real" {
		t.Errorf("got %q, want \"real\"", got[0].Specifier)
	}
}

// A dynamic import we cannot resolve must be recorded, never dropped —
// otherwise a file loaded only through a computed path looks dead.
func TestUnanalyzableDynamicImportIsRecorded(t *testing.T) {
	got := parse(t, "a.ts", "const m = await import(routeFor(slug));\n")
	if len(got) != 1 {
		t.Fatalf("got %d imports, want 1: %v", len(got), got)
	}
	if got[0].Kind != lang.Unanalyzable {
		t.Errorf("kind = %v, want Unanalyzable", got[0].Kind)
	}
	if got[0].Expr != "routeFor(slug)" {
		t.Errorf("Expr = %q, want %q", got[0].Expr, "routeFor(slug)")
	}
	if got[0].Specifier != "" {
		t.Errorf("Specifier should be empty for an unanalyzable import, got %q", got[0].Specifier)
	}
}

func TestTSXGeneric(t *testing.T) {
	// In .tsx this is JSX; in .ts it would be a type assertion. Using the wrong
	// grammar produces a parse error and silently loses the import below it.
	src := `import { Button } from "./Button";
export const App = () => <Button<string> value="x" />;
`
	got := parse(t, "App.tsx", src)
	if len(got) != 1 || got[0].Specifier != "./Button" {
		t.Fatalf("got %v, want one import of ./Button", got)
	}
}

func TestTypeOnlyIsErasedAtRuntime(t *testing.T) {
	if !lang.TypeOnly.ErasedAtRuntime() {
		t.Error("TypeOnly should be erased at runtime")
	}
	if lang.Static.ErasedAtRuntime() {
		t.Error("Static should not be erased at runtime")
	}
}

func TestHandles(t *testing.T) {
	p := New()
	for _, ok := range []string{"a.ts", "a.tsx", "a.js", "a.mjs", "b/c.CJS"} {
		if !p.Handles(ok) {
			t.Errorf("Handles(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"a.py", "a.go", "a.css", "a"} {
		if p.Handles(no) {
			t.Errorf("Handles(%q) = true, want false", no)
		}
	}
}

// A file with a syntax error must not abort the scan.
func TestBrokenFileDoesNotError(t *testing.T) {
	if _, err := New().Parse("a.ts", []byte("import { from './x'\nfunction (")); err != nil {
		t.Fatalf("a broken file should not return an error, got: %v", err)
	}
}
