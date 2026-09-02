package scan

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// htmlRef matches src= and href= attributes.
//
// A regex is acceptable here where it would not be for code: an HTML entry
// point is a single attribute on a script or link tag, the files are tiny, and
// the failure mode of a miss is one extra file reported as unreachable rather
// than a wrong graph.
var htmlRef = regexp.MustCompile(`(?i)<(?:script|link)[^>]*?(?:src|href)\s*=\s*["']([^"']+)["']`)

// htmlEntries finds the source files an HTML document loads directly.
//
// This is how a Vite app declares its entry point:
//
//	<script type="module" src="/src/main.tsx"></script>
//
// Nothing imports that file, so without reading the HTML it looks unreachable —
// and so does everything only it reaches. Measured on Excalidraw: its app entry,
// its example app's entry, and its service worker were all reported dead.
func htmlEntries(root string, htmlFiles []string) []string {
	seen := map[string]bool{}
	var out []string

	for _, rel := range htmlFiles {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		dir := filepath.Dir(rel)

		for _, m := range htmlRef.FindAllStringSubmatch(string(data), -1) {
			ref := strings.TrimSpace(m[1])
			if ref == "" || strings.HasPrefix(ref, "http") || strings.HasPrefix(ref, "//") ||
				strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
				continue
			}
			// Strip any query or fragment the bundler would use.
			if i := strings.IndexAny(ref, "?#"); i > 0 {
				ref = ref[:i]
			}

			var candidate string
			if strings.HasPrefix(ref, "/") {
				candidate = strings.TrimPrefix(ref, "/")
			} else {
				candidate = filepath.ToSlash(filepath.Join(dir, filepath.FromSlash(ref)))
			}
			if candidate == "" || strings.HasPrefix(candidate, "..") {
				continue
			}
			if !seen[candidate] {
				seen[candidate] = true
				out = append(out, candidate)
			}
		}
	}
	return out
}

// findHTML lists the HTML documents in a repo, skipping the directories a scan
// already ignores.
func findHTML(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(path)); ext == ".html" || ext == ".htm" {
			if rel, err := filepath.Rel(root, path); err == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return out
}
