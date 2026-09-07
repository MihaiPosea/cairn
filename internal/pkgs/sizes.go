package pkgs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MeasureSizes fills in the installed byte size of every package by walking
// node_modules once.
//
// This is what makes "this import costs 1.2 MB" a real number rather than a
// package count. It is a stat-only walk, so it is fast even over a few hundred
// megabytes, but it is still the most expensive thing cairn does - hence it is
// opt-in rather than part of every scan.
//
// Nested node_modules are attributed to the package that contains them, which
// is the honest answer: those bytes exist on disk because of that package.
func MeasureSizes(root string, g *Graph) error {
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
					assign(g, e.Name()+"/"+s.Name(), filepath.Join(nm, e.Name(), s.Name()))
				}
			}
			continue
		}
		assign(g, e.Name(), filepath.Join(nm, e.Name()))
	}
	return nil
}

func assign(g *Graph, name, dir string) {
	p, ok := g.Packages[name]
	if !ok {
		// Installed but not in the lockfile - worth knowing about.
		p = &Package{Name: name}
		g.Packages[name] = p
		g.Warnings = append(g.Warnings, name+" is installed but absent from the lockfile")
	}
	p.Bytes = dirSize(dir)
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
