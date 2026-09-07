package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// loadSubpathImports reads the "imports" field of a package.json.
//
// Node's subpath imports are the standard way to give a package private
// internal aliases:
//
//	"imports": {
//	  "#internal/*": "./src/internal/*.js",
//	  "#config":     { "node": "./config.node.js", "default": "./config.js" }
//	}
//
// These are genuinely resolvable, so classifying them as "virtual" - which is
// where they landed at first, alongside astro: and virtual: - was giving up on
// information that is right there in the manifest. A '#' specifier only stays
// virtual when no manifest explains it.
//
// Conditional values are collapsed by preferring source-shaped conditions, the
// same way workspace entries are, because the graph should follow code someone
// can open.
func loadSubpathImports(dir string) map[string][]string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pj struct {
		Imports map[string]json.RawMessage `json:"imports"`
	}
	if json.Unmarshal(data, &pj) != nil || len(pj.Imports) == 0 {
		return nil
	}

	out := map[string][]string{}
	for pattern, raw := range pj.Imports {
		if !strings.HasPrefix(pattern, "#") {
			continue // the spec requires it; anything else is malformed
		}
		if targets := conditionTargets(raw); len(targets) > 0 {
			out[pattern] = targets
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// conditionTargets flattens a value that may be a string, a conditions object,
// or an array of alternatives, into candidate paths in preference order.
func conditionTargets(raw json.RawMessage) []string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}

	// Source-shaped conditions first, then the general ones. "types" is last:
	// it usually points at a .d.ts beside a build output rather than at source.
	preferred := []string{"source", "development", "import", "module", "require", "node", "default", "browser", "types"}

	var walk func(any) []string
	walk = func(x any) []string {
		switch t := x.(type) {
		case string:
			if t == "" || strings.HasPrefix(t, "http") {
				return nil
			}
			return []string{t}
		case []any:
			var out []string
			for _, e := range t {
				out = append(out, walk(e)...)
			}
			return out
		case map[string]any:
			var out []string
			seen := map[string]bool{}
			for _, k := range preferred {
				if sub, ok := t[k]; ok {
					for _, s := range walk(sub) {
						if !seen[s] {
							seen[s] = true
							out = append(out, s)
						}
					}
				}
			}
			// Any condition not in the preference list still beats nothing.
			for k, sub := range t {
				if strings.HasPrefix(k, "#") {
					continue
				}
				for _, s := range walk(sub) {
					if !seen[s] {
						seen[s] = true
						out = append(out, s)
					}
				}
			}
			return out
		}
		return nil
	}
	return walk(v)
}

// subpathConfigFor builds the alias rules for the nearest package.json that
// declares "imports", walking up from a directory to the repo root.
func (r *Resolver) subpathConfigFor(dir string) *dirConfig {
	if v, ok := r.subpaths.Load(dir); ok {
		return v.(*dirConfig)
	}

	cfg := &dirConfig{}
	for d := dir; ; {
		if mapping := loadSubpathImports(d); mapping != nil {
			rules := make([]AliasRule, 0, len(mapping))
			for pattern, targets := range mapping {
				rules = append(rules, AliasRule{Pattern: pattern, Targets: targets, Base: d})
			}
			cfg = &dirConfig{aliases: compileAliases(rules)}
			break
		}
		if d == r.root || len(d) <= len(r.root) {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	r.subpaths.Store(dir, cfg)
	return cfg
}
