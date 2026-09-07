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
	"regexp"
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
	// ToVirtual is a module synthesised by a framework or bundler.
	ToVirtual
	// ToGlob is a pattern matching many files at once.
	ToGlob
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
	case ToVirtual:
		return "virtual"
	case ToGlob:
		return "glob"
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
	// Matches lists every file a glob specifier expands to, repo-relative.
	//
	// A glob import depends on all of them, so the graph gets an edge to each.
	// Collapsing it to one file would understate the dependency, and dropping
	// it would lose the edges entirely.
	Matches []string
	// Reason explains an Unresolved result. This text is shown to users, so it
	// says what was tried, not just that it failed.
	Reason string
	// Via records which rule matched, for debugging and for the UI.
	Via string
	// Platform is set when the import resolved through a platform-qualified
	// file such as Button.ios.tsx, naming which variant was chosen.
	Platform string
	// FromBuildOutput is true when the import named a compiled file that does
	// not exist and was resolved to the source it is built from.
	FromBuildOutput bool
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
// Order matters twice over. TypeScript comes before JavaScript, because a repo
// mid-migration often has both utils.ts and a stale utils.js, and the compiler
// prefers .ts. And declaration files come last, because a .d.ts describes an
// implementation that may also be present — if utils.ts exists, that is the
// file you want.
//
// Declaration files must be here at all, though. A repo importing "./utils"
// where only utils.d.ts exists is entirely normal — ambient typings, generated
// declarations, .d.ts-only test suites — and leaving them out accounted for
// most of what was still unresolved across Astro, Nx, Vue and TanStack Query.
var extensionLadder = []string{
	".ts", ".tsx", ".mts", ".cts",
	".js", ".jsx", ".mjs", ".cjs", ".json",
	".d.ts", ".d.mts", ".d.cts",
}

// buildToSource maps a build-output directory name onto the source directories
// it is usually compiled from.
//
// A monorepo package routinely imports its own compiled output —
// "../../../dist/core/errors/index.js" — which does not exist until the repo is
// built. In the Astro repo that was 917 imports, 98% of everything unresolved.
//
// The compiled file is a build of a source file that *is* present, and for a
// dependency graph the source is the better answer: it is the file someone can
// open, and the edge is the same edge. So when a path lands in a build
// directory that has nothing in it, the source twin is tried.
//
// Only attempted after the real path fails, so an actually-built repo always
// resolves to its real output.
var buildToSource = map[string][]string{
	"dist":  {"src", "source", "lib"},
	"build": {"src", "source"},
	"lib":   {"src", "source"},
	"es":    {"src", "source"},
	"esm":   {"src", "source"},
	"cjs":   {"src", "source"},
	"out":   {"src", "source"},
	"types": {"src", "source"},
}

// staticDirs are served at the site root by the common frameworks.
var staticDirs = []string{"public", "static", "assets"}

// projectRootsFrom lists the candidate site roots for a file, nearest first:
// every ancestor directory holding a package.json, then the repository root.
func (r *Resolver) projectRootsFrom(fromFile string) []string {
	var out []string
	dir := filepath.Dir(filepath.Join(r.root, filepath.FromSlash(fromFile)))
	for {
		if _, ok := r.dirIndex(dir)["package.json"]; ok {
			out = append(out, dir)
		}
		if dir == r.root || len(dir) <= len(r.root) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if len(out) == 0 || out[len(out)-1] != r.root {
		out = append(out, r.root)
	}
	return out
}

// platformSuffixes are the qualifiers bundlers try before the plain filename.
//
// React Native resolves "./Button" to Button.ios.tsx on iOS and
// Button.android.tsx on Android; Metro, Expo and several web bundlers all do a
// version of this. Without it, every platform-split component in a React Native
// app is an unresolved import.
//
// Order is deterministic rather than platform-correct: cairn is describing the
// whole codebase, not building for one target, so the first match wins and the
// choice is stable between runs.
var platformSuffixes = []string{".native", ".ios", ".android", ".web", ".macos", ".windows"}

// jsToTS maps a JavaScript extension to the TypeScript ones that can produce it.
//
// In ESM TypeScript you must write `import "./x.js"` even though the file on
// disk is x.ts — the specifier describes the output, not the source. Miss this
// and every ESM-strict TypeScript repo looks broken.
var jsToTS = map[string][]string{
	".js":  {".ts", ".tsx", ".d.ts"},
	".jsx": {".tsx"},
	".mjs": {".mts", ".d.mts"},
	".cjs": {".cts", ".d.cts"},
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
	root string // absolute repo root

	// rootConfig is the tsconfig at the repo root, kept for HasAliases and for
	// repos with a single config.
	rootConfig  *TSConfig
	rootAliases []alias

	// subpaths caches the nearest package.json "imports" map for a directory.
	subpaths sync.Map

	// configs caches the nearest tsconfig for a directory: dir -> *dirConfig.
	//
	// A monorepo puts a tsconfig in each package, and only the nearest one
	// applies. Using the root config everywhere turns "@/helpers" into a
	// phantom dependency on a package named "@".
	configs sync.Map

	// workspaces maps a monorepo package name to its location, so a
	// cross-package import lands on source rather than being treated as an
	// external dependency.
	workspaces map[string]*Workspace

	// dirs caches directory listings: path -> set of file names.
	dirs sync.Map
}

// dirConfig is a compiled tsconfig applying to some directory.
type dirConfig struct {
	aliases []alias
}

// alias is one compiled tsconfig paths rule.
type alias struct {
	prefix   string   // text before the '*', or the whole pattern if none
	suffix   string   // text after the '*'
	wildcard bool     //
	targets  []string // raw target patterns
	base     string   // absolute directory the targets are relative to
}

// New builds a Resolver for a repo root, loading its tsconfig if present.
func New(root string) (*Resolver, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cfg, cfgErr := LoadTSConfig(abs)
	r := &Resolver{
		root:       abs,
		rootConfig: cfg,
		workspaces: findWorkspaces(abs),
	}
	r.rootAliases = compileAliases(cfg.Rules)
	// A broken tsconfig is reported but not fatal — we simply have no aliases.
	return r, cfgErr
}

// AddAliases registers path aliases discovered outside tsconfig — typically
// from a bundler config.
//
// They are appended to the root alias set and re-sorted, so the same
// longest-prefix rule applies across both sources. Must be called before the
// resolver is used concurrently.
func (r *Resolver) AddAliases(base string, mapping map[string]string) {
	if len(mapping) == 0 {
		return
	}
	rules := make([]AliasRule, 0, len(mapping))
	for pattern, target := range mapping {
		// A bundler alias is a prefix match, not a glob: "@" -> "/src" means
		// "@/x" becomes "/src/x". Expressing it as "@/*" -> "target/*" reuses
		// the tsconfig machinery exactly.
		p, t := pattern, target
		if !strings.HasSuffix(p, "*") {
			p = strings.TrimSuffix(p, "/") + "/*"
			t = strings.TrimSuffix(t, "/") + "/*"
		}
		rules = append(rules, AliasRule{Pattern: p, Targets: []string{t}, Base: base})
		// Also register the bare form, so "@" alone resolves to the directory's
		// index file.
		rules = append(rules, AliasRule{Pattern: pattern, Targets: []string{target}, Base: base})
	}
	r.rootAliases = append(r.rootAliases, compileAliases(rules)...)
	sort.SliceStable(r.rootAliases, func(i, j int) bool {
		return len(r.rootAliases[i].prefix) > len(r.rootAliases[j].prefix)
	})
	r.configs.Range(func(k, _ any) bool { r.configs.Delete(k); return true })
}

// Workspaces returns the monorepo packages that were discovered, if any.
func (r *Resolver) Workspaces() map[string]*Workspace { return r.workspaces }

// configFor returns the compiled tsconfig nearest to a directory, walking up
// to the repo root. Results are cached per directory.
func (r *Resolver) configFor(dir string) *dirConfig {
	if v, ok := r.configs.Load(dir); ok {
		return v.(*dirConfig)
	}

	var found *dirConfig
	for d := dir; ; {
		if cfg, err := LoadTSConfig(d); err == nil && len(cfg.Rules) > 0 {
			found = &dirConfig{aliases: compileAliases(cfg.Rules)}
			break
		}
		if d == r.root || len(d) <= len(r.root) {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if found == nil {
		found = &dirConfig{aliases: r.rootAliases}
	}
	r.configs.Store(dir, found)
	return found
}

// compileAliases turns a paths map into a list ordered by descending prefix
// length.
//
// TypeScript resolves ambiguity by longest matching prefix: given both "@/*"
// and "@/lib/*", the specifier "@/lib/x" must use the second. Sorting once
// makes the lookup a simple first-match scan.
func compileAliases(rules []AliasRule) []alias {
	var out []alias
	for _, rule := range rules {
		pattern, targets := rule.Pattern, rule.Targets
		a := alias{targets: targets, base: rule.Base}
		if i := strings.Index(pattern, "*"); i >= 0 {
			a.wildcard = true
			a.prefix = pattern[:i]
			a.suffix = pattern[i+1:]
		} else {
			a.prefix = pattern
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return len(out[i].prefix) > len(out[j].prefix)
	})
	return out
}

// Root returns the absolute repo root.
func (r *Resolver) Root() string { return r.root }

// HasAliases reports whether any tsconfig contributed path aliases.
func (r *Resolver) HasAliases() bool { return len(r.rootAliases) > 0 }

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

	// 1b. a '#' subpath import declared in package.json "imports".
	//
	// Tried before the virtual-module check, because a manifest that explains
	// the specifier is better information than "some framework invents this".
	if strings.HasPrefix(specifier, "#") {
		fromDir := filepath.Dir(filepath.Join(r.root, filepath.FromSlash(fromFile)))
		if res, ok := r.tryAliases(r.subpathConfigFor(fromDir), specifier); ok {
			res.Via = "subpath-imports"
			return res
		}
	}

	// 1c. any other scheme, or a framework namespace prefix.
	//
	// Frameworks synthesise modules that never exist on disk: astro:content,
	// virtual:uno.css, bun:sqlite, $app/stores, #imports, and Deno's npm: /
	// jsr: / https: specifiers. Measured on the Astro repo before this
	// existed: 546 of them reported as broken imports, which is both wrong and
	// the kind of noise that makes someone close the tool.
	//
	// A scheme is recognised structurally rather than by keeping a list of
	// frameworks — "word:" is simply not a file path — so a framework invented
	// next year is handled without a change here.
	if name, ok := virtualModule(specifier); ok {
		return Result{Kind: ToVirtual, Name: name, Via: "virtual"}
	}

	// 1d. a glob pattern.
	//
	// Parcel and Vite both let a specifier match many files at once —
	// "../intl/*.json" pulls in every locale file beside it. React Spectrum
	// uses this heavily; treated as a single path it resolves to nothing and
	// the dependency on all of those files is simply missing from the graph.
	if strings.ContainsAny(specifier, "*") && (strings.HasPrefix(specifier, ".") || strings.HasPrefix(specifier, "/")) {
		if res, ok := r.tryGlob(fromFile, specifier); ok {
			return res
		}
	}

	// 2. relative or absolute path
	if strings.HasPrefix(specifier, ".") || strings.HasPrefix(specifier, "/") {
		// A relative path that reaches into node_modules is a package import
		// written the long way — "../../node_modules/astro/dist/transitions".
		// node_modules is never walked, so the path can never resolve to a
		// file; naming the package is both resolvable and the truer answer.
		if name, sub, ok := packageFromNodeModulesPath(specifier); ok {
			return Result{Kind: ToPackage, Package: name, Subpath: sub, Via: "node-modules-path"}
		}
		base := filepath.Dir(filepath.Join(r.root, filepath.FromSlash(fromFile)))
		if strings.HasPrefix(specifier, "/") {
			base = r.root
		}
		target := filepath.Join(base, filepath.FromSlash(specifier))

		// A root-absolute specifier usually names a static asset, and every
		// framework serves those from a directory rather than from the repo
		// root: Vite, Next and Astro use public/, SvelteKit uses static/.
		// "/vite.svg" is public/vite.svg on disk, and resolving it against the
		// root alone finds nothing.
		if strings.HasPrefix(specifier, "/") {
			// Searched from the nearest project root outward, not from the repo
			// root. In a monorepo "/typescript.svg" imported from
			// examples/with-vite-react/apps/web/src/main.tsx lives in that
			// app's own public/ directory — the site root is the app, not the
			// repository. Same principle as the nearest tsconfig winning.
			for _, projectRoot := range r.projectRootsFrom(fromFile) {
				for _, dir := range staticDirs {
					if res, ok := r.tryFile(filepath.Join(projectRoot, dir, filepath.FromSlash(specifier))); ok {
						res.Via = "static-asset"
						return res
					}
				}
			}
		}
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

	// 3. tsconfig path alias, using the config nearest the importing file
	fromDir := filepath.Dir(filepath.Join(r.root, filepath.FromSlash(fromFile)))
	if res, ok := r.tryAliases(r.configFor(fromDir), specifier); ok {
		return res
	}

	// 4. bare builtin
	if nodeBuiltins[specifier] {
		return Result{Kind: ToBuiltin, Name: specifier, Via: "bare-builtin"}
	}

	// 4b. "@name" with no slash cannot be a package.
	//
	// npm requires a scoped name to be @scope/name, so a bare @-prefixed
	// specifier is necessarily something a bundler plugin provides:
	// @qwik-router-config, @qwik-client-manifest, @docs-updated. If an alias
	// were going to explain it, that already happened above.
	if strings.HasPrefix(specifier, "@") && !strings.Contains(specifier, "/") {
		return Result{Kind: ToVirtual, Name: specifier, Via: "virtual-at-prefix"}
	}

	// 5. a package in this monorepo — your own code, not a dependency
	if res, ok := r.tryWorkspace(specifier); ok {
		return res
	}

	// 6. package
	name, sub := splitPackage(specifier)
	if name == "" {
		return Result{
			Kind:   Unresolved,
			Reason: specifier + " is neither a file nor a valid package name",
			Via:    "bare",
		}
	}
	return Result{Kind: ToPackage, Package: name, Subpath: sub, Via: "bare"}
}

// tryAliases applies tsconfig paths, longest prefix first.
func (r *Resolver) tryAliases(cfg *dirConfig, specifier string) (Result, bool) {
	for _, a := range cfg.aliases {
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
			abs := filepath.Join(a.base, filepath.FromSlash(candidate))
			if res, ok := r.tryFile(abs); ok {
				res.Via = "tsconfig-paths"
				return res, true
			}
		}
		// The alias matched but nothing on disk did. Resolution must CONTINUE
		// rather than stop here.
		//
		// TypeScript falls through to node_modules when a path mapping finds
		// no file, and repos rely on that: a catch-all mapping such as
		// "*": ["./src/*"] matches every bare specifier, so short-circuiting
		// here reported `react` itself as a broken alias. Measured on shadcn/ui
		// before the fix: 5,766 unresolved imports, 29% of the repo.
		//
		// The phantom-package problem this once guarded against is handled
		// instead by validating the package name, which rejects "@/lib/x".
		return Result{}, false
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

	// Platform-qualified: ./Button -> Button.ios.tsx
	//
	// Tried after the plain ladder, so an unqualified file always wins when
	// both exist — which is what a bundler does too.
	for _, plat := range platformSuffixes {
		for _, ext := range extensionLadder {
			if res, ok := r.file(abs + plat + ext); ok {
				res.Platform = strings.TrimPrefix(plat, ".")
				return res, true
			}
		}
	}

	// Directory import.
	//
	// A directory can say where its own entry is. Node reads the package.json
	// inside it and follows main/module/types before falling back to index,
	// and a monorepo relies on that constantly: preact's compat/src imports
	// "../../hooks", which is a directory holding a manifest that points at
	// src/index. Going straight to index leaves that unresolved, and the
	// dependency on the whole package disappears from the graph.
	//
	// The manifest is consulted first, exactly as Node does, then index.
	if ws := readWorkspace(abs); ws != nil && ws.Entry != "" {
		if res, ok := r.file(ws.Entry); ok {
			res.Via = "directory-manifest"
			return res, true
		}
	}
	for _, ext := range extensionLadder {
		if res, ok := r.file(filepath.Join(abs, "index"+ext)); ok {
			return res, true
		}
	}

	// Last resort: the path points into an unbuilt output directory. Look for
	// the source file it would be compiled from.
	if res, ok := r.trySourceTwin(abs); ok {
		return res, true
	}
	return Result{}, false
}

// trySourceTwin rewrites a build-output path to its source equivalent.
//
//	packages/astro/dist/core/errors/index.js
//	packages/astro/src/core/errors/index.ts
//
// Segments are tried outermost first. Anchoring on the *nearest* build
// directory seems more natural and is wrong: "dist/types/public/common.js"
// contains two names from the table, and rewriting the inner one produces
// "dist/src/public/common.js" — nonsense. The outer one gives
// "src/types/public/common.ts", which is the real file. That mistake accounted
// for 92 of Astro's remaining unresolved imports.
//
// Rewriting is only ever attempted after the literal path has failed, so a repo
// that has actually been built always resolves to its real output.
func (r *Resolver) trySourceTwin(abs string) (Result, bool) {
	rel, err := filepath.Rel(r.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Result{}, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")

	for i := 0; i < len(parts)-1; i++ { // never rewrite the filename itself
		sources, ok := buildToSource[parts[i]]
		if !ok {
			continue
		}
		for _, src := range sources {
			swapped := append(append([]string{}, parts[:i]...), src)
			swapped = append(swapped, parts[i+1:]...)
			candidate := filepath.Join(r.root, filepath.Join(swapped...))

			// Reuse the ordinary rules on the rewritten path so the extension
			// ladder and the .js -> .ts mapping both still apply.
			if res, ok := r.tryFileNoTwin(candidate); ok {
				res.FromBuildOutput = true
				return res, true
			}
		}
		// Also try dropping the build directory entirely: some packages compile
		// "src/x.ts" to "dist/x.js" and others to "dist/src/x.js".
		dropped := append(append([]string{}, parts[:i]...), parts[i+1:]...)
		if res, ok := r.tryFileNoTwin(filepath.Join(r.root, filepath.Join(dropped...))); ok {
			res.FromBuildOutput = true
			return res, true
		}

		// Last resort: a bundle whose filename encodes the format rather than a
		// source path — "dist/compiler-core.cjs.prod.js", built from the whole
		// of src. There is no per-file twin, but the package's own entry point
		// is the right endpoint: the import means "this package", and that is
		// where its code starts.
		pkgDir := filepath.Join(r.root, filepath.Join(parts[:i]...))
		for _, entry := range []string{"src/index", "src/main", "index"} {
			if res, ok := r.tryFileNoTwin(filepath.Join(pkgDir, filepath.FromSlash(entry))); ok {
				res.FromBuildOutput = true
				return res, true
			}
		}
	}
	return Result{}, false
}

// tryFileNoTwin is tryFile without the source-twin step, so the rewrite cannot
// recurse.
func (r *Resolver) tryFileNoTwin(abs string) (Result, bool) {
	if ext := filepath.Ext(abs); ext != "" {
		if res, ok := r.file(abs); ok {
			return res, true
		}
		if swaps, ok := jsToTS[ext]; ok {
			stem := strings.TrimSuffix(abs, ext)
			for _, alt := range swaps {
				if res, ok := r.file(stem + alt); ok {
					return res, true
				}
			}
		}
	}
	for _, ext := range extensionLadder {
		if res, ok := r.file(abs + ext); ok {
			return res, true
		}
	}
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

// packageFromNodeModulesPath extracts the package named by a path that walks
// into node_modules.
func packageFromNodeModulesPath(spec string) (name, subpath string, ok bool) {
	i := strings.LastIndex(spec, "node_modules/")
	if i < 0 {
		return "", "", false
	}
	name, subpath = splitPackage(spec[i+len("node_modules/"):])
	if name == "" {
		return "", "", false
	}
	return name, subpath, true
}

// splitPackage separates a bare specifier into package name and subpath, and
// returns an empty name when the specifier cannot be one.
//
//	"react"            -> "react", ""
//	"lodash/fp"        -> "lodash", "fp"
//	"@scope/pkg/sub/x" -> "@scope/pkg", "sub/x"
//	"@/lib/x"          -> "", ""      (an unresolved alias, not a package)
//
// The validation matters more than it looks. Once a missed alias falls through
// to here, an alias like "@/*" that pointed nowhere would otherwise invent a
// dependency on a package named "@" — which is exactly the phantom this used to
// short-circuit to avoid.
func splitPackage(spec string) (name, subpath string) {
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") {
		// A scoped package is @scope/name; both halves must be non-empty.
		if len(parts) < 2 || len(parts[0]) < 2 || parts[1] == "" {
			return "", ""
		}
		name, subpath = parts[0]+"/"+parts[1], strings.Join(parts[2:], "/")
	} else {
		if parts[0] == "" {
			return "", ""
		}
		name, subpath = parts[0], strings.Join(parts[1:], "/")
	}

	// npm names are lowercase and cannot contain whitespace or most
	// punctuation. This is a sanity filter, not a full validator.
	for _, r := range name {
		if r == ' ' || r == '\t' || r == '\\' || r == ':' || r == '?' || r == '*' || r == '"' {
			return "", ""
		}
	}
	return name, subpath
}

// tryWorkspace resolves an import of another package in the same monorepo.
//
// "@acme/ui" inside a workspace is not a dependency you installed, it is code
// in the next folder. Treating it as external disconnects the graph exactly
// where a monorepo is most interesting: at the boundaries between packages.
func (r *Resolver) tryWorkspace(specifier string) (Result, bool) {
	if len(r.workspaces) == 0 {
		return Result{}, false
	}

	name, subpath := splitPackage(specifier)
	ws, ok := r.workspaces[name]
	if !ok {
		return Result{}, false
	}

	// A subpath import addresses a file inside the package.
	//
	// "@acme/ui/button" is usually packages/ui/src/button.ts rather than
	// packages/ui/button.ts, because the manifest maps subpaths onto a source
	// directory. Both layouts are tried.
	if subpath != "" {
		for _, prefix := range []string{"", "src", "lib", "source"} {
			if res, ok := r.tryFile(filepath.Join(ws.Dir, prefix, filepath.FromSlash(subpath))); ok {
				res.Via = "workspace-subpath"
				return res, true
			}
		}
	}

	if ws.Entry != "" {
		return Result{Kind: ToFile, Path: r.rel(ws.Entry), Via: "workspace"}, true
	}
	// A workspace package whose manifest points at a build output that does not
	// exist yet. Try the conventional source entry before giving up.
	for _, guess := range []string{"src/index", "index", "src/main"} {
		if res, ok := r.tryFile(filepath.Join(ws.Dir, filepath.FromSlash(guess))); ok {
			res.Via = "workspace-guess"
			return res, true
		}
	}
	// A workspace package whose entry points at an unbuilt output is still a
	// real dependency. Reporting it as a package is truthful and keeps it out
	// of the unresolved count, where it would look like a broken import.
	return Result{
		Kind:    ToPackage,
		Package: name,
		Subpath: subpath,
		Via:     "workspace-unbuilt",
	}, true
}

// schemePattern matches a URI-style scheme at the start of a specifier:
// astro:content, bun:sqlite, virtual:uno.css, npm:react, https://esm.sh/x.
//
// Windows drive letters ("C:\...") are excluded by requiring at least two
// characters before the colon.
var schemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.\-]+:`)

// namespacePrefixes are framework conventions that are not schemes.
//
// SvelteKit uses $app and $env; Node's own subpath imports and Nuxt both use a
// leading '#'. None of these are files, and none can be resolved without
// running the framework's own resolver.
var namespacePrefixes = []string{"$app/", "$env/", "$service-worker", "#"}

// generatedRelative are relative specifiers a framework synthesises.
//
// SvelteKit writes "./$types" into .svelte-kit during `svelte-kit sync`, so it
// is absent from a fresh checkout and looks like a broken relative import.
// Found in the TanStack Query repo, where it was most of the remaining
// unresolved imports.
var generatedRelative = []string{
	"./$types", "../$types", "./$houdini", "./$env",
	// React Router v7 writes route types into .react-router/types and exposes
	// them as "./+types/<route>"; `react-router typegen` produces them, so a
	// fresh checkout has none.
	"./+types", "../+types",
}

// virtualModule reports whether a specifier names a synthesised module.
func virtualModule(spec string) (string, bool) {
	// A placeholder the build substitutes: SvelteKit's runtime imports
	// "<sveltekit:generated>/server.js", which its compiler rewrites. No real
	// path begins with an angle bracket, so this is unambiguous.
	if strings.HasPrefix(spec, "<") {
		return spec, true
	}
	if schemePattern.MatchString(spec) {
		return spec, true
	}
	for _, g := range generatedRelative {
		if spec == g || strings.HasPrefix(spec, g+"/") {
			return strings.TrimLeft(spec, "./"), true
		}
	}
	for _, p := range namespacePrefixes {
		if strings.HasPrefix(spec, p) {
			return spec, true
		}
	}
	return "", false
}

// tryGlob expands a glob specifier into the files it matches.
//
// Only patterns anchored at a relative or absolute path are expanded; a bare
// specifier containing "*" is far more likely to be a typo than a glob, and
// inventing matches for one would be worse than reporting it.
func (r *Resolver) tryGlob(fromFile, specifier string) (Result, bool) {
	base := filepath.Dir(filepath.Join(r.root, filepath.FromSlash(fromFile)))
	if strings.HasPrefix(specifier, "/") {
		base = r.root
	}
	pattern := filepath.Join(base, filepath.FromSlash(specifier))

	// "**" is not something filepath.Glob understands; collapse it to "*" so a
	// recursive pattern at least matches one level rather than nothing.
	pattern = strings.ReplaceAll(pattern, "**", "*")

	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return Result{}, false
	}

	out := Result{Kind: ToGlob, Via: "glob"}
	for _, m := range matches {
		rel := r.rel(m)
		if strings.HasPrefix(rel, "..") {
			continue // a glob may not reach outside the repository
		}
		if fi, err := os.Stat(m); err != nil || fi.IsDir() {
			continue
		}
		out.Matches = append(out.Matches, rel)
	}
	if len(out.Matches) == 0 {
		return Result{}, false
	}
	sort.Strings(out.Matches)
	out.Path = out.Matches[0]
	return out, true
}
