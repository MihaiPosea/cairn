package pkgs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MihaiPosea/cairn/internal/resolve"
)

// ── npm ─────────────────────────────────────────────────────────────────────

// loadNPMLock reads package-lock.json v2 or v3.
//
// The `packages` map is keyed by install path, so nested duplicates appear as
// node_modules/a/node_modules/b. The root entry has an empty key and holds the
// repo's own declared dependencies.
func loadNPMLock(root, path string) (*Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Version      string            `json:"version"`
			Dependencies map[string]string `json:"dependencies"`
			Dev          bool              `json:"dev"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	if len(lock.Packages) == 0 {
		return nil, fmt.Errorf("package-lock.json v%d has no packages map (v1 lockfiles are not supported)", lock.LockfileVersion)
	}

	g := newGraph("package-lock.json")
	for installPath, entry := range lock.Packages {
		if installPath == "" {
			continue // the root project, handled by readDeclared
		}
		name := packageNameFromPath(installPath)
		if name == "" {
			continue
		}
		g.add(&Package{Name: name, Version: entry.Version, Deps: depNames(entry.Dependencies), Dev: entry.Dev})
	}
	return g, nil
}

// ── bun ─────────────────────────────────────────────────────────────────────

// loadBunLock reads bun.lock.
//
// It is JSONC — trailing commas and all — so it reuses the same scanner the
// tsconfig loader needs. Each package entry is an array whose shape is
// positional and undocumented:
//
//	"pkg": ["pkg@1.2.3", "<registry>", { "dependencies": {...} }, "<integrity>"]
//
// Only slots 0 and 2 matter here. Reading it positionally is fragile by
// nature, so anything unexpected degrades to "name with no version" rather
// than failing the scan.
func loadBunLock(root, path string) (*Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lock struct {
		Packages map[string][]json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(resolve.StripJSONC(data), &lock); err != nil {
		return nil, err
	}
	if len(lock.Packages) == 0 {
		return nil, fmt.Errorf("bun.lock has no packages")
	}

	g := newGraph("bun.lock")
	for key, slots := range lock.Packages {
		name := key
		if i := strings.LastIndex(key, "node_modules/"); i >= 0 {
			name = packageNameFromPath(key)
		}
		p := &Package{Name: name}

		if len(slots) > 0 {
			var nameAtVersion string
			if json.Unmarshal(slots[0], &nameAtVersion) == nil {
				if at := strings.LastIndex(nameAtVersion, "@"); at > 0 {
					p.Version = nameAtVersion[at+1:]
				}
			}
		}
		if len(slots) > 2 {
			var meta struct {
				Dependencies map[string]string `json:"dependencies"`
			}
			if json.Unmarshal(slots[2], &meta) == nil {
				p.Deps = depNames(meta.Dependencies)
			}
		}
		g.add(p)
	}
	return g, nil
}

// ── pnpm ────────────────────────────────────────────────────────────────────

// loadPnpmLock reads pnpm-lock.yaml v6 and v9.
//
// Keys look like "/react@19.0.0" or "react@19.0.0" depending on version, and
// may carry a peer-dependency suffix in parentheses that is not part of the
// version.
func loadPnpmLock(root, path string) (*Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lock struct {
		LockfileVersion any `yaml:"lockfileVersion"`
		Packages        map[string]struct {
			Dependencies map[string]string `yaml:"dependencies"`
		} `yaml:"packages"`
		Snapshots map[string]struct {
			Dependencies map[string]string `yaml:"dependencies"`
		} `yaml:"snapshots"`
	}
	if err := yaml.Unmarshal(data, &lock); err != nil {
		return nil, err
	}

	g := newGraph("pnpm-lock.yaml")
	consume := func(key string, deps map[string]string) {
		name, version := splitPnpmKey(key)
		if name == "" {
			return
		}
		g.add(&Package{Name: name, Version: version, Deps: depNames(deps)})
	}
	for k, v := range lock.Packages {
		consume(k, v.Dependencies)
	}
	// v9 splits metadata (packages) from the resolved tree (snapshots); the
	// dependency edges live in snapshots.
	for k, v := range lock.Snapshots {
		name, _ := splitPnpmKey(k)
		if p, ok := g.Packages[name]; ok && len(p.Deps) == 0 {
			p.Deps = depNames(v.Dependencies)
			continue
		}
		consume(k, v.Dependencies)
	}
	if len(g.Packages) == 0 {
		return nil, fmt.Errorf("pnpm-lock.yaml has no packages")
	}
	return g, nil
}

// splitPnpmKey turns "/@scope/pkg@1.2.3(peer@4)" into "@scope/pkg", "1.2.3".
func splitPnpmKey(key string) (name, version string) {
	k := strings.TrimPrefix(key, "/")
	if i := strings.Index(k, "("); i >= 0 {
		k = k[:i] // drop the peer-dependency suffix
	}
	return splitNameVersion(k)
}

// splitNameVersion separates a package name from whatever follows it.
//
// Splitting at the *last* "@" looks right and is wrong for aliased
// dependencies: "lodash-es@npm:lodash@^4.0.0" yields "lodash-es@npm:lodash",
// a name nothing imports, so the package never joins to the code that uses it.
// The name is everything before the first "@" that is not the scope marker.
func splitNameVersion(s string) (name, version string) {
	search := s
	offset := 0
	if strings.HasPrefix(s, "@") {
		search = s[1:] // the leading @ is a scope, not a separator
		offset = 1
	}
	at := strings.Index(search, "@")
	if at < 0 {
		return s, ""
	}
	return s[:offset+at], s[offset+at+1:]
}

// ── yarn ────────────────────────────────────────────────────────────────────

// loadYarnLock reads both yarn formats.
//
// Berry (v2+) is YAML with a __metadata key. Classic (v1) is a bespoke
// indentation format that predates yarn adopting YAML, and needs a line
// scanner. Classic entries look like:
//
//	"react@^18.0.0", "react@^18.2.0":
//	  version "18.3.1"
func loadYarnLock(root, path string) (*Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(data), "__metadata:") {
		return loadYarnBerry(data)
	}
	return loadYarnClassic(data)
}

func loadYarnBerry(data []byte) (*Graph, error) {
	var lock map[string]struct {
		Version      string            `yaml:"version"`
		Dependencies map[string]string `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	g := newGraph("yarn.lock (berry)")
	for key, entry := range lock {
		if key == "__metadata" {
			continue
		}
		// A key may list several ranges: "react@npm:^18, react@npm:^18.2".
		first := strings.TrimSpace(strings.Split(key, ",")[0])
		name := yarnDescriptorName(first)
		if name == "" {
			continue
		}
		g.add(&Package{Name: name, Version: entry.Version, Deps: depNames(entry.Dependencies)})
	}
	if len(g.Packages) == 0 {
		return nil, fmt.Errorf("yarn.lock (berry) has no packages")
	}
	return g, nil
}

func loadYarnClassic(data []byte) (*Graph, error) {
	g := newGraph("yarn.lock (v1)")
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)

	var current *Package
	inDeps := false

	flush := func() {
		if current != nil && current.Name != "" {
			g.add(current)
		}
		current, inDeps = nil, false
	}

	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// A header line starts at column 0 and ends with a colon.
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(trimmed, ":") {
			flush()
			first := strings.TrimSpace(strings.Split(strings.TrimSuffix(trimmed, ":"), ",")[0])
			current = &Package{Name: yarnDescriptorName(strings.Trim(first, `"`))}
			continue
		}
		if current == nil {
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "version "):
			current.Version = strings.Trim(strings.TrimPrefix(trimmed, "version "), `" `)
			inDeps = false
		case trimmed == "dependencies:":
			inDeps = true
		case inDeps && strings.HasPrefix(line, "    "):
			if name := strings.Trim(strings.Fields(trimmed)[0], `"`); name != "" {
				current.Deps = append(current.Deps, name)
			}
		default:
			inDeps = false
		}
	}
	flush()

	if len(g.Packages) == 0 {
		return nil, fmt.Errorf("yarn.lock (v1) has no packages")
	}
	return g, nil
}

// yarnDescriptorName strips the range from "react@^18.0.0",
// "@scope/pkg@npm:^1.0.0", or an alias like "lodash-es@npm:lodash@^4.0.0",
// leaving the name the code actually imports.
func yarnDescriptorName(desc string) string {
	name, _ := splitNameVersion(strings.Trim(desc, `"`))
	return name
}

// ── node_modules ────────────────────────────────────────────────────────────

// walkNodeModules reads what is actually installed.
//
// This is the fallback when there is no readable lockfile, and it is also the
// ground truth: a lockfile says what should be there, this says what is.
func walkNodeModules(root string, g *Graph) error {
	nm := filepath.Join(root, "node_modules")
	if _, err := os.Stat(nm); err != nil {
		return err
	}

	entries, err := os.ReadDir(nm)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if strings.HasPrefix(e.Name(), "@") {
			scoped, err := os.ReadDir(filepath.Join(nm, e.Name()))
			if err != nil {
				continue
			}
			for _, s := range scoped {
				if s.IsDir() {
					readInstalled(filepath.Join(nm, e.Name(), s.Name()), g)
				}
			}
			continue
		}
		readInstalled(filepath.Join(nm, e.Name()), g)
	}
	return nil
}

func readInstalled(dir string, g *Graph) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return
	}
	var pj struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(data, &pj) != nil || pj.Name == "" {
		return
	}
	g.add(&Package{Name: pj.Name, Version: pj.Version, Deps: depNames(pj.Dependencies)})
}
