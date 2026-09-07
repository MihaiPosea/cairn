package pkgs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestEntries returns the files a repo's own package.json declares as its
// entry points: main, module, browser, types, bin, and the exports map.
//
// This exists because dead-code analysis is otherwise catastrophically wrong on
// a library. A Next.js app has entry points cairn can recognise by convention;
// a published package has exactly one signal, and it is in the manifest. Without
// reading it, every file in a library is unreachable and the tool advises
// deleting the entire codebase.
//
// Paths are returned repo-relative and slash-separated. They may not exist on
// disk - a manifest often points at a build output - so callers must check.
func ManifestEntries(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}

	var pj struct {
		Main    string          `json:"main"`
		Module  string          `json:"module"`
		Browser json.RawMessage `json:"browser"`
		Types   string          `json:"types"`
		Typings string          `json:"typings"`
		Bin     json.RawMessage `json:"bin"`
		Exports json.RawMessage `json:"exports"`
	}
	if json.Unmarshal(data, &pj) != nil {
		return nil
	}

	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimPrefix(strings.TrimSpace(p), "./")
		if p == "" || strings.HasPrefix(p, "http") {
			return
		}
		seen[filepath.ToSlash(p)] = true
	}

	for _, p := range []string{pj.Main, pj.Module, pj.Types, pj.Typings} {
		add(p)
	}
	// browser and bin are each either a string or a map of strings.
	for _, raw := range []json.RawMessage{pj.Browser, pj.Bin} {
		collectStrings(raw, add)
	}
	// exports nests arbitrarily deep: conditions inside subpaths inside
	// conditions. Every string leaf is a path, so collect them all rather than
	// modelling a structure that keeps gaining cases.
	collectStrings(pj.Exports, add)

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// collectStrings walks arbitrary JSON and hands every string leaf to add.
func collectStrings(raw json.RawMessage, add func(string)) {
	if len(raw) == 0 {
		return
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			add(t)
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
}
