package scan

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A real machine has directories you cannot read, symlinks that point nowhere,
// symlinks that point at themselves, and filenames nobody expected. A scan that
// dies on any of them is useless outside a fixture.

func TestUnreadableDirectoryDoesNotAbortTheScan(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not apply")
	}
	root := t.TempDir()
	mustWrite(t, root, "src/a.ts", `import "./b";`)
	mustWrite(t, root, "src/b.ts", `export const b = 1;`)
	mustWrite(t, root, "secret/hidden.ts", `export const h = 1;`)

	secret := filepath.Join(root, "secret")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Skip("cannot remove read permission")
	}
	t.Cleanup(func() { os.Chmod(secret, 0o755) })

	res, err := RunWith(root, Options{NoCache: true, SkipPackages: true})
	if err != nil {
		t.Fatalf("an unreadable directory must not abort the scan: %v", err)
	}
	if res.FilesScanned < 2 {
		t.Errorf("scanned %d files, expected the readable ones to still be found", res.FilesScanned)
	}
}

func TestBrokenSymlink(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "a.ts", `import "./gone";`)
	if err := os.Symlink(filepath.Join(root, "nowhere.ts"), filepath.Join(root, "gone.ts")); err != nil {
		t.Skip("symlinks unavailable")
	}

	res, err := RunWith(root, Options{NoCache: true, SkipPackages: true})
	if err != nil {
		t.Fatalf("a broken symlink must not abort the scan: %v", err)
	}
	t.Logf("files=%d unresolved=%d", res.FilesScanned, len(res.Unresolved))
}

// A symlink loop is the classic way to hang a directory walker forever.
func TestSymlinkLoopTerminates(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "src/a.ts", `export const a = 1;`)
	if err := os.Symlink(root, filepath.Join(root, "src", "loop")); err != nil {
		t.Skip("symlinks unavailable")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := RunWith(root, Options{NoCache: true, SkipPackages: true}); err != nil {
			t.Errorf("symlink loop: %v", err)
		}
	}()
	select {
	case <-done:
	case <-timeoutAfterSeconds(20):
		t.Fatal("a symlink loop hung the scan")
	}
}

// A directory symlinked to its own parent, one level down.
func TestSelfReferentialDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "pkg/a.ts", `export const a = 1;`)
	pkg := filepath.Join(root, "pkg")
	if err := os.Symlink(pkg, filepath.Join(pkg, "self")); err != nil {
		t.Skip("symlinks unavailable")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunWith(root, Options{NoCache: true, SkipPackages: true})
	}()
	select {
	case <-done:
	case <-timeoutAfterSeconds(20):
		t.Fatal("a self-referential symlink hung the scan")
	}
}

func TestUnusualFilenames(t *testing.T) {
	root := t.TempDir()
	names := []string{
		"src/café.ts",
		"src/日本語.ts",
		"src/emoji-🎉.ts",
		"src/spaces in name.ts",
		"src/weird'quote.ts",
		"src/semi;colon.ts",
		"src/dash-and.dot.ts",
		"src/UPPER.TS",
	}
	for _, n := range names {
		mustWrite(t, root, n, "export const x = 1;")
	}
	mustWrite(t, root, "src/index.ts", `import "./café";
import "./日本語";
import "./emoji-🎉";
import "./spaces in name";`)

	res, err := RunWith(root, Options{NoCache: true, SkipPackages: true})
	if err != nil {
		t.Fatalf("unusual filenames must not break the scan: %v", err)
	}
	t.Logf("files=%d unresolved=%d", res.FilesScanned, len(res.Unresolved))
	for _, u := range res.Unresolved {
		t.Errorf("unresolved %s:%d %q - %s", u.File, u.Line, u.Specifier, u.Reason)
	}
	if res.FilesScanned < len(names) {
		t.Errorf("scanned %d files, want at least %d", res.FilesScanned, len(names))
	}
}

// Deep nesting, near what a real monorepo with nested node_modules produces.
func TestVeryDeepNesting(t *testing.T) {
	root := t.TempDir()
	deep := strings.Repeat("a/", 60)
	mustWrite(t, root, deep+"leaf.ts", "export const l = 1;")
	mustWrite(t, root, "root.ts", `import "./`+deep+`leaf";`)

	res, err := RunWith(root, Options{NoCache: true, SkipPackages: true})
	if err != nil {
		t.Fatalf("deep nesting: %v", err)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("deep path did not resolve: %v", res.Unresolved)
	}
}

// A file that is a symlink to somewhere outside the repo.
func TestSymlinkEscapingTheRepo(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "repo")
	os.MkdirAll(root, 0o755)
	mustWrite(t, outer, "outside.ts", "export const o = 1;")
	mustWrite(t, root, "a.ts", `import "./linked";`)
	if err := os.Symlink(filepath.Join(outer, "outside.ts"), filepath.Join(root, "linked.ts")); err != nil {
		t.Skip("symlinks unavailable")
	}

	res, err := RunWith(root, Options{NoCache: true, SkipPackages: true})
	if err != nil {
		t.Fatal(err)
	}
	// The symlink lives inside the repo, so it is a repo file. What matters is
	// that no node escapes the root.
	for _, id := range res.Graph.IDs() {
		if p := res.Graph.Nodes[id].Path; strings.HasPrefix(p, "..") || filepath.IsAbs(p) {
			t.Errorf("node path escaped the repo: %q", p)
		}
	}
}

func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func timeoutAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}
