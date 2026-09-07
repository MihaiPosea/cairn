package jsts

import (
	"path/filepath"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
)

// ExtractAliases pulls path aliases out of a bundler config file.
//
// Vite, Rollup, Rspack and webpack all declare aliases in JavaScript rather
// than in tsconfig:
//
//	resolve: { alias: { "@": path.resolve(__dirname, "./src") } }
//
// Many projects mirror them into tsconfig for the editor's benefit, which is
// why this went unnoticed - but plenty do not, and in those repos every
// aliased import is unresolved.
//
// The config is read from its syntax tree, not matched with a regex: these
// files contain comments, template literals and nested objects, and the word
// "alias" appears in prose as often as in code.
//
// Values are evaluated only as far as taking the last string literal in the
// expression, which covers the forms that actually appear:
//
//	"@": "/src"
//	"@": path.resolve(__dirname, "./src")
//	"@": fileURLToPath(new URL("./src", import.meta.url))
//
// Anything genuinely computed is skipped rather than guessed at.
func ExtractAliases(path string, src []byte) map[string]string {
	p := New()
	language := p.languageFor(path)
	parser := p.borrow(language)
	defer p.release(language, parser)

	tree, err := parser.Parse(src)
	if err != nil || tree == nil {
		return nil
	}

	out := map[string]string{}

	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		if n == nil {
			return
		}
		if n.Type(language) == "pair" && keyName(n, language, src) == "alias" {
			collectAliasPairs(valueOf(n, language), language, src, out)
		}
		for _, c := range n.Children() {
			walk(c)
		}
	}
	walk(tree.RootNode())

	if len(out) == 0 {
		return nil
	}
	return out
}

// collectAliasPairs reads both shapes an alias value can take: an object of
// key/value pairs, or an array of {find, replacement} entries.
func collectAliasPairs(v *ts.Node, l *ts.Language, src []byte, out map[string]string) {
	if v == nil {
		return
	}
	switch v.Type(l) {
	case "object":
		for _, pair := range v.Children() {
			if pair.Type(l) != "pair" {
				continue
			}
			key := keyName(pair, l, src)
			target := lastStringLiteral(valueOf(pair, l), l, src)
			if key != "" && target != "" {
				out[key] = target
			}
		}
	case "array":
		for _, entry := range v.Children() {
			if entry.Type(l) != "object" {
				continue
			}
			var find, replacement string
			for _, pair := range entry.Children() {
				if pair.Type(l) != "pair" {
					continue
				}
				switch keyName(pair, l, src) {
				case "find":
					find = lastStringLiteral(valueOf(pair, l), l, src)
				case "replacement":
					replacement = lastStringLiteral(valueOf(pair, l), l, src)
				}
			}
			if find != "" && replacement != "" {
				out[find] = replacement
			}
		}
	}
}

// keyName returns a pair's key, whether written bare or quoted.
func keyName(pair *ts.Node, l *ts.Language, src []byte) string {
	for _, c := range pair.Children() {
		switch c.Type(l) {
		case "property_identifier", "identifier":
			return c.Text(src)
		case "string":
			return stringValue(c, l, src)
		}
	}
	return ""
}

// valueOf returns the node after the ":" in a pair.
func valueOf(pair *ts.Node, l *ts.Language) *ts.Node {
	seenColon := false
	for _, c := range pair.Children() {
		if seenColon && c.IsNamed() {
			return c
		}
		if c.Type(l) == ":" {
			seenColon = true
		}
	}
	return nil
}

// lastStringLiteral returns the final string in an expression subtree.
//
// For path.resolve(__dirname, "./src") that is "./src", and for
// fileURLToPath(new URL("./src", import.meta.url)) it is also "./src" -
// import.meta.url contributes no string literal. Taking the last one rather
// than the first is what makes both work.
func lastStringLiteral(n *ts.Node, l *ts.Language, src []byte) string {
	if n == nil {
		return ""
	}
	var found string
	var walk func(*ts.Node)
	walk = func(c *ts.Node) {
		if c == nil {
			return
		}
		if c.Type(l) == "string" {
			if v := stringValue(c, l, src); v != "" && validSpecifier(v) {
				found = v
			}
		}
		for _, ch := range c.Children() {
			walk(ch)
		}
	}
	walk(n)
	return found
}

// ConfigFiles are the bundler configs worth reading for aliases.
var ConfigFiles = []string{
	"vite.config.ts", "vite.config.js", "vite.config.mts", "vite.config.mjs",
	"vitest.config.ts", "vitest.config.js",
	"rollup.config.ts", "rollup.config.js",
	"webpack.config.ts", "webpack.config.js",
	"rspack.config.ts", "rspack.config.js",
	"svelte.config.js", "nuxt.config.ts",
}

// IsConfigFile reports whether a repo-relative path is one of them.
func IsConfigFile(rel string) bool {
	base := strings.ToLower(filepath.Base(rel))
	for _, c := range ConfigFiles {
		if base == c {
			return true
		}
	}
	return false
}
