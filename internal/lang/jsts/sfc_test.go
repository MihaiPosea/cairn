package jsts

import (
	"testing"

	"github.com/MihaiPosea/cairn/internal/lang"
)

func TestVueBothScriptBlocks(t *testing.T) {
	src := `<template>
  <div/>
</template>

<script lang="ts">
import { legacy } from "./legacy";
</script>

<script setup lang="ts">
import Child from "./Child.vue";
</script>
`
	imps, err := New().Parse("App.vue", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(imps) != 2 {
		t.Fatalf("got %v, want both script blocks parsed", imps)
	}
	// Line numbers must point at the real line in the .vue file, not at an
	// offset into the extracted fragment.
	want := map[string]int{"./legacy": 6, "./Child.vue": 10}
	for _, imp := range imps {
		if w, ok := want[imp.Specifier]; !ok || imp.Line != w {
			t.Errorf("%q at line %d, want %d", imp.Specifier, imp.Line, w)
		}
	}
}

func TestSvelteScript(t *testing.T) {
	src := `<script lang="ts">
  import Child from "./Child.svelte";
  import { u } from "./utils";
</script>

<div>{u}</div>
`
	imps, err := New().Parse("App.svelte", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(imps) != 2 {
		t.Fatalf("got %v, want 2", imps)
	}
	if imps[0].Line != 2 {
		t.Errorf("first import at line %d, want 2", imps[0].Line)
	}
}

func TestAstroFrontmatterAndScript(t *testing.T) {
	src := `---
import Layout from "../layouts/Layout.astro";
import { u } from "../lib/utils";
---

<Layout>{u}</Layout>

<script>
  import "./client-side";
</script>
`
	imps, err := New().Parse("index.astro", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(imps) != 3 {
		t.Fatalf("got %v, want frontmatter (2) plus the client script (1)", imps)
	}
	if imps[0].Line != 2 {
		t.Errorf("first frontmatter import at line %d, want 2", imps[0].Line)
	}
}

// Markup with no code at all is common and must not error.
func TestMarkupOnlyComponents(t *testing.T) {
	for name, src := range map[string]string{
		"Child.vue":    `<template><span/></template>`,
		"Child.svelte": `<div>hello</div>`,
		"page.astro":   `<h1>hello</h1>`,
	} {
		imps, err := New().Parse(name, []byte(src))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(imps) != 0 {
			t.Errorf("%s: expected no imports, got %v", name, imps)
		}
	}
}

// An unterminated script block should still yield what it can.
func TestUnterminatedScriptBlock(t *testing.T) {
	imps, err := New().Parse("App.vue", []byte("<script>\nimport a from \"./a\";\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(imps) != 1 || imps[0].Specifier != "./a" {
		t.Errorf("got %v, want ./a recovered from a truncated block", imps)
	}
}

// A </script> inside a string must not end the block early.
func TestScriptTagInsideAString(t *testing.T) {
	src := `<script setup lang="ts">
const s = "not a real closing tag";
import a from "./a";
</script>`
	imps, err := New().Parse("App.vue", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(imps) != 1 {
		t.Fatalf("got %v, want ./a", imps)
	}
}

func TestSFCHandledExtensions(t *testing.T) {
	p := New()
	for _, ok := range []string{"a.vue", "a.svelte", "a.astro", "A.VUE"} {
		if !p.Handles(ok) {
			t.Errorf("Handles(%q) = false, want true", ok)
		}
	}
}

func TestSFCImportKindsSurvive(t *testing.T) {
	src := `<script setup lang="ts">
import type { T } from "./types";
const lazy = await import("./lazy");
</script>`
	imps, err := New().Parse("App.vue", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]lang.ImportKind{}
	for _, i := range imps {
		kinds[i.Specifier] = i.Kind
	}
	if kinds["./types"] != lang.TypeOnly {
		t.Errorf("./types kind = %v, want TypeOnly", kinds["./types"])
	}
	if kinds["./lazy"] != lang.Dynamic {
		t.Errorf("./lazy kind = %v, want Dynamic", kinds["./lazy"])
	}
}
