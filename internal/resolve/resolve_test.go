package resolve

import (
	"os"
	"path/filepath"
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
		"a.ts":              "",
		"src/lib/thing.ts":  "",
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
		t.Error("Unresolved must carry a Reason — it is shown to users")
	}
}

// A directory must not satisfy an import that a real file could satisfy.
func TestDirectoryDoesNotShadowAFile(t *testing.T) {
	root := repo(t, map[string]string{
		"a.ts":            "",
		"utils.ts":        "",
		"utils/keep.txt":  "",
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
