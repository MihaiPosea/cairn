package scan

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// A cache is only worth having if it can never change an answer. This mutates
// a repo the way someone actually works on one — edits, additions, deletions,
// renames, reverts — and after every step compares the cached scan against a
// scan with the cache disabled.
//
// The incremental index is the component where a bug is most likely and least
// visible: a stale entry produces a plausible graph that is quietly out of date,
// and nothing in the output would say so.
func TestCacheSurvivesMutation(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "tsconfig.json"),
		[]byte(`{"compilerOptions":{"paths":{"@/*":["./*"]}}}`), 0o644)
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)

	const initial = 40
	write := func(i int, deps []int) {
		var body string
		for _, d := range deps {
			body += fmt.Sprintf("import { v%d } from \"@/src/m%d\";\n", d, d)
		}
		body += fmt.Sprintf("export const v%d = %d;\n", i, i)
		os.WriteFile(filepath.Join(src, fmt.Sprintf("m%d.ts", i)), []byte(body), 0o644)
	}
	for i := 0; i < initial; i++ {
		var deps []int
		if i > 0 {
			deps = append(deps, i-1)
		}
		if i > 4 {
			deps = append(deps, i-5)
		}
		write(i, deps)
	}

	compare := func(t *testing.T, step string) {
		t.Helper()
		cold, err := RunWith(root, Options{SkipPackages: true, NoCache: true})
		if err != nil {
			t.Fatalf("%s: uncached scan: %v", step, err)
		}
		warm, err := RunWith(root, Options{SkipPackages: true})
		if err != nil {
			t.Fatalf("%s: cached scan: %v", step, err)
		}

		if warm.FilesScanned != cold.FilesScanned {
			t.Fatalf("%s: files cached=%d uncached=%d", step, warm.FilesScanned, cold.FilesScanned)
		}
		if warm.ImportsFound != cold.ImportsFound {
			t.Fatalf("%s: imports cached=%d uncached=%d", step, warm.ImportsFound, cold.ImportsFound)
		}
		if len(warm.Unresolved) != len(cold.Unresolved) {
			t.Fatalf("%s: unresolved cached=%d uncached=%d", step, len(warm.Unresolved), len(cold.Unresolved))
		}
		a, b := cold.Graph.IDs(), warm.Graph.IDs()
		if len(a) != len(b) {
			t.Fatalf("%s: nodes cached=%d uncached=%d", step, len(b), len(a))
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("%s: node %d cached=%s uncached=%s", step, i, b[i], a[i])
			}
		}
		if warm.Graph.EdgeCount() != cold.Graph.EdgeCount() {
			t.Fatalf("%s: edges cached=%d uncached=%d", step,
				warm.Graph.EdgeCount(), cold.Graph.EdgeCount())
		}
	}

	compare(t, "initial")

	rng := rand.New(rand.NewSource(11))
	next := initial

	for step := 0; step < 40; step++ {
		switch rng.Intn(5) {
		case 0: // edit an existing file's imports
			i := rng.Intn(next)
			write(i, []int{rng.Intn(next)})

		case 1: // add a new file
			write(next, []int{rng.Intn(next)})
			next++

		case 2: // delete a file
			i := rng.Intn(next)
			os.Remove(filepath.Join(src, fmt.Sprintf("m%d.ts", i)))

		case 3: // rename: same content, different path
			i := rng.Intn(next)
			from := filepath.Join(src, fmt.Sprintf("m%d.ts", i))
			if data, err := os.ReadFile(from); err == nil {
				os.WriteFile(filepath.Join(src, fmt.Sprintf("r%d.ts", i)), data, 0o644)
				os.Remove(from)
			}

		case 4: // revert a file to content it had before, so the cache must hit
			i := rng.Intn(next)
			write(i, []int{})
			compare(t, fmt.Sprintf("step %d pre-revert", step))
			write(i, []int{})
		}

		compare(t, fmt.Sprintf("step %d", step))
	}
}

// Two different repos must not share cache entries even when a file's contents
// are byte-identical, because the cache lives under each repo root.
func TestCachesAreIsolatedPerRepo(t *testing.T) {
	body := []byte(`import { x } from "./other";` + "\n")

	mk := func(t *testing.T) string {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "a.ts"), body, 0o644)
		os.WriteFile(filepath.Join(root, "other.ts"), []byte("export const x = 1;\n"), 0o644)
		return root
	}

	one, two := mk(t), mk(t)
	if _, err := Run(one); err != nil {
		t.Fatal(err)
	}
	res, err := Run(two)
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheHits != 0 {
		t.Errorf("a fresh repo hit %d cache entries; caches must be per-repo", res.CacheHits)
	}
}
