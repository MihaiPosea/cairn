package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	patterns := append(npmWorkspacePatterns(root), pnpmWorkspacePatterns(root)...)
	if len(patterns) == 0 {
		return nil
	}

	out := map[string]*Workspace{}
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
func exportsSource(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	// Prefer keys that name source over keys that name builds.
	preferred := []string{"source", "development", "import", "default", "require"}
	var pick func(any) string
	pick = func(x any) string {
		switch t := x.(type) {
		case string:
			return t
		case map[string]any:
			for _, k := range preferred {
				if sub, ok := t[k]; ok {
					if s := pick(sub); s != "" {
						return s
					}
				}
			}
			for _, sub := range t {
				if s := pick(sub); s != "" {
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
