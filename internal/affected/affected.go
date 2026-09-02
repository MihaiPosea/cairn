// Package affected answers the question the graph was built to earn the right
// to ask: given what changed, what actually needs re-running?
//
// This is the payoff. Once you know what depends on what, you can skip work
// that could not possibly have been affected. It is also where being wrong
// stops being cosmetic: a missed edge in a picture is a slightly wrong
// picture, and a missed edge here is a test that should have run and did not.
//
// So the default is to fail loudly toward running everything. Every condition
// that could make the answer unsound produces a Bail with a stated reason,
// rather than a smaller and quietly incorrect answer.
package affected

import (
	"errors"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

// errNotAGitRepo is returned rather than git's own message, which talks about
// --no-index and confuses everyone who sees it.
var errNotAGitRepo = errors.New("not a git repository, so there is nothing to compare against")

// ErrNotAGitRepo reports whether an error came from a directory without git.
func ErrNotAGitRepo(err error) bool { return errors.Is(err, errNotAGitRepo) }

// Result is what needs attention after a change.
type Result struct {
	// Changed is the files git reported, repo-relative.
	Changed []string
	// Unknown are changed paths cairn has no node for — a new file, a deleted
	// one, or a file type it does not parse.
	Unknown []string
	// Affected is every file transitively depending on something changed,
	// including the changed files themselves.
	Affected []string
	// Tests is the subset of Affected that is a test file.
	Tests []string
	// Entries is the subset that is an entry point.
	Entries []string

	// TotalFiles is the size of the repo, for the reduction figure.
	TotalFiles int
	// TotalTests is how many test files exist at all.
	TotalTests int

	// Bail is set when the answer cannot be trusted. When it is non-empty the
	// caller must run everything, and the string says why.
	Bail string
}

// Reduction is the share of the repo that does not need attention. It is only
// meaningful when Bail is empty.
func (r *Result) Reduction() float64 {
	if r.TotalFiles == 0 {
		return 0
	}
	return 1 - float64(len(r.Affected))/float64(r.TotalFiles)
}

// TestReduction is the share of tests that can be skipped.
func (r *Result) TestReduction() float64 {
	if r.TotalTests == 0 {
		return 0
	}
	return 1 - float64(len(r.Tests))/float64(r.TotalTests)
}

// ChangedFiles asks git which files differ from a base ref.
//
// Falls back to the working tree when the base ref does not exist, which is
// what someone running this locally before committing actually wants.
func ChangedFiles(root, base string) ([]string, error) {
	if err := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return nil, errNotAGitRepo
	}

	// Two different questions, and the answer is the union of both:
	//
	//   base...HEAD  what this branch changed relative to the base
	//   HEAD         what is edited but not yet committed
	//
	// CI cares about the first, someone running it before pushing cares about
	// the second, and taking only one silently gives the wrong answer to the
	// other. Untracked files are included too: a brand new file is a change.
	seen := map[string]bool{}
	add := func(out []byte) {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, ".cairn/") {
				continue // cairn's own cache is not a change to the repo
			}
			seen[filepath.ToSlash(line)] = true
		}
	}

	if out, err := gitOut(root, "diff", "--name-only", base+"...HEAD"); err == nil {
		add(out)
	}
	if out, err := gitOut(root, "diff", "--name-only", "HEAD"); err == nil {
		add(out)
	}
	if out, err := gitOut(root, "ls-files", "--others", "--exclude-standard"); err == nil {
		add(out)
	}

	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	return files, nil
}

func gitOut(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	return cmd.Output()
}

// Compute works out what a set of changed files affects.
func Compute(res *scan.Result, changed []string) *Result {
	g := res.Graph
	out := &Result{Changed: changed}

	isTest := func(p string) bool {
		base := filepath.Base(p)
		return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
			strings.Contains(p, "__tests__/") || strings.Contains(p, "/e2e/")
	}

	entryReason := map[string]string{}
	for _, e := range query.EntryPoints(g) {
		entryReason[e.File] = e.Reason
	}

	for _, id := range g.IDs() {
		n := g.Nodes[id]
		if n.Kind != graph.File {
			continue
		}
		out.TotalFiles++
		if isTest(n.Path) {
			out.TotalTests++
		}
	}

	// Anything that makes the graph incomplete makes the answer unsound.
	switch {
	case len(res.Unanalyzable) > 0:
		out.Bail = "this repo has import() calls with computed paths, which cairn cannot follow — the affected set could be missing files"
	case len(res.Unresolved) > 0:
		out.Bail = "this repo has unresolved imports — the graph is incomplete, so the affected set could be missing files"
	}

	affected := map[string]bool{}
	for _, f := range changed {
		id := graph.NodeID(graph.File, f)
		if _, ok := g.Nodes[id]; !ok {
			out.Unknown = append(out.Unknown, f)
			// A changed file cairn does not know about could affect anything.
			// Config files are the common case and the dangerous one: change
			// a tsconfig or a build config and every assumption here is void.
			if out.Bail == "" {
				out.Bail = "changed files are not in the graph (" + f + ") — a config or a new file can affect anything"
			}
			continue
		}
		affected[id] = true
		for dep := range query.ReachableFrom(g, id, query.AllEdges) {
			affected[dep] = true
		}
	}

	for id := range affected {
		n := g.Nodes[id]
		out.Affected = append(out.Affected, n.Path)
		if isTest(n.Path) {
			out.Tests = append(out.Tests, n.Path)
		}
		if _, ok := entryReason[id]; ok {
			out.Entries = append(out.Entries, n.Path)
		}
	}
	sort.Strings(out.Affected)
	sort.Strings(out.Tests)
	sort.Strings(out.Entries)
	return out
}
