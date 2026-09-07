package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hrepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(body), 0o644)
	}
	return root
}

// SUSPECT 1: an import that escapes the repo root.
func TestEscapingRepoRoot(t *testing.T) {
	root := hrepo(t, map[string]string{"app/page.tsx": ""})
	os.WriteFile(filepath.Join(filepath.Dir(root), "outside.ts"), []byte(""), 0o644)
	r, _ := New(root)
	got := r.Resolve("app/page.tsx", "../../outside")
	t.Logf("escape -> kind=%v path=%q reason=%q", got.Kind, got.Path, got.Reason)
	if got.Kind == ToFile && strings.HasPrefix(got.Path, "..") {
		t.Errorf("BUG: created a file node outside the repo: %q", got.Path)
	}
}

// SUSPECT 2: Vite/webpack resource query suffixes.
func TestResourceQuerySuffix(t *testing.T) {
	root := hrepo(t, map[string]string{
		"app/page.tsx":       "",
		"assets/shader.glsl": "",
		"lib/worker.ts":      "",
	})
	r, _ := New(root)
	for _, spec := range []string{"../assets/shader.glsl?raw", "../lib/worker?worker", "../assets/shader.glsl#frag"} {
		got := r.Resolve("app/page.tsx", spec)
		t.Logf("%-34s -> kind=%v path=%q", spec, got.Kind, got.Path)
		if got.Kind != ToFile {
			t.Errorf("BUG: %q did not resolve (bundler query suffixes are common)", spec)
		}
	}
}

// SUSPECT 3: case-mismatched import. Works on macOS, breaks on Linux CI.
func TestCaseMismatch(t *testing.T) {
	root := hrepo(t, map[string]string{"app/page.tsx": "", "lib/utils.ts": ""})
	r, _ := New(root)
	got := r.Resolve("app/page.tsx", "../lib/Utils")
	if got.Path != "lib/utils.ts" {
		t.Errorf("must resolve to the real on-disk file, got %q", got.Path)
	}
	// The mismatch is reported as the filename the import asked for, after the
	// extension ladder resolved it.
	if got.CaseMismatch != "Utils.ts" {
		t.Errorf("CaseMismatch = %q, want \"Utils.ts\" - this breaks on Linux and must be reported", got.CaseMismatch)
	}
}

// SUSPECT 4: a directory whose name has a dot in it.
func TestDottedDirectory(t *testing.T) {
	root := hrepo(t, map[string]string{"app/page.tsx": "", "lib.v2/index.ts": "", "lib.v2/thing.ts": ""})
	r, _ := New(root)
	for _, spec := range []string{"../lib.v2", "../lib.v2/thing"} {
		got := r.Resolve("app/page.tsx", spec)
		t.Logf("%-20s -> kind=%v path=%q reason=%q", spec, got.Kind, got.Path, got.Reason)
		if got.Kind != ToFile {
			t.Errorf("BUG: %q should resolve", spec)
		}
	}
}

// SUSPECT 5: a dotfile with no extension, and a file whose ext is not in the ladder.
func TestDotfilesAndUnknownExtensions(t *testing.T) {
	root := hrepo(t, map[string]string{
		"app/page.tsx": "", "config/.eslintrc": "", "assets/logo.svg": "", "data/notes.md": "",
	})
	r, _ := New(root)
	for _, spec := range []string{"../assets/logo.svg", "../data/notes.md"} {
		got := r.Resolve("app/page.tsx", spec)
		t.Logf("%-22s -> kind=%v path=%q", spec, got.Kind, got.Path)
		if got.Kind != ToFile {
			t.Errorf("BUG: %q exists on disk and should resolve", spec)
		}
	}
}

// SUSPECT 6: windows-style separators in a specifier.
func TestBackslashSpecifier(t *testing.T) {
	root := hrepo(t, map[string]string{"app/page.tsx": "", "lib/utils.ts": ""})
	r, _ := New(root)
	got := r.Resolve("app/page.tsx", "..\\lib\\utils")
	t.Logf("backslash -> kind=%v path=%q", got.Kind, got.Path)
}

// SUSPECT 7: alias whose target has no wildcard, and an exact (non-wildcard) alias.
func TestExactAlias(t *testing.T) {
	root := hrepo(t, map[string]string{
		"tsconfig.json": `{"compilerOptions":{"paths":{"~config":["./config/app.ts"],"@lib/*":["./lib/*"]}}}`,
		"app/page.tsx":  "", "config/app.ts": "", "lib/x.ts": "",
	})
	r, _ := New(root)
	for _, spec := range []string{"~config", "@lib/x"} {
		got := r.Resolve("app/page.tsx", spec)
		t.Logf("%-10s -> kind=%v path=%q reason=%q", spec, got.Kind, got.Path, got.Reason)
		if got.Kind != ToFile {
			t.Errorf("BUG: exact alias %q should resolve", spec)
		}
	}
}

// SUSPECT 8: a symlinked file inside the repo.
func TestSymlinkedFile(t *testing.T) {
	root := hrepo(t, map[string]string{"app/page.tsx": "", "real/impl.ts": ""})
	os.MkdirAll(filepath.Join(root, "linked"), 0o755)
	if err := os.Symlink(filepath.Join(root, "real", "impl.ts"), filepath.Join(root, "linked", "impl.ts")); err != nil {
		t.Skip("symlinks unavailable")
	}
	r, _ := New(root)
	got := r.Resolve("app/page.tsx", "../linked/impl")
	t.Logf("symlink -> kind=%v path=%q", got.Kind, got.Path)
	if got.Kind != ToFile {
		t.Errorf("BUG: a symlinked source file should resolve")
	}
}

// Framework and bundler virtual modules are not broken imports.
func TestVirtualModules(t *testing.T) {
	root := hrepo(t, map[string]string{"app/page.tsx": ""})
	r := resolver(t, root)

	for _, spec := range []string{
		"astro:content", "astro:assets", "astro:env/server", // Astro
		"virtual:uno.css", "virtual:pwa-register", // Vite plugins
		"bun:sqlite", "bun:ffi", // Bun
		"npm:react@18", "jsr:@std/path", // Deno
		"https://esm.sh/react", "data:text/javascript,export default 1",
		"$app/stores", "$env/static/private", // SvelteKit
		"#internal/config", "#imports", // Node subpath imports, Nuxt
	} {
		got := r.Resolve("app/page.tsx", spec)
		if got.Kind != ToVirtual {
			t.Errorf("Resolve(%q) = %v (%s), want virtual", spec, got.Kind, got.Reason)
		}
	}
}

// A Windows drive letter is not a scheme.
func TestDriveLetterIsNotAScheme(t *testing.T) {
	root := hrepo(t, map[string]string{"a.ts": ""})
	if got := resolver(t, root).Resolve("a.ts", "C:/x/y"); got.Kind == ToVirtual {
		t.Error("a drive letter must not be treated as a virtual module scheme")
	}
}

// A missed alias must fall through to package resolution, not short-circuit.
//
// shadcn/ui maps "react" through a paths entry that points at a types
// directory; short-circuiting there reported react itself as a broken import,
// 5,766 times across the repo.
func TestMissedAliasFallsThroughToPackage(t *testing.T) {
	root := hrepo(t, map[string]string{
		"tsconfig.json": `{"compilerOptions":{"paths":{"*":["./src/*"],"@/*":["./*"]}}}`,
		"app/page.tsx":  "",
	})
	r := resolver(t, root)

	if got := r.Resolve("app/page.tsx", "react"); got.Kind != ToPackage || got.Package != "react" {
		t.Errorf("react = %v %q (%s), want package react", got.Kind, got.Package, got.Reason)
	}
	// But an alias that points nowhere must still not become a phantom package.
	if got := r.Resolve("app/page.tsx", "@/does/not/exist"); got.Kind != Unresolved {
		t.Errorf("@/does/not/exist = %v pkg=%q, want unresolved not a package named @",
			got.Kind, got.Package)
	}
}

// Node subpath imports are declared in package.json and are genuinely
// resolvable, so they must not be written off as virtual modules.
func TestSubpathImports(t *testing.T) {
	root := hrepo(t, map[string]string{
		"package.json": `{
			"name":"app",
			"imports":{
				"#internal/*": "./src/internal/*.js",
				"#config": {"node":"./src/config.node.ts","default":"./src/config.ts"},
				"#alt": ["./missing.ts", "./src/fallback.ts"]
			}
		}`,
		"src/app.ts":              "",
		"src/internal/helpers.ts": "",
		"src/config.node.ts":      "",
		"src/config.ts":           "",
		"src/fallback.ts":         "",
	})
	r := resolver(t, root)

	for _, tc := range []struct{ spec, want string }{
		{"#internal/helpers", "src/internal/helpers.ts"},
		{"#config", "src/config.node.ts"}, // "node" is preferred over "default"
		{"#alt", "src/fallback.ts"},       // first candidate missing, second wins
	} {
		got := r.Resolve("src/app.ts", tc.spec)
		if got.Kind != ToFile || got.Path != tc.want {
			t.Errorf("Resolve(%q) = %v %q (%s), want file %q",
				tc.spec, got.Kind, got.Path, got.Reason, tc.want)
		}
	}
}

// An ESM-style "./x.js" target must still find x.ts, as elsewhere.
func TestSubpathImportsWithJSExtension(t *testing.T) {
	root := hrepo(t, map[string]string{
		"package.json": `{"imports":{"#lib/*":"./lib/*.js"}}`,
		"src/a.ts":     "",
		"lib/thing.ts": "",
	})
	got := resolver(t, root).Resolve("src/a.ts", "#lib/thing")
	if got.Path != "lib/thing.ts" {
		t.Errorf("got %v %q, want lib/thing.ts", got.Kind, got.Path)
	}
}

// In a monorepo, the nearest package.json wins.
func TestSubpathImportsUseNearestManifest(t *testing.T) {
	root := hrepo(t, map[string]string{
		"package.json":              `{"imports":{"#x":"./root-x.ts"}}`,
		"root-x.ts":                 "",
		"packages/app/package.json": `{"imports":{"#x":"./inner-x.ts"}}`,
		"packages/app/inner-x.ts":   "",
		"packages/app/src/main.ts":  "",
	})
	got := resolver(t, root).Resolve("packages/app/src/main.ts", "#x")
	if got.Path != "packages/app/inner-x.ts" {
		t.Errorf("got %q, want packages/app/inner-x.ts (the nearest manifest)", got.Path)
	}
}

// With no manifest to explain it, a '#' specifier is still virtual.
func TestUnexplainedHashSpecifierStaysVirtual(t *testing.T) {
	root := hrepo(t, map[string]string{"package.json": `{}`, "a.ts": ""})
	if got := resolver(t, root).Resolve("a.ts", "#imports"); got.Kind != ToVirtual {
		t.Errorf("got %v, want virtual when no manifest declares it", got.Kind)
	}
}

// A repo that has not been built imports its own compiled output. The source
// twin is the same edge and an openable file, so it is the better answer.
func TestUnbuiltOutputResolvesToSource(t *testing.T) {
	root := hrepo(t, map[string]string{
		"packages/astro/src/core/errors/index.ts": "",
		"packages/astro/src/cli/check/index.ts":   "",
		"packages/astro/components/Code.astro":    "",
		"packages/astro/src/thing.tsx":            "",
	})
	r := resolver(t, root)

	for _, tc := range []struct{ from, spec, want string }{
		{"packages/astro/components/Code.astro", "../dist/core/errors/index.js", "packages/astro/src/core/errors/index.ts"},
		{"packages/astro/components/Code.astro", "../dist/cli/check/index.js", "packages/astro/src/cli/check/index.ts"},
		{"packages/astro/components/Code.astro", "../dist/thing.js", "packages/astro/src/thing.tsx"},
	} {
		got := r.Resolve(tc.from, tc.spec)
		if got.Kind != ToFile || got.Path != tc.want {
			t.Errorf("Resolve(%q) = %v %q (%s), want %q", tc.spec, got.Kind, got.Path, got.Reason, tc.want)
		}
		if !got.FromBuildOutput {
			t.Errorf("Resolve(%q) should be flagged as coming from a build output", tc.spec)
		}
	}
}

// A repo that HAS been built must resolve to the real output, not the source.
func TestBuiltOutputWinsOverSource(t *testing.T) {
	root := hrepo(t, map[string]string{
		"a.ts":          "",
		"dist/thing.js": "// compiled",
		"src/thing.ts":  "// source",
	})
	got := resolver(t, root).Resolve("a.ts", "./dist/thing.js")
	if got.Path != "dist/thing.js" {
		t.Errorf("got %q, want the real built file dist/thing.js", got.Path)
	}
	if got.FromBuildOutput {
		t.Error("a real build output must not be flagged as a source twin")
	}
}

// The rewrite must not invent a file when no twin exists.
func TestNoSourceTwinStaysUnresolved(t *testing.T) {
	root := hrepo(t, map[string]string{"a.ts": "", "src/other.ts": ""})
	got := resolver(t, root).Resolve("a.ts", "./dist/missing.js")
	if got.Kind != Unresolved {
		t.Errorf("got %v %q, want unresolved - there is no src/missing", got.Kind, got.Path)
	}
}

// Root-absolute specifiers name static assets, which frameworks serve from a
// directory rather than from the repository root.
func TestStaticAssetsResolveFromTheNearestProjectRoot(t *testing.T) {
	root := hrepo(t, map[string]string{
		"package.json":                   `{"name":"root","workspaces":["apps/*"]}`,
		"public/root-logo.svg":           "<svg/>",
		"apps/web/package.json":          `{"name":"web"}`,
		"apps/web/public/typescript.svg": "<svg/>",
		"apps/web/src/main.tsx":          "",
		"apps/site/package.json":         `{"name":"site"}`,
		"apps/site/static/hero.png":      "x",
		"apps/site/src/index.ts":         "",
	})
	r := resolver(t, root)

	// The app's own public/ wins over the repository's.
	if got := r.Resolve("apps/web/src/main.tsx", "/typescript.svg"); got.Path != "apps/web/public/typescript.svg" {
		t.Errorf("got %v %q (%s), want apps/web/public/typescript.svg", got.Kind, got.Path, got.Reason)
	}
	// SvelteKit's static/ works too.
	if got := r.Resolve("apps/site/src/index.ts", "/hero.png"); got.Path != "apps/site/static/hero.png" {
		t.Errorf("got %q, want apps/site/static/hero.png", got.Path)
	}
	// Falling back to the repository root when the app has no public/.
	if got := r.Resolve("apps/site/src/index.ts", "/root-logo.svg"); got.Path != "public/root-logo.svg" {
		t.Errorf("got %q, want public/root-logo.svg", got.Path)
	}
}

// Declaration files must be found, but must never win over an implementation.
func TestDeclarationFiles(t *testing.T) {
	root := hrepo(t, map[string]string{
		"a.ts":               "",
		"typings/style.d.ts": "",
		"lib/both.ts":        "",
		"lib/both.d.ts":      "",
		"lib/types.js":       "", // an import of ./types.js where only .d.ts exists
		"esm/only.d.ts":      "",
	})
	r := resolver(t, root)

	if got := r.Resolve("a.ts", "./typings/style"); got.Path != "typings/style.d.ts" {
		t.Errorf("got %q, want typings/style.d.ts", got.Path)
	}
	// The implementation wins when both are present.
	if got := r.Resolve("a.ts", "./lib/both"); got.Path != "lib/both.ts" {
		t.Errorf("got %q, want lib/both.ts - an implementation beats its declaration", got.Path)
	}
	// ESM-style ".js" specifier finding only a declaration.
	if got := r.Resolve("a.ts", "./esm/only.js"); got.Path != "esm/only.d.ts" {
		t.Errorf("got %q, want esm/only.d.ts", got.Path)
	}
}

// A relative path reaching into node_modules names a package.
func TestNodeModulesRelativePathIsAPackage(t *testing.T) {
	root := hrepo(t, map[string]string{"src/a.ts": ""})
	got := resolver(t, root).Resolve("src/a.ts", "../../node_modules/astro/dist/transitions")
	if got.Kind != ToPackage || got.Package != "astro" {
		t.Errorf("got %v %q, want package astro", got.Kind, got.Package)
	}
	if got.Subpath != "dist/transitions" {
		t.Errorf("subpath = %q, want dist/transitions", got.Subpath)
	}
}

// A package importing its own bundle resolves to the package entry.
func TestOwnBundleResolvesToPackageEntry(t *testing.T) {
	root := hrepo(t, map[string]string{
		"packages/compiler-core/index.js":     "",
		"packages/compiler-core/src/index.ts": "",
	})
	got := resolver(t, root).Resolve("packages/compiler-core/index.js", "./dist/compiler-core.cjs.prod.js")
	if got.Path != "packages/compiler-core/src/index.ts" {
		t.Errorf("got %v %q, want packages/compiler-core/src/index.ts", got.Kind, got.Path)
	}
	if !got.FromBuildOutput {
		t.Error("should be flagged as coming from a build output")
	}
}

// Parcel and Vite expand a glob specifier into every matching file, and the
// importing module depends on all of them.
func TestGlobImports(t *testing.T) {
	root := hrepo(t, map[string]string{
		"src/Alert.tsx":      "",
		"intl/en-US.json":    "{}",
		"intl/fr-FR.json":    "{}",
		"intl/de-DE.json":    "{}",
		"intl/nested/x.json": "{}",
		"other/thing.json":   "{}",
	})
	got := resolver(t, root).Resolve("src/Alert.tsx", "../intl/*.json")

	if got.Kind != ToGlob {
		t.Fatalf("got %v (%s), want a glob", got.Kind, got.Reason)
	}
	if len(got.Matches) != 3 {
		t.Fatalf("matches = %v, want the three locale files", got.Matches)
	}
	for _, m := range got.Matches {
		if !strings.HasPrefix(m, "intl/") || strings.Contains(m, "nested") {
			t.Errorf("unexpected match %q", m)
		}
	}
	// Deterministic ordering, like everything else.
	if got.Matches[0] != "intl/de-DE.json" {
		t.Errorf("matches are not sorted: %v", got.Matches)
	}
}

// A glob that matches nothing must not silently succeed.
func TestGlobMatchingNothingIsUnresolved(t *testing.T) {
	root := hrepo(t, map[string]string{"src/a.ts": ""})
	if got := resolver(t, root).Resolve("src/a.ts", "../intl/*.json"); got.Kind == ToGlob {
		t.Errorf("a glob matching nothing should not resolve, got %v", got.Matches)
	}
}

// A bare specifier containing "*" is a typo, not a glob.
func TestBareStarIsNotAGlob(t *testing.T) {
	root := hrepo(t, map[string]string{"a.ts": ""})
	if got := resolver(t, root).Resolve("a.ts", "some*package"); got.Kind == ToGlob {
		t.Error("a bare specifier with a star must not be expanded as a glob")
	}
}

// A glob must not reach outside the repository.
func TestGlobCannotEscapeTheRepo(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "repo")
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(outer, "secret.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(root, "src", "a.ts"), []byte(""), 0o644)

	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	got := r.Resolve("src/a.ts", "../../*.json")
	for _, m := range got.Matches {
		if strings.HasPrefix(m, "..") {
			t.Errorf("glob escaped the repository: %q", m)
		}
	}
}

// A bare "@name" with no slash cannot be an npm package - npm requires
// @scope/name - so it is necessarily provided by a bundler plugin.
func TestBareAtPrefixIsVirtual(t *testing.T) {
	root := hrepo(t, map[string]string{"a.ts": ""})
	r := resolver(t, root)
	for _, spec := range []string{"@qwik-router-config", "@qwik-client-manifest", "@docs-updated"} {
		if got := r.Resolve("a.ts", spec); got.Kind != ToVirtual {
			t.Errorf("Resolve(%q) = %v (%s), want virtual", spec, got.Kind, got.Reason)
		}
	}
	// A properly scoped package is still a package.
	if got := r.Resolve("a.ts", "@scope/pkg"); got.Kind != ToPackage {
		t.Errorf("@scope/pkg = %v, want package", got.Kind)
	}
}

// React Router v7 generates route types and exposes them as ./+types/<route>.
func TestReactRouterTypegenIsVirtual(t *testing.T) {
	root := hrepo(t, map[string]string{"app/routes/index.tsx": ""})
	r := resolver(t, root)
	for _, spec := range []string{"./+types/route", "./+types/_index", "../+types/root"} {
		if got := r.Resolve("app/routes/index.tsx", spec); got.Kind != ToVirtual {
			t.Errorf("Resolve(%q) = %v, want virtual", spec, got.Kind)
		}
	}
}

// SvelteKit's runtime imports build-time placeholders in angle brackets.
func TestAngleBracketPlaceholderIsVirtual(t *testing.T) {
	root := hrepo(t, map[string]string{"a.js": ""})
	r := resolver(t, root)
	for _, spec := range []string{
		"<sveltekit:generated>/server.js",
		"<sveltekit:generated>/env/config.js",
	} {
		if got := r.Resolve("a.js", spec); got.Kind != ToVirtual {
			t.Errorf("Resolve(%q) = %v, want virtual", spec, got.Kind)
		}
	}
}
