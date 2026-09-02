// Package pkgs builds the external half of the graph: the packages a repo has
// installed, and what they depend on.
//
// There are four lockfile formats in common use and they agree on nothing.
// npm writes JSON keyed by install path. bun writes JSONC with arrays. pnpm
// writes YAML. yarn writes two different things depending on major version.
// And plenty of repos have no lockfile at all, or one that disagrees with what
// is actually on disk.
//
// So this package reads whatever it finds, records which source it used, and
// says so. Pretending there is one canonical answer is how tools here end up
// quietly wrong.
package pkgs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Package is one installed dependency.
type Package struct {
	Name    string
	Version string
	// Deps are the package names this one requires.
	Deps []string
	// Dev is true when the package is only reachable through devDependencies.
	Dev bool
	// Bytes is the installed size on disk, filled in by MeasureSizes.
	Bytes int64
}

// Graph is the package half of the dependency graph.
type Graph struct {
	// Source names the file this came from, e.g. "bun.lock", or
	// "node_modules (no lockfile)".
	Source string
	// Declared is what package.json asks for: name -> version range.
	Declared    map[string]string
	DeclaredDev map[string]string
	// Packages is what is actually installed, keyed by name.
	Packages map[string]*Package
	// Warnings records anything the caller should know: duplicate versions,
	// a format we only partly understand, a lockfile that disagrees with disk.
	Warnings []string
}

func newGraph(source string) *Graph {
	return &Graph{
		Source:      source,
		Declared:    map[string]string{},
		DeclaredDev: map[string]string{},
		Packages:    map[string]*Package{},
	}
}

// add records a package, keeping the first version seen.
//
// A real node_modules routinely contains several versions of the same package
// at different depths. Collapsing to one node per name keeps the graph legible
// and matches how people think about "do I depend on lodash"; the duplicates
// become a warning rather than silently vanishing.
func (g *Graph) add(p *Package) {
	if existing, ok := g.Packages[p.Name]; ok {
		if existing.Version != p.Version && p.Version != "" && existing.Version != "" {
			g.Warnings = append(g.Warnings,
				fmt.Sprintf("%s installed at both %s and %s; using %s",
					p.Name, existing.Version, p.Version, existing.Version))
		}
		return
	}
	g.Packages[p.Name] = p
}

// Names returns every installed package name, sorted.
func (g *Graph) Names() []string {
	out := make([]string, 0, len(g.Packages))
	for n := range g.Packages {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// DeclaredCount is how many packages package.json asks for directly.
func (g *Graph) DeclaredCount() int { return len(g.Declared) + len(g.DeclaredDev) }

// Load reads the package graph for a repo, trying each known source in turn.
//
// Order is deliberate: a lockfile is a precise statement of what should be
// installed, while node_modules is a messy record of what happened. Prefer the
// statement, fall back to the evidence.
func Load(root string) (*Graph, error) {
	type source struct {
		file string
		load func(string, string) (*Graph, error)
	}
	for _, s := range []source{
		{"bun.lock", loadBunLock},
		{"package-lock.json", loadNPMLock},
		{"pnpm-lock.yaml", loadPnpmLock},
		{"yarn.lock", loadYarnLock},
	} {
		p := filepath.Join(root, s.file)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		g, err := s.load(root, p)
		if err == nil && g != nil && len(g.Packages) > 0 {
			readDeclared(root, g)
			return g, nil
		}
		// A lockfile we cannot read is worth saying out loud, then we fall
		// through to reading the directory instead of failing the scan.
		g2 := newGraph("node_modules (unreadable " + s.file + ")")
		if err != nil {
			g2.Warnings = append(g2.Warnings, s.file+": "+err.Error())
		}
		if err := walkNodeModules(root, g2); err == nil && len(g2.Packages) > 0 {
			readDeclared(root, g2)
			return g2, nil
		}
	}

	// bun.lockb is binary and undocumented; this is the common path for it.
	g := newGraph("node_modules (no readable lockfile)")
	if _, err := os.Stat(filepath.Join(root, "bun.lockb")); err == nil {
		g.Warnings = append(g.Warnings, "bun.lockb is a binary format cairn does not read; using node_modules instead")
	}
	if err := walkNodeModules(root, g); err != nil {
		return g, err
	}
	readDeclared(root, g)
	return g, nil
}

// readDeclared fills in what the repo's own package.json asks for.
func readDeclared(root string, g *Graph) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return
	}
	var pj struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pj) != nil {
		return
	}
	for k, v := range pj.Dependencies {
		g.Declared[k] = v
	}
	for k, v := range pj.DevDependencies {
		g.DeclaredDev[k] = v
	}
}

// packageNameFromPath turns an install path into a package name.
//
//	node_modules/react                      -> react
//	node_modules/@scope/pkg                 -> @scope/pkg
//	node_modules/a/node_modules/b           -> b       (nested duplicate)
func packageNameFromPath(p string) string {
	i := strings.LastIndex(p, "node_modules/")
	if i < 0 {
		return ""
	}
	rest := p[i+len("node_modules/"):]
	parts := strings.Split(rest, "/")
	if strings.HasPrefix(rest, "@") && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func depNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
