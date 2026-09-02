package web

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

// short strips the "file:" / "pkg:" prefix from a node ID. The CLI has its own
// copy; duplicating four lines is cheaper than a shared package that exists
// only to hold them.
func short(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[i+1:]
	}
	return id
}

func shortAll(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = short(id)
	}
	return out
}

// buildReport turns the queries into things a person can read.
//
// The header used to carry "cycles 5" and "unreachable 14". Those are counts,
// and a count is not a finding: it tells the reader something is wrong without
// telling them what, where, or whether it matters. Every row here names the
// thing and says what it costs.
func buildReport(res *scan.Result, mm *modules.Map, deadRep *query.DeadReport,
	entries map[string]string, blast map[string]int, used map[string]bool) Report {

	r := Report{
		Entries:      []EntryPoint{},
		Cycles:       []Finding{},
		Unreachable:  []Finding{},
		Heavy:        []Finding{},
		Boundary:     []Finding{},
		UnresolvedBy: []Finding{},
	}
	g := res.Graph

	for id, why := range entries {
		r.Entries = append(r.Entries, EntryPoint{File: short(id), Reason: why})
	}
	sort.Slice(r.Entries, func(i, j int) bool { return r.Entries[i].File < r.Entries[j].File })
	if deadRep != nil {
		r.EntryBail = deadRep.Bail
	}

	r.Cycles = cycleFindings(g, mm, used)
	r.Unreachable = unreachableFindings(deadRep)
	r.Heavy = heavyFindings(g, blast, entries)
	r.Boundary = boundaryFindings(mm, used)
	r.UnresolvedBy = unresolvedFindings(res)
	return r
}

// cycleFindings leads with module cycles. A loop between two files is often
// deliberate — a type and its guard, split for readability. A loop between two
// modules means the boundary between them is not real, which is a different
// kind of statement and belongs first.
func cycleFindings(g *graph.Graph, mm *modules.Map, used map[string]bool) []Finding {
	// Two lists concatenated rather than one list with a weighted sort key.
	// The key would have to be a number, and that number is also what the row
	// displays — a module cycle of three would have rendered as "3000".
	mods := []Finding{}
	fileCycles := []Finding{}
	name := map[string]string{}
	for _, m := range mm.Modules {
		name[m.ID] = m.Name
	}
	for _, c := range mm.Cycles {
		keep := true
		for _, id := range c {
			if !used[id] {
				keep = false
			}
		}
		if !keep {
			continue
		}
		names := make([]string, 0, len(c))
		for _, id := range c {
			if n, ok := name[id]; ok {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		mods = append(mods, Finding{
			Title:  strings.Join(names, " ↔ "),
			Detail: fmt.Sprintf("%d modules depend on each other in a loop — the boundary between them is not real", len(c)),
			N:      len(c),
			Go:     c[0],
			Items:  names,
		})
	}

	for _, c := range query.Cycles(g, query.AllEdges) {
		files := shortAll(c.Nodes)
		sort.Strings(files)
		title := strings.Join(files, " → ")
		if len(files) > 3 {
			title = strings.Join(files[:3], " → ") + fmt.Sprintf(" → … (%d files)", len(files))
		}
		fileCycles = append(fileCycles, Finding{
			Title:  title,
			Detail: fmt.Sprintf("%d files import each other in a loop", len(files)),
			N:      len(files),
			Go:     c.Nodes[0],
			Items:  files,
		})
	}
	sort.SliceStable(mods, func(i, j int) bool { return mods[i].N > mods[j].N })
	sort.SliceStable(fileCycles, func(i, j int) bool { return fileCycles[i].N > fileCycles[j].N })
	return append(mods, fileCycles...)
}

// unreachableFindings groups dead files by the directory holding them. Fifty
// separate rows saying "this file is unreachable" is a list; one row saying
// "nothing reaches these 50 files in tests/fixtures" is a finding.
func unreachableFindings(rep *query.DeadReport) []Finding {
	out := []Finding{}
	if rep == nil || rep.Bail != "" {
		return out
	}
	byDir := map[string][]string{}
	why := map[string]string{}
	for _, d := range rep.Files {
		f := short(d.File)
		dir := path.Dir(f)
		if dir == "." {
			dir = "(repository root)"
		} else {
			dir += "/"
		}
		byDir[dir] = append(byDir[dir], f)
		why[dir] = d.Why
	}
	for dir, files := range byDir {
		sort.Strings(files)
		out = append(out, Finding{
			Title:  dir,
			Detail: why[dir],
			N:      len(files),
			Items:  files,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// heavyFindings ranks by blast radius: the files where a change is felt
// furthest. Entry points are excluded — an entry point with a large reach is
// the normal shape of a program, not a finding about it.
func heavyFindings(g *graph.Graph, blast map[string]int, entries map[string]string) []Finding {
	type row struct {
		id string
		n  int
	}
	var rows []row
	for id, n := range blast {
		if n <= 1 {
			continue
		}
		if _, isEntry := entries[id]; isEntry {
			continue
		}
		rows = append(rows, row{id, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].id < rows[j].id
	})
	if len(rows) > 12 {
		rows = rows[:12]
	}
	out := []Finding{}
	for _, r := range rows {
		out = append(out, Finding{
			Title:  short(r.id),
			Detail: fmt.Sprintf("change this and %d files are downstream of it", r.n),
			N:      r.n,
			Go:     r.id,
		})
	}
	return out
}

// boundaryFindings reports imports that reach past a module's declared entry
// point into its internals.
//
// This is the one finding here that is about design rather than fact. A module
// that publishes an entry point is saying "come in this way"; an import that
// goes around it means the boundary exists on paper only, and every such
// import is a thing that breaks when the module is reorganised.
func boundaryFindings(mm *modules.Map, used map[string]bool) []Finding {
	name := map[string]string{}
	entry := map[string]string{}
	for _, m := range mm.Modules {
		name[m.ID] = m.Name
		entry[m.ID] = m.Entry
	}
	out := []Finding{}
	for _, e := range mm.Edges {
		if e.ViaEntry || entry[e.To] == "" || !used[e.From] || !used[e.To] {
			continue
		}
		out = append(out, Finding{
			Title: name[e.From] + " → " + name[e.To],
			Detail: fmt.Sprintf("%d imports reach past %s into its internals rather than through %s",
				e.Count, name[e.To], entry[e.To]),
			N:  e.Count,
			Go: e.To,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].N > out[j].N })
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// unresolvedFindings reports what could not be resolved, by cause.
//
// Kept in the report rather than reduced to a percentage in the header,
// because the causes are the interesting part: a repo whose unresolved imports
// are all test fixtures asserting that an import fails is in a completely
// different state from one that is missing a generated directory.
func unresolvedFindings(res *scan.Result) []Finding {
	out := []Finding{}
	for _, c := range scan.ClusterUnresolved(res.Unresolved) {
		out = append(out, Finding{
			Title:  c.Category,
			Detail: fmt.Sprintf("%s — for example %s", c.Prefix, c.Example),
			N:      c.Count,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].N > out[j].N })
	return out
}
