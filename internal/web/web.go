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
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
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
	X     int    `json:"x"`
	Y     int    `json:"y"`
	// Deps and Dependents are counts, shown before anything is clicked.
	Deps       int `json:"deps"`
	Dependents int `json:"dependents"`
	// Blast is how many files transitively depend on this one.
	Blast int `json:"blast"`
	// Bytes is installed size for packages.
	Bytes int64 `json:"bytes,omitempty"`
	// Note carries an entry-point reason or a dead-file warning.
	Note string `json:"note,omitempty"`
	Dead bool   `json:"dead,omitempty"`
	Entry bool  `json:"entry,omitempty"`
}

// Edge is one edge as the page sees it.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Spec string `json:"spec,omitempty"`
	Line int    `json:"line,omitempty"`
}

// Payload is everything the page needs. It is embedded in the HTML, which is
// what makes the exported file work with no server.
type Payload struct {
	Root      string            `json:"root"`
	Nodes     []Node            `json:"nodes"`
	Edges     []Edge            `json:"edges"`
	Stats     map[string]any    `json:"stats"`
	Truncated int               `json:"truncated"`
	Rules     map[string]int    `json:"rules"`
	Warnings  []string          `json:"warnings"`
}

// maxNodes caps what is drawn.
//
// A 5,000-node SVG is both unusable and unreadable. Truncating is honest as
// long as it is stated, so the page says exactly how many nodes it left out
// rather than quietly showing a subset.
const maxNodes = 1200

// Build turns a scan result into a page payload.
func Build(res *scan.Result, includePackages bool) *Payload {
	g := res.Graph

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

	// When there are too many nodes, keep the ones with the largest blast
	// radius: those are the files that matter, and a truncated view of the
	// load-bearing parts beats a complete view of nothing legible.
	blast := make(map[string]int, len(ids))
	for _, id := range ids {
		blast[id] = len(query.ReachableFrom(g, id, query.AllEdges)) - 1
	}
	truncated := 0
	if len(ids) > maxNodes {
		sort.SliceStable(ids, func(i, j int) bool { return blast[ids[i]] > blast[ids[j]] })
		truncated = len(ids) - maxNodes
		ids = ids[:maxNodes]
	}

	entries := map[string]string{}
	for _, e := range query.EntryPoints(g) {
		entries[e.File] = e.Reason
	}
	dead := map[string]string{}
	for _, d := range query.DeadFiles(g, len(res.Unanalyzable) > 0) {
		dead[d.File] = d.Why
	}

	label := func(id string) string {
		if i := strings.Index(id, ":"); i >= 0 {
			return id[i+1:]
		}
		return id
	}

	depth := layers(g, ids)
	pos := position(depth, ids, label)

	inSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		inSet[id] = true
	}

	p := &Payload{
		Root:      res.Root,
		Truncated: truncated,
		Rules:     res.ResolvedVia,
	}
	for _, id := range ids {
		n := g.Nodes[id]
		xy := pos[id]
		node := Node{
			ID: id, Label: label(id), Kind: n.Kind.String(),
			X: xy[0], Y: xy[1],
			Deps: len(g.Dependencies(id)), Dependents: len(g.Dependents(id)),
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
	html, err := Render(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(html), 0o644)
}

// Serve renders the page and serves it on addr until interrupted.
func Serve(p *Payload, addr string) error {
	html, err := Render(p)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	})
	fmt.Printf("cairn is on http://%s  (ctrl-c to stop)\n", addr)
	return http.ListenAndServe(addr, mux)
}
