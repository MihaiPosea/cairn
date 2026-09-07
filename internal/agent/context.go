// Package agent answers the questions a coding agent asks before it edits a
// file, rather than the questions a person asks while reading a graph.
//
// The two are not the same. A person asks "how is this repository shaped".
// An agent, about to change one file, asks "what else must I read so that I do
// not break something", and it asks under a hard context budget. Today it
// answers that by grepping: a name, then the names that turned up, outward
// until the budget is gone. That is expensive and, worse, it is wrong in a
// specific way - grep cannot tell which file `./utils` means, so the agent
// reads the wrong utils and is confidently incorrect.
//
// The division of labour this package assumes:
//
//	grep answers "where are these words".
//	the graph answers "what is connected to what".
//
// Neither subsumes the other. The graph knows that resize.ts is imported by
// eleven files and has no idea what any of them mean; grep knows every place
// the word "resize" appears and cannot tell you which of the nine files named
// utils.ts the import on line 3 refers to. An agent wants the graph to pick
// the files and grep to read them.
package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

// Why records the reason a file is in a read-set. The reason is the point: an
// agent handed twelve paths with no explanation has to open all of them to
// find out which matter, which is the cost the read-set exists to avoid.
type Why string

const (
	// WhyTarget is the file being changed.
	WhyTarget Why = "the file you are changing"
	// WhyImports is something the target imports: you cannot change a caller
	// without knowing what it calls.
	WhyImports Why = "imported by the target - its behaviour is used here"
	// WhyImportedBy is something that imports the target: these break.
	WhyImportedBy Why = "imports the target - a change here can break it"
	// WhyTypes is a type-only dependency. Cheap to read and it carries the
	// contract, so it is worth including early even on a tight budget.
	WhyTypes Why = "declares types the target uses"
	// WhyTest is a test that covers the target. Tests state the contract in
	// executable form, which is usually the fastest way to learn it.
	WhyTest Why = "tests the target"
	// WhyEntry is the public entry point of the target's module - the
	// boundary the change has to keep honouring.
	WhyEntry Why = "the module's entry point - its public surface"
	// WhySibling is another file in the same module, included only when the
	// budget allows, for local convention.
	WhySibling Why = "sits in the same module"
)

// File is one entry in a read-set.
type File struct {
	Path string `json:"path"`
	// Why says what put this file in the set.
	Why Why `json:"why"`
	// Hops is the distance from the target: 0 is the target itself.
	Hops int `json:"hops"`
	// Bytes is the file's size on disk, 0 if it could not be read.
	Bytes int64 `json:"bytes"`
	// Tokens is a rough cost estimate, for budgeting.
	Tokens int `json:"tokens"`
	// Blast is how many files depend on this one, transitively.
	Blast int `json:"blast"`
}

// Context is the answer to "I am about to change this file".
type Context struct {
	Target string `json:"target"`
	// Module is the part of the repository the target belongs to.
	Module string `json:"module,omitempty"`
	// Read is the ranked set of files worth loading, budget applied.
	Read []File `json:"read"`
	// Omitted counts files that belong in the set but did not fit.
	Omitted int `json:"omitted"`
	// OmittedTokens is what they would have cost.
	OmittedTokens int `json:"omittedTokens"`
	// Tokens is the estimated cost of everything in Read.
	Tokens int `json:"tokens"`
	// Blast is how many files depend on the target, transitively. The number
	// an agent should see before deciding how careful to be.
	Blast int `json:"blast"`
	// Scope is every file that could possibly be affected - the set to
	// constrain a search to instead of searching the repository. Much larger
	// than Read and much smaller than the repository.
	Scope []string `json:"scope"`
	// Warning is set when the answer is less trustworthy than it looks.
	Warning string `json:"warning,omitempty"`
}

// Options tunes a read-set.
type Options struct {
	// Budget is the token ceiling for Read. Zero means a default.
	Budget int
	// IncludeTests keeps test files in the set.
	IncludeTests bool
}

// DefaultBudget is a deliberately small ceiling.
//
// The value of a read-set is that it is short. An agent given eighty files has
// been handed the same problem it started with, so the default is closer to
// what fits comfortably beside a task than to what fits in a context window.
const DefaultBudget = 60000

// Build produces the read-set for changing one file.
func Build(res *scan.Result, mm *modules.Map, target string, opt Options) (*Context, error) {
	g := res.Graph
	id := graph.NodeID(graph.File, target)
	if g.Nodes[id] == nil {
		return nil, &NotFound{Path: target}
	}
	if opt.Budget <= 0 {
		opt.Budget = DefaultBudget
	}

	c := &Context{Target: target, Read: []File{}, Scope: []string{}}
	if mm != nil {
		if m := mm.Of[id]; m != "" {
			for _, mod := range mm.Modules {
				if mod.ID == m {
					c.Module = mod.Name
				}
			}
		}
	}

	// Everything that would be affected by the change, and everything the
	// target leans on. The first is the honest blast radius; the second is
	// what the change has to stay compatible with.
	up := query.ReachableFrom(g, id, query.AllEdges) // dependents
	down := query.Reachable(g, []string{id}, query.AllEdges)
	c.Blast = len(up) - 1
	if c.Blast < 0 {
		c.Blast = 0
	}

	// The search scope is the union: anything that could be involved either
	// way. This is what makes grep cheap - it is the set a search can be
	// restricted to without losing a hit that matters.
	scope := map[string]bool{}
	for k := range up {
		scope[k] = true
	}
	for k := range down {
		scope[k] = true
	}
	for k := range scope {
		if n := g.Nodes[k]; n != nil && n.Kind == graph.File {
			c.Scope = append(c.Scope, n.Path)
		}
	}
	sort.Strings(c.Scope)

	cand := candidates(g, mm, id, opt)
	blast := blastOf(g, cand, len(g.Nodes))

	// Rank by how much a reader needs the file, not by graph distance alone:
	// direct neighbours first, then the load-bearing ones, then the cheap
	// ones, so a tight budget spends itself on the files that decide whether
	// the change is correct.
	sort.SliceStable(cand, func(i, j int) bool {
		a, b := cand[i], cand[j]
		if ra, rb := rank(a.Why), rank(b.Why); ra != rb {
			return ra < rb
		}
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if blast[a.Path] != blast[b.Path] {
			return blast[a.Path] > blast[b.Path]
		}
		return a.Path < b.Path
	})

	for i := range cand {
		cand[i].Blast = blast[cand[i].Path]
		cand[i].Bytes, cand[i].Tokens = cost(res.Root, cand[i].Path)
	}

	spent := 0
	for _, f := range cand {
		// The target is never omitted, whatever it costs: a read-set without
		// the file being changed is not an answer.
		if f.Why != WhyTarget && spent+f.Tokens > opt.Budget {
			c.Omitted++
			c.OmittedTokens += f.Tokens
			continue
		}
		spent += f.Tokens
		c.Read = append(c.Read, f)
	}
	c.Tokens = spent

	if len(res.Unanalyzable) > 0 {
		c.Warning = "this repository has import() calls with computed specifiers, " +
			"which no static tool can follow - the set may be missing edges those create"
	}
	return c, nil
}

func rank(w Why) int {
	switch w {
	case WhyTarget:
		return 0
	case WhyTypes:
		return 1
	case WhyImports:
		return 2
	case WhyImportedBy:
		return 3
	case WhyTest:
		return 4
	case WhyEntry:
		return 5
	default:
		return 6
	}
}

// candidates collects the files worth considering, each with its reason.
func candidates(g *graph.Graph, mm *modules.Map, id string, opt Options) []File {
	seen := map[string]Why{}
	var out []File
	add := func(nodeID string, why Why, hops int) {
		n := g.Nodes[nodeID]
		if n == nil || n.Kind != graph.File {
			return
		}
		if !opt.IncludeTests && why != WhyTarget && isTest(n.Path) && why != WhyTest {
			return
		}
		if _, dup := seen[n.Path]; dup {
			return
		}
		seen[n.Path] = why
		out = append(out, File{Path: n.Path, Why: why, Hops: hops})
	}

	add(id, WhyTarget, 0)

	for _, e := range g.Dependencies(id) {
		why := WhyImports
		if e.Kind == graph.TypeOnly {
			why = WhyTypes
		}
		add(e.To, why, 1)
	}
	for _, e := range g.Dependents(id) {
		n := g.Nodes[e.From]
		if n != nil && isTest(n.Path) {
			add(e.From, WhyTest, 1)
			continue
		}
		add(e.From, WhyImportedBy, 1)
	}

	// The module's entry point is the contract the change must keep. It is
	// often not a neighbour of the target at all, which is exactly why it has
	// to be added deliberately.
	if mm != nil {
		if modID := mm.Of[id]; modID != "" {
			for _, m := range mm.Modules {
				if m.ID == modID && m.Entry != "" {
					add(graph.NodeID(graph.File, m.Entry), WhyEntry, 1)
				}
			}
			if p, ok := mm.Parts[modID]; ok && p.Entry != "" {
				add(graph.NodeID(graph.File, p.Entry), WhyEntry, 1)
			}
		}
	}
	return out
}

func blastOf(g *graph.Graph, cand []File, n int) map[string]int {
	out := make(map[string]int, len(cand))
	// Exact reachability per candidate is O(files x edges) and the candidate
	// list is short, so it stays cheap; on a very large graph fall back to the
	// direct count rather than making the caller wait.
	exact := n <= 8000
	for _, f := range cand {
		id := graph.NodeID(graph.File, f.Path)
		if exact {
			out[f.Path] = len(query.ReachableFrom(g, id, query.AllEdges)) - 1
		} else {
			out[f.Path] = len(g.Dependents(id))
		}
	}
	return out
}

func isTest(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.Contains(p, "__tests__/") || strings.Contains(p, "/e2e/")
}

// cost estimates what a file costs to read.
//
// Four bytes to the token is the usual rule of thumb for source text. It is an
// estimate and is labelled as one; the alternative is to tokenise, which would
// mean shipping a tokeniser for a number that only has to be roughly right.
func cost(root, rel string) (int64, int) {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return 0, 0
	}
	return fi.Size(), int(fi.Size()/4) + 1
}

// NotFound is returned when the target is not in the graph.
type NotFound struct{ Path string }

func (e *NotFound) Error() string {
	return e.Path + " is not a file in this repository's graph - check the path is " +
		"repo-relative, and that it is a source file cairn scans"
}
