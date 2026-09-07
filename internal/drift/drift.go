// Package drift compares the architecture of two revisions.
//
// Every other check in this repository looks at one state of the code. This
// one looks at the difference between two, and it exists because that is where
// the damage happens.
//
// A change that adds a cycle, or couples two parts that were independent, or
// strands a file nothing reaches any more, does not look like anything in a
// line diff. Each individual line is reasonable. The reviewer sees twelve
// files touched and plausible edits in each, and the architecture quietly gets
// worse. That failure mode is old, but generated code makes it constant:
// something that writes plausible code very fast, with no memory of why the
// boundary was there, will cross it whenever crossing is the shortest path.
//
// git diff answers "what text changed". This answers "what did that do to the
// shape of the program".
package drift

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

// Severity says how much a finding should worry the reader.
type Severity string

const (
	// Regression is a change that made the architecture worse. These are what
	// a CI gate should fail on.
	Regression Severity = "regression"
	// Improvement is the same measure moving the right way. Reported because
	// a tool that only ever complains gets turned off.
	Improvement Severity = "improvement"
	// Note is a change worth seeing that is neither good nor bad on its own.
	Note Severity = "note"
)

// Finding is one architectural change.
type Finding struct {
	Severity Severity `json:"severity"`
	Kind     string   `json:"kind"`
	// Title names the thing in the reader's terms.
	Title string `json:"title"`
	// Detail says why it matters, and where.
	Detail string `json:"detail"`
	// Items are the members: a cycle's files, the imports that created a new
	// coupling. What makes the finding actionable rather than a claim.
	Items []string `json:"items,omitempty"`
}

// Report is the whole comparison.
type Report struct {
	Base string `json:"base"`
	Head string `json:"head"`
	// Findings, worst first.
	Findings []Finding `json:"findings"`
	// Counts of each severity, so a caller can gate without reading them all.
	Regressions  int `json:"regressions"`
	Improvements int `json:"improvements"`
	// Coupling is the share of the repository a single change can reach, at
	// each revision. It is the one number here that says how modular the
	// codebase is rather than what happened to it.
	Coupling     Coupling `json:"coupling"`
	FilesChanged int      `json:"filesChanged"`
	// Bail explains why the comparison could not be made.
	Bail string `json:"bail,omitempty"`
}

// Coupling is the median share of the repository reachable from one file.
//
// A change to a file in a well-separated codebase can only affect a small part
// of it; in a ball of mud it can affect most of it. Measured on real
// repositories this ranges from under a percent to over three quarters, which
// makes it the bluntest useful summary of whether the boundaries are real.
type Coupling struct {
	Base float64 `json:"base"`
	Head float64 `json:"head"`
	// Sampled is how many files the median was taken over.
	Sampled int `json:"sampled"`
}

// Compare scans the working tree and the given base revision, and reports what
// changed about the shape of the program.
func Compare(root, base string) (*Report, error) {
	r := &Report{Base: base, Head: "working tree", Findings: []Finding{}}

	// A worktree rather than a stash: stashing mutates the user's checkout,
	// and a tool that can lose uncommitted work because it wanted to read
	// something is not one anybody should run.
	tmp, err := os.MkdirTemp("", "cairn-base-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	wt := filepath.Join(tmp, "base")

	if out, err := git(root, "worktree", "add", "--detach", wt, base); err != nil {
		r.Bail = fmt.Sprintf("could not check out %q: %s", base, firstLine(out))
		return r, nil
	}
	defer git(root, "worktree", "remove", "--force", wt)

	before, err := scan.RunWith(wt, scan.Options{SkipPackages: true})
	if err != nil {
		r.Bail = "could not scan the base revision: " + err.Error()
		return r, nil
	}
	after, err := scan.RunWith(root, scan.Options{SkipPackages: true})
	if err != nil {
		return nil, err
	}

	if out, err := git(root, "diff", "--name-only", base, "--"); err == nil {
		r.FilesChanged = len(nonEmptyLines(out))
	}

	mBefore, mAfter := modules.Build(before), modules.Build(after)
	r.Findings = append(r.Findings, cycleFindings(before, after)...)
	r.Findings = append(r.Findings, couplingFindings(mBefore, mAfter)...)
	r.Findings = append(r.Findings, reachFindings(before, after)...)
	r.Findings = append(r.Findings, blastFindings(before, after)...)

	r.Coupling = Coupling{
		Base:    medianReach(before),
		Head:    medianReach(after),
		Sampled: sampleSize(after),
	}
	if r.Coupling.Head > r.Coupling.Base+0.02 {
		r.Findings = append(r.Findings, Finding{
			Severity: Regression, Kind: "coupling",
			Title: fmt.Sprintf("a change now reaches %.0f%% of the repository, up from %.0f%%",
				r.Coupling.Head*100, r.Coupling.Base*100),
			Detail: "the median share of files reachable from one file. It rises when new " +
				"imports join parts that were separate, and it is the bluntest measure of " +
				"whether the boundaries are still real",
		})
	}

	sort.SliceStable(r.Findings, func(i, j int) bool {
		return sevRank(r.Findings[i].Severity) < sevRank(r.Findings[j].Severity)
	})
	for _, f := range r.Findings {
		switch f.Severity {
		case Regression:
			r.Regressions++
		case Improvement:
			r.Improvements++
		}
	}
	return r, nil
}

func sevRank(s Severity) int {
	switch s {
	case Regression:
		return 0
	case Note:
		return 1
	default:
		return 2
	}
}

// cycleFindings reports loops that appeared or went away.
//
// A cycle is keyed by its member set, not by its order, so the same loop
// reported from a different starting file is recognised as the same loop
// rather than as one cycle removed and one added.
func cycleFindings(before, after *scan.Result) []Finding {
	b := cycleSet(before)
	a := cycleSet(after)
	var out []Finding

	// A cycle that disappeared because a bigger one swallowed it has not been
	// broken, and saying so would be the tool lying in the reassuring
	// direction. One added import to vue merged three separate loops of 14, 13
	// and 62 files into a single loop of 104 - reported naively that is one
	// regression and three improvements, which reads as a wash.
	absorbedBy := map[string][]string{}
	for k, files := range b {
		if _, still := a[k]; still {
			continue
		}
		for ak, af := range a {
			if _, had := b[ak]; had {
				continue
			}
			if subset(files, af) {
				absorbedBy[ak] = append(absorbedBy[ak], k)
				break
			}
		}
	}

	for k, files := range a {
		if _, had := b[k]; had {
			continue
		}
		detail := fmt.Sprintf("%d files now import each other in a loop, which did not before", len(files))
		if n := len(absorbedBy[k]); n > 0 {
			was := 0
			for _, bk := range absorbedBy[k] {
				was += len(b[bk])
			}
			detail = fmt.Sprintf("%d files now import each other in a loop. It swallowed %d "+
				"smaller cycle%s covering %d files, which were separate before - so this is one "+
				"loop where there were %d", len(files), n, plural(n), was, n)
		}
		out = append(out, Finding{
			Severity: Regression, Kind: "cycle-added",
			Title:  "new import cycle: " + summarise(files),
			Detail: detail,
			Items:  files,
		})
	}
	for k, files := range b {
		if _, still := a[k]; still {
			continue
		}
		// Absorbed, not broken. Already accounted for above.
		if absorbed(absorbedBy, k) {
			continue
		}
		out = append(out, Finding{
			Severity: Improvement, Kind: "cycle-removed",
			Title:  "import cycle broken: " + summarise(files),
			Detail: fmt.Sprintf("%d files no longer import each other in a loop", len(files)),
			Items:  files,
		})
	}
	return out
}

// subset reports whether every member of small is in big.
func subset(small, big []string) bool {
	set := make(map[string]bool, len(big))
	for _, x := range big {
		set[x] = true
	}
	for _, x := range small {
		if !set[x] {
			return false
		}
	}
	return true
}

func absorbed(by map[string][]string, key string) bool {
	for _, keys := range by {
		for _, k := range keys {
			if k == key {
				return true
			}
		}
	}
	return false
}

func cycleSet(res *scan.Result) map[string][]string {
	out := map[string][]string{}
	for _, c := range query.Cycles(res.Graph, query.AllEdges) {
		files := shortAll(c.Nodes)
		sort.Strings(files)
		out[strings.Join(files, "\x00")] = files
	}
	return out
}

// couplingFindings reports parts of the repository that became connected, or
// stopped being.
//
// This is the finding a line diff cannot produce at all. One added import can
// join two halves of a system that were deliberately independent, and it looks
// like one line.
func couplingFindings(before, after *modules.Map) []Finding {
	name := map[string]string{}
	for _, m := range after.Modules {
		name[m.ID] = m.Name
	}
	for _, m := range before.Modules {
		if _, ok := name[m.ID]; !ok {
			name[m.ID] = m.Name
		}
	}
	label := func(id string) string {
		if n, ok := name[id]; ok {
			return n
		}
		return strings.TrimPrefix(id, "g:")
	}

	b := map[[2]string]int{}
	for _, e := range before.Edges {
		b[[2]string{e.From, e.To}] = e.Count
	}
	var out []Finding
	for _, e := range after.Edges {
		k := [2]string{e.From, e.To}
		if _, had := b[k]; had {
			delete(b, k)
			continue
		}
		out = append(out, Finding{
			Severity: Regression, Kind: "coupling-added",
			Title: label(e.From) + " now depends on " + label(e.To),
			Detail: fmt.Sprintf("%d imports where there were none - two parts that were "+
				"independent no longer are", e.Count),
		})
	}
	for k := range b {
		out = append(out, Finding{
			Severity: Improvement, Kind: "coupling-removed",
			Title:  label(k[0]) + " no longer depends on " + label(k[1]),
			Detail: "a dependency between two parts of the repository is gone",
		})
	}
	return out
}

// reachFindings reports files that stopped being reachable.
//
// Deleting the last import of a file leaves it on disk and out of the program.
// Nothing fails, nothing is flagged, and it is dead weight from then on.
func reachFindings(before, after *scan.Result) []Finding {
	b := reachable(before)
	a := reachable(after)
	present := map[string]bool{}
	for _, id := range after.Graph.SortedIDs() {
		if n := after.Graph.Nodes[id]; n != nil && n.Kind == graph.File {
			present[n.Path] = true
		}
	}

	var stranded []string
	for p := range b {
		// Still in the repository, but nothing reaches it any more.
		if present[p] && !a[p] {
			stranded = append(stranded, p)
		}
	}
	sort.Strings(stranded)
	if len(stranded) == 0 {
		return nil
	}
	return []Finding{{
		Severity: Regression, Kind: "stranded",
		Title: fmt.Sprintf("%d file%s nothing reaches any more", len(stranded), plural(len(stranded))),
		Detail: "these were reachable from an entry point before and are not now. They are " +
			"still on disk, so nothing fails - they have just stopped being part of the program",
		Items: stranded,
	}}
}

func reachable(res *scan.Result) map[string]bool {
	entries := query.EntryPointsWith(res.Graph, res.ManifestEntries)
	roots := make([]string, 0, len(entries))
	for _, e := range entries {
		roots = append(roots, e.File)
	}
	out := map[string]bool{}
	for id := range query.Reachable(res.Graph, roots, query.AllEdges) {
		if n := res.Graph.Nodes[id]; n != nil && n.Kind == graph.File {
			out[n.Path] = true
		}
	}
	return out
}

// blastFindings reports files that became substantially more load-bearing.
//
// A file going from twenty dependents to four hundred is a change in what the
// codebase is, and it happens one import at a time without anyone deciding it.
func blastFindings(before, after *scan.Result) []Finding {
	b := blastMap(before)
	a := blastMap(after)
	type row struct {
		path     string
		from, to int
	}
	var rows []row
	for p, to := range a {
		from, had := b[p]
		if !had || to < 25 || to < from*2 || to-from < 20 {
			continue
		}
		rows = append(rows, row{p, from, to})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].to-rows[i].from > rows[j].to-rows[j].from })
	var out []Finding
	for i, r := range rows {
		if i >= 5 {
			break
		}
		out = append(out, Finding{
			Severity: Note, Kind: "blast-grew",
			Title: fmt.Sprintf("%s is now depended on by %d files, up from %d", r.path, r.to, r.from),
			Detail: "more of the repository now breaks when this file changes. Worth knowing " +
				"before it becomes the file nobody dares touch",
		})
	}
	return out
}

func blastMap(res *scan.Result) map[string]int {
	g := res.Graph
	out := map[string]int{}
	ids := g.SortedIDs()
	// Exact reachability everywhere is quadratic; above this size the direct
	// count is used and the comparison is still like-for-like.
	exact := len(ids) <= 4000
	for _, id := range ids {
		n := g.Nodes[id]
		if n == nil || n.Kind != graph.File {
			continue
		}
		if exact {
			out[n.Path] = len(query.ReachableFrom(g, id, query.AllEdges)) - 1
		} else {
			out[n.Path] = len(g.Dependents(id))
		}
	}
	return out
}

// medianReach is the coupling number: for a sample of files, the median share
// of the repository reachable from one of them, in either direction.
func medianReach(res *scan.Result) float64 {
	g := res.Graph
	var files []string
	for _, id := range g.SortedIDs() {
		if n := g.Nodes[id]; n != nil && n.Kind == graph.File {
			files = append(files, id)
		}
	}
	if len(files) == 0 {
		return 0
	}
	// A fixed stride rather than a random sample, so two runs of the same
	// commit produce the same number. A metric that moves on its own cannot
	// be used as a gate.
	step := 1
	if len(files) > 120 {
		step = len(files) / 120
	}
	var shares []float64
	for i := 0; i < len(files); i += step {
		up := query.ReachableFrom(g, files[i], query.AllEdges)
		down := query.Reachable(g, []string{files[i]}, query.AllEdges)
		seen := map[string]bool{}
		for k := range up {
			seen[k] = true
		}
		for k := range down {
			seen[k] = true
		}
		shares = append(shares, float64(len(seen))/float64(len(files)))
	}
	sort.Float64s(shares)
	return shares[len(shares)/2]
}

func sampleSize(res *scan.Result) int {
	n := 0
	for _, id := range res.Graph.SortedIDs() {
		if x := res.Graph.Nodes[id]; x != nil && x.Kind == graph.File {
			n++
		}
	}
	if n > 120 {
		return 120
	}
	return n
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func shortAll(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if j := strings.IndexByte(id, ':'); j >= 0 {
			out[i] = id[j+1:]
		} else {
			out[i] = id
		}
	}
	return out
}

func summarise(files []string) string {
	if len(files) <= 3 {
		return strings.Join(files, " → ")
	}
	return fmt.Sprintf("%s → … (%d files)", strings.Join(files[:2], " → "), len(files))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
