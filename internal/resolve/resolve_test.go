package resolve

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repo writes a fixture tree and returns its root.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func resolver(t *testing.T, root string) *Resolver {
	t.Helper()
	r, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestRelativeExtensionLadder(t *testing.T) {
	root := repo(t, map[string]string{
		"app/page.tsx":     "",
		"lib/utils.ts":     "",
		"lib/format.tsx":   "",
		"data/config.json": "",
	})
	r := resolver(t, root)

	for _, tc := range []struct{ spec, want string }{
		{"../lib/utils", "lib/utils.ts"},
		{"../lib/format", "lib/format.tsx"},
		{"../data/config.json", "data/config.json"},
	} {
		got := r.Resolve("app/page.tsx", tc.spec)
		if got.Kind != ToFile || got.Path != tc.want {
			t.Errorf("Resolve(%q) = %v %q, want file %q", tc.spec, got.Kind, got.Path, tc.want)
		}
	}
}

// A repo mid-migration has both utils.ts and a stale utils.js. TypeScript wins.
func TestTypeScriptBeatsStaleJavaScript(t *testing.T) {
	root := repo(t, map[string]string{"a.ts": "", "lib/utils.ts": "", "lib/utils.js": ""})
	got := resolver(t, root).Resolve("a.ts", "./lib/utils")
	if got.Path != "lib/utils.ts" {
		t.Errorf("got %q, want lib/utils.ts", got.Path)
	}
}

func TestDirectoryIndexImport(t *testing.T) {
	root := repo(t, map[string]string{"a.ts": "", "components/index.ts": "", "components/Button.tsx": ""})
	got := resolver(t, root).Resolve("a.ts", "./components")
	if got.Kind != ToFile || got.Path != "components/index.ts" {
		t.Errorf("got %v %q, want components/index.ts", got.Kind, got.Path)
	}
}

// ESM TypeScript makes you write ./x.js for a file that is really x.ts.
func TestESMTypeScriptJSExtensionMapsToTS(t *testing.T) {
	root := repo(t, map[string]string{"a.mts": "", "lib/thing.mts": "", "lib/other.ts": ""})
	r := resolver(t, root)

	if got := r.Resolve("a.mts", "./lib/thing.mjs"); got.Path != "lib/thing.mts" {
		t.Errorf(".mjs -> .mts: got %q", got.Path)
	}
	if got := r.Resolve("a.mts", "./lib/other.js"); got.Path != "lib/other.ts" {
		t.Errorf(".js -> .ts: got %q", got.Path)
	}
}

// The exact shape a Next.js starter ships: paths with no baseUrl.
func TestTSConfigPathAliasWithoutBaseURL(t *testing.T) {
	root := repo(t, map[string]string{
		"tsconfig.json": `{
  // Next.js default
  "compilerOptions": {
    "paths": { "@/*": ["./*"] },
  }
}`,
		"app/page.tsx":          "",
		"lib/utils.ts":          "",
		"components/Button.tsx": "",
	})
	r := resolver(t, root)
	if !r.HasAliases() {
		t.Fatal("expected the tsconfig alias to load")
	}
	for _, tc := range []struct{ spec, want string }{
		{"@/lib/utils", "lib/utils.ts"},
		{"@/components/Button", "components/Button.tsx"},
	} {
		got := r.Resolve("app/page.tsx", tc.spec)
		if got.Kind != ToFile || got.Path != tc.want {
			t.Errorf("Resolve(%q) = %v %q (%s), want file %q", tc.spec, got.Kind, got.Path, got.Reason, tc.want)
		}
	}
}

// TypeScript picks the longest matching prefix, not the first one listed.
func TestLongestAliasPrefixWins(t *testing.T) {
	root := repo(t, map[string]string{
		"tsconfig.json": `{"compilerOptions":{"baseUrl":".","paths":{
			"@/*": ["./src/*"],
			"@/lib/*": ["./vendor/lib/*"]
		}}}`,
		"a.ts":                "",
		"src/lib/thing.ts":    "",
		"vendor/lib/thing.ts": "",
	})
	got := resolver(t, root).Resolve("a.ts", "@/lib/thing")
	if got.Path != "vendor/lib/thing.ts" {
		t.Errorf("got %q, want vendor/lib/thing.ts (longest prefix should win)", got.Path)
	}
}

// An alias that matches but points nowhere is a broken import, not a package.
func TestAliasMatchedButMissingIsUnresolvedNotAPackage(t *testing.T) {
	root := repo(t, map[string]string{
		"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["./*"]}}}`,
		"a.ts":          "",
	})
	got := resolver(t, root).Resolve("a.ts", "@/does/not/exist")
	if got.Kind != Unresolved {
		t.Fatalf("got %v, want Unresolved (a phantom package named '@' would be worse than useless)", got.Kind)
	}
	if got.Reason == "" {
		t.Error("an Unresolved result must explain itself")
	}
}

func TestBuiltins(t *testing.T) {
	root := repo(t, map[string]string{"a.ts": ""})
	r := resolver(t, root)
	for _, spec := range []string{"node:fs", "fs", "path", "node:worker_threads"} {
		if got := r.Resolve("a.ts", spec); got.Kind != ToBuiltin {
			t.Errorf("Resolve(%q) = %v, want builtin", spec, got.Kind)
		}
	}
}

func TestBarePackages(t *testing.T) {
	root := repo(t, map[string]string{"a.ts": ""})
	r := resolver(t, root)
	for _, tc := range []struct{ spec, pkg, sub string }{
		{"react", "react", ""},
		{"lodash/fp", "lodash", "fp"},
		{"@scope/pkg", "@scope/pkg", ""},
		{"@scope/pkg/sub/deep", "@scope/pkg", "sub/deep"},
	} {
		got := r.Resolve("a.ts", tc.spec)
		if got.Kind != ToPackage || got.Package != tc.pkg || got.Subpath != tc.sub {
			t.Errorf("Resolve(%q) = %v pkg=%q sub=%q, want package %q/%q",
				tc.spec, got.Kind, got.Package, got.Subpath, tc.pkg, tc.sub)
		}
	}
}

func TestMissingRelativeFileExplainsItself(t *testing.T) {
	root := repo(t, map[string]string{"app/page.tsx": ""})
	got := resolver(t, root).Resolve("app/page.tsx", "./gone")
	if got.Kind != Unresolved {
		t.Fatalf("got %v, want Unresolved", got.Kind)
	}
	if got.Reason == "" {
		t.Error("Unresolved must carry a Reason - it is shown to users")
	}
}

// A directory must not satisfy an import that a real file could satisfy.
func TestDirectoryDoesNotShadowAFile(t *testing.T) {
	root := repo(t, map[string]string{
		"a.ts":           "",
		"utils.ts":       "",
		"utils/keep.txt": "",
	})
	got := resolver(t, root).Resolve("a.ts", "./utils")
	if got.Path != "utils.ts" {
		t.Errorf("got %q, want utils.ts", got.Path)
	}
}

func TestNonCodeExtensionsResolve(t *testing.T) {
	root := repo(t, map[string]string{"a.tsx": "", "styles/globals.css": ""})
	got := resolver(t, root).Resolve("a.tsx", "./styles/globals.css")
	if got.Kind != ToFile || got.Path != "styles/globals.css" {
		t.Errorf("got %v %q, want styles/globals.css", got.Kind, got.Path)
	}
}

// A workspace import must land on the package's code, never on its manifest.
//
// Almost every modern package publishes "./package.json": "./package.json" in
// its exports, and the old reader treated that as just another key to fall
// back to. Because Go randomises map iteration it won a coin flip: measured on
// tanstack-query, 319 edges - a twelfth of the graph - pointed at
// package.json on one run and at src/index.ts on the next, for an unchanged
// repository.
func TestWorkspaceExportsNeverResolveToTheManifest(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json": `{"name":"root","workspaces":["packages/*"]}`,
		"packages/ui/package.json": `{
			"name": "@acme/ui",
			"exports": {
				".": { "source": "./src/index.ts" },
				"./package.json": "./package.json"
			}
		}`,
		"packages/ui/src/index.ts": `export const ui = 1;`,
		"app/main.ts":              `import { ui } from "@acme/ui";`,
	})

	// Ten runs: one coin flip landing right proves nothing.
	for i := range 10 {
		r, err := New(root)
		if err != nil {
			t.Fatal(err)
		}
		got := r.Resolve("app/main.ts", "@acme/ui")
		if got.Kind != ToFile {
			t.Fatalf("run %d: @acme/ui did not resolve to a file: %+v", i, got)
		}
		if strings.HasSuffix(got.Path, "package.json") {
			t.Fatalf("run %d: resolved to the manifest %q instead of the package's code", i, got.Path)
		}
		if got.Path != "packages/ui/src/index.ts" {
			t.Fatalf("run %d: resolved to %q, want packages/ui/src/index.ts", i, got.Path)
		}
	}
}

// The same specifier must resolve the same way every time, or every number
// built on the graph moves on its own.
func TestExportsResolutionIsDeterministic(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json": `{"name":"root","workspaces":["packages/*"]}`,
		// No preferred condition anywhere: the old code fell through to
		// ranging over the map, which is where the randomness got in.
		"packages/x/package.json": `{
			"name": "@acme/x",
			"exports": {
				".": {
					"@acme/custom-condition": "./src/index.ts",
					"browser": "./src/browser.ts",
					"node": "./src/node.ts"
				},
				"./package.json": "./package.json"
			}
		}`,
		"packages/x/src/index.ts":   `export const a = 1;`,
		"packages/x/src/browser.ts": `export const b = 1;`,
		"packages/x/src/node.ts":    `export const c = 1;`,
		"app/main.ts":               `import "@acme/x";`,
	})

	seen := map[string]bool{}
	for range 12 {
		r, err := New(root)
		if err != nil {
			t.Fatal(err)
		}
		got := r.Resolve("app/main.ts", "@acme/x")
		seen[got.Path] = true
	}
	if len(seen) != 1 {
		t.Errorf("twelve runs produced %d different answers: %v", len(seen), keysOfSet(seen))
	}
}

func keysOfSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A package may import itself by name. Node allows it whenever the manifest
// has an "exports" field, and libraries with subpath exports rely on it:
// preact's own sources say `import "preact/compat"` rather than reaching
// across the tree with a relative path.
//
// Read as an ordinary bare specifier it resolves to an external package, so
// the edge leaves the repository and the file it really points at loses a
// dependent.
func TestAPackageCanImportItselfByName(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json": `{
			"name": "mylib",
			"exports": { ".": "./src/index.ts", "./helper": "./src/helper.ts" }
		}`,
		"src/index.ts":  `export const i = 1;`,
		"src/helper.ts": `export const h = 1;`,
		"src/use.ts":    `import { h } from "mylib/helper"; import { i } from "mylib";`,
	})
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ spec, want string }{
		{"mylib", "src/index.ts"},
		{"mylib/helper", "src/helper.ts"},
	} {
		got := r.Resolve("src/use.ts", c.spec)
		if got.Kind != ToFile {
			t.Errorf("%q resolved to %v, not a local file", c.spec, got.Kind)
			continue
		}
		if got.Path != c.want {
			t.Errorf("%q resolved to %q, want %q", c.spec, got.Path, c.want)
		}
	}
}

// Without an exports field Node does not permit self-reference, so neither
// should this - treating the name as external is then the correct answer.
func TestSelfReferenceNeedsAnExportsField(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json": `{"name":"mylib","main":"./src/index.ts"}`,
		"src/index.ts": `export const i = 1;`,
		"src/use.ts":   `import { i } from "mylib";`,
	})
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("src/use.ts", "mylib"); got.Kind == ToFile {
		t.Errorf("no exports field, so mylib is an external package; resolved to %q", got.Path)
	}
}

// A directory can say where its own entry is. Node reads the package.json
// inside it and follows main/module/types before falling back to index, and a
// monorepo relies on that: preact's compat/src imports "../../hooks", a
// directory holding a manifest that points at src/index. Going straight to
// index left it unresolved and the dependency on the whole package vanished.
func TestARelativeImportOfADirectoryReadsItsManifest(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json":        `{"name":"root"}`,
		"hooks/package.json":  `{"name":"hooks","main":"src/index.js"}`,
		"hooks/src/index.js":  `export const h = 1;`,
		"compat/src/index.ts": `import "../../hooks";`,
	})
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	got := r.Resolve("compat/src/index.ts", "../../hooks")
	if got.Kind != ToFile {
		t.Fatalf("../../hooks did not resolve to a file: %+v", got)
	}
	if got.Path != "hooks/src/index.js" {
		t.Errorf("resolved to %q, want hooks/src/index.js", got.Path)
	}
}

// The manifest wins over index when both could answer, because that is the
// order Node uses - a directory that names an entry means it.
func TestADirectoryManifestBeatsItsIndexFile(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json":     `{"name":"root"}`,
		"lib/package.json": `{"name":"lib","main":"src/entry.ts"}`,
		"lib/src/entry.ts": `export const e = 1;`,
		"lib/index.ts":     `export const wrong = 1;`,
		"app.ts":           `import "./lib";`,
	})
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("app.ts", "./lib"); got.Path != "lib/src/entry.ts" {
		t.Errorf("resolved to %q, want the manifest's entry lib/src/entry.ts", got.Path)
	}
}

// With no manifest the index fallback must still work, or this trades one
// gap for another.
func TestADirectoryWithNoManifestStillFindsIndex(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json":        `{"name":"root"}`,
		"components/index.ts": `export const c = 1;`,
		"app.ts":              `import "./components";`,
	})
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("app.ts", "./components"); got.Path != "components/index.ts" {
		t.Errorf("resolved to %q, want components/index.ts", got.Path)
	}
}
