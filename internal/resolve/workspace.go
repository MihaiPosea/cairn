package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Workspace is one package inside a monorepo.
type Workspace struct {
	Name string // the name other packages import, e.g. "@acme/ui"
	Dir  string // absolute directory
	// Entry is the file its package.json points at, absolute, or "" if none
	// could be determined.
	Entry string
}

// findWorkspaces discovers the packages of a monorepo.
//
// Without this a monorepo is not merely incomplete, it is empty: every
// cross-package import resolves to an external package instead of the source
// file, so the graph has file nodes and no edges between them. Measured on a
// three-package fixture: three nodes, zero edges. That is worse than an error,
// because it looks like a working answer.
//
// Both conventions are read — npm/yarn/bun put a "workspaces" array in
// package.json, pnpm uses pnpm-workspace.yaml — because a repo may have either.
func findWorkspaces(root string) map[string]*Workspace {
	out := map[string]*Workspace{}

	// A package may import itself by its own name. Node allows it whenever the
	// manifest has an "exports" field, and libraries with subpath exports use
	// it constantly: preact's own sources say `import "preact/compat"` rather
	// than reaching across the tree with `../../compat/src`.
	//
	// Without this the specifier looks like any other bare name and resolves to
	// an external package, so the edge leaves the repository and the file it
	// actually points at appears to have one fewer dependent. Measured against
	// the TypeScript compiler on preact: 88% of all disagreements, and the
	// repository scored 49% until it was handled.
	if self := readWorkspace(root); self != nil && self.Name != "" {
		if raw, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
			var pkg struct {
				Exports json.RawMessage `json:"exports"`
			}
			// Only with an exports field: that is the condition Node puts on
			// self-reference, and honouring it keeps the behaviour the same as
			// the runtime's.
			if json.Unmarshal(raw, &pkg) == nil && len(pkg.Exports) > 0 {
				out[self.Name] = self
			}
		}
	}

	patterns := append(npmWorkspacePatterns(root), pnpmWorkspacePatterns(root)...)
	if len(patterns) == 0 {
		if len(out) == 0 {
			return nil
		}
		return out
	}

	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "!") {
			continue // negations are rare; ignoring one costs an extra package, not correctness
		}
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			continue
		}
		for _, dir := range matches {
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				continue
			}
			if ws := readWorkspace(dir); ws != nil {
				if _, dup := out[ws.Name]; !dup {
					out[ws.Name] = ws
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func npmWorkspacePatterns(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	// "workspaces" is either ["packages/*"] or {"packages":["packages/*"]}.
	var probe struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal(data, &probe) != nil || len(probe.Workspaces) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(probe.Workspaces, &list) == nil {
		return list
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(probe.Workspaces, &obj) == nil {
		return obj.Packages
	}
	return nil
}

func pnpmWorkspacePatterns(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml"))
	if err != nil {
		return nil
	}
	var cfg struct {
		Packages []string `yaml:"packages"`
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		return nil
	}
	return cfg.Packages
}

// readWorkspace reads one workspace package's manifest.
func readWorkspace(dir string) *Workspace {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pj struct {
		Name    string          `json:"name"`
		Main    string          `json:"main"`
		Module  string          `json:"module"`
		Types   string          `json:"types"`
		Source  string          `json:"source"`
		Exports json.RawMessage `json:"exports"`
	}
	if json.Unmarshal(data, &pj) != nil || pj.Name == "" {
		return nil
	}

	ws := &Workspace{Name: pj.Name, Dir: dir}

	// Prefer whatever points at source over whatever points at a build output.
	// A monorepo package usually declares both, and the graph should follow the
	// code someone can actually edit.
	for _, cand := range []string{pj.Source, exportsSource(pj.Exports), pj.Module, pj.Main, pj.Types} {
		if cand == "" {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(cand, "./")))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			ws.Entry = p
			break
		}
	}
	return ws
}

// exportsSource digs the first string out of an exports map.
// exportsSource picks the source file an "exports" map points at.
//
// Two things make this delicate, and getting either wrong is silent.
//
// The map has two shapes at different depths. The outer one is keyed by
// subpath — ".", "./styles", "./package.json" — and only "." is the package's
// main entry. The inner ones are keyed by condition — "import", "require",
// "types". Treating a subpath map as a condition map is how a package ends up
// resolving to its own package.json, since almost every modern manifest
// publishes "./package.json": "./package.json" and it is just another key.
//
// And the fallback must not iterate a Go map. Map order is randomised per run,
// so a manifest whose keys miss the preferred list resolves differently on
// different runs of the same scan. Measured on tanstack-query: 319 edges — a
// twelfth of the graph — flipped between two runs of an unchanged repository,
// every one of them a workspace import landing on package.json half the time
// and on src/index.ts the other half.
func exportsSource(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}

	// Conditions, in the order a source-first reader wants them.
	preferred := []string{"source", "development", "import", "module", "default", "require"}

	var pick func(any) string
	pick = func(x any) string {
		switch t := x.(type) {
		case string:
			if strings.HasSuffix(t, "package.json") {
				return "" // the manifest is not the package's code
			}
			return t

		case map[string]any:
			// A subpath map: only "." is the main entry. The others address
			// other files entirely and none of them substitute for it.
			if isSubpathMap(t) {
				if main, ok := t["."]; ok {
					return pick(main)
				}
				return ""
			}
			for _, k := range preferred {
				if sub, ok := t[k]; ok {
					if s := pick(sub); s != "" {
						return s
					}
				}
			}
			// Sorted, never range order: two runs must agree.
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if s := pick(t[k]); s != "" {
					return s
				}
			}

		case []any:
			for _, sub := range t {
				if s := pick(sub); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return pick(v)
}

// isSubpathMap reports whether an exports object is keyed by subpath rather
// than by condition. Subpath keys are "." or begin with "./"; condition keys
// never do.
func isSubpathMap(m map[string]any) bool {
	for k := range m {
		if k == "." || strings.HasPrefix(k, "./") {
			return true
		}
	}
	return false
}
