// Package resolve turns an import specifier into the thing it actually points
// at.
//
// This is the hard part of the whole project. "./utils" might mean utils.ts,
// utils/index.tsx, or nothing. "@/lib/x" depends on a tsconfig alias. "react"
// is a package. "node:fs" is neither. Every ecosystem gets this wrong
// differently and there is no shortcut — only rules, applied in the right
// order, with the failures counted honestly.
package resolve

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Kind is what a specifier resolved to.
type Kind uint8

const (
	// ToFile is a source file inside the repo.
	ToFile Kind = iota
	// ToPackage is an installed dependency, treated as a single unit.
	ToPackage
	// ToBuiltin is a runtime builtin such as node:fs.
	ToBuiltin
	// Unresolved means none of the rules matched. Always carries a Reason.
	Unresolved
)

func (k Kind) String() string {
	switch k {
	case ToFile:
		return "file"
	case ToPackage:
		return "package"
	case ToBuiltin:
		return "builtin"
	case Unresolved:
		return "unresolved"
	}
	return "unknown"
}

// Result is where a specifier ended up.
type Result struct {
	Kind Kind
	// Path is repo-relative and slash-separated, for ToFile.
	Path string
	// Package is the package name for ToPackage, e.g. "react" or "@scope/pkg".
	Package string
	// Subpath is anything after the package name, e.g. "fp" in "lodash/fp".
	Subpath string
	// Name is the builtin's name for ToBuiltin.
	Name string
	// Reason explains an Unresolved result. This text is shown to users, so it
	// says what was tried, not just that it failed.
	Reason string
	// Via records which rule matched, for debugging and for the UI.
	Via string
	// CaseMismatch holds the specifier's casing when it differs from the file
	// actually on disk.
	//
	// macOS and Windows filesystems are case-insensitive, so `import
	// "./Utils"` finds utils.ts locally and fails on Linux CI. Resolving it
	// silently would also split the file into two graph nodes: one from the
	// directory walk, one from the import, leaving the real file looking dead.
	CaseMismatch string
}

// extensionLadder is tried in order when a specifier has no usable extension.
//
// Order matters: TypeScript before JavaScript, because a repo mid-migration
// often has both utils.ts and a stale utils.js, and the compiler prefers .ts.
var extensionLadder = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".json"}

// jsToTS maps a JavaScript extension to the TypeScript ones that can produce it.
//
// In ESM TypeScript you must write `import "./x.js"` even though the file on
// disk is x.ts — the specifier describes the output, not the source. Miss this
// and every ESM-strict TypeScript repo looks broken.
var jsToTS = map[string][]string{
	".js":  {".ts", ".tsx"},
	".jsx": {".tsx"},
	".mjs": {".mts"},
	".cjs": {".cts"},
}

// nodeBuiltins is the set of module names Node provides itself.
var nodeBuiltins = map[string]bool{}

func init() {
	for _, n := range strings.Split("assert async_hooks buffer child_process cluster console constants crypto dgram diagnostics_channel dns domain events fs http http2 https inspector module net os path perf_hooks process punycode querystring readline repl stream string_decoder sys timers tls trace_events tty url util v8 vm wasi worker_threads zlib", " ") {
		nodeBuiltins[n] = true
	}
}

// Resolver answers "what does this specifier point at" for one repo.
//
// It is read-only after construction and safe for concurrent use, which is what
// lets the scanner fan out across files.
type Resolver struct {
	root     string // absolute repo root
	tsconfig *TSConfig
	aliases  []alias // precompiled, longest prefix first

	// dirs caches directory listings: path -> set of file names.
	dirs sync.Map
}

// alias is one compiled tsconfig paths rule.
type alias struct {
	prefix   string   // text before the '*', or the whole pattern if none
	suffix   string   // text after the '*'
	wildcard bool     //
	targets  []string // raw target patterns
}

// New builds a Resolver for a repo root, loading its tsconfig if present.
func New(root string) (*Resolver, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cfg, cfgErr := LoadTSConfig(abs)
	r := &Resolver{
		root:     abs,
		tsconfig: cfg,
	}
	r.compileAliases()
	// A broken tsconfig is reported but not fatal — we simply have no aliases.
	return r, cfgErr
}

// compileAliases turns the paths map into a list ordered by descending prefix
// length.
//
// TypeScript resolves ambiguity by longest matching prefix: given both "@/*"
// and "@/lib/*", the specifier "@/lib/x" must use the second. Sorting once at
// construction makes the lookup a simple first-match scan.
func (r *Resolver) compileAliases() {
	for pattern, targets := range r.tsconfig.Paths {
		a := alias{targets: targets}
		if i := strings.Index(pattern, "*"); i >= 0 {
			a.wildcard = true
			a.prefix = pattern[:i]
			a.suffix = pattern[i+1:]
		} else {
			a.prefix = pattern
		}
		r.aliases = append(r.aliases, a)
	}
	sort.SliceStable(r.aliases, func(i, j int) bool {
		return len(r.aliases[i].prefix) > len(r.aliases[j].prefix)
	})
}

// Root returns the absolute repo root.
func (r *Resolver) Root() string { return r.root }

// HasAliases reports whether a tsconfig contributed any path aliases.
func (r *Resolver) HasAliases() bool { return len(r.aliases) > 0 }

// Resolve answers what specifier points at, as imported from fromFile.
// fromFile is repo-relative and slash-separated.
//
// The rules are applied in this order, and the order is the design:
//
//  1. explicit node: prefix        — unambiguous, cheap, check first
//  2. relative or absolute path    — the common case
//  3. tsconfig path alias          — looks bare but is really a path
//  4. bare node builtin            — "fs" with no prefix
//  5. bare specifier               — a package
//
// Aliases must be tried before treating something as a package, or "@/lib/x"
// in a Next.js repo becomes a phantom dependency on a package named "@".
func (r *Resolver) Resolve(fromFile, specifier string) Result {
	if specifier == "" {
		return Result{Kind: Unresolved, Reason: "empty specifier"}
	}

	// Bundlers attach a resource query or fragment to a specifier to change
	// how a file is loaded: "./shader.glsl?raw", "./worker?worker",
	// "./sprite.svg#icon". The suffix is an instruction to the bundler, not
	// part of the path, so it is stripped before resolving and kept on the
	// edge for display.
	if i := strings.IndexAny(specifier, "?#"); i > 0 {
		specifier = specifier[:i]
	}

	// 1. node: prefix
	if rest, ok := strings.CutPrefix(specifier, "node:"); ok {
		return Result{Kind: ToBuiltin, Name: "node:" + rest, Via: "node-prefix"}
	}

	// 2. relative or absolute path
	if strings.HasPrefix(specifier, ".") || strings.HasPrefix(specifier, "/") {
		base := filepath.Dir(filepath.Join(r.root, filepath.FromSlash(fromFile)))
		if strings.HasPrefix(specifier, "/") {
			base = r.root
		}
		target := filepath.Join(base, filepath.FromSlash(specifier))
		if res, ok := r.tryFile(target); ok {
			// An import may not climb out of the repository. Allowing it would
			// put nodes with "../" paths into the graph, which every traversal
			// downstream then treats as project files.
			if strings.HasPrefix(res.Path, "..") {
				return Result{
					Kind:   Unresolved,
					Reason: specifier + " resolves outside the repository",
					Via:    "relative",
				}
			}
			res.Via = "relative"
			return res
		}
		return Result{
			Kind:   Unresolved,
			Reason: "no file matches " + specifier + " from " + fromFile,
			Via:    "relative",
		}
	}

	// 3. tsconfig path alias
	if res, ok := r.tryAliases(specifier); ok {
		return res
	}

	// 4. bare builtin
	if nodeBuiltins[specifier] {
		return Result{Kind: ToBuiltin, Name: specifier, Via: "bare-builtin"}
	}

	// 5. package
	name, sub := splitPackage(specifier)
	if name == "" {
		return Result{Kind: Unresolved, Reason: "not a valid package name: " + specifier}
	}
	return Result{Kind: ToPackage, Package: name, Subpath: sub, Via: "bare"}
}

// tryAliases applies tsconfig paths, longest prefix first.
func (r *Resolver) tryAliases(specifier string) (Result, bool) {
	for _, a := range r.aliases {
		var capture string
		if a.wildcard {
			if !strings.HasPrefix(specifier, a.prefix) || !strings.HasSuffix(specifier, a.suffix) {
				continue
			}
			if len(specifier) < len(a.prefix)+len(a.suffix) {
				continue
			}
			capture = specifier[len(a.prefix) : len(specifier)-len(a.suffix)]
		} else if specifier != a.prefix {
			continue
		}

		for _, target := range a.targets {
			candidate := strings.Replace(target, "*", capture, 1)
			abs := filepath.Join(r.tsconfig.BaseURL, filepath.FromSlash(candidate))
			if res, ok := r.tryFile(abs); ok {
				res.Via = "tsconfig-paths"
				return res, true
			}
		}
		// The alias matched but nothing on disk did. That is a broken import,
		// not a package — saying so is far more useful than inventing a
		// dependency on a package called "@".
		return Result{
			Kind:   Unresolved,
			Reason: "tsconfig alias matched " + specifier + " but no target file exists",
			Via:    "tsconfig-paths",
		}, true
	}
	return Result{}, false
}

// tryFile applies the extension ladder and index-file rules to an absolute
// path, returning a Result on success.
//
// Existence is checked against a cached directory listing rather than one
// os.Stat per candidate. That is faster — the ladder tries up to nine
// extensions per import, so a listing amortises across all of them — and,
// more importantly, it is *case-exact*, which os.Stat is not on macOS or
// Windows.
func (r *Resolver) tryFile(abs string) (Result, bool) {
	// Exact hit, but only for a path that already names a file extension we
	// understand. Without that guard, a directory named "utils" would satisfy
	// "./utils" before we ever look for utils.ts.
	if ext := filepath.Ext(abs); ext != "" {
		if res, ok := r.file(abs); ok {
			return res, true
		}
		// ESM TypeScript: "./x.js" on disk is x.ts.
		if swaps, ok := jsToTS[ext]; ok {
			stem := strings.TrimSuffix(abs, ext)
			for _, alt := range swaps {
				if res, ok := r.file(stem + alt); ok {
					return res, true
				}
			}
		}
	}

	// Extension ladder.
	for _, ext := range extensionLadder {
		if res, ok := r.file(abs + ext); ok {
			return res, true
		}
	}

	// Directory import: ./components -> ./components/index.ts
	for _, ext := range extensionLadder {
		if res, ok := r.file(filepath.Join(abs, "index"+ext)); ok {
			return res, true
		}
	}
	return Result{}, false
}

// file reports whether abs names a real file, matching case exactly, and
// records a mismatch when only the casing differs.
func (r *Resolver) file(abs string) (Result, bool) {
	dir, base := filepath.Split(abs)
	names := r.dirIndex(filepath.Clean(dir))
	if names == nil {
		return Result{}, false
	}

	if names[base] {
		return Result{Kind: ToFile, Path: r.rel(abs)}, true
	}

	// Case-insensitive fallback. The file exists but the import spells it
	// differently: it works here and breaks on Linux. Resolve to the real file
	// so the graph stays correct, and report the discrepancy.
	lower := strings.ToLower(base)
	for name := range names {
		if strings.ToLower(name) == lower {
			real := filepath.Join(filepath.Clean(dir), name)
			return Result{
				Kind:         ToFile,
				Path:         r.rel(real),
				CaseMismatch: base,
			}, true
		}
	}
	return Result{}, false
}

// dirIndex returns the set of regular-file names in a directory, cached.
//
// Cached in a sync.Map so the resolver stays safe for concurrent use. A
// missing or unreadable directory caches as an empty set, so a repo with
// thousands of broken imports does not re-stat the same absent directory
// thousands of times.
func (r *Resolver) dirIndex(dir string) map[string]bool {
	if v, ok := r.dirs.Load(dir); ok {
		return v.(map[string]bool)
	}
	names := map[string]bool{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				names[e.Name()] = true
			}
		}
	}
	r.dirs.Store(dir, names)
	return names
}

// rel converts an absolute path to a repo-relative, slash-separated one.
func (r *Resolver) rel(abs string) string {
	rel, err := filepath.Rel(r.root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// splitPackage separates a bare specifier into package name and subpath.
//
//	"react"            -> "react", ""
//	"lodash/fp"        -> "lodash", "fp"
//	"@scope/pkg/sub/x" -> "@scope/pkg", "sub/x"
func splitPackage(spec string) (name, subpath string) {
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") {
		if len(parts) < 2 {
			return "", "" // "@scope" alone is not a package
		}
		return parts[0] + "/" + parts[1], strings.Join(parts[2:], "/")
	}
	return parts[0], strings.Join(parts[1:], "/")
}
