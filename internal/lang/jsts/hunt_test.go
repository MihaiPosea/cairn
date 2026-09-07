package jsts

import (
	"strings"
	"testing"

	"github.com/MihaiPosea/cairn/internal/lang"
)

func specs(t *testing.T, path, src string) []string {
	t.Helper()
	imps, err := New().Parse(path, []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var out []string
	for _, i := range imps {
		out = append(out, i.Specifier)
	}
	return out
}

// SUSPECT: a UTF-8 byte-order mark before the first import.
func TestByteOrderMark(t *testing.T) {
	src := "\uFEFF" + `import x from "./a";` + "\n"
	got := specs(t, "a.ts", src)
	t.Logf("BOM -> %v", got)
	if len(got) != 1 {
		t.Errorf("BUG: a BOM hid the import (found %v)", got)
	}
}

// SUSPECT: CRLF line endings shifting reported line numbers.
func TestCRLFLineNumbers(t *testing.T) {
	src := "// header\r\n// second\r\nimport x from \"./a\";\r\n"
	imps, err := New().Parse("a.ts", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(imps) != 1 {
		t.Fatalf("BUG: CRLF hid the import: %v", imps)
	}
	t.Logf("CRLF -> line %d", imps[0].Line)
	if imps[0].Line != 3 {
		t.Errorf("BUG: line = %d, want 3 - line numbers are shown to users", imps[0].Line)
	}
}

func TestEmptyAndTrivialFiles(t *testing.T) {
	for _, src := range []string{"", "\n", "   ", "// nothing here"} {
		if _, err := New().Parse("a.ts", []byte(src)); err != nil {
			t.Errorf("BUG: %q returned an error: %v", src, err)
		}
	}
}

// SUSPECT: TypeScript's import-equals form, still common in older code.
func TestImportEquals(t *testing.T) {
	got := specs(t, "a.ts", "import fs = require(\"node:fs\");\n")
	t.Logf("import-equals -> %v", got)
	if len(got) != 1 || got[0] != "node:fs" {
		t.Errorf("BUG: import-equals not extracted, got %v", got)
	}
}

// SUSPECT: import attributes, the current syntax for JSON and CSS modules.
func TestImportAttributes(t *testing.T) {
	src := `import data from "./data.json" with { type: "json" };
import sheet from "./styles.css" assert { type: "css" };
`
	got := specs(t, "a.ts", src)
	t.Logf("import attributes -> %v", got)
	if len(got) != 2 {
		t.Errorf("BUG: import attributes broke extraction, got %v", got)
	}
}

// SUSPECT: JSX inside a .js file, which the JavaScript grammar must handle.
func TestJSXInPlainJS(t *testing.T) {
	src := `import { Button } from "./Button";
export const App = () => <Button prop={1} />;
`
	got := specs(t, "App.js", src)
	t.Logf("jsx in .js -> %v", got)
	if len(got) != 1 {
		t.Errorf("BUG: JSX in a .js file lost the import, got %v", got)
	}
}

// SUSPECT: decorators, which change the parse shape considerably.
func TestDecorators(t *testing.T) {
	src := `import { Injectable } from "./di";
@Injectable()
export class Service {}
`
	got := specs(t, "a.ts", src)
	t.Logf("decorators -> %v", got)
	if len(got) != 1 {
		t.Errorf("BUG: decorators lost the import, got %v", got)
	}
}

// SUSPECT: a file that is mostly binary but named .ts.
func TestBinaryContentDoesNotPanic(t *testing.T) {
	src := make([]byte, 4096)
	for i := range src {
		src[i] = byte(i % 251)
	}
	if _, err := New().Parse("a.ts", src); err != nil {
		t.Logf("binary content returned err=%v (acceptable)", err)
	}
}

// SUSPECT: a specifier containing an escape sequence or a quote.
func TestEscapedSpecifier(t *testing.T) {
	got := specs(t, "a.ts", "import x from \"./with\\u0020space\";\n")
	t.Logf("escaped specifier -> %v", got)
	if len(got) != 1 {
		t.Errorf("expected one import, got %v", got)
	}
}

// SUSPECT: a very long single line, which some parsers handle badly.
func TestVeryLongLine(t *testing.T) {
	src := "const s = \"" + strings.Repeat("x", 200000) + "\";\nimport a from \"./a\";\n"
	got := specs(t, "a.ts", src)
	t.Logf("long line -> %v", got)
	if len(got) != 1 {
		t.Errorf("BUG: a long line hid the import, got %v", got)
	}
}

// SUSPECT: require() shadowed by a local variable is not a module import.
func TestShadowedRequire(t *testing.T) {
	src := `function load(require) { return require("./not-a-module"); }`
	imps, _ := New().Parse("a.ts", []byte(src))
	t.Logf("shadowed require -> %v", imps)
	// Documented behaviour: cairn cannot do scope analysis, so this is a known
	// false positive. The test exists to record it, not to fail.
	if len(imps) > 0 && imps[0].Kind != lang.Require {
		t.Errorf("unexpected kind %v", imps[0].Kind)
	}
}

// SUSPECT: export-star-as, and side-effect-only re-export forms.
func TestExportStarAs(t *testing.T) {
	src := `export * as ns from "./ns";
export { default } from "./def";
`
	got := specs(t, "a.ts", src)
	t.Logf("export forms -> %v", got)
	if len(got) != 2 {
		t.Errorf("BUG: export forms lost an import, got %v", got)
	}
}

// `export default "some string"` is not a re-export. Without the `from` check
// it read as one, inventing a module named after the string's contents.
func TestExportDefaultStringIsNotAnImport(t *testing.T) {
	for _, src := range []string{
		`export default 'hello vite'`,
		`export default "absolute import";`,
		"export default `template literal`;",
		`export default { a: "./not-an-import" };`,
		`export default ["./nor-this"];`,
		`export const msg = "./definitely-not";`,
		`export default 42;`,
	} {
		got := specs(t, "a.ts", src)
		if len(got) != 0 {
			t.Errorf("%q produced imports %v - none of these are imports", src, got)
		}
	}
}

// Real re-exports must still be found.
func TestRealReexportsStillWork(t *testing.T) {
	src := `export { a } from "./a";
export * from "./b";
export * as ns from "./c";
export type { T } from "./d";
export { default } from "./e";
`
	got := specs(t, "a.ts", src)
	want := []string{"./a", "./b", "./c", "./d", "./e"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("import %d = %q, want %q", i, got[i], want[i])
		}
	}
}
