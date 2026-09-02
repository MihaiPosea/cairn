// Package join stitches the two halves of the graph together.
//
// The file graph knows that app/page.tsx imports "framer-motion". The package
// graph knows that framer-motion pulls in nine other packages weighing 4 MB.
// Neither is interesting alone. Joined, they answer the question this whole
// tool exists for: what does this one import actually cost me?
package join

import (
	"sort"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/pkgs"
)

// Report is what the join learned.
type Report struct {
	// PackagesAdded is how many package nodes the package graph contributed
	// beyond those the code referenced directly.
	PackagesAdded int
	// PackageEdges is how many package-to-package edges were added.
	PackageEdges int
	// UnusedDeclared are packages package.json asks for that no file imports.
	//
	// This is not proof they are unused — a package can be needed by a config
	// file, a build step, or a plugin loaded by name. It is a strong hint, and
	// it is labelled as one.
	UnusedDeclared []string
	// ImportedNotDeclared are packages the code imports that package.json does
	// not list. These are real bugs: the code works only because something else
	// happened to install the package, and it will break for the next person.
	ImportedNotDeclared []string
}

// Apply merges the package graph into the file graph.
func Apply(g *graph.Graph, pg *pkgs.Graph) *Report {
	rep := &Report{}

	// Which packages does the code actually import?
	imported := map[string]bool{}
	for _, id := range g.IDs() {
		if n := g.Nodes[id]; n.Kind == graph.Package {
			imported[n.Name] = true
		}
	}

	// Add every installed package as a node, and enrich the ones already
	// present with the version and size the lockfile knows about.
	for _, name := range pg.Names() {
		p := pg.Packages[name]
		id := graph.NodeID(graph.Package, name)
		if existing, ok := g.Nodes[id]; ok {
			existing.Version = p.Version
			existing.Bytes = p.Bytes
			continue
		}
		g.AddNode(&graph.Node{
			ID: id, Kind: graph.Package,
			Name: name, Version: p.Version, Bytes: p.Bytes,
		})
		rep.PackagesAdded++
	}

	// Package-to-package edges. A dependency naming something not installed is
	// skipped rather than invented — an edge to a node that does not exist
	// would corrupt every traversal downstream.
	for _, name := range pg.Names() {
		from := graph.NodeID(graph.Package, name)
		for _, dep := range pg.Packages[name].Deps {
			to := graph.NodeID(graph.Package, dep)
			if _, ok := g.Nodes[to]; !ok {
				continue
			}
			if err := g.AddEdge(graph.Edge{From: from, To: to, Kind: graph.Requires}); err == nil {
				rep.PackageEdges++
			}
		}
	}

	// Declared but never imported.
	for name := range pg.Declared {
		if !imported[name] {
			rep.UnusedDeclared = append(rep.UnusedDeclared, name)
		}
	}
	// Imported but never declared.
	for name := range imported {
		_, dec := pg.Declared[name]
		_, dev := pg.DeclaredDev[name]
		if !dec && !dev {
			rep.ImportedNotDeclared = append(rep.ImportedNotDeclared, name)
		}
	}

	sort.Strings(rep.UnusedDeclared)
	sort.Strings(rep.ImportedNotDeclared)
	return rep
}
