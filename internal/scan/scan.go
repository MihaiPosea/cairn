// Package scan walks a repository, parses every file it understands, resolves
// the imports it finds, and builds the graph.
//
// Parsing is CPU-bound and embarrassingly parallel; the graph is not safe for
// concurrent mutation. So the design is: fan out to parse, funnel back to
// build. Workers never touch the graph, which removes the whole class of race
// this would otherwise have.
package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/join"
	"github.com/MihaiPosea/cairn/internal/lang"
	"github.com/MihaiPosea/cairn/internal/lang/jsts"
	"github.com/MihaiPosea/cairn/internal/pkgs"
	"github.com/MihaiPosea/cairn/internal/resolve"
)

// skipDirs are never descended into.
//
// node_modules is the important one: it holds hundreds of megabytes of code
// that is not yours. Packages are treated as single units, so their internals
// never enter the file graph.
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, ".next": true, ".turbo": true,
	"dist": true, "build": true, "out": true, "coverage": true,
	".svelte-kit": true, ".vercel": true, ".cache": true, "vendor": true,
}

// Unresolvable is one specifier that did not resolve, kept for reporting.
type Unresolvable struct {
	File      string
	Specifier string
	Line      int
	Reason    string
	Kind      lang.ImportKind
}

// Result is everything one scan produced.
type Result struct {
	Graph *graph.Graph
	Root  string

	FilesScanned  int
	FilesParsed   int
	ImportsFound  int
	ParseFailures []string

	// Unresolved lists every specifier that did not resolve, sorted for stable
	// output. This is the tool's honesty metric and is printed on every scan.
	Unresolved []Unresolvable

	// Unanalyzable counts import(expr) calls — real dependencies on something
	// we cannot name. Reported separately because they are a different problem
	// from a broken import.
	Unanalyzable []Unresolvable

	// AliasesLoaded reports whether a tsconfig contributed path aliases. If a
	// repo has them and we did not load them, the unresolved rate explodes and
	// this is the first thing to check.
	AliasesLoaded bool

	// Packages is the external half of the graph, nil if packages were skipped.
	Packages *pkgs.Graph
	// Join reports what merging the two halves revealed.
	Join *join.Report
	// Disagree is the lockfile-versus-disk comparison, nil if unavailable.
	Disagree *pkgs.Disagreement
}

// Options controls how much work a scan does.
type Options struct {
	// SkipPackages builds only the file graph. Faster, and all that is needed
	// for questions about your own code.
	SkipPackages bool
	// MeasureSizes walks node_modules to get installed byte sizes. It is the
	// most expensive thing cairn does, so it is opt-in.
	MeasureSizes bool
}

// UnresolvedRate is the share of resolvable specifiers that did not resolve.
func (r *Result) UnresolvedRate() float64 {
	if r.ImportsFound == 0 {
		return 0
	}
	return float64(len(r.Unresolved)) / float64(r.ImportsFound)
}

// parsed is one file's worth of work handed back from a worker.
type parsed struct {
	path    string // repo-relative, slash-separated
	imports []lang.RawImport
	err     error
}

// Run scans the repo rooted at dir with default options.
func Run(dir string) (*Result, error) { return RunWith(dir, Options{}) }

// RunWith scans the repo rooted at dir.
func RunWith(dir string, opts Options) (*Result, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	resolver, cfgErr := resolve.New(root)
	_ = cfgErr // a broken tsconfig means no aliases, not a failed scan

	parser := jsts.New()

	files, err := walk(root, parser)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Graph:         graph.New(),
		Root:          root,
		FilesScanned:  len(files),
		AliasesLoaded: resolver.HasAliases(),
	}

	// Every file becomes a node before any edge is added, so an edge can never
	// reference a node that does not exist yet.
	for _, f := range files {
		res.Graph.AddNode(&graph.Node{ID: graph.NodeID(graph.File, f), Kind: graph.File, Path: f})
	}

	for p := range parseAll(root, files, parser) {
		if p.err != nil {
			res.ParseFailures = append(res.ParseFailures, p.path)
			continue
		}
		res.FilesParsed++
		res.ImportsFound += len(p.imports)
		for _, imp := range p.imports {
			addImport(res, resolver, p.path, imp)
		}
	}

	sort.Slice(res.Unresolved, func(i, j int) bool {
		if res.Unresolved[i].File != res.Unresolved[j].File {
			return res.Unresolved[i].File < res.Unresolved[j].File
		}
		return res.Unresolved[i].Line < res.Unresolved[j].Line
	})
	sort.Strings(res.ParseFailures)

	if !opts.SkipPackages {
		attachPackages(root, res, opts)
	}
	return res, nil
}

// attachPackages loads the package half and joins it onto the file graph.
//
// Failure here degrades the scan rather than ending it: a repo with no
// node_modules and no lockfile still has a perfectly good file graph, and
// answering half the questions beats answering none.
func attachPackages(root string, res *Result, opts Options) {
	pg, err := pkgs.Load(root)
	if err != nil || pg == nil {
		return
	}
	if opts.MeasureSizes {
		_ = pkgs.MeasureSizes(root, pg)
	}
	res.Packages = pg
	res.Join = join.Apply(res.Graph, pg)
	if d, err := pkgs.Compare(root, pg); err == nil {
		res.Disagree = d
	}
}

// parseAll fans out across CPUs and funnels results back through one channel.
func parseAll(root string, files []string, parser lang.Parser) <-chan parsed {
	out := make(chan parsed, 64)
	jobs := make(chan string, 64)

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8 // parsing is memory-hungry; past this, disk and GC dominate
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for rel := range jobs {
				src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil {
					out <- parsed{path: rel, err: err}
					continue
				}
				imports, err := parser.Parse(rel, src)
				out <- parsed{path: rel, imports: imports, err: err}
			}
		}()
	}

	go func() {
		for _, f := range files {
			jobs <- f
		}
		close(jobs)
		wg.Wait()
		close(out)
	}()

	return out
}

// addImport resolves one import and records it on the graph.
func addImport(res *Result, r *resolve.Resolver, fromFile string, imp lang.RawImport) {
	from := graph.NodeID(graph.File, fromFile)

	if imp.Kind == lang.Unanalyzable {
		res.Unanalyzable = append(res.Unanalyzable, Unresolvable{
			File: fromFile, Line: imp.Line, Kind: imp.Kind,
			Reason: "computed specifier: " + imp.Expr,
		})
		return
	}

	out := r.Resolve(fromFile, imp.Specifier)

	var to *graph.Node
	switch out.Kind {
	case resolve.ToFile:
		to = &graph.Node{ID: graph.NodeID(graph.File, out.Path), Kind: graph.File, Path: out.Path}
	case resolve.ToPackage:
		// Version is unknown until the package graph is read (M3); the node is
		// keyed by name alone and gets enriched then.
		to = &graph.Node{ID: graph.NodeID(graph.Package, out.Package), Kind: graph.Package, Name: out.Package}
	case resolve.ToBuiltin:
		to = &graph.Node{ID: graph.NodeID(graph.Builtin, out.Name), Kind: graph.Builtin, Name: out.Name}
	default:
		res.Unresolved = append(res.Unresolved, Unresolvable{
			File: fromFile, Specifier: imp.Specifier, Line: imp.Line,
			Reason: out.Reason, Kind: imp.Kind,
		})
		to = &graph.Node{ID: graph.NodeID(graph.Unresolved, imp.Specifier), Kind: graph.Unresolved, Name: imp.Specifier}
	}

	res.Graph.AddNode(to)
	_ = res.Graph.AddEdge(graph.Edge{
		From: from, To: to.ID,
		Kind:      edgeKind(imp.Kind),
		Specifier: imp.Specifier,
		Line:      imp.Line,
	})
}

// edgeKind maps a parsed import form onto a graph edge kind.
//
// Re-exports become plain imports: at runtime `export { x } from "./y"` loads
// ./y exactly like an import does. Only the type-only forms are erased.
func edgeKind(k lang.ImportKind) graph.EdgeKind {
	switch k {
	case lang.TypeOnly, lang.TypeOnlyReexport:
		return graph.TypeOnly
	case lang.Dynamic:
		return graph.Dynamic
	default:
		return graph.Import
	}
}

// walk collects every parseable file, repo-relative and sorted so a scan is
// deterministic regardless of filesystem ordering.
func walk(root string, parser lang.Parser) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable directory is not fatal
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !parser.Handles(path) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(files)
	return files, err
}
