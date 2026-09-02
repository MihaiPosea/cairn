// Package lang defines how cairn extracts import statements from source code.
//
// A Parser turns bytes into raw import specifiers and nothing more. It does not
// resolve them — it has no idea whether "./utils" means utils.ts, utils/index.ts
// or nothing at all. That is internal/resolve's job, and keeping the two apart
// is what makes adding a language a small change instead of a rewrite.
package lang

import "fmt"

// ImportKind distinguishes forms that behave differently at runtime.
type ImportKind uint8

const (
	// Static is a normal import: `import x from "y"`.
	Static ImportKind = iota
	// TypeOnly is erased when the code compiles: `import type { X } from "y"`.
	// It must never keep a file alive or count toward the cost of an import.
	TypeOnly
	// Dynamic is `import("y")` with a literal string.
	Dynamic
	// Require is CommonJS `require("y")` with a literal string.
	Require
	// Reexport is `export { x } from "y"` or `export * from "y"`. It creates a
	// real dependency, and it is also how a file can be "used" without anyone
	// importing it directly.
	Reexport
	// TypeOnlyReexport is `export type { X } from "y"`.
	TypeOnlyReexport
	// Unanalyzable is `import(someVariable)` or `require(buildPath())` — a real
	// dependency on something we cannot name.
	//
	// These are recorded rather than dropped. A file that is only ever loaded
	// through a computed path would otherwise look dead, and deleting it on
	// that advice is how a tool like this earns a bad reputation.
	Unanalyzable
)

func (k ImportKind) String() string {
	switch k {
	case Static:
		return "static"
	case TypeOnly:
		return "type-only"
	case Dynamic:
		return "dynamic"
	case Require:
		return "require"
	case Reexport:
		return "reexport"
	case TypeOnlyReexport:
		return "type-only-reexport"
	case Unanalyzable:
		return "unanalyzable"
	}
	return "unknown"
}

// ErasedAtRuntime reports whether this import disappears when the code is
// compiled, and therefore must not create a runtime dependency.
func (k ImportKind) ErasedAtRuntime() bool {
	return k == TypeOnly || k == TypeOnlyReexport
}

// RawImport is one import found in one file, before any resolution.
type RawImport struct {
	// Specifier is the text inside the quotes: "./utils", "@/lib/x", "react".
	// Empty when Kind is Unanalyzable.
	Specifier string
	Kind      ImportKind
	// Line is 1-based, so it matches what an editor shows.
	Line int
	// Expr is the source text of a non-literal argument, kept for reporting
	// when Kind is Unanalyzable.
	Expr string
}

func (r RawImport) String() string {
	if r.Kind == Unanalyzable {
		return fmt.Sprintf("%d: %s(%s)", r.Line, r.Kind, r.Expr)
	}
	return fmt.Sprintf("%d: %s %q", r.Line, r.Kind, r.Specifier)
}

// Parser extracts imports from one language's source files.
type Parser interface {
	// Name identifies the parser in diagnostics.
	Name() string
	// Handles reports whether this parser understands the given file path.
	Handles(path string) bool
	// Parse returns every import found in src, in source order.
	//
	// A file that fails to parse cleanly must still return whatever was found
	// rather than an error: real repos contain syntax errors, generated files,
	// and dialects nobody anticipated, and refusing to scan a repo because one
	// file is odd would make the tool useless.
	Parse(path string, src []byte) ([]RawImport, error)
}
