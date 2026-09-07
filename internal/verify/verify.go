// Package verify measures whether cairn's graph is actually true.
//
// It runs TypeScript's own module resolver over the same repo and diffs the
// answers. This is the difference between a tool that produces a plausible
// picture and one that produces a checked one - and the resulting precision
// and recall numbers are the only honest claim cairn can make about itself.
//
// The oracle is the real compiler, deliberately. A second implementation
// written by the same author would share the same misunderstandings and agree
// with the first for the wrong reasons.
package verify

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/scan"
)

//go:embed oracle.js
var oracleScript []byte

// Disagreement is one specifier the two resolvers answered differently.
type Disagreement struct {
	File      string
	Specifier string
	Cairn     string
	Oracle    string
	// Class explains the shape of the difference, so the report can separate
	// real errors from known, explainable gaps.
	Class string
}

// Report is the outcome of a verification run.
type Report struct {
	Available bool
	Reason    string
	TSVersion string

	// Compared is the number of (file, specifier) pairs both resolvers saw.
	Compared int
	Agreed   int

	// CairnMissed are specifiers TypeScript found that cairn did not - a hole
	// in the parser.
	CairnMissed []Disagreement
	// CairnExtra are specifiers cairn found that TypeScript did not. Usually
	// require() calls, which TypeScript's preprocessor reports differently.
	CairnExtra []Disagreement
	// Wrong are specifiers both saw but resolved to different things. These
	// are the real bugs.
	Wrong []Disagreement
	// Explained are differences with a known cause, listed separately so they
	// do not inflate or hide the error rate.
	Explained []Disagreement

	// RulesExercised counts how many specifiers each resolution rule handled
	// during the run.
	//
	// Without this a perfect score is unfalsifiable. A repo whose imports are
	// all relative never touches path aliases, so 100% on it proves nothing
	// about aliases - and that is not a hypothetical: measured on travel-site,
	// which has a tsconfig alias configured and zero imports that use it.
	RulesExercised map[string]int
}

// Precision is the share of cairn's resolutions that match TypeScript.
func (r *Report) Precision() float64 {
	total := r.Agreed + len(r.Wrong) + len(r.CairnExtra)
	if total == 0 {
		return 0
	}
	return float64(r.Agreed) / float64(total)
}

// Recall is the share of TypeScript's resolutions cairn reproduced.
func (r *Report) Recall() float64 {
	total := r.Agreed + len(r.Wrong) + len(r.CairnMissed)
	if total == 0 {
		return 0
	}
	return float64(r.Agreed) / float64(total)
}

type oracleOut struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason"`
	TSVersion  string `json:"tsVersion"`
	ConfigPath string `json:"configPath"`
	Files      []struct {
		File    string `json:"file"`
		Imports []struct {
			Specifier string `json:"specifier"`
			Resolved  string `json:"resolved"`
		} `json:"imports"`
	} `json:"files"`
}

// Run verifies a repo. It always scans with the cache disabled: measuring a
// cached answer would test the cache, not the resolver.
func Run(root string) (*Report, error) {
	res, err := scan.RunWith(root, scan.Options{SkipPackages: true, NoCache: true})
	if err != nil {
		return nil, err
	}

	files := make([]string, 0, res.FilesScanned)
	for _, id := range res.Graph.IDs() {
		n := res.Graph.Nodes[id]
		if n.Kind == graph.File && isTypeScriptish(n.Path) {
			files = append(files, n.Path)
		}
	}
	sort.Strings(files)

	out, err := runOracle(root, files)
	if err != nil {
		return nil, err
	}
	if !out.Available {
		return &Report{Available: false, Reason: out.Reason}, nil
	}

	rep := compare(root, res, out)
	rep.RulesExercised = res.ResolvedVia
	return rep, nil
}

// runOracle writes the embedded script to a temp file and runs it under node.
func runOracle(root string, files []string) (*oracleOut, error) {
	dir, err := os.MkdirTemp("", "cairn-oracle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	script := filepath.Join(dir, "oracle.js")
	if err := os.WriteFile(script, oracleScript, 0o644); err != nil {
		return nil, err
	}

	input, err := json.Marshal(map[string]any{"root": root, "files": files})
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("node", script)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running the oracle: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	var out oracleOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("parsing oracle output: %w", err)
	}
	return &out, nil
}

// compare diffs cairn's edges against the oracle's, keyed by (file, specifier).
func compare(root string, res *scan.Result, out *oracleOut) *Report {
	rep := &Report{Available: true, TSVersion: out.TSVersion}

	// cairn's answers, keyed the same way.
	type key struct{ file, spec string }
	mine := map[key]string{}
	for _, id := range res.Graph.IDs() {
		n := res.Graph.Nodes[id]
		if n.Kind != graph.File {
			continue
		}
		for _, e := range res.Graph.Dependencies(id) {
			if e.Specifier == "" {
				continue
			}
			mine[key{n.Path, e.Specifier}] = e.To
		}
	}

	seen := map[key]bool{}
	for _, f := range out.Files {
		for _, imp := range f.Imports {
			k := key{f.File, imp.Specifier}
			seen[k] = true

			theirs := normalizeOracle(root, imp.Resolved)
			ours, found := mine[k]

			if !found {
				rep.CairnMissed = append(rep.CairnMissed, Disagreement{
					File: f.File, Specifier: imp.Specifier,
					Cairn: "(not found)", Oracle: theirs,
					Class: "parser missed this import",
				})
				continue
			}

			rep.Compared++
			switch {
			case ours == theirs:
				rep.Agreed++

			// Both failed on the same specifier. That is agreement: the node
			// IDs differ only because cairn keeps the specifier text on its
			// unresolved node and TypeScript keeps nothing.
			case strings.HasPrefix(ours, "unresolved:") && strings.HasPrefix(theirs, "unresolved:"):
				rep.Agreed++

			// TypeScript refuses to resolve non-code imports, which cairn
			// resolves on purpose because a .css file is a real dependency.
			case strings.HasPrefix(theirs, "unresolved:") && !isTypeScriptish(strings.TrimPrefix(ours, "file:")) && strings.HasPrefix(ours, "file:"):
				rep.Agreed++
				rep.Explained = append(rep.Explained, Disagreement{
					File: f.File, Specifier: imp.Specifier, Cairn: ours, Oracle: theirs,
					Class: "non-code asset; TypeScript does not resolve these",
				})

			// TypeScript resolves a bare package to a .d.ts inside
			// node_modules; cairn treats packages as single units by design.
			case strings.HasPrefix(ours, "pkg:") && strings.HasPrefix(theirs, "pkg:"):
				rep.Agreed++

			// Node builtins. TypeScript only resolves these when @types/node is
			// installed *and* the importing file is inside the tsconfig program,
			// so a plain .mjs config file comes back unresolved. cairn is right
			// here and the oracle is not, which is worth stating plainly rather
			// than quietly counting as a loss.
			case strings.HasPrefix(ours, "builtin:") && strings.HasPrefix(theirs, "unresolved:"):
				rep.Agreed++
				rep.Explained = append(rep.Explained, Disagreement{
					File: f.File, Specifier: imp.Specifier, Cairn: ours, Oracle: theirs,
					Class: "Node builtin; tsc resolves these only with @types/node in-program",
				})

			default:
				rep.Wrong = append(rep.Wrong, Disagreement{
					File: f.File, Specifier: imp.Specifier, Cairn: ours, Oracle: theirs,
					Class: "resolved differently",
				})
			}
		}
	}

	for k, v := range mine {
		if !seen[k] {
			rep.CairnExtra = append(rep.CairnExtra, Disagreement{
				File: k.file, Specifier: k.spec, Cairn: v, Oracle: "(not seen)",
				Class: "cairn found an import TypeScript's preprocessor did not report",
			})
		}
	}

	sortDis(rep.Wrong)
	sortDis(rep.CairnMissed)
	sortDis(rep.CairnExtra)
	sortDis(rep.Explained)
	return rep
}

// normalizeOracle maps TypeScript's absolute answer onto cairn's node IDs.
func normalizeOracle(root, resolved string) string {
	if resolved == "" {
		return "unresolved:"
	}
	if i := strings.LastIndex(resolved, "node_modules/"); i >= 0 {
		rest := resolved[i+len("node_modules/"):]
		parts := strings.Split(rest, "/")
		name := parts[0]
		if strings.HasPrefix(name, "@") && len(parts) > 1 {
			name = name + "/" + parts[1]
		}
		return graph.NodeID(graph.Package, name)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "unresolved:" + resolved
	}
	return graph.NodeID(graph.File, filepath.ToSlash(rel))
}

func isTypeScriptish(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		return true
	}
	return false
}

func sortDis(d []Disagreement) {
	sort.Slice(d, func(i, j int) bool {
		if d[i].File != d[j].File {
			return d[i].File < d[j].File
		}
		return d[i].Specifier < d[j].Specifier
	})
}
