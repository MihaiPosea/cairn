package query

import (
	"path"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
)

// ── blast radius ────────────────────────────────────────────────────────────

// Blast is everything affected by changing one file.
type Blast struct {
	Target string
	// Affected is every file that depends on Target, transitively, with the
	// number of hops away it sits.
	Affected map[string]int
	// Direct is the set one hop away — the files that import Target itself.
	Direct []string
}

// BlastRadius answers "what breaks if I change this".
//
// It follows every edge kind including type-only, because changing a type
// breaks the compile of everything that imports it. The question is "what do I
// have to re-check", not "what ships".
func BlastRadius(g *graph.Graph, fileID string) *Blast {
	reached := ReachableFrom(g, fileID, AllEdges)
	delete(reached, fileID)

	b := &Blast{Target: fileID, Affected: reached}
	for id, depth := range reached {
		if depth == 1 {
			b.Direct = append(b.Direct, id)
		}
	}
	sort.Strings(b.Direct)
	return b
}

// ── dead files ──────────────────────────────────────────────────────────────

// Dead is a file nothing reaches, with the confidence to state it.
type Dead struct {
	File string
	// Why explains the confidence level, and it is shown to the user.
	Why string
}

// EntryPoint is a file the framework or package manifest loads directly.
type EntryPoint struct {
	File   string
	Reason string
}

// EntryPoints finds the files something outside the graph will load.
//
// This is the part of dead-code detection that actually matters. Reachability
// is trivial; knowing where to start is not, and getting it wrong means
// declaring half a Next.js app dead because nothing "imports" a page.
func EntryPoints(g *graph.Graph) []EntryPoint {
	return EntryPointsWith(g, nil)
}

// EntryPointsWith adds paths declared by the repo's own package.json —
// main, module, exports, bin — to the ones recognised by convention.
//
// Without them a library has no entry points at all, and every file in it is
// unreachable. See DeadReport.Bail.
func EntryPointsWith(g *graph.Graph, manifest []string) []EntryPoint {
	declared := map[string]bool{}
	for _, p := range manifest {
		declared[p] = true
	}

	var out []EntryPoint
	for _, id := range g.IDs() {
		n := g.Nodes[id]
		if n.Kind != graph.File {
			continue
		}
		if declared[n.Path] {
			out = append(out, EntryPoint{File: id, Reason: "declared in package.json"})
			continue
		}
		if reason := entryReason(n.Path); reason != "" {
			out = append(out, EntryPoint{File: id, Reason: reason})
		}
	}
	return out
}

// nextRouteFiles are the filenames Next.js loads by convention from app/.
var nextRouteFiles = map[string]bool{
	"page": true, "layout": true, "route": true, "loading": true,
	"error": true, "not-found": true, "template": true, "default": true,
	"global-error": true, "sitemap": true, "robots": true, "manifest": true,
	"opengraph-image": true, "icon": true, "apple-icon": true,
}

func entryReason(p string) string {
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	dir := path.Dir(p)
	top := strings.SplitN(p, "/", 2)[0]

	switch {
	case strings.HasSuffix(base, ".d.ts"):
		return "ambient type declaration"
	case top == "app" && nextRouteFiles[stem]:
		return "Next.js app router convention"
	case top == "pages" || strings.HasPrefix(p, "src/pages/"):
		return "Next.js pages router convention"
	case top == "src" && dir == "src" && stem == "index":
		return "package entry point"
	case dir == "." && stem == "index":
		return "package entry point"
	case stem == "middleware" || stem == "instrumentation":
		return "Next.js runtime hook"
	case strings.HasSuffix(stem, ".config") || strings.HasSuffix(stem, ".setup"):
		return "config file, loaded by tooling"
	case stem == "setupTests" || stem == "setup-tests" || stem == "vitest.setup" || stem == "jest.setup":
		return "test setup, loaded by the runner"
	case top == "public" || top == "static":
		return "served as a static asset"
	case stem == "index" || strings.HasPrefix(stem, "index-"):
		return "an index file, a conventional entry point"
	case stem == "service-worker" || stem == "sw" || stem == "serviceWorker":
		return "service worker, registered by URL"
	case strings.Contains(base, ".test.") || strings.Contains(base, ".spec."):
		return "test file"
	case strings.Contains(p, "__tests__/") || strings.Contains(p, "/e2e/"):
		return "test file"
	case strings.HasPrefix(p, "scripts/") || strings.HasPrefix(p, "bin/"):
		return "script, run directly"
	}
	return ""
}

// DeadReport is the outcome of a dead-file analysis.
type DeadReport struct {
	Files   []Dead
	Entries []EntryPoint
	// Bail is set when the result must not be trusted. When it is non-empty,
	// Files is empty regardless of what the traversal found.
	Bail string
}

// DeadFiles returns files no entry point can reach.
//
// Kept for callers that already know their entry points are sound.
func DeadFiles(g *graph.Graph, hasUnanalyzableImports bool) []Dead {
	return DeadFilesWith(g, nil, hasUnanalyzableImports).Files
}

// DeadFilesWith runs the analysis with manifest-declared entry points.
//
// It refuses to answer when no entry point exists at all. Reachability from an
// empty root set marks every file dead, and "delete your entire codebase" is
// never a useful answer — it is the failure mode that would make this tool
// dangerous rather than merely wrong.
func DeadFilesWith(g *graph.Graph, manifest []string, hasUnanalyzableImports bool) *DeadReport {
	entries := EntryPointsWith(g, manifest)

	files := 0
	for _, id := range g.IDs() {
		if g.Nodes[id].Kind == graph.File {
			files++
		}
	}
	if len(entries) == 0 && files > 0 {
		return &DeadReport{Bail: "no entry points found — nothing here matches a framework convention " +
			"and package.json declares no main, module, exports or bin, so every file would be " +
			"reported dead. Add an entry point or ignore this result."}
	}

	rep := &DeadReport{Entries: entries}
	rep.Files = deadFrom(g, entries, hasUnanalyzableImports)
	return rep
}

func deadFrom(g *graph.Graph, entries []EntryPoint, hasUnanalyzableImports bool) []Dead {
	roots := make([]string, 0, len(entries))
	for _, e := range entries {
		roots = append(roots, e.File)
	}

	reached := Reachable(g, roots, AllEdges)

	var out []Dead
	for _, id := range g.IDs() {
		n := g.Nodes[id]
		if n.Kind != graph.File {
			continue
		}
		if _, ok := reached[id]; ok {
			continue
		}
		why := "no entry point reaches it"
		if hasUnanalyzableImports {
			why = "no entry point reaches it, but this repo has computed import() calls that cairn cannot follow"
		}
		out = append(out, Dead{File: id, Why: why})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// ── why is this here ────────────────────────────────────────────────────────

// Why is the chain that pulled a package into the tree.
type Why struct {
	Target string
	// Path is the shortest chain that reaches the package.
	Path []string
	// Direct is true when one of your own files imports it itself.
	Direct bool
	// From says where the chain starts: "your code" or "package.json".
	//
	// Both are legitimate answers and they mean different things. A package
	// reachable from your code is one you use. A package reachable only from
	// package.json is one your dependencies use - react-dom is the everyday
	// example, since Next.js loads it rather than your code importing it.
	From string
}

// WhyPackage traces the shortest path that explains a package's presence.
//
// It tries your own source first, because "your file imports this" is the most
// actionable answer. Failing that it starts from what package.json declares,
// which explains packages your dependencies pull in behind your back.
func WhyPackage(g *graph.Graph, pkgName string, declared []string) *Why {
	target := graph.NodeID(graph.Package, pkgName)
	if _, ok := g.Nodes[target]; !ok {
		return nil
	}

	var files []string
	for _, id := range g.IDs() {
		if g.Nodes[id].Kind == graph.File {
			files = append(files, id)
		}
	}
	if p := ShortestPath(g, files, target, AllEdges); p != nil {
		return &Why{Target: target, Path: p, Direct: len(p) == 2, From: "your code"}
	}

	roots := make([]string, 0, len(declared))
	for _, name := range declared {
		roots = append(roots, graph.NodeID(graph.Package, name))
	}
	if p := ShortestPath(g, roots, target, PackageEdges); p != nil {
		return &Why{Target: target, Path: p, From: "package.json"}
	}
	return &Why{Target: target}
}

// ── cost ────────────────────────────────────────────────────────────────────

// Cost is what one package pulls in behind it.
type Cost struct {
	Package string
	// Packages is the transitive closure, including the package itself.
	Packages []string
	Bytes    int64
	// Measured is false when sizes were not collected, in which case Bytes is
	// meaningless and must not be shown.
	Measured bool
}

// PackageCost walks the package graph from one package and totals it.
func PackageCost(g *graph.Graph, pkgName string) *Cost {
	start := graph.NodeID(graph.Package, pkgName)
	if _, ok := g.Nodes[start]; !ok {
		return nil
	}

	reached := Reachable(g, []string{start}, PackageEdges)
	c := &Cost{Package: pkgName}
	for id := range reached {
		n := g.Nodes[id]
		c.Packages = append(c.Packages, id)
		c.Bytes += n.Bytes
		if n.Bytes > 0 {
			c.Measured = true
		}
	}
	sort.Strings(c.Packages)
	return c
}

// ── cycles ──────────────────────────────────────────────────────────────────

// Cycle is a group of nodes that all depend on each other.
type Cycle struct {
	Nodes []string
}

// Cycles finds every strongly connected component with more than one member.
//
// Tarjan's algorithm, iterative rather than recursive: a deep dependency chain
// in a large repo will overflow the stack otherwise, and "it crashed on a big
// repo" is the one failure a tool like this cannot afford.
//
// Note that a cycle here is a finding, not an error. JavaScript permits
// circular imports and real codebases are full of them; some are harmless and
// some cause undefined-at-import-time bugs. cairn reports them and lets you
// judge.
func Cycles(g *graph.Graph, follow EdgeFilter) []Cycle {
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	next := 0
	var out []Cycle

	type frame struct {
		node string
		edge int
	}

	for _, root := range g.IDs() {
		if _, done := index[root]; done {
			continue
		}

		callStack := []frame{{node: root}}
		index[root] = next
		low[root] = next
		next++
		stack = append(stack, root)
		onStack[root] = true

		for len(callStack) > 0 {
			f := &callStack[len(callStack)-1]
			edges := g.Dependencies(f.node)

			advanced := false
			for f.edge < len(edges) {
				e := edges[f.edge]
				f.edge++
				if !follow(e) {
					continue
				}
				if _, seen := index[e.To]; !seen {
					index[e.To] = next
					low[e.To] = next
					next++
					stack = append(stack, e.To)
					onStack[e.To] = true
					callStack = append(callStack, frame{node: e.To})
					advanced = true
					break
				}
				if onStack[e.To] && low[f.node] > index[e.To] {
					low[f.node] = index[e.To]
				}
			}
			if advanced {
				continue
			}

			// Done with this node: pop it and propagate its lowlink up.
			done := f.node
			callStack = callStack[:len(callStack)-1]
			if len(callStack) > 0 {
				parent := callStack[len(callStack)-1].node
				if low[parent] > low[done] {
					low[parent] = low[done]
				}
			}

			if low[done] == index[done] {
				var comp []string
				for {
					top := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[top] = false
					comp = append(comp, top)
					if top == done {
						break
					}
				}
				if len(comp) > 1 {
					sort.Strings(comp)
					out = append(out, Cycle{Nodes: comp})
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Nodes[0] < out[j].Nodes[0] })
	return out
}
