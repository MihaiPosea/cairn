package web

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// sourceCache reads repository files once and hands out individual lines.
//
// Every edge wants the line of code that created it, and a file with forty
// imports would otherwise be read forty times. Files are read lazily, so a
// scan that never asks for source never pays for it.
type sourceCache struct {
	root string
	mu   sync.Mutex
	// lines maps a repo-relative path to its split contents; a nil entry means
	// the file was tried and could not be read.
	lines map[string][]string
}

func newSourceCache(root string) *sourceCache {
	return &sourceCache{root: root, lines: map[string][]string{}}
}

// maxSourceBytes skips files too large to be worth holding in memory. A
// generated bundle is not something anyone reads a line of in a side panel.
const maxSourceBytes = 2 << 20

// line returns the 1-based line of a repo-relative file, trimmed, or "".
func (c *sourceCache) line(path string, n int) string {
	if path == "" || n <= 0 {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	ls, seen := c.lines[path]
	if !seen {
		ls = c.read(path)
		c.lines[path] = ls
	}
	if n > len(ls) {
		return ""
	}
	s := strings.TrimSpace(ls[n-1])
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func (c *sourceCache) read(path string) []string {
	full := filepath.Join(c.root, filepath.FromSlash(path))
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() || fi.Size() > maxSourceBytes {
		return nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	return strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
}
