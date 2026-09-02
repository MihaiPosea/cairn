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
	"strconv"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/MihaiPosea/cairn/internal/lang"
)

// Extensions this parser claims.
//
// The single-file-component formats are here because their imports are
// ordinary TypeScript wrapped in markup — see sfc.go. Leaving them out does not
// produce a partial answer for a Vue or Svelte app, it produces a graph with
// the components missing entirely, which reads as a working scan of a much
// smaller project.
var extensions = map[string]bool{
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".vue": true, ".svelte": true, ".astro": true,
}

// sfcExtensions are the formats that embed code in markup.
var sfcExtensions = map[string]bool{".vue": true, ".svelte": true, ".astro": true}

// Parser implements lang.Parser for JS/TS.
//
// Grammars are loaded lazily and shared: the loader caches them and a grammar
// is read-only once built. Parser *instances* are not safe to share across
// goroutines, so each language keeps a sync.Pool of them.
//
// The pool is not premature optimisation. Measured on a generated 5,000-file
// repo, allocating a parser per file cost 5.6s of a 5.6s scan budget; parsers
// are expensive to construct relative to parsing one small file, and a scan
// constructs one per file across eight workers. Pooling is the single change
// that brought the scan under the target.
type Parser struct {
	once sync.Once
	tsx  *ts.Language
	tsL  *ts.Language
	jsL  *ts.Language

	pools sync.Map // *ts.Language -> *sync.Pool of *ts.Parser
}

func New() *Parser { return &Parser{} }

// borrow takes a parser for a language, creating one only when the pool is
// empty.
func (p *Parser) borrow(l *ts.Language) *ts.Parser {
	v, _ := p.pools.LoadOrStore(l, &sync.Pool{
		New: func() any { return ts.NewParser(l) },
	})
	return v.(*sync.Pool).Get().(*ts.Parser)
}

func (p *Parser) release(l *ts.Language, parser *ts.Parser) {
	if v, ok := p.pools.Load(l); ok {
		v.(*sync.Pool).Put(parser)
	}
}

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
	if ext := strings.ToLower(filepath.Ext(path)); sfcExtensions[ext] {
		return p.parseSFC(ext, src)
	}
	return p.parseCode(path, src, 0)
}

// parseSFC extracts the embedded code from a single-file component and parses
// each block, shifting line numbers so they point at the real line in the
// original file.
func (p *Parser) parseSFC(ext string, src []byte) ([]lang.RawImport, error) {
	var out []lang.RawImport
	for _, block := range extractBlocks(ext, string(src)) {
		imports, err := p.parseCode("block.ts", []byte(block.code), block.lineOffset)
		if err != nil {
			continue // one unparseable block must not lose the others
		}
		out = append(out, imports...)
	}
	return out, nil
}

// parseCode parses ordinary JavaScript or TypeScript. lineOffset is added to
// every reported line, for code extracted from a larger document.
func (p *Parser) parseCode(path string, src []byte, lineOffset int) ([]lang.RawImport, error) {
	language := p.languageFor(path)
	parser := p.borrow(language)
	defer p.release(language, parser)

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

	if lineOffset != 0 {
		for i := range out {
			out[i].Line += lineOffset
		}
	}
	return out, nil
}

// validSpecifier reports whether a decoded specifier is usable as one.
//
// A module specifier containing a control character is not a real path, and
// letting one through means it reaches filepath handling, a node ID, the JSON
// output, and the HTML page. Found by fuzzing in six seconds: `import "0\000"`
// decodes an octal escape to a NUL byte.
//
// Such an import is not dropped — it is reported as unanalyzable, the same
// treatment a computed import() gets, because something is being imported and
// pretending otherwise would hide it.
func validSpecifier(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// fromImportStatement handles `import ... from "x"`, bare `import "x"`, and
// TypeScript's `import x = require("y")`.
func fromImportStatement(n *ts.Node, l *ts.Language, src []byte) (lang.RawImport, bool) {
	// `import fs = require("node:fs")` nests its string inside an
	// import_require_clause rather than hanging it off the statement, so a
	// direct-child lookup misses it entirely. This form is still common in
	// older TypeScript and throughout .d.ts files.
	if clause := directChild(n, l, "import_require_clause"); clause != nil {
		if s := directChild(clause, l, "string"); s != nil {
			spec := stringValue(s, l, src)
			if !validSpecifier(spec) {
				return unusable(n, src), true
			}
			return lang.RawImport{Specifier: spec, Kind: lang.Require, Line: line(n)}, true
		}
	}

	spec, ok := sourceString(n, l, src)
	if !ok {
		return lang.RawImport{}, false
	}
	if !validSpecifier(spec) {
		return unusable(n, src), true
	}
	kind := lang.Static
	if hasDirectChild(n, l, "type") {
		kind = lang.TypeOnly
	}
	return lang.RawImport{Specifier: spec, Kind: kind, Line: line(n)}, true
}

// unusable records an import whose specifier cannot be used as a path.
func unusable(n *ts.Node, src []byte) lang.RawImport {
	expr := strings.TrimSpace(n.Text(src))
	if len(expr) > 80 {
		expr = expr[:80]
	}
	return lang.RawImport{Kind: lang.Unanalyzable, Line: line(n), Expr: sanitize(expr)}
}

// sanitize strips control characters so a diagnostic string is safe to print.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// fromExportStatement handles re-exports.
//
// The `from` keyword must be present. Without that check, `export default
// 'hello vite'` reads as a re-export of a module named "hello vite": the string
// is a direct child of the export statement either way, and only the keyword
// distinguishes a re-export from a plain exported value.
//
// That produced *invented* edges — worse than a missed one, because a missing
// edge understates the graph while a fabricated one puts a node in it that no
// code ever mentions. Found across the Vite repository, where playground
// fixtures export string literals by the dozen.
func fromExportStatement(n *ts.Node, l *ts.Language, src []byte) (lang.RawImport, bool) {
	if !hasDirectChild(n, l, "from") {
		return lang.RawImport{}, false
	}
	spec, ok := sourceString(n, l, src)
	if !ok {
		return lang.RawImport{}, false
	}
	if !validSpecifier(spec) {
		return unusable(n, src), true
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
		spec := stringValue(first, l, src)
		if !validSpecifier(spec) {
			return unusable(n, src), true
		}
		return lang.RawImport{Specifier: spec, Kind: kind, Line: line(n)}, true
	}
	// import(someVariable) — a real dependency we cannot name. Record it.
	return lang.RawImport{Kind: lang.Unanalyzable, Line: line(n), Expr: sanitize(first.Text(src))}, true
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

// stringValue reads the text inside the quotes, decoding escapes.
//
// tree-sitter models a string as quote / string_fragment / quote, so the
// fragment already excludes them. But a specifier containing an escape splits
// into several children — string_fragment, escape_sequence, string_fragment —
// and returning only the first silently truncates the path. "./with\u0020space"
// became "./with", which then fails to resolve for no visible reason.
//
// So: take the raw text and let strconv.Unquote decode it, which handles \u,
// \n and \\ exactly as JavaScript does. Unquote rejects single-quoted and
// backtick strings, so those fall back to concatenating the fragments.
func stringValue(s *ts.Node, l *ts.Language, src []byte) string {
	raw := s.Text(src)
	if len(raw) >= 2 && raw[0] == '"' {
		if decoded, err := strconv.Unquote(raw); err == nil {
			return decoded
		}
	}

	var sb strings.Builder
	for _, c := range s.Children() {
		switch c.Type(l) {
		case "string_fragment":
			sb.WriteString(c.Text(src))
		case "escape_sequence":
			// Decode by wrapping in quotes so strconv does the work.
			if decoded, err := strconv.Unquote(`"` + c.Text(src) + `"`); err == nil {
				sb.WriteString(decoded)
			} else {
				sb.WriteString(c.Text(src))
			}
		}
	}
	if sb.Len() > 0 {
		return sb.String()
	}
	return strings.Trim(raw, `"'`+"`")
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
