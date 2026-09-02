package resolve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TSConfig holds the only two things from tsconfig.json that affect where an
// import points: baseUrl and paths.
type TSConfig struct {
	// Dir is the directory the winning config was found in. Relative paths in
	// baseUrl are resolved against it.
	Dir string
	// BaseURL is an absolute directory, or "" if unset.
	BaseURL string
	// Paths maps an alias pattern to candidate targets, e.g. "@/*" -> ["./*"].
	Paths map[string][]string
}

// LoadTSConfig reads tsconfig.json (or jsconfig.json) from dir and follows its
// `extends` chain.
//
// Returns a zero TSConfig and no error when there is no config: a repo without
// one is normal, not broken.
func LoadTSConfig(dir string) (*TSConfig, error) {
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			cfg := &TSConfig{Dir: dir, Paths: map[string][]string{}}
			if err := loadInto(cfg, p, dir, 0); err != nil {
				return cfg, err
			}
			return cfg, nil
		}
	}
	return &TSConfig{Dir: dir, Paths: map[string][]string{}}, nil
}

type rawTSConfig struct {
	Extends         any `json:"extends"`
	CompilerOptions struct {
		BaseURL string              `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
}

// loadInto applies one config file, recursing into `extends` first so that the
// child's values win. depth guards against a config that extends itself.
func loadInto(cfg *TSConfig, path, repoRoot string, depth int) error {
	if depth > 16 {
		return fmt.Errorf("tsconfig extends chain too deep at %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var raw rawTSConfig
	if err := json.Unmarshal(StripJSONC(data), &raw); err != nil {
		// A malformed tsconfig should degrade to "no aliases", not kill the
		// scan. The unresolved rate will show the cost.
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	// Parents first, so the child overrides them.
	for _, parent := range extendsList(raw.Extends) {
		if resolved := resolveExtends(parent, filepath.Dir(path), repoRoot); resolved != "" {
			_ = loadInto(cfg, resolved, repoRoot, depth+1)
		}
	}

	if raw.CompilerOptions.BaseURL != "" {
		cfg.BaseURL = filepath.Join(filepath.Dir(path), raw.CompilerOptions.BaseURL)
	}
	for k, v := range raw.CompilerOptions.Paths {
		cfg.Paths[k] = v
		// TypeScript 5 allows `paths` with no `baseUrl`, in which case targets
		// are relative to the config file's own directory. Next.js templates
		// ship exactly this shape, so it is the common case, not the exotic one.
		if cfg.BaseURL == "" {
			cfg.BaseURL = filepath.Dir(path)
		}
	}
	return nil
}

// extendsList normalises `extends`, which may be a string or an array.
func extendsList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// resolveExtends turns an `extends` value into a file path. It may be relative
// ("./base.json") or a package reference ("next/tsconfig.json").
func resolveExtends(spec, fromDir, repoRoot string) string {
	if strings.HasPrefix(spec, ".") || filepath.IsAbs(spec) {
		p := spec
		if !filepath.IsAbs(p) {
			p = filepath.Join(fromDir, spec)
		}
		if filepath.Ext(p) == "" {
			p += ".json"
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	// Package reference: walk up looking for it in node_modules.
	dir := fromDir
	for {
		cand := filepath.Join(dir, "node_modules", spec)
		if filepath.Ext(cand) == "" {
			cand += ".json"
		}
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir || len(dir) < len(repoRoot) {
			return ""
		}
		dir = parent
	}
}

// StripJSONC removes comments and trailing commas so encoding/json can read a
// tsconfig.
//
// tsconfig.json is not JSON. It permits // and /* */ comments and trailing
// commas, and virtually every real one uses them. Reaching for a JSON5 library
// would be the obvious move; a byte scanner that tracks string state is ~40
// lines, adds no dependency, and is exactly as correct for this input.
func StripJSONC(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString, escaped := false, false

	for i := 0; i < len(src); i++ {
		c := src[i]

		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch {
		case c == '"':
			inString = true
			out = append(out, c)

		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out = append(out, '\n')

		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // land on '/', loop increments past it

		case c == ',':
			// Drop the comma if the next non-space character closes a block.
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
				j++
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue
			}
			out = append(out, c)

		default:
			out = append(out, c)
		}
	}
	return out
}
