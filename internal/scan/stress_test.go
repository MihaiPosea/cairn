package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MihaiPosea/cairn/internal/index"
	"github.com/MihaiPosea/cairn/internal/lang"
	"github.com/MihaiPosea/cairn/internal/resolve"
)

// bigFixture writes a repo with n files wired into a dense-ish graph.
func bigFixture(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "tsconfig.json"),
		[]byte(`{"compilerOptions":{"paths":{"@/*":["./*"]}}}`), 0o644)
	os.MkdirAll(filepath.Join(root, "src"), 0o755)

	for i := 0; i < n; i++ {
		var body string
		for _, j := range []int{i - 1, i - 3, i - 7} {
			if j >= 0 {
				body += fmt.Sprintf("import { v%d } from \"@/src/m%d\";\n", j, j)
			}
		}
		body += fmt.Sprintf("export const v%d = %d;\n", i, i)
		os.WriteFile(filepath.Join(root, "src", fmt.Sprintf("m%d.ts", i)), []byte(body), 0o644)
	}
	os.WriteFile(filepath.Join(root, "src", "index.ts"),
		[]byte(fmt.Sprintf("export { v%d } from \"@/src/m%d\";\n", n-1, n-1)), 0o644)
	return root
}

// Concurrent scans of the same repo must all agree, and must not corrupt the
// shared cache file. Run with -race.
func TestConcurrentScansAgree(t *testing.T) {
	root := bigFixture(t, 150)

	const workers = 12
	var wg sync.WaitGroup
	results := make([][]string, workers)
	errs := make([]error, workers)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			res, err := Run(root)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = res.Graph.IDs()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d failed: %v", i, err)
		}
	}
	for i := 1; i < workers; i++ {
		if len(results[i]) != len(results[0]) {
			t.Fatalf("worker %d produced %d nodes, worker 0 produced %d",
				i, len(results[i]), len(results[0]))
		}
		for j := range results[0] {
			if results[i][j] != results[0][j] {
				t.Fatalf("worker %d differs at node %d: %s vs %s",
					i, j, results[i][j], results[0][j])
			}
		}
	}

	// The cache must still be usable after twelve writers raced on it.
	after, err := Run(root)
	if err != nil {
		t.Fatalf("scan after concurrent writes failed: %v", err)
	}
	if after.CacheHits == 0 {
		t.Error("cache was left unusable by concurrent writers")
	}
}

// The resolver claims to be safe for concurrent use; hold it to that.
func TestResolverIsConcurrencySafe(t *testing.T) {
	root := bigFixture(t, 40)
	r, err := resolve.New(root)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				spec := fmt.Sprintf("@/src/m%d", i%40)
				got := r.Resolve("src/index.ts", spec)
				if got.Kind != resolve.ToFile {
					t.Errorf("worker %d: %q did not resolve (%s)", w, spec, got.Reason)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}

// The index is written and read by every parse worker.
func TestIndexIsConcurrencySafe(t *testing.T) {
	ix := index.Open(t.TempDir())

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := index.Hash([]byte(fmt.Sprintf("file-%d", i%50)))
				if _, ok := ix.Get(key); !ok {
					ix.Put(key, []lang.RawImport{{Specifier: "./x", Line: 1}})
				}
			}
		}(w)
	}
	wg.Wait()

	if ix.Len() != 50 {
		t.Errorf("index holds %d entries, want 50", ix.Len())
	}
	if err := ix.Save(); err != nil {
		t.Errorf("Save after concurrent use: %v", err)
	}
}

// A scan must not depend on whether the cache was warm.
func TestCachedAndUncachedAgreeOnALargeRepo(t *testing.T) {
	root := bigFixture(t, 300)

	cold, err := RunWith(root, Options{SkipPackages: true, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunWith(root, Options{SkipPackages: true}); err != nil {
		t.Fatal(err)
	}
	warm, err := RunWith(root, Options{SkipPackages: true})
	if err != nil {
		t.Fatal(err)
	}

	if warm.ImportsFound != cold.ImportsFound {
		t.Errorf("imports: cached=%d uncached=%d", warm.ImportsFound, cold.ImportsFound)
	}
	if len(warm.Unresolved) != len(cold.Unresolved) {
		t.Errorf("unresolved: cached=%d uncached=%d", len(warm.Unresolved), len(cold.Unresolved))
	}
	a, b := cold.Graph.IDs(), warm.Graph.IDs()
	if len(a) != len(b) {
		t.Fatalf("node count: cached=%d uncached=%d", len(b), len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("node %d: cached=%s uncached=%s", i, b[i], a[i])
		}
	}
}
