// Package web renders the graph as a single self-contained HTML page.
//
// One file, no build step, no npm. That decision does double duty: it keeps
// cairn a single Go binary, and it makes "export something you can send
// someone" the same artifact as the live view rather than a second
// implementation of it.
package web

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

//go:embed app.html
var appHTML string

// Node is one node as the page sees it.
type Node struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// Depth is the node's layer: the longest path from an entry point. The
	// browser packs the layout, because what is on screen changes as groups
	// expand and collapse.
	Depth int `json:"depth"`
	// Deps and Dependents are counts, shown before anything is clicked.
	Deps       int `json:"deps"`
	Dependents int `json:"dependents"`
	// Blast is how many files transitively depend on this one.
	Blast int `json:"blast"`
	// Bytes is installed size for packages.
	Bytes int64 `json:"bytes,omitempty"`
	// Note carries an entry-point reason or a dead-file warning.
	Note  string `json:"note,omitempty"`
	Dead  bool   `json:"dead,omitempty"`
	Entry bool   `json:"entry,omitempty"`
}

// Edge is one edge as the page sees it.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Spec string `json:"spec,omitempty"`
	Line int    `json:"line,omitempty"`
	// Text is the source line that created this edge.
	//
	// Carried in the payload rather than fetched, so the exported single file
	// shows real code with no server. A name tells you two files are connected;
	// the line tells you what the connection is, which is the thing someone
	// actually wants to see.
	Text string `json:"text,omitempty"`
}

// Payload is everything the page needs. It is embedded in the HTML, which is
// what makes the exported file work with no server.
type Payload struct {
	Root      string         `json:"root"`
	Nodes     []Node         `json:"nodes"`
	Edges     []Edge         `json:"edges"`
	Stats     map[string]any `json:"stats"`
	Truncated int            `json:"truncated"`
	Rules     map[string]int `json:"rules"`
	Warnings  []string       `json:"warnings"`

	// ExactBlast reports whether Node.Blast is the transitive figure or the
	// cheaper direct-dependent count used on very large graphs.
	ExactBlast bool `json:"exactBlast"`

	// Exported marks a standalone file with no server behind it, so the page
	// hides the parts that would need one.
	Exported bool `json:"exported,omitempty"`

	// Modules is the top level of the picture: the handful of parts someone
	// would name if asked what this codebase is made of. The page opens here
	// and scopes everything it draws to one of them at a time.
	Modules []modules.Module `json:"modules"`
	// ModuleOf maps a node ID to its module. Packages are included as well as
	// files — a node missing from this map is an edge endpoint the page
	// cannot place, and it would drop the edge without saying so.
	ModuleOf map[string]string `json:"moduleOf"`
	// ModuleEdges are the import relationships between modules.
	ModuleEdges []modules.Edge `json:"moduleEdges"`
	// ModuleCycles are modules that depend on each other in a loop. A cycle
	// between two files is often deliberate; one between two modules means
	// the boundary is not real.
	ModuleCycles [][]string `json:"moduleCycles,omitempty"`

	// Report is the readable account of the repository: what it is, and what
	// is worth knowing about it. A count is not a finding — "cycles 5" tells
	// nobody anything, so each of these names the thing and what it costs.
	Report Report `json:"report"`
}

// Report is the second tab: findings, not counters.
type Report struct {
	Entries []EntryPoint `json:"entries"`
	// EntryBail explains why dead-file analysis was refused, when it was.
	EntryBail string `json:"entryBail,omitempty"`
	// Cycles are import loops, as readable chains, worst first.
	Cycles []Finding `json:"cycles"`
	// Unreachable groups files nothing reaches by the directory holding them.
	Unreachable []Finding `json:"unreachable"`
	// Heavy are the files with the largest blast radius: change one and this
	// many others are downstream of it.
	Heavy []Finding `json:"heavy"`
	// Boundary are imports reaching past a module entry point into its
	// internals.
	Boundary []Finding `json:"boundary"`
	// UnresolvedBy groups what could not be resolved, by cause.
	UnresolvedBy []Finding `json:"unresolvedBy"`
}

// EntryPoint is a file the outside world enters through, and why we think so.
type EntryPoint struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Finding is one row of the report: a thing, a number, and where to look.
type Finding struct {
	// Title names the thing, in the reader's terms.
	Title string `json:"title"`
	// Detail says what it costs or how it was decided.
	Detail string `json:"detail"`
	// N is the number the row is ranked by.
	N int `json:"n,omitempty"`
	// Go is a node or module ID the graph tab can navigate to.
	Go string `json:"go,omitempty"`
	// Items are the members, for rows that stand for several things.
	Items []string `json:"items,omitempty"`
}

// maxNodes caps what is sent.
//
// The cap used to be 1,200 because that was as many boxes as could be drawn.
// It is far higher now: the page groups files by directory and shows a few
// dozen nodes until you expand one, so the limit is the payload size rather
// than the picture. Truncation is still stated when it happens.
const maxNodes = 20000

// Build turns a scan result into a page payload.
func Build(res *scan.Result, includePackages bool) *Payload {
	g := res.Graph
	src := newSourceCache(res.Root)

	var ids []string
	for _, id := range g.IDs() {
		n := g.Nodes[id]
		switch n.Kind {
		case graph.File:
			ids = append(ids, id)
		case graph.Package:
			if includePackages {
				ids = append(ids, id)
			}
		}
	}

	// When there are too many nodes, keep the most-depended-on: a truncated
	// view of the load-bearing parts beats a complete view of nothing legible.
	//
	// Ranking uses direct dependents, which is one pass over the edges.
	// Ranking by *transitive* blast radius would need a traversal per node —
	// O(nodes x edges), which on a 50,000-file repo with 75,000 edges took
	// longer than two minutes and looked like a hang. Direct dependents is a
	// good proxy for "load-bearing" and costs nothing.
	truncated := 0
	if len(ids) > maxNodes {
		direct := make(map[string]int, len(ids))
		for _, id := range ids {
			direct[id] = len(g.Dependents(id))
		}
		sort.SliceStable(ids, func(i, j int) bool { return direct[ids[i]] > direct[ids[j]] })
		truncated = len(ids) - maxNodes
		ids = ids[:maxNodes]
	}

	// Transitive blast radius costs one traversal per node — O(nodes x edges).
	// That was fine when only 1,200 nodes were sent and is not now, so above a
	// threshold the cheaper direct-dependent count stands in and the payload
	// says so rather than quietly reporting a different number under the same
	// name.
	const exactBlastLimit = 4000
	blast := make(map[string]int, len(ids))
	exact := len(ids) <= exactBlastLimit
	for _, id := range ids {
		if exact {
			blast[id] = len(query.ReachableFrom(g, id, query.AllEdges)) - 1
		} else {
			blast[id] = len(g.Dependents(id))
		}
	}

	deadRep := query.DeadFilesWith(g, res.ManifestEntries, len(res.Unanalyzable) > 0)
	entries := map[string]string{}
	for _, e := range deadRep.Entries {
		entries[e.File] = e.Reason
	}
	dead := map[string]string{}
	for _, d := range deadRep.Files {
		dead[d.File] = d.Why
	}

	label := func(id string) string {
		if i := strings.Index(id, ":"); i >= 0 {
			return id[i+1:]
		}
		return id
	}

	depth := layers(g, ids)

	inSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		inSet[id] = true
	}

	p := &Payload{
		Root:       res.Root,
		Truncated:  truncated,
		Rules:      res.ResolvedVia,
		ExactBlast: exact,
	}
	for _, id := range ids {
		n := g.Nodes[id]
		node := Node{
			ID: id, Label: label(id), Kind: n.Kind.String(),
			Depth: depth[id],
			Deps:  len(g.Dependencies(id)), Dependents: len(g.Dependents(id)),
			Blast: blast[id], Bytes: n.Bytes,
		}
		if reason, ok := entries[id]; ok {
			node.Entry, node.Note = true, reason
		}
		if why, ok := dead[id]; ok {
			node.Dead, node.Note = true, why
		}
		p.Nodes = append(p.Nodes, node)
	}
	for _, id := range ids {
		for _, e := range g.Dependencies(id) {
			if !inSet[e.To] {
				continue
			}
			p.Edges = append(p.Edges, Edge{
				From: e.From, To: e.To, Kind: e.Kind.String(),
				Spec: e.Specifier, Line: e.Line,
				Text: src.line(g.Nodes[id].Path, e.Line),
			})
		}
	}

	s := g.Stats()
	p.Stats = map[string]any{
		"files":       res.FilesScanned,
		"imports":     res.ImportsFound,
		"packages":    s[graph.Package],
		"unresolved":  len(res.Unresolved),
		"cycles":      len(query.Cycles(g, query.AllEdges)),
		"dead":        len(dead),
		"entryPoints": len(entries),
	}
	if res.Packages != nil {
		p.Stats["declared"] = res.Packages.DeclaredCount()
		p.Stats["source"] = res.Packages.Source
		p.Warnings = res.Packages.Warnings
	}

	// Built from the whole graph, not the possibly-truncated node list: the
	// module map is the one view that must describe the entire repository,
	// since it is what the reader sees first and navigates by.
	mm := modules.Build(res)

	// Keep only what the page can actually draw. Packages are in the graph
	// even when they are left out of the payload, so the map would otherwise
	// offer an "external packages" module holding 84 nodes that do not exist
	// here — a box the reader can click into and find empty.
	drawn := make(map[string]string, len(p.Nodes))
	used := map[string]bool{}
	for _, n := range p.Nodes {
		if m, ok := mm.Of[n.ID]; ok {
			drawn[n.ID] = m
			used[m] = true
		}
	}
	p.ModuleOf = drawn
	p.Modules = make([]modules.Module, 0, len(mm.Modules))
	for _, m := range mm.Modules {
		if used[m.ID] {
			p.Modules = append(p.Modules, m)
		}
	}
	p.ModuleEdges = make([]modules.Edge, 0, len(mm.Edges))
	for _, e := range mm.Edges {
		if used[e.From] && used[e.To] {
			p.ModuleEdges = append(p.ModuleEdges, e)
		}
	}
	p.Report = buildReport(res, mm, deadRep, entries, blast, used)

	for _, c := range mm.Cycles {
		keep := true
		for _, id := range c {
			if !used[id] {
				keep = false
			}
		}
		if keep {
			p.ModuleCycles = append(p.ModuleCycles, c)
		}
	}
	return p
}

// Render produces the standalone HTML page.
func Render(p *Payload) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	tpl, err := template.New("app").Parse(appHTML)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	// The payload is injected as JSON inside a script tag. html/template's
	// JS context escaping handles the "</script>" problem correctly; doing
	// this by hand is how XSS gets into a local dev tool.
	if err := tpl.Execute(&sb, map[string]any{
		"Data": template.JS(data),
		"Root": p.Root,
	}); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// Export writes the standalone page to a file.
func Export(p *Payload, path string) error {
	p.Exported = true
	html, err := Render(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(html), 0o644)
}

// Serve renders the page and serves it on addr until interrupted.
//
// It also serves file contents, so the page can show the code behind an edge
// rather than only its name. The exported single file has no server and falls
// back to the import lines carried in the payload.
func Serve(p *Payload, addr string) error {
	html, err := Render(p)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	})
	mux.HandleFunc("/source", func(w http.ResponseWriter, r *http.Request) {
		body, err := readRepoFile(p.Root, r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(body)
	})
	fmt.Printf("cairn is on http://%s  (ctrl-c to stop)\n", addr)
	return http.ListenAndServe(addr, mux)
}

// readRepoFile returns a file's contents, refusing anything outside the repo.
//
// The path arrives from a query string, so it is attacker-controlled in the
// only sense that matters here: a page in another tab could ask for it. The
// resolved path is required to stay under the root, which rules out "..",
// absolute paths, and symlinks pointing elsewhere.
func readRepoFile(root, rel string) ([]byte, error) {
	if rel == "" {
		return nil, errors.New("no path")
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, errors.New("not found")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, errors.New("not found")
	}
	if resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(filepath.Separator)) {
		return nil, errors.New("outside the repository")
	}
	fi, err := os.Stat(resolved)
	if err != nil || fi.IsDir() || fi.Size() > maxSourceBytes {
		return nil, errors.New("not readable")
	}
	return os.ReadFile(resolved)
}
