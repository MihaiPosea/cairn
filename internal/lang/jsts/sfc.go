package jsts

import (
	"strings"
)

// Single-file components put JavaScript inside a markup document: Vue and
// Svelte in <script> blocks, Astro in a --- fenced frontmatter. Without
// handling them, an entire ecosystem is invisible — not merely incomplete.
// A Vue app scanned without .vue support shows a handful of .ts utility files
// and no components at all, which looks like a working scan of a tiny project.
//
// The blocks are extracted and handed to the same TypeScript grammar the rest
// of the parser uses. Extracting rather than adding three more grammars means
// one parser to keep correct, and the imports inside a <script> really are
// ordinary TypeScript.

// scriptBlock is one region of embedded code.
type scriptBlock struct {
	code string
	// lineOffset is how many lines precede this block in the file, so reported
	// line numbers point at the real line in the .vue file rather than at an
	// offset into an extracted fragment.
	lineOffset int
}

// extractBlocks pulls the code out of a single-file component.
func extractBlocks(ext, src string) []scriptBlock {
	switch ext {
	case ".astro":
		return astroBlocks(src)
	case ".vue", ".svelte":
		return scriptTagBlocks(src)
	}
	return nil
}

// scriptTagBlocks finds every <script ...> ... </script> region.
//
// A Vue file often has two — `<script>` and `<script setup>` — and both may
// contain imports, so all of them are returned rather than just the first.
func scriptTagBlocks(src string) []scriptBlock {
	var out []scriptBlock
	lower := strings.ToLower(src)

	pos := 0
	for {
		start := strings.Index(lower[pos:], "<script")
		if start < 0 {
			break
		}
		start += pos

		// Skip to the end of the opening tag. A tag with no ">" is malformed
		// markup; stop rather than guess.
		open := strings.IndexByte(src[start:], '>')
		if open < 0 {
			break
		}
		codeStart := start + open + 1

		end := strings.Index(lower[codeStart:], "</script")
		if end < 0 {
			// Unterminated block: take the rest of the file. Half a script is
			// still better than none, and the parser tolerates a truncated one.
			out = append(out, scriptBlock{
				code:       src[codeStart:],
				lineOffset: strings.Count(src[:codeStart], "\n"),
			})
			break
		}
		codeEnd := codeStart + end

		out = append(out, scriptBlock{
			code:       src[codeStart:codeEnd],
			lineOffset: strings.Count(src[:codeStart], "\n"),
		})
		pos = codeEnd
	}
	return out
}

// astroBlocks returns the frontmatter fence plus any <script> tags.
//
// Astro's imports live between --- fences at the very top of the file, which is
// where the component's TypeScript goes. Client-side <script> tags may also
// import, so both are collected.
func astroBlocks(src string) []scriptBlock {
	var out []scriptBlock

	trimmed := strings.TrimLeft(src, " \t\r\n")
	lead := len(src) - len(trimmed)
	if strings.HasPrefix(trimmed, "---") {
		rest := trimmed[3:]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			codeStart := lead + 3
			out = append(out, scriptBlock{
				code:       rest[:end],
				lineOffset: strings.Count(src[:codeStart], "\n"),
			})
		}
	}
	return append(out, scriptTagBlocks(src)...)
}
