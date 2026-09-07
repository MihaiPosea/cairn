package agent

import (
	"sort"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

// Ladder is where a file sits in the stack of things standing on things.
//
// "Understand two levels up and two levels down" is usually said as a maxim
// about seniority. In a codebase it is not a metaphor at all: a level is a
// position in the import graph, and two levels is two hops. Down is what a
// file stands on. Up is what stands on it.
//
// Editors solved down thirty years ago. Cmd-click a name and you are in the
// file it came from - one hop, instantly, every editor. Nothing solved up.
// Find-references is symbol-level and noisy; it cannot say "eleven files need
// this one, and fifty-five more need those", and it certainly cannot do it two
// levels out.
//
// So every engineer knows what they are standing on, because they wrote the
// import, and almost nobody knows what is standing on them. That asymmetry is
// most of why people break things they never opened.
type Ladder struct {
	File   string `json:"file"`
	Module string `json:"module,omitempty"`
	// Up is what needs this file: index 0 is one level up, index 1 is two.
	Up []Level `json:"up"`
	// Down is what this file stands on, same indexing.
	Down []Level `json:"down"`
	// Verdict is the one sentence a reader wants: how much care this file
	// deserves, and why.
	Verdict string `json:"verdict"`
	// Reach is everything above, transitively - the honest blast radius.
	Reach int `json:"reach"`
	// Depends is everything below, transitively.
	Depends int `json:"depends"`
	// Share is Reach as a fraction of the repository. The absolute number
	// cannot be read without it: thirty files above you is the whole world in
	// a small repo and a corner of a large one.
	Share float64 `json:"share"`
	// Entry marks a file the outside world enters through, which has nothing
	// above it by design rather than by neglect.
	Entry bool `json:"entry,omitempty"`
	// Orphan marks a file with nothing above it that is not an entry point -
	// a different fact wearing the same shape.
	Orphan bool `json:"orphan,omitempty"`
}

// Level is one rung.
type Level struct {
	// Distance is 1 or 2.
	Distance int `json:"distance"`
	// Count is how many files are on this rung.
	Count int `json:"count"`
	// Files are their paths, sorted. Every one of them, so the answer can be
	// checked; callers that only want a preview take the first few.
	Files []string `json:"files"`
	// Modules names the parts of the repository this rung reaches into, which
	// is often the more useful summary once a rung is large.
	Modules []string `json:"modules,omitempty"`
}

// BuildLadder computes the two rungs either side of a file.
func BuildLadder(res *scan.Result, mm *modules.Map, file string) (*Ladder, error) {
	g := res.Graph
	id := graph.NodeID(graph.File, file)
	if g.Nodes[id] == nil {
		return nil, &NotFound{Path: file}
	}

	l := &Ladder{File: file, Up: []Level{}, Down: []Level{}}
	if mm != nil {
		if modID := mm.Of[id]; modID != "" {
			for _, m := range mm.Modules {
				if m.ID == modID {
					l.Module = m.Name
				}
			}
		}
	}

	l.Up = rungs(g, mm, id, func(n string) []string { return froms(g.Dependents(n)) })
	l.Down = rungs(g, mm, id, func(n string) []string { return tos(g.Dependencies(n)) })

	l.Reach = reachCount(g, id, true)
	l.Depends = reachCount(g, id, false)

	for _, e := range query.EntryPointsWith(g, res.ManifestEntries) {
		if e.File == id {
			l.Entry = true
		}
	}
	l.Orphan = !l.Entry && l.Up[0].Count == 0
	total := 0
	for _, n := range g.Nodes {
		if n.Kind == graph.File {
			total++
		}
	}
	if total > 0 {
		l.Share = float64(l.Reach) / float64(total)
	}
	l.Verdict = verdict(l)
	return l, nil
}

// rungs walks two hops, keeping each file on the nearest rung it appears on.
// A file that is both a direct neighbour and reachable in two hops belongs on
// rung one - the shorter path is the one that describes the relationship.
func rungs(g *graph.Graph, mm *modules.Map, start string, next func(string) []string) []Level {
	seen := map[string]bool{start: true}
	cur := []string{start}
	out := make([]Level, 0, 2)

	for d := 1; d <= 2; d++ {
		var rung []string
		for _, n := range cur {
			for _, m := range next(n) {
				if seen[m] {
					continue
				}
				seen[m] = true
				if node := g.Nodes[m]; node != nil && node.Kind == graph.File {
					rung = append(rung, node.Path)
				}
			}
		}
		sort.Strings(rung)
		out = append(out, Level{
			Distance: d, Count: len(rung), Files: rung,
			Modules: modulesOf(mm, rung),
		})
		cur = nil
		for _, p := range rung {
			cur = append(cur, graph.NodeID(graph.File, p))
		}
	}
	return out
}

func modulesOf(mm *modules.Map, files []string) []string {
	if mm == nil {
		return nil
	}
	name := map[string]string{}
	for _, m := range mm.Modules {
		name[m.ID] = m.Name
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if id, ok := mm.Of[graph.NodeID(graph.File, f)]; ok {
			if n, ok := name[id]; ok && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

func froms(es []graph.Edge) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.From
	}
	return out
}

func tos(es []graph.Edge) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.To
	}
	return out
}

// verdict says how much care the file deserves, in one sentence.
//
// This is the point of the whole view. The numbers are only useful once
// somebody has told you what they mean, and "eleven files import this" means
// something different in a repository of forty files than in one of nine
// thousand - which is why this reads the share of the repository above the
// file, not the count. An absolute threshold called a file merely load-bearing
// when thirty things stood on it, in a repository that only had thirty-two.
//
// Share needs a floor under it for the same reason. In a repository of two
// files everything is half of it, and a tool that calls a two-file toy project
// a foundation has stopped saying anything. Both a share and a count have to
// clear the bar.
//
// The thresholds are deliberately coarse. The reader needs to know which of
// four situations they are in, not a score to three decimal places.
func verdict(l *Ladder) string {
	up1 := l.Up[0].Count
	switch {
	case l.Entry && up1 == 0:
		return "An entry point. Nothing imports it because it is where the outside world comes " +
			"in - you can change how it works, but not what it is called or where it lives."
	case l.Orphan:
		return "Nothing imports this file. Either it is reached in a way no static tool can see, " +
			"or it is no longer part of the program."
	case (l.Share >= 0.25 && l.Reach >= 10) || l.Reach >= 200:
		return "A foundation. It carries a large part of the repository, and a change here is " +
			"felt a long way from where you make it. Read the rung above before touching it."
	case (l.Share >= 0.05 && l.Reach >= 5) || l.Reach >= 40:
		return "Load-bearing. Enough depends on this that a change wants a reason and a test."
	case up1 <= 2 && l.Depends <= 5:
		return "A leaf. Little stands on it and it stands on little - the cheapest kind of file " +
			"to change."
	default:
		return "Ordinary. A handful of files either side; change it with normal care."
	}
}

func reachCount(g *graph.Graph, id string, up bool) int {
	seen := map[string]bool{id: true}
	queue := []string{id}
	n := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		var next []string
		if up {
			next = froms(g.Dependents(cur))
		} else {
			next = tos(g.Dependencies(cur))
		}
		for _, m := range next {
			if seen[m] {
				continue
			}
			seen[m] = true
			if node := g.Nodes[m]; node != nil && node.Kind == graph.File {
				n++
				queue = append(queue, m)
			}
		}
	}
	return n
}
