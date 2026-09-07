// Package modules groups a repository's files into the handful of parts a
// person would name if you asked them what the codebase is made of.
//
// The viewer had no such level. It drew directories, auto-opening the biggest
// ones until roughly thirty boxes showed, and drilling in *added* detail
// without ever removing any. On excalidraw the default view was 66 boxes and
// 301 edges; opening five nested folders took it to 268 boxes and 1,159 edges
// across a canvas 15,084 pixels wide. Someone opening a folder to look at four
// files got 1,159 lines crossing the screen, which is the picture that makes
// people decide these tools are useless.
//
// A module map is the fix at the top: every repository measured - nine of
// them, from a 243-file starter to nx at 5,731 - collapses to between four and
// fourteen boxes with at most twenty-five edges between them. That is a
// picture read in seconds, and the thing you click into.
package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

// Module is one part of the repository.
type Module struct {
	// ID is the viewer's group id for this path: "g:packages/excalidraw".
	//
	// It is deliberately the id the browser already computes for the same
	// directory. The page builds a group object per directory carrying its
	// file count, blast radius and depth; agreeing on the identifier means the
	// module map slots into that structure instead of needing a parallel one.
	ID string `json:"id"`
	// Path is the directory, repo-relative with a trailing slash. Empty for
	// the module holding files at the repository root.
	Path string `json:"path"`
	// Name is what to call it: the declared workspace name where there is one,
	// otherwise the directory.
	Name string `json:"name"`
	// Kind is "workspace", "directory" or "root".
	Kind string `json:"kind"`
	// Files is how many files it holds, including nested ones.
	Files int `json:"files"`
	// Entry is the file the outside world enters through, if known.
	Entry string `json:"entry,omitempty"`
	// Desc is the package's own one-line description, when it declares one.
	// A name says where code lives; this says what it is for, and it is the
	// only such sentence anywhere in a repository that can be read cheaply.
	Desc string `json:"desc,omitempty"`
}

// Edge is an import relationship between two modules.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Count is how many individual imports it stands for.
	Count int `json:"count"`
	// ViaEntry reports whether every one of those imports arrives through the
	// target's declared entry point. When false, something is reaching past
	// the front door into another module's internals.
	ViaEntry bool `json:"viaEntry"`
}

// Map is the module view of a repository.
type Map struct {
	Modules []Module `json:"modules"`
	// Parts describes every workspace package in the repository, keyed by the
	// viewer's group id - not only the dozen chosen as top-level modules.
	//
	// The opening view is usually one or two levels in, so the boxes on screen
	// are rarely the modules themselves. Without this, everything a package
	// declares about itself - its name, its purpose, the entry point it wants
	// you to use - was known and then not shown, because the box the reader
	// clicked was not in the module list.
	Parts map[string]Module `json:"parts"`
	// Of maps a node ID to its module ID. Packages are included, not only
	// files: the viewer scopes by this map, and a package left out of it
	// becomes an edge endpoint that resolves to nothing.
	Of     map[string]string `json:"of"`
	Edges  []Edge            `json:"edges"`
	Cycles [][]string        `json:"cycles,omitempty"`
}

// maxModules is the ceiling on how many boxes the top level may show.
//
// Not a rendering limit - the layout would draw forty happily. It is a reading
// limit: past a dozen the picture stops being something you take in at once,
// which is the only thing this level is for.
const maxModules = 12

// splitShare is how much of the repository a module must hold before it is
// worth splitting into its children.
//
// Set low and every directory fragments; set high and a monorepo shows one box
// labelled "packages/". At a fifth of the repo, the nine measured repositories
// land between four and fourteen modules.
const splitShare = 0.20

// Build groups the repository's files into modules.
func Build(res *scan.Result) *Map {
	files := fileNodes(res.Graph)
	m := &Map{Of: map[string]string{}}
	if len(files) == 0 {
		m.Modules = []Module{}
		m.Edges = []Edge{}
		return m
	}

	depth := splitDepths(files)
	ws := workspaceIndex(res.Workspaces)

	// Assign every file, then keep the largest modules. The tail is merged
	// rather than dropped: a file with no module is an edge endpoint the
	// viewer cannot place, and silently losing edges is the failure mode this
	// whole layer exists to avoid.
	assigned := make(map[string]string, len(files))
	count := map[string]int{}
	for _, f := range files {
		p := modulePath(f.Path, depth)
		assigned[f.ID] = p
		count[p]++
	}
	keep := largest(count, maxModules)

	byPath := map[string]*Module{}
	for _, f := range files {
		p := assigned[f.ID]
		if !keep[p] {
			p = otherPath
		}
		mod, ok := byPath[p]
		if !ok {
			mod = newModule(p, ws)
			byPath[p] = mod
		}
		mod.Files++
		m.Of[f.ID] = mod.ID
	}

	// Packages sit outside the tree but are still edge endpoints.
	for _, id := range res.Graph.SortedIDs() {
		if n := res.Graph.Nodes[id]; n != nil && n.Kind != graph.File {
			m.Of[id] = externalID
		}
	}
	if len(m.Of) > len(assigned) {
		byPath[externalPath] = &Module{
			ID: externalID, Path: externalPath, Name: "external packages",
			Kind: "external", Files: len(m.Of) - len(assigned),
		}
	}

	for _, mod := range byPath {
		m.Modules = append(m.Modules, *mod)
	}
	sort.Slice(m.Modules, func(i, j int) bool {
		if m.Modules[i].Files != m.Modules[j].Files {
			return m.Modules[i].Files > m.Modules[j].Files
		}
		return m.Modules[i].Path < m.Modules[j].Path
	})

	for i := range m.Modules {
		m.Modules[i].Desc = describe(res.Root, m.Modules[i].Path)
	}

	m.Parts = map[string]Module{}
	for _, w := range res.Workspaces {
		dir := strings.TrimSuffix(w.Dir, "/")
		if dir == "" {
			continue
		}
		part := Module{
			ID: "g:" + dir, Path: dir + "/", Name: w.Name, Kind: "workspace",
			Entry: w.Entry, Desc: describe(res.Root, dir+"/"),
		}
		if part.Name == "" {
			part.Name = dir
		}
		m.Parts[part.ID] = part
	}

	m.Edges = moduleEdges(res.Graph, m.Of, byPath)
	m.Cycles = moduleCycles(m.Modules, m.Edges)
	return m
}

const (
	otherPath    = "\x00other"
	externalPath = "\x00external"
	externalID   = "g:\x00external"
)

func newModule(path string, ws map[string]scan.Workspace) *Module {
	switch path {
	case otherPath:
		return &Module{ID: "g:" + otherPath, Path: otherPath, Name: "other", Kind: "other"}
	case "":
		return &Module{ID: "g:", Path: "", Name: "repository root", Kind: "root"}
	}
	m := &Module{ID: "g:" + strings.TrimSuffix(path, "/"), Path: path,
		Name: strings.TrimSuffix(path, "/"), Kind: "directory"}
	if w, ok := ws[strings.TrimSuffix(path, "/")]; ok {
		m.Kind = "workspace"
		if w.Name != "" {
			m.Name = w.Name
		}
		m.Entry = w.Entry
	}
	return m
}

// describe reads a module's own account of itself out of its package.json.
func describe(root, modPath string) string {
	if modPath == "" || strings.HasPrefix(modPath, "\x00") {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(modPath), "package.json"))
	if err != nil || len(b) > 1<<20 {
		return ""
	}
	var pkg struct {
		Description string `json:"description"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return ""
	}
	d := strings.TrimSpace(pkg.Description)
	if len(d) > 200 {
		d = d[:200] + "…"
	}
	return d
}

func workspaceIndex(list []scan.Workspace) map[string]scan.Workspace {
	ws := make(map[string]scan.Workspace, len(list))
	for _, w := range list {
		ws[strings.TrimSuffix(w.Dir, "/")] = w
	}
	return ws
}

func fileNodes(g *graph.Graph) []*graph.Node {
	var out []*graph.Node
	for _, id := range g.SortedIDs() {
		if n := g.Nodes[id]; n != nil && n.Kind == graph.File {
			out = append(out, n)
		}
	}
	return out
}

// splitDepths decides how deep to cut each top-level directory.
//
// Everything starts at one segment. The largest module holding at least
// splitShare of the repository is replaced by its children, repeatedly, while
// that keeps the total within the cap. This is what makes a monorepo show its
// real top-level shape - packages/, e2e/, docs/ - rather than either one box
// labelled "packages/" or fifty-seven workspace packages.
func splitDepths(files []*graph.Node) map[string]int {
	total := len(files)
	depth := map[string]int{}
	for _, f := range files {
		depth[topSegment(f.Path)] = 1
	}

	for pass := 0; pass < 8; pass++ {
		count := map[string]int{}
		for _, f := range files {
			count[modulePath(f.Path, depth)]++
		}

		var best string
		var bestN int
		for p, n := range count {
			if p == "" || float64(n) < float64(total)*splitShare {
				continue
			}
			if n > bestN || (n == bestN && p < best) {
				best, bestN = p, n
			}
		}
		if best == "" {
			return depth
		}

		// The first segment of the module path, not topSegment: that returns
		// "" for a string with no slash, which "packages" is once the trailing
		// slash comes off.
		top := strings.SplitN(strings.TrimSuffix(best, "/"), "/", 2)[0]
		kids := map[string]bool{}
		for _, f := range files {
			if modulePath(f.Path, depth) != best {
				continue
			}
			if segs := strings.Split(f.Path, "/"); len(segs) > depth[top]+1 {
				kids[strings.Join(segs[:depth[top]+1], "/")] = true
			}
		}
		if len(kids) < 2 || len(count)-1+len(kids) > maxModules {
			return depth
		}
		depth[top]++
	}
	return depth
}

func topSegment(path string) string {
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}

// modulePath is the module a file belongs to at the given split depths. A file
// with no directory component belongs to the repository root module, "".
func modulePath(path string, depth map[string]int) string {
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		return ""
	}
	d := depth[segs[0]]
	if d < 1 {
		d = 1
	}
	if len(segs) <= d {
		d = len(segs) - 1
	}
	return strings.Join(segs[:d], "/") + "/"
}

func largest(count map[string]int, n int) map[string]bool {
	paths := make([]string, 0, len(count))
	for p := range count {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		if count[paths[i]] != count[paths[j]] {
			return count[paths[i]] > count[paths[j]]
		}
		return paths[i] < paths[j]
	})
	keep := map[string]bool{}
	for i, p := range paths {
		if i < n {
			keep[p] = true
		}
	}
	return keep
}

// moduleEdges aggregates file imports into module relationships, recording for
// each whether every import it stands for arrived through the target's entry
// point.
func moduleEdges(g *graph.Graph, of map[string]string, byPath map[string]*Module) []Edge {
	entry := map[string]string{}
	for _, m := range byPath {
		if m.Entry != "" {
			entry[m.ID] = m.Entry
		}
	}

	type agg struct {
		n       int
		allWays bool
	}
	acc := map[[2]string]*agg{}
	for _, id := range g.SortedIDs() {
		from, ok := of[id]
		if !ok {
			continue
		}
		for _, e := range g.Dependencies(id) {
			to, ok := of[e.To]
			if !ok || to == from {
				continue
			}
			k := [2]string{from, to}
			a := acc[k]
			if a == nil {
				a = &agg{allWays: true}
				acc[k] = a
			}
			a.n++
			if want, has := entry[to]; has && graph.NodeID(graph.File, want) != e.To {
				a.allWays = false
			}
		}
	}

	out := make([]Edge, 0, len(acc))
	for k, a := range acc {
		out = append(out, Edge{From: k[0], To: k[1], Count: a.n, ViaEntry: a.allWays})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// moduleCycles finds groups of modules that depend on each other in a loop.
//
// A cycle between two files is often deliberate. A cycle between two modules
// means the boundary is not real, which is worth saying out loud.
func moduleCycles(mods []Module, edges []Edge) [][]string {
	g := graph.New()
	for _, m := range mods {
		g.AddNode(&graph.Node{ID: m.ID, Kind: graph.File, Path: m.Path})
	}
	for _, e := range edges {
		_ = g.AddEdge(graph.Edge{From: e.From, To: e.To, Kind: graph.Import})
	}
	var out [][]string
	for _, c := range query.Cycles(g, query.AllEdges) {
		out = append(out, c.Nodes)
	}
	return out
}
