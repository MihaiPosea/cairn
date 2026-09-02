package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MihaiPosea/cairn/internal/scan"
)

func build(t *testing.T, files map[string]string) *scan.Result {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := scan.RunWith(root, scan.Options{SkipPackages: true, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A repository where the same identifier appears in a file that imports the
// anchor and in one that has nothing to do with it. Plain grep returns both
// and cannot say which is which; that is the entire problem.
func repoWithADecoy(t *testing.T) *scan.Result {
	return build(t, map[string]string{
		"src/target.ts":   `export function parseThing(x: string) { return x; }`,
		"src/caller.ts":   `import { parseThing } from "./target"; export const c = parseThing("a");`,
		"src/deep.ts":     `import { c } from "./caller"; export const d = c;`,
		"other/decoy.ts":  `function parseThing(y: number) { return y; }`,
		"other/decoy2.ts": `export const note = "parseThing lives elsewhere";`,
		"src/index.ts":    `import "./deep";`,
	})
}

// The ordering is the product. A file that imports the anchor has to come
// before one that merely contains the same word, or the caller is back to
// opening results until something looks right.
func TestConnectedMatchesRankAboveUnrelatedOnes(t *testing.T) {
	res := repoWithADecoy(t)
	r, err := Grep(res, "parseThing", SearchOptions{Anchor: "src/target.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Connected == 0 || r.Unrelated == 0 {
		t.Fatalf("expected both connected and unrelated hits, got %d and %d",
			r.Connected, r.Unrelated)
	}

	seenUnrelated := false
	for _, h := range r.Hits {
		if h.Hops < 0 {
			seenUnrelated = true
			continue
		}
		if seenUnrelated {
			t.Errorf("%s is connected (%d hops) but ranked below an unrelated match",
				h.Path, h.Hops)
		}
	}
	if r.Hits[0].Path != "src/target.ts" {
		t.Errorf("the anchor's own match should rank first, got %s", r.Hits[0].Path)
	}
}

// A file that breaks when the anchor changes is more urgent than one the
// anchor merely uses, so at equal distance upstream wins.
func TestUpstreamOutranksDownstreamAtTheSameDistance(t *testing.T) {
	res := build(t, map[string]string{
		"src/anchor.ts": `import { helper } from "./below"; export const a = helper;`,
		"src/above.ts":  `import { a } from "./anchor"; export const up = a;`,
		"src/below.ts":  `export const helper = 1;`,
		"src/index.ts":  `import "./above";`,
	})
	r, err := Grep(res, "helper|a", SearchOptions{Anchor: "src/anchor.ts"})
	if err != nil {
		t.Fatal(err)
	}
	var firstUp, firstDown = -1, -1
	for i, h := range r.Hits {
		if h.Hops != 1 {
			continue
		}
		if h.Direction == "upstream" && firstUp < 0 {
			firstUp = i
		}
		if h.Direction == "downstream" && firstDown < 0 {
			firstDown = i
		}
	}
	if firstUp >= 0 && firstDown >= 0 && firstUp > firstDown {
		t.Errorf("a one-hop upstream match ranked below a one-hop downstream one (%d vs %d)",
			firstUp, firstDown)
	}
}

// --connected is the aggressive mode: drop what the graph cannot join at all.
func TestConnectedOnlyDropsTheDecoys(t *testing.T) {
	res := repoWithADecoy(t)
	r, err := Grep(res, "parseThing", SearchOptions{Anchor: "src/target.ts", ConnectedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Unrelated != 0 {
		t.Errorf("--connected should leave no unrelated hits, got %d", r.Unrelated)
	}
	for _, h := range r.Hits {
		if h.Path == "other/decoy.ts" {
			t.Error("the unconnected decoy survived --connected")
		}
	}
	if r.Searched >= r.OfFiles {
		t.Errorf("--connected should read fewer files than the repository holds, read %d of %d",
			r.Searched, r.OfFiles)
	}
}

// Every ranked hit should be able to show why it was ranked there.
func TestConnectedHitsCarryTheChainThatJoinsThem(t *testing.T) {
	res := repoWithADecoy(t)
	r, err := Grep(res, "parseThing|c", SearchOptions{Anchor: "src/target.ts"})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range r.Hits {
		if h.Hops <= 0 {
			continue
		}
		if len(h.Via) == 0 {
			t.Errorf("%s is %d hops away but carries no chain explaining the connection",
				h.Path, h.Hops)
			continue
		}
		if h.Via[0] != h.Path && h.Via[len(h.Via)-1] != h.Path {
			t.Errorf("the chain for %s should start or end at it, got %v", h.Path, h.Via)
		}
	}
}

// With no anchor this is an ordinary grep, and must behave like one rather
// than inventing an order it has no basis for.
func TestWithoutAnAnchorNothingIsRanked(t *testing.T) {
	res := repoWithADecoy(t)
	r, err := Grep(res, "parseThing", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Connected != 0 {
		t.Errorf("without an anchor no hit can be connected to anything, got %d", r.Connected)
	}
	if len(r.Hits) == 0 {
		t.Error("expected matches")
	}
}

func TestAMissingAnchorSaysSo(t *testing.T) {
	res := repoWithADecoy(t)
	if _, err := Grep(res, "parseThing", SearchOptions{Anchor: "src/nope.ts"}); err == nil {
		t.Error("expected an error for an anchor that is not in the graph")
	}
}

func TestABadPatternIsAnErrorNotAPanic(t *testing.T) {
	res := repoWithADecoy(t)
	if _, err := Grep(res, "([unclosed", SearchOptions{}); err == nil {
		t.Error("expected a regex compilation error")
	}
}
