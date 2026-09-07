// Package scan walks a repository, parses every file it understands, resolves
// the imports it finds, and builds the graph.
//
// Parsing is CPU-bound and embarrassingly parallel; the graph is not safe for
// concurrent mutation. So the design is: fan out to parse, funnel back to
// build. Workers never touch the graph, which removes the whole class of race
// this would otherwise have.
package scan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/index"
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
	"coverage":    true,
	".svelte-kit": true, ".vercel": true, ".cache": true,
}

// outputDirs hold generated code - but only where they are actually output.
//
// These names are also perfectly ordinary names for source directories, and
// skipping them everywhere silently loses real code. A 34-repository sweep
// found astro's packages/astro/src/core/build/ excluded in full: a hundred
// lines of hand-written TypeScript that the walk never saw, and nothing said
// so, because a directory that is never descended into leaves no trace.
//
// The distinguishing fact is position, not name. Build output sits beside the
// package.json of the thing it was built from. Source sits under src/.
var outputDirs = map[string]bool{
	"dist": true, "build": true, "out": true, "vendor": true,
}

// isGenerated reports whether a directory of one of the output names is in the
// position build output actually occupies: a direct child of a package root,
// and not underneath a source directory.
func isGenerated(path string) bool {
	if inSource(path) {
		return false
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(filepath.Join(parent, "package.json")); err == nil {
		return true
	}
	// No manifest beside it and not under src/: treat it as output, which is
	// what these names mean at the top of a repository.
	return true
}

func inSource(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		switch seg {
		case "src", "source", "lib":
			return true
		}
	}
	return false
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

	// FromBuildOutput counts imports that named a compiled file which does not
	// exist and were resolved to the source it is built from.
	//
	// Reported rather than left silent: the edge is real and the source is the
	// more useful endpoint, but the reader should know the repo is unbuilt.
	FromBuildOutput int

	// CaseMismatches are imports whose spelling differs from the file on disk.
	//
	// These work on macOS and Windows and fail on Linux, so they are usually
	// discovered by a CI failure rather than by anyone reading the code.
	CaseMismatches []Unresolvable

	// Unanalyzable counts import(expr) calls - real dependencies on something
	// we cannot name. Reported separately because they are a different problem
	// from a broken import.
	Unanalyzable []Unresolvable

	// AliasesLoaded reports whether a tsconfig contributed path aliases. If a
	// repo has them and we did not load them, the unresolved rate explodes and
	// this is the first thing to check.
	AliasesLoaded bool

	// CacheHits and CacheMisses count files served from the parse cache.
	CacheHits, CacheMisses int

	// ManifestEntries are entry points declared outside the code: the repo's
	// own package.json, and the script tags of any HTML document.
	ManifestEntries []string

	// Workspaces are the monorepo packages, empty for a single-package repo.
	//
	// The resolver discovers these to resolve cross-package imports and then
	// threw them away. They are the truest module boundary a repo declares
	// about itself - better than any heuristic over directory names - so the
	// module map and everything built on it need them to escape the scan.
	Workspaces []Workspace

	// ResolvedVia counts how many specifiers each resolution rule handled.
	//
	// This is what makes a verification score mean something. A repo whose
	// imports are all relative never exercises path aliases, so scoring 100%
	// on it says nothing about alias handling. Reporting which rules actually
	// ran turns "we passed" into "we passed, on these rules".
	ResolvedVia map[string]int

	// Packages is the external half of the graph, nil if packages were skipped.
	Packages *pkgs.Graph
	// Join reports what merging the two halves revealed.
	Join *join.Report
	// Disagree is the lockfile-versus-disk comparison, nil if unavailable.
	Disagree *pkgs.Disagreement
}

// Options controls how much work a scan does.
// Workspace is one package of a monorepo, with paths made repo-relative.
type Workspace struct {
	// Name is what other packages import, e.g. "@acme/ui".
	Name string
	// Dir is the package directory, repo-relative and slash-separated.
	Dir string
	// Entry is the source file its package.json points at, repo-relative, or
	// "" if none could be determined.
	Entry string
}

type Options struct {
	// SkipPackages builds only the file graph. Faster, and all that is needed
	// for questions about your own code.
	SkipPackages bool
	// MeasureSizes walks node_modules to get installed byte sizes. It is the
	// most expensive thing cairn does, so it is opt-in.
	MeasureSizes bool
	// NoCache skips the parse cache entirely, in both directions. Used by the
	// correctness harness, which must never measure a cached answer.
	NoCache bool
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

// workspacesOf converts the resolver's absolute workspace paths to
// repo-relative ones. A workspace outside the repo root is dropped rather than
// emitted with a "../" path, which nothing downstream could match against a
// file node.
func workspacesOf(r *resolve.Resolver, root string) []Workspace {
	if r == nil {
		return nil
	}
	var out []Workspace
	for _, w := range r.Workspaces() {
		rel, err := filepath.Rel(root, w.Dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		ws := Workspace{Name: w.Name, Dir: filepath.ToSlash(rel)}
		if ws.Dir == "." {
			ws.Dir = ""
		}
		if w.Entry != "" {
			if e, err := filepath.Rel(root, w.Entry); err == nil && !strings.HasPrefix(e, "..") {
				ws.Entry = filepath.ToSlash(e)
			}
		}
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// Run scans the repo rooted at dir with default options.
func Run(dir string) (*Result, error) { return RunWith(dir, Options{}) }

// RunWith scans the repo rooted at dir.
func RunWith(dir string, opts Options) (*Result, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	// Validate the root before walking it.
	//
	// filepath.WalkDir reports a missing root through the callback, which
	// ignores errors so an unreadable subdirectory cannot abort a scan. The
	// result was that a typo'd path produced "0 files, 0 imports, 0%
	// unresolved" and exit status 0 - indistinguishable from a clean scan of a
	// real repo, which is the worst way to be wrong.
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("cannot scan %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cannot scan %s: it is a file, not a directory", root)
	}

	resolver, cfgErr := resolve.New(root)
	_ = cfgErr // a broken tsconfig means no aliases, not a failed scan

	parser := jsts.New()

	files, err := walk(root, parser)
	if err != nil {
		return nil, err
	}

	var ix *index.Index
	if !opts.NoCache {
		ix = index.Open(root)
	}

	res0Manifest := pkgs.ManifestEntries(root)
	// An HTML document names its entry script directly, which is how every
	// Vite app declares one. Without this, that file and everything only it
	// reaches are reported unreachable.
	res0Manifest = append(res0Manifest, htmlEntries(root, findHTML(root))...)

	// Aliases declared in a bundler config rather than tsconfig. Many projects
	// mirror them into tsconfig for the editor; the ones that do not would
	// otherwise have every aliased import unresolved.
	bundlerAliases := loadBundlerAliases(root, files)
	resolver.AddAliases(root, bundlerAliases)

	res := &Result{
		Graph:           graph.New(),
		Root:            root,
		FilesScanned:    len(files),
		AliasesLoaded:   resolver.HasAliases(),
		ResolvedVia:     map[string]int{},
		ManifestEntries: res0Manifest,
		Workspaces:      workspacesOf(resolver, root),
	}

	// Every file becomes a node before any edge is added, so an edge can never
	// reference a node that does not exist yet.
	for _, f := range files {
		res.Graph.AddNode(&graph.Node{ID: graph.NodeID(graph.File, f), Kind: graph.File, Path: f})
	}

	// Parse in parallel, then process in a fixed order.
	//
	// Workers finish in whatever order the scheduler and disk decide, so
	// consuming the channel directly would add nodes to the graph in a
	// different order on every run. The graph would be equivalent but not
	// identical, which breaks every golden test and makes two scans of an
	// unchanged repo diff against each other. Collecting first and sorting by
	// path costs one slice and buys determinism outright.
	byPath := make(map[string]parsed, len(files))
	for p := range parseAll(root, files, parser, ix) {
		byPath[p.path] = p
	}
	for _, f := range files { // files is already sorted
		p, ok := byPath[f]
		if !ok {
			continue
		}
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

	if ix != nil {
		res.CacheHits, res.CacheMisses = ix.Stats()
		// A cache that fails to save costs a slow scan next time, nothing more.
		_ = ix.Save()
	}

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
//
// Each worker reads a file, hashes it, and consults the cache before parsing.
// The read has to happen regardless - hashing is a few microseconds on top,
// and parsing is what actually costs.
func parseAll(root string, files []string, parser lang.Parser, ix *index.Index) <-chan parsed {
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

				if ix != nil {
					key := cacheKey(rel, src)
					if imports, ok := ix.Get(key); ok {
						out <- parsed{path: rel, imports: imports}
						continue
					}
					imports, err := parser.Parse(rel, src)
					if err == nil {
						ix.Put(key, imports)
					}
					out <- parsed{path: rel, imports: imports, err: err}
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

// loadBundlerAliases reads alias mappings out of any bundler config in the
// repo root.
//
// Only the root is searched: a config nested inside a package usually applies
// to that package's own build, and applying its aliases repo-wide would
// resolve imports that the real bundler would not.
func loadBundlerAliases(root string, files []string) map[string]string {
	out := map[string]string{}
	for _, rel := range files {
		if strings.Contains(rel, "/") || !jsts.IsConfigFile(rel) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		for k, v := range jsts.ExtractAliases(rel, src) {
			if _, dup := out[k]; !dup {
				out[k] = v
			}
		}
	}
	return out
}

// cacheKey combines the file extension with its content hash.
//
// The extension is part of the key because it selects the grammar: the same
// bytes parsed as .ts and as .tsx produce different trees, since <T>x is a
// type assertion in one and JSX in the other. Keying on content alone would
// serve one file's parse for the other.
func cacheKey(rel string, src []byte) string {
	return filepath.Ext(rel) + ":" + index.Hash(src)
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
	if out.Via != "" {
		res.ResolvedVia[out.Via]++
	}

	if out.FromBuildOutput {
		res.FromBuildOutput++
	}
	if out.CaseMismatch != "" {
		res.CaseMismatches = append(res.CaseMismatches, Unresolvable{
			File: fromFile, Specifier: imp.Specifier, Line: imp.Line, Kind: imp.Kind,
			Reason: "imports " + out.CaseMismatch + " but the file on disk is " + filepath.Base(out.Path) + "; this breaks on Linux",
		})
	}

	// A glob import depends on every file it matches, so it becomes one edge
	// per match rather than a single edge to the first.
	if out.Kind == resolve.ToGlob {
		for _, m := range out.Matches {
			n := res.Graph.AddNode(&graph.Node{ID: graph.NodeID(graph.File, m), Kind: graph.File, Path: m})
			_ = res.Graph.AddEdge(graph.Edge{
				From: from, To: n.ID, Kind: edgeKind(imp.Kind),
				Specifier: imp.Specifier, Line: imp.Line,
			})
		}
		return
	}

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
	case resolve.ToVirtual:
		to = &graph.Node{ID: graph.NodeID(graph.Virtual, out.Name), Kind: graph.Virtual, Name: out.Name}
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
			if path != root && outputDirs[name] && isGenerated(path) {
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
