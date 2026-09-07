package drift

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes a real repository with one commit, because Compare shells out
// to git and the worktree path is the half that unit tests of the finding
// functions never touch.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, files)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"add", "-A"},
		{"commit", "-qm", "base"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable or refused (%v): %s", err, out)
		}
	}
	return root
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// An unchanged tree must report nothing. This is the property that exposed a
// non-deterministic resolver: drift found regressions against a working copy
// with no edits, because two scans of the same code disagreed.
func TestCompareOnAnUnchangedTreeIsSilent(t *testing.T) {
	root := gitRepo(t, map[string]string{
		"package.json": `{"name":"p","main":"src/index.ts"}`,
		"src/index.ts": `import "./a"; import "./b";`,
		"src/a.ts":     `import "./b"; export const a = 1;`,
		"src/b.ts":     `export const b = 1;`,
	})
	r, err := Compare(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if r.Bail != "" {
		t.Fatalf("bailed: %s", r.Bail)
	}
	if len(r.Findings) != 0 {
		t.Errorf("nothing changed, but %d findings were reported: %+v", len(r.Findings), r.Findings)
	}
	if r.Regressions != 0 {
		t.Errorf("an unchanged tree reported %d regressions - this is a CI gate", r.Regressions)
	}
	if r.Coupling.Head != r.Coupling.Base {
		t.Errorf("coupling moved on its own: %v then %v", r.Coupling.Base, r.Coupling.Head)
	}
}

// The archetypal case: one added import closes a loop.
func TestCompareCatchesACycleAddedInTheWorkingTree(t *testing.T) {
	root := gitRepo(t, map[string]string{
		"package.json": `{"name":"p","main":"src/index.ts"}`,
		"src/index.ts": `import "./a";`,
		"src/a.ts":     `import "./b"; export const a = 1;`,
		"src/b.ts":     `export const b = 1;`,
	})
	write(t, root, map[string]string{"src/b.ts": `import "./a"; export const b = 1;`})

	r, err := Compare(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if r.Bail != "" {
		t.Fatalf("bailed: %s", r.Bail)
	}
	if r.Regressions == 0 {
		t.Fatalf("a new cycle should be a regression; got %+v", r.Findings)
	}
	found := false
	for _, f := range r.Findings {
		if f.Kind == "cycle-added" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a cycle-added finding, got %+v", r.Findings)
	}
	if r.FilesChanged != 1 {
		t.Errorf("one file was edited, FilesChanged=%d", r.FilesChanged)
	}
}

// A base that does not exist must be explained, not crash - and must not
// leave a worktree behind.
func TestCompareBailsOnAnUnknownBase(t *testing.T) {
	root := gitRepo(t, map[string]string{"a.ts": `export const a = 1;`})
	r, err := Compare(root, "no-such-ref-anywhere")
	if err != nil {
		t.Fatalf("an unknown ref should bail, not error: %v", err)
	}
	if r.Bail == "" {
		t.Error("expected an explanation")
	}
	if !strings.Contains(r.Bail, "no-such-ref-anywhere") {
		t.Errorf("the message should name the ref it could not find: %q", r.Bail)
	}
}

// Compare adds a worktree and must always remove it, or a second run fails
// with "already exists" and the user's repo accumulates junk.
func TestCompareLeavesNoWorktreeBehind(t *testing.T) {
	root := gitRepo(t, map[string]string{
		"package.json": `{"name":"p","main":"a.ts"}`,
		"a.ts":         `export const a = 1;`,
	})
	for i := range 2 {
		if _, err := Compare(root, "HEAD"); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(string(out)), "\n"); n != 0 {
		t.Errorf("worktrees left behind after two runs:\n%s", out)
	}
}

// Uncommitted files must be visible to the head scan - comparing the working
// tree is the whole point, and reading HEAD twice would report nothing ever.
func TestCompareSeesUncommittedWork(t *testing.T) {
	root := gitRepo(t, map[string]string{
		"package.json": `{"name":"p","main":"src/index.ts"}`,
		"src/index.ts": `import "./a";`,
		"src/a.ts":     `export const a = 1;`,
	})
	// A brand new file, never committed, that strands nothing and adds no
	// cycle - but does change the module edges.
	write(t, root, map[string]string{
		"src/index.ts":    `import "./a"; import "./newthing";`,
		"src/newthing.ts": `export const n = 1;`,
	})
	r, err := Compare(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if r.FilesChanged == 0 {
		t.Error("the working tree differs from HEAD but FilesChanged is 0")
	}
}
