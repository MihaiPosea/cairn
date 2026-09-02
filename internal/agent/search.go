package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

// Search is text search ordered by the dependency graph.
//
// Every search tool in the family — grep, ack, ag, ripgrep — has spent thirty
// years getting faster at the same question: which files contain these bytes.
// None of them can order the answer, because the file system does not know
// which files matter. Hits come back in path order, which is to say in an
// order chosen by whoever named the directories.
//
// That is fine for a person, who reads three results and stops. It is the
// central problem for an agent, which cannot tell hit 3 from hit 180 without
// opening both, and so opens twenty and guesses. Searching "parse" in a large
// repository returns hundreds of matches across subsystems that share a word
// and nothing else.
//
// The graph supplies exactly the missing ordering. A file that imports the one
// you are working on is relevant; a file six subsystems away that happens to
// contain the same identifier is not, however similar the two lines look. So
// this searches the same bytes and sorts the answer by whether the match can
// actually reach you, and how directly.
//
// The two halves are complementary and neither works alone: the graph has no
// idea what any file means, and grep has no idea which file the import on line
// three refers to.

// Hit is one matching line.
type Hit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
	// Hops is the distance through the import graph from the anchor. 0 is the
	// anchor itself, 1 is a direct neighbour. -1 means unconnected.
	Hops int `json:"hops"`
	// Direction is "upstream" when the match is in something that imports the
	// anchor — those break when the anchor changes — and "downstream" when the
	// anchor imports it.
	Direction string `json:"direction,omitempty"`
	// Via is the import chain from the anchor to this file: the evidence that
	// the two are connected at all.
	Via []string `json:"via,omitempty"`
}

// SearchResult is the whole answer.
type SearchResult struct {
	Pattern string `json:"pattern"`
	Anchor  string `json:"anchor,omitempty"`
	Hits    []Hit  `json:"hits"`
	// Connected and Unrelated split the hits by whether the graph joins them
	// to the anchor. The second number is what a plain grep would have handed
	// over without comment.
	Connected int `json:"connected"`
	Unrelated int `json:"unrelated"`
	// Searched is how many files were read.
	Searched int `json:"searched"`
	// OfFiles is how many the repository holds.
	OfFiles int `json:"ofFiles"`
}

// SearchOptions tunes a search.
type SearchOptions struct {
	// Anchor is the file the search is relative to. Without one this is an
	// ordinary grep and says so.
	Anchor string
	// ConnectedOnly drops matches the graph cannot join to the anchor.
	ConnectedOnly bool
	// Limit caps the hits returned. Zero means a default.
	Limit int
	// IgnoreCase does what it says.
	IgnoreCase bool
}

const defaultSearchLimit = 200

// maxSearchBytes skips files too large to be worth matching line by line.
const maxSearchBytes = 4 << 20

// Grep runs the search.
func Grep(res *scan.Result, pattern string, opt SearchOptions) (*SearchResult, error) {
	if opt.Limit <= 0 {
		opt.Limit = defaultSearchLimit
	}
	expr := pattern
	if opt.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}

	g := res.Graph
	out := &SearchResult{Pattern: pattern, Anchor: opt.Anchor, Hits: []Hit{}}

	var files []string
	for _, id := range g.SortedIDs() {
		if n := g.Nodes[id]; n != nil && n.Kind == graph.File {
			files = append(files, n.Path)
		}
	}
	out.OfFiles = len(files)

	// Distance from the anchor, in both directions. Upstream is what would
	// break if the anchor changed; downstream is what the anchor leans on.
	hops := map[string]int{}
	dir := map[string]string{}
	if opt.Anchor != "" {
		aid := graph.NodeID(graph.File, opt.Anchor)
		if g.Nodes[aid] == nil {
			return nil, &NotFound{Path: opt.Anchor}
		}
		for id, d := range query.ReachableFrom(g, aid, query.AllEdges) {
			if n := g.Nodes[id]; n != nil && n.Kind == graph.File {
				hops[n.Path] = d
				dir[n.Path] = "upstream"
			}
		}
		for id, d := range query.Reachable(g, []string{aid}, query.AllEdges) {
			if n := g.Nodes[id]; n != nil && n.Kind == graph.File {
				// Whichever direction reaches it in fewer hops is the one to
				// report; a file both above and below is closer one way.
				if old, seen := hops[n.Path]; !seen || d < old {
					hops[n.Path] = d
					dir[n.Path] = "downstream"
				}
			}
		}
		hops[opt.Anchor] = 0
		dir[opt.Anchor] = ""

		if opt.ConnectedOnly {
			var keep []string
			for _, p := range files {
				if _, ok := hops[p]; ok {
					keep = append(keep, p)
				}
			}
			files = keep
		}
	}
	out.Searched = len(files)

	hits := scanFiles(res.Root, files, re)

	for i := range hits {
		if d, ok := hops[hits[i].Path]; ok {
			hits[i].Hops = d
			hits[i].Direction = dir[hits[i].Path]
			out.Connected++
		} else {
			hits[i].Hops = -1
			out.Unrelated++
		}
	}

	// The ordering is the whole point. Connected before unconnected, then by
	// how few hops away, then upstream before downstream — a file that breaks
	// when you change the anchor is more urgent than one the anchor merely
	// uses — and finally by path so two runs agree.
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		ac, bc := a.Hops >= 0, b.Hops >= 0
		if ac != bc {
			return ac
		}
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.Direction != b.Direction {
			return a.Direction == "upstream"
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})

	if len(hits) > opt.Limit {
		hits = hits[:opt.Limit]
	}

	// The chain from the anchor to the file, so a caller can see why a hit was
	// ranked where it was rather than taking the number on trust. Only for the
	// ones actually shown, since each is a graph traversal.
	if opt.Anchor != "" {
		aid := graph.NodeID(graph.File, opt.Anchor)
		seen := map[string][]string{}
		for i := range hits {
			if hits[i].Hops <= 0 {
				continue
			}
			p := hits[i].Path
			if v, ok := seen[p]; ok {
				hits[i].Via = v
				continue
			}
			var chain []string
			if hits[i].Direction == "downstream" {
				chain = query.ShortestPath(g, []string{aid}, graph.NodeID(graph.File, p), query.AllEdges)
			} else {
				chain = query.ShortestPath(g, []string{graph.NodeID(graph.File, p)}, aid, query.AllEdges)
			}
			via := shortAll(chain)
			seen[p] = via
			hits[i].Via = via
		}
	}

	out.Hits = hits
	return out, nil
}

// scanFiles reads and matches in parallel. Matching is IO-bound and every file
// is independent, so this fans out and collects in a fixed order afterwards.
func scanFiles(root string, files []string, re *regexp.Regexp) []Hit {
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	in := make(chan string)
	var mu sync.Mutex
	var out []Hit
	var wg sync.WaitGroup

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range in {
				h := matchFile(root, rel, re)
				if len(h) == 0 {
					continue
				}
				mu.Lock()
				out = append(out, h...)
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		in <- f
	}
	close(in)
	wg.Wait()
	return out
}

func matchFile(root, rel string, re *regexp.Regexp) []Hit {
	full := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() || fi.Size() > maxSearchBytes {
		return nil
	}
	f, err := os.Open(full)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []Hit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		t := sc.Text()
		if !re.MatchString(t) {
			continue
		}
		t = strings.TrimSpace(t)
		if len(t) > 240 {
			t = t[:240] + "…"
		}
		out = append(out, Hit{Path: rel, Line: line, Text: t})
	}
	return out
}

func shortAll(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if i := strings.IndexByte(id, ':'); i >= 0 {
			out = append(out, id[i+1:])
		} else {
			out = append(out, id)
		}
	}
	return out
}
