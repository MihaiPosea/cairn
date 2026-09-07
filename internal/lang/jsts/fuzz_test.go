package jsts

import (
	"strings"
	"testing"
)

// FuzzParse throws arbitrary bytes at the parser.
//
// The contract is narrow but absolute: never panic, and never return a
// specifier containing a NUL or a newline. A scan walks whatever is on disk -
// generated files, truncated downloads, binaries someone named .ts - and one
// panic takes down the whole run.
func FuzzParse(f *testing.F) {
	seeds := []string{
		`import a from "./a";`,
		`import type {T} from "./t";`,
		`export * from "./s";`,
		`const x = await import("./d");`,
		`const r = require("node:fs");`,
		`import fs = require("node:fs");`,
		"import a from \"./\\u0041\";",
		"\uFEFFimport a from \"./bom\";",
		"import a from './single';",
		"import a from `./tick`;",
		`import a from "";`,
		`import`,
		`import from from from`,
		"import a from \"./a\";\r\nimport b from \"./b\";",
		`/* import a from "./commented"; */`,
		`import { a, b, c as d, type e } from "./many";`,
		`export { default as x } from "./def";`,
		`import a from "./a" with { type: "json" };`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	p := New()

	f.Fuzz(func(t *testing.T, src string) {
		for _, path := range []string{"a.ts", "a.tsx", "a.js"} {
			imports, err := p.Parse(path, []byte(src))
			if err != nil {
				continue // an unparseable file is a fact, not a failure
			}
			for _, imp := range imports {
				if strings.ContainsRune(imp.Specifier, 0) {
					t.Fatalf("specifier contains NUL: %q (from %q)", imp.Specifier, src)
				}
				if strings.ContainsAny(imp.Specifier, "\n\r") {
					t.Fatalf("specifier contains a newline: %q (from %q)", imp.Specifier, src)
				}
				if imp.Line < 1 && imp.Kind != 0 {
					t.Fatalf("line %d is not 1-based: %v (from %q)", imp.Line, imp, src)
				}
			}
		}
	})
}
