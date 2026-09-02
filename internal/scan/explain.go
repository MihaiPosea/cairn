package scan

import (
	"fmt"
	"sort"
	"strings"
)

// Cluster is a group of unresolved imports that share a cause.
type Cluster struct {
	// Prefix is the common start of the specifiers, e.g. "@/styles/base-nova".
	Prefix string
	Count  int
	// Category names why they are likely unresolved, in words a reader can act on.
	Category string
	// Example is one file:line, so the reader can go and look.
	Example string
}

// buildDirs are the conventional names for generated output.
var buildDirs = map[string]bool{
	"dist": true, "build": true, "out": true, "lib": true, "es": true,
	"esm": true, "cjs": true, "umd": true, ".next": true, ".output": true,
	".svelte-kit": true, "target": true, "generated": true, "__generated__": true,
}

// ClusterUnresolved groups unresolved imports so a large number becomes an
// explanation rather than a wall of text.
//
// This is the difference between a tool someone trusts and one they close.
// Measured on shadcn/ui: 4,623 unresolved imports, of which 4,611 share the
// prefix "@/styles/base-nova" — files that repo generates during its build and
// which genuinely do not exist in a fresh clone. One line saying so is useful;
// 4,623 lines saying "not found" looks like the tool is broken.
func ClusterUnresolved(items []Unresolvable) []Cluster {
	byPrefix := map[string][]Unresolvable{}
	for _, u := range items {
		byPrefix[groupKey(u.Specifier)] = append(byPrefix[groupKey(u.Specifier)], u)
	}

	out := make([]Cluster, 0, len(byPrefix))
	for prefix, group := range byPrefix {
		c := Cluster{Prefix: prefix, Count: len(group), Category: categorise(prefix, group)}
		if len(group) > 0 {
			c.Example = group[0].File
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Prefix < out[j].Prefix
	})
	return out
}

// groupKey is the first meaningful path segments of a specifier.
//
// Leading "./" and "../" are stripped before grouping, because they say where
// the importing file sits rather than what is being imported. Without that,
// "../../dist/core/x" groups under "../../" alongside every other deep
// relative import, hiding the shared cause — measured on Astro, where 757
// imports collapsed into one meaningless group that was really 303 pointing at
// dist/core, 91 at dist/assets, and so on.
func groupKey(spec string) string {
	if spec == "" {
		return "(computed)"
	}

	rest := spec
	up := 0
	for {
		switch {
		case strings.HasPrefix(rest, "./"):
			rest = rest[2:]
		case strings.HasPrefix(rest, "../"):
			rest = rest[3:]
			up++
		default:
			goto done
		}
	}
done:
	prefix := ""
	if up > 0 {
		prefix = strings.Repeat("../", up)
	} else if rest != spec {
		prefix = "./"
	}

	// Group by directory, never by filename.
	//
	// Including the filename splits a shared cause into one group per file:
	// tldraw's auto-generated asset manifest imports 179 images from
	// ./embed-icons/, none of which exist in a fresh clone, and each became its
	// own group of one. The systematic-absence rule then never fired, because
	// no group ever reached its threshold.
	parts := strings.Split(rest, "/")
	if len(parts) == 1 {
		return prefix + rest
	}
	dir := parts[:len(parts)-1]
	if len(dir) > 2 {
		dir = dir[:2]
	}
	return prefix + strings.Join(dir, "/") + "/…"
}

// generatedDirs are written by a framework or codegen step, never committed.
var generatedDirs = map[string]bool{
	".next": true, ".nuxt": true, ".svelte-kit": true, ".astro": true,
	".source": true, ".vinxi": true, ".output": true, ".contentlayer": true,
	"generated": true, "__generated__": true, ".wxt": true,
	"gen": true, "__gen__": true, ".react-router": true, ".tanstack": true,
}

// categorise gives a group a plain-language cause.
//
// The categories exist because the remaining unresolved imports in a healthy
// repo are almost never mistakes. They are test fixtures asserting that an
// import fails, scaffolding templates referencing files that appear later,
// codegen output, and binaries for other platforms. Calling all of that
// "not found" is technically true and useless; naming it lets a reader decide
// in one glance whether anything is actually wrong.
func categorise(prefix string, group []Unresolvable) string {
	// The importing file's location says more than the specifier does.
	if allMatch(group, isTestFixture) {
		return "test fixtures — these imports are meant to fail"
	}
	if allMatch(group, isTemplate) {
		return "scaffolding templates — the files appear when the template is used"
	}
	if allMatch(group, isPlayground) {
		return "playground and example apps — demos, not the library itself"
	}

	// Inspect the group's actual specifiers, not just the shared prefix.
	//
	// The prefix is truncated to two segments for display, which throws away
	// exactly the part that identifies a cause: "../../runtime/client/…"
	// hides the ".prebuilt.js" that makes it codegen. Astro's remaining
	// unresolved imports were all mislabelled for that reason.
	if allMatch(group, func(_ string) bool { return true }) {
		codegen, templates := true, true
		for _, u := range group {
			if !looksGenerated(u.Specifier) {
				codegen = false
			}
			if !specifierIsTemplate(u.Specifier) {
				templates = false
			}
		}
		if codegen {
			return "codegen output — written by a framework or generator, not committed"
		}
		if templates {
			return "scaffolding templates — the files appear when the template is used"
		}
	}

	segments := strings.Split(strings.TrimPrefix(prefix, "./"), "/")
	for _, seg := range segments {
		if generatedDirs[seg] {
			return "codegen output — written by a framework or generator, not committed"
		}
	}
	if strings.HasSuffix(prefix, ".node") || strings.HasSuffix(prefix, ".wasm") {
		return "native binaries for other platforms"
	}
	for _, seg := range segments {
		if buildDirs[seg] {
			return "build output with no source equivalent — run the repo's build"
		}
	}
	// A whole directory tree that is systematically absent.
	//
	// Individual mistakes do not cluster: nobody typos the same directory
	// 4,611 times. When that many imports share a path prefix and none of them
	// resolve, the tree is produced by something — a registry, a codegen step,
	// a fetch — rather than missing by accident. shadcn/ui is the case that
	// forced this: apps/v4/styles/ contains a README and nothing else, and its
	// contents arrive during a build.
	//
	// The threshold matters. Set it low and real broken imports get excused;
	// set it here and a genuine mistake still stands out as itself.
	const systematic = 10
	if len(group) >= systematic && strings.Contains(prefix, "/") {
		return "an entire directory tree is absent — produced by a build or generator"
	}

	if strings.HasPrefix(prefix, ".") {
		return "relative path with no matching file"
	}
	if strings.HasPrefix(prefix, "@") || strings.Contains(prefix, "/") {
		return "path does not exist in a fresh checkout"
	}
	return "not a file, a package, or a known virtual module"
}

func allMatch(group []Unresolvable, pred func(string) bool) bool {
	if len(group) == 0 {
		return false
	}
	for _, u := range group {
		if !pred(u.File) {
			return false
		}
	}
	return true
}

func isTestFixture(path string) bool {
	for _, marker := range []string{
		"/tests/", "/test/", "__tests__/", "__testfixtures__/",
		"/fixtures/", "/samples/", "/e2e/", "/__snapshots__/",
	} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return strings.HasPrefix(path, "tests/") || strings.HasPrefix(path, "test/")
}

// looksGenerated recognises names a build step produces.
func looksGenerated(spec string) bool {
	for _, seg := range strings.Split(spec, "/") {
		if generatedDirs[seg] {
			return true
		}
	}
	// Names that mark a file as generated regardless of directory: Astro's
	// "*.prebuilt.js", and the widespread "*.gen.ts" / "*.generated.ts"
	// convention used by GraphQL codegen and friends.
	base := spec
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		base = spec[i+1:]
	}
	for _, marker := range []string{".prebuilt", ".gen.", ".generated.", "-generated."} {
		if strings.Contains(base, marker) {
			return true
		}
	}
	return strings.HasSuffix(base, ".gen") || strings.HasSuffix(base, ".generated")
}

func specifierIsTemplate(spec string) bool {
	for _, seg := range strings.Split(spec, "/") {
		for _, d := range templateDirs {
			if seg == d {
				return true
			}
		}
	}
	return false
}

// templateDirs hold code that is copied into a new project rather than run in
// place, so its imports refer to files that appear only after scaffolding.
//
// The names vary by project — t3 uses "template", Qwik uses "starters" — and
// missing one turns an entire directory of intentional dangling imports into
// what looks like a broken repository.
var templateDirs = []string{
	"template", "templates", "starter", "starters",
	"scaffold", "scaffolds", "boilerplate", "blueprints",
}

// isPlayground recognises the demo apps a framework repository ships beside
// its library.
//
// Kept as its own category rather than folded into test fixtures: a playground
// is real code that really runs, and saying "this is demo code" is a different
// statement from "this import is meant to fail". Excusing them silently would
// hide genuine breakage in a directory people do read.
func isPlayground(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "playground" || seg == "playgrounds" || seg == "sandbox" || seg == "demo" || seg == "demos" {
			return true
		}
	}
	return false
}

func isTemplate(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		for _, d := range templateDirs {
			if seg == d {
				return true
			}
		}
	}
	return false
}

// UnresolvedSummary is a one-line explanation of the unresolved rate, or "" if
// no single cause dominates.
//
// Aggregating by category rather than by largest group is what makes the
// sentence true: Astro's unresolved imports spread across ninety-odd path
// prefixes, but 88% of them are the same cause — a fresh clone that has not
// been built. The largest single group is only 28%, so reporting that would
// understate it.
func (r *Result) UnresolvedSummary() string {
	if len(r.Unresolved) == 0 {
		return ""
	}
	byCategory := map[string]int{}
	for _, c := range ClusterUnresolved(r.Unresolved) {
		byCategory[c.Category] += c.Count
	}

	best, bestN := "", 0
	for cat, n := range byCategory {
		if n > bestN || (n == bestN && cat < best) {
			best, bestN = cat, n
		}
	}
	share := float64(bestN) / float64(len(r.Unresolved))
	if share < 0.5 {
		return ""
	}
	return fmt.Sprintf("%d of %d (%.0f%%) have the same cause: %s",
		bestN, len(r.Unresolved), share*100, best)
}
