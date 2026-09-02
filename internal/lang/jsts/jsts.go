// Package jsts extracts imports from JavaScript and TypeScript source.
//
// It uses gotreesitter — a pure-Go tree-sitter runtime — so cairn stays a
// single static binary with no C toolchain, and so `go install` works for
// anyone. Parsing with a real grammar rather than regexes matters more than it
// looks: an import inside a comment, a string containing the word "import", and
// a template literal spanning lines all defeat pattern matching, and all appear
// in real code.
package jsts

import (
	"path/filepath"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/MihaiPosea/cairn/internal/lang"
)

// Extensions this parser claims.
var extensions = map[string]bool{
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
}

// Parser implements lang.Parser for JS/TS.
//
// Grammars are loaded lazily and shared; the underlying loader caches them, and
// a grammar is read-only once built. Parsers themselves are not safe to share
// across goroutines, so one is created per Parse call. That allocation is
// cheap next to the parse itself; if it ever shows up in a profile, the fix is
// gotreesitter's ParserPool.
type Parser struct {
	once sync.Once
	tsx  *ts.Language
	tsL  *ts.Language
	jsL  *ts.Language
}

func New() *Parser { return &Parser{} }

func (p *Parser) Name() string { return "jsts" }

func (p *Parser) Handles(path string) bool {
	return extensions[strings.ToLower(filepath.Ext(path))]
}

func (p *Parser) load() {
	p.once.Do(func() {
		p.tsx = grammars.TsxLanguage()
		p.tsL = grammars.TypescriptLanguage()
		p.jsL = grammars.JavascriptLanguage()
	})
}

// languageFor picks a grammar by extension.
//
// .tsx needs the TSX grammar because TypeScript's `<T>x` type assertion and
// JSX's `<T>` are genuinely ambiguous — tree-sitter ships two grammars for
// exactly this reason. The JavaScript grammar already understands JSX, so .js
// and .jsx share it.
func (p *Parser) languageFor(path string) *ts.Language {
	p.load()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx":
		return p.tsx
	case ".ts", ".mts", ".cts":
		return p.tsL
	default:
		return p.jsL
	}
}

// Parse walks the syntax tree and collects every import.
func (p *Parser) Parse(path string, src []byte) ([]lang.RawImport, error) {
	language := p.languageFor(path)
	parser := ts.NewParser(language)

	tree, err := parser.Parse(src)
	if err != nil || tree == nil {
		// A file we cannot parse is a fact about the repo, not a reason to
		// abort the scan. Report nothing found and let the caller count it.
		return nil, err
	}

	var out []lang.RawImport
	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		if n == nil {
			return
		}
		switch n.Type(language) {
		case "import_statement":
			if imp, ok := fromImportStatement(n, language, src); ok {
				out = append(out, imp)
			}
		case "export_statement":
			if imp, ok := fromExportStatement(n, language, src); ok {
				out = append(out, imp)
			}
		case "call_expression":
			if imp, ok := fromCallExpression(n, language, src); ok {
				out = append(out, imp)
			}
		}
		for _, c := range n.Children() {
			walk(c)
		}
	}
	walk(tree.RootNode())
	return out, nil
}

// fromImportStatement handles `import ... from "x"` and bare `import "x"`.
func fromImportStatement(n *ts.Node, l *ts.Language, src []byte) (lang.RawImport, bool) {
	spec, ok := sourceString(n, l, src)
	if !ok {
		return lang.RawImport{}, false
	}
	kind := lang.Static
	if hasDirectChild(n, l, "type") {
		kind = lang.TypeOnly
	}
	return lang.RawImport{Specifier: spec, Kind: kind, Line: line(n)}, true
}

// fromExportStatement handles re-exports. A plain `export const x = 1` has no
// source string and is correctly ignored.
func fromExportStatement(n *ts.Node, l *ts.Language, src []byte) (lang.RawImport, bool) {
	spec, ok := sourceString(n, l, src)
	if !ok {
		return lang.RawImport{}, false
	}
	kind := lang.Reexport
	if hasDirectChild(n, l, "type") {
		kind = lang.TypeOnlyReexport
	}
	return lang.RawImport{Specifier: spec, Kind: kind, Line: line(n)}, true
}

// fromCallExpression handles `import(...)` and `require(...)`.
func fromCallExpression(n *ts.Node, l *ts.Language, src []byte) (lang.RawImport, bool) {
	children := n.Children()
	if len(children) == 0 {
		return lang.RawImport{}, false
	}
	callee := children[0]

	var kind lang.ImportKind
	switch {
	case callee.Type(l) == "import":
		kind = lang.Dynamic
	case callee.Type(l) == "identifier" && callee.Text(src) == "require":
		kind = lang.Require
	default:
		return lang.RawImport{}, false
	}

	args := directChild(n, l, "arguments")
	if args == nil {
		return lang.RawImport{}, false
	}
	// First meaningful argument. Punctuation is unnamed, so named children
	// gives us the argument list directly.
	var first *ts.Node
	for _, c := range args.Children() {
		if c.IsNamed() {
			first = c
			break
		}
	}
	if first == nil {
		return lang.RawImport{}, false
	}
	if first.Type(l) == "string" {
		return lang.RawImport{Specifier: stringValue(first, l, src), Kind: kind, Line: line(n)}, true
	}
	// import(someVariable) — a real dependency we cannot name. Record it.
	return lang.RawImport{Kind: lang.Unanalyzable, Line: line(n), Expr: first.Text(src)}, true
}

// sourceString returns the value of the first direct `string` child, which is
// where both import and export statements keep their module specifier.
func sourceString(n *ts.Node, l *ts.Language, src []byte) (string, bool) {
	s := directChild(n, l, "string")
	if s == nil {
		return "", false
	}
	return stringValue(s, l, src), true
}

// stringValue reads the text inside the quotes.
//
// tree-sitter models a string as quote / string_fragment / quote, so the
// fragment already excludes them. An empty string ("") has no fragment child,
// which is why the fallback trims manually rather than assuming one exists.
func stringValue(s *ts.Node, l *ts.Language, src []byte) string {
	for _, c := range s.Children() {
		if c.Type(l) == "string_fragment" {
			return c.Text(src)
		}
	}
	return strings.Trim(s.Text(src), `"'`+"`")
}

func directChild(n *ts.Node, l *ts.Language, typ string) *ts.Node {
	for _, c := range n.Children() {
		if c.Type(l) == typ {
			return c
		}
	}
	return nil
}

func hasDirectChild(n *ts.Node, l *ts.Language, typ string) bool {
	return directChild(n, l, typ) != nil
}

// line converts tree-sitter's 0-based row to the 1-based line an editor shows.
func line(n *ts.Node) int { return int(n.StartPoint().Row) + 1 }
