package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MihaiPosea/cairn/internal/lang"
)

func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	ix := Open(root)

	want := []lang.RawImport{{Specifier: "./x", Kind: lang.Static, Line: 3}}
	key := Hash([]byte("source"))
	ix.Put(key, want)
	if err := ix.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened := Open(root)
	got, ok := reopened.Get(key)
	if !ok {
		t.Fatal("entry did not survive a save/load round trip")
	}
	if len(got) != 1 || got[0].Specifier != "./x" || got[0].Line != 3 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDifferentContentMisses(t *testing.T) {
	ix := Open(t.TempDir())
	ix.Put(Hash([]byte("a")), []lang.RawImport{{Specifier: "./a"}})
	if _, ok := ix.Get(Hash([]byte("b"))); ok {
		t.Error("different content must not hit the cache")
	}
}

// A cache written by an older parser must be discarded, not trusted. Serving
// stale parse results looks exactly like serving correct ones.
func TestStaleVersionIsDiscarded(t *testing.T) {
	root := t.TempDir()
	ix := Open(root)
	ix.Put(Hash([]byte("x")), []lang.RawImport{{Specifier: "./x"}})
	if err := ix.Save(); err != nil {
		t.Fatal(err)
	}

	// Corrupt the version by rewriting the file with garbage.
	p := filepath.Join(root, ".cairn", "parse-cache.gob")
	if err := os.WriteFile(p, []byte("not a gob stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := Open(root).Len(); n != 0 {
		t.Errorf("a corrupt cache should be discarded, got %d entries", n)
	}
}

func TestMissingCacheIsNotAnError(t *testing.T) {
	if n := Open(t.TempDir()).Len(); n != 0 {
		t.Error("a fresh repo should start with an empty cache")
	}
}

func TestStatsCountHitsAndMisses(t *testing.T) {
	ix := Open(t.TempDir())
	ix.Put(Hash([]byte("a")), nil)
	ix.Get(Hash([]byte("a")))
	ix.Get(Hash([]byte("b")))
	hits, misses := ix.Stats()
	if hits != 1 || misses != 1 {
		t.Errorf("hits=%d misses=%d, want 1 and 1", hits, misses)
	}
}
