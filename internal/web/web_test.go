package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MihaiPosea/cairn/internal/scan"
)

func build(t *testing.T, files map[string]string) *scan.Result {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := scan.RunWith(root, scan.Options{SkipPackages: true, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The page embeds file paths and import specifiers, both of which come from
// disk. A specifier containing "</script>" would close the tag early and
// everything after it would be parsed as HTML.
//
// Go's encoding/json escapes <, > and & by default, which is what stops this.
// That is easy to lose — someone reaching for a json.Encoder with
// SetEscapeHTML(false) to make the output prettier would reopen it silently.
// This test is the tripwire.
func TestPayloadCannotBreakOutOfTheScriptTag(t *testing.T) {
	res := build(t, map[string]string{
		"app/page.tsx": `import a from "</script><img src=x onerror=alert(1)>";
import b from "./</script>";
`,
		"lib/<img src=x onerror=alert(1)>.ts": `export const x = 1;`,
	})

	html, err := Render(Build(res, false))
	if err != nil {
		t.Fatal(err)
	}

	// The only </script> in the document must be the one that closes our own
	// script block. Any other means content escaped the JSON.
	if n := strings.Count(strings.ToLower(html), "</script>"); n != 1 {
		t.Fatalf("found %d </script> occurrences, want exactly 1 — content escaped the payload", n)
	}
	if strings.Contains(html, "<img src=x onerror=alert(1)>") {
		t.Error("raw markup from a filename or specifier reached the document unescaped")
	}
	if !strings.Contains(html, `<`) {
		t.Error("expected < to be escaped as \\u003c in the embedded JSON")
	}
}

func TestPayloadIsValidForAnEmptyRepo(t *testing.T) {
	res := build(t, map[string]string{"README.md": "nothing to see"})
	p := Build(res, false)
	if p == nil {
		t.Fatal("Build returned nil for an empty repo")
	}
	html, err := Render(p)
	if err != nil {
		t.Fatalf("Render failed on an empty repo: %v", err)
	}
	if !strings.Contains(html, "<canvas") {
		t.Error("the page should still render its shell with no nodes")
	}
}

// Every edge must point at a node that is present, or the page draws lines to
// nowhere and the click handler dereferences undefined.
func TestEveryEdgeEndpointIsPresent(t *testing.T) {
	res := build(t, map[string]string{
		"app/page.tsx":          `import {B} from "../components/Button"; import "react";`,
		"components/Button.tsx": `import "../lib/utils"; export const B = 1;`,
		"lib/utils.ts":          `export const u = 1;`,
	})

	p := Build(res, true)
	present := map[string]bool{}
	for _, n := range p.Nodes {
		present[n.ID] = true
	}
	for _, e := range p.Edges {
		if !present[e.From] || !present[e.To] {
			t.Errorf("edge %s -> %s references a node the page does not draw", e.From, e.To)
		}
	}
	if len(p.Edges) == 0 {
		t.Error("expected some edges")
	}
}

// Depth must increase along a dependency chain, since the browser packs the
// layout from it.
func TestDepthIncreasesAlongAChain(t *testing.T) {
	res := build(t, map[string]string{
		"app/page.tsx": `import "./a";`,
		"app/a.ts":     `import "./b";`,
		"app/b.ts":     `export const b = 1;`,
	})
	depth := map[string]int{}
	for _, n := range Build(res, false).Nodes {
		if n.Depth < 0 {
			t.Errorf("node %s has a negative depth", n.ID)
		}
		depth[n.Label] = n.Depth
	}
	if !(depth["app/page.tsx"] < depth["app/a.ts"] && depth["app/a.ts"] < depth["app/b.ts"]) {
		t.Errorf("depth should increase along the chain, got %v", depth)
	}
}
