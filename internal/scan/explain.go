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

	parts := strings.Split(rest, "/")
	if len(parts) <= 2 {
		return prefix + rest
	}
	return prefix + strings.Join(parts[:2], "/") + "/…"
}

// categorise gives a group a plain-language cause.
func categorise(prefix string, group []Unresolvable) string {
	for _, seg := range strings.Split(strings.TrimPrefix(prefix, "./"), "/") {
		if buildDirs[seg] {
			return "points into a build output — run the repo's build first"
		}
	}
	if strings.HasPrefix(prefix, ".") {
		return "relative path with no matching file"
	}
	if strings.HasPrefix(prefix, "@") || strings.Contains(prefix, "/") {
		return "alias or generated path that does not exist in a fresh checkout"
	}
	return "not a file, a package, or a known virtual module"
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
