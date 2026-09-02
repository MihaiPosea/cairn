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
		t.Errorf("CaseMismatch = %q, want \"Utils.ts\" — this breaks on Linux and must be reported", got.CaseMismatch)
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
