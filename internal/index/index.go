// Package index caches parse results so a rescan only reparses what changed.
//
// The cache is keyed by the SHA-256 of a file's contents, never by
// modification time. mtime changes on a fresh checkout, a touch, or a clock
// skew without the file changing, and does not change when a file is restored
// from a backup — it is wrong in both directions. Content hashing costs one
// read, and the read has to happen anyway.
package index

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"github.com/MihaiPosea/cairn/internal/lang"
)

// formatVersion is bumped whenever the parser's output shape changes.
//
// Without it, upgrading cairn would silently serve parse results produced by
// the old parser — a stale cache that looks exactly like a correct one. On a
// mismatch the whole cache is discarded, which costs one slow scan and is the
// only safe answer.
// Bumped to 2 when the parser learned `import x = require("y")` and stopped
// truncating specifiers containing escapes. Any cache written before that
// holds results the current parser would not produce.
const formatVersion = 2

// Index is a content-addressed cache of parse results.
//
// Safe for concurrent use: the scanner's workers read and write it in parallel.
type Index struct {
	path string

	mu      sync.RWMutex
	entries map[string][]lang.RawImport

	hits   int
	misses int
}

type onDisk struct {
	Version int
	Entries map[string][]lang.RawImport
}

// Open loads the cache for a repo, or returns an empty one.
//
// A missing, corrupt, or stale-version cache is not an error. The cost of
// being wrong here is a slow scan; the cost of trusting a bad cache is a wrong
// answer, so anything suspicious is discarded.
func Open(root string) *Index {
	ix := &Index{
		path:    filepath.Join(root, ".cairn", "parse-cache.gob"),
		entries: map[string][]lang.RawImport{},
	}

	f, err := os.Open(ix.path)
	if err != nil {
		return ix
	}
	defer f.Close()

	var stored onDisk
	if err := gob.NewDecoder(f).Decode(&stored); err != nil {
		return ix
	}
	if stored.Version != formatVersion || stored.Entries == nil {
		return ix
	}
	ix.entries = stored.Entries
	return ix
}

// Hash returns the cache key for a file's contents.
func Hash(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

// Get returns cached imports for a content hash.
func (ix *Index) Get(hash string) ([]lang.RawImport, bool) {
	ix.mu.RLock()
	imports, ok := ix.entries[hash]
	ix.mu.RUnlock()

	ix.mu.Lock()
	if ok {
		ix.hits++
	} else {
		ix.misses++
	}
	ix.mu.Unlock()
	return imports, ok
}

// Put stores parse results under a content hash.
func (ix *Index) Put(hash string, imports []lang.RawImport) {
	ix.mu.Lock()
	ix.entries[hash] = imports
	ix.mu.Unlock()
}

// Stats reports cache hits and misses for this run.
func (ix *Index) Stats() (hits, misses int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.hits, ix.misses
}

// Len is how many entries the cache holds.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.entries)
}

// Save writes the cache back to disk.
//
// Writes to a temporary file and renames it into place, because rename is
// atomic and a half-written cache is a permanent, silent corruption — the
// worst failure this package could have.
//
// Entries for files that no longer exist are kept: they cost a few hundred
// bytes each and make branch switching free, since checking out an old branch
// finds its files already cached.
func (ix *Index) Save() error {
	ix.mu.RLock()
	stored := onDisk{Version: formatVersion, Entries: ix.entries}
	ix.mu.RUnlock()

	dir := filepath.Dir(ix.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Make the cache directory ignore itself rather than editing the user's
	// .gitignore. Found the hard way: without this, cairn's own cache file
	// shows up in `git diff` and `cairn affected` bails on its own artifact.
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o644)
	tmp, err := os.CreateTemp(filepath.Dir(ix.path), "parse-cache-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := gob.NewEncoder(tmp).Encode(stored); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), ix.path)
}
