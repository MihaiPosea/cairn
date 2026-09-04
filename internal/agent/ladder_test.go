package agent

import (
	"strings"
	"testing"

	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/scan"
)

func ladder(t *testing.T, res *scan.Result, file string) *Ladder {
	t.Helper()
	l, err := BuildLadder(res, modules.Build(res), file)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// A chain of five: the middle file should see exactly two rungs each way, and
// nothing from the far ends leaking onto the near rung.
func TestRungsAreExactlyTwoHopsEachWay(t *testing.T) {
	res := build(t, map[string]string{
		"a.ts": `import "./b";`,       // 2 up
		"b.ts": `import "./c";`,       // 1 up
		"c.ts": `import "./d";`,       // the file
		"d.ts": `import "./e";`,       // 1 down
		"e.ts": `export const e = 1;`, // 2 down
	})
	l := ladder(t, res, "c.ts")

	for _, c := range []struct {
		got  Level
		want string
	}{
		{l.Up[0], "b.ts"}, {l.Up[1], "a.ts"},
		{l.Down[0], "d.ts"}, {l.Down[1], "e.ts"},
	} {
		if c.got.Count != 1 || c.got.Files[0] != c.want {
			t.Errorf("rung %d expected [%s], got %v", c.got.Distance, c.want, c.got.Files)
		}
	}
}

// A file reachable both directly and via a longer path belongs on the nearer
// rung: the shorter path is the one that describes the relationship.
func TestAFileAppearsOnItsNearestRungOnly(t *testing.T) {
	res := build(t, map[string]string{
		// hub imports leaf directly AND through mid.
		"hub.ts":  `import "./leaf"; import "./mid";`,
		"mid.ts":  `import "./leaf";`,
		"leaf.ts": `export const l = 1;`,
	})
	l := ladder(t, res, "hub.ts")

	if l.Down[0].Count != 2 {
		t.Errorf("one level down should hold leaf.ts and mid.ts, got %v", l.Down[0].Files)
	}
	for _, f := range l.Down[1].Files {
		if f == "leaf.ts" {
			t.Error("leaf.ts is a direct import and must not repeat on the second rung")
		}
	}
}

// The whole point of the view. A file with one direct importer can still carry
// most of the repository, and every editor's "find references" would show one
// result and imply it is safe.
func TestOneImporterCanStillBeAFoundation(t *testing.T) {
	files := map[string]string{
		"core.ts":  `export const core = 1;`,
		"index.ts": `import "./core"; export const i = 1;`,
	}
	// Thirty files import the barrel, none import core directly.
	for i := range 30 {
		files["f"+string(rune('a'+i%26))+string(rune('0'+i/26))+".ts"] = `import "./index";`
	}
	l := ladder(t, build(t, files), "core.ts")

	if l.Up[0].Count != 1 {
		t.Fatalf("core.ts has exactly one direct importer, got %d", l.Up[0].Count)
	}
	if l.Reach < 25 {
		t.Fatalf("core.ts should carry the whole repo through the barrel, reach=%d", l.Reach)
	}
	if !strings.Contains(l.Verdict, "foundation") {
		t.Errorf("a file with 1 importer and %d files above it is a foundation, got %q",
			l.Reach, l.Verdict)
	}
}

// Nothing above a file means two completely different things, and saying the
// wrong one sends the reader to delete something load-bearing.
func TestNothingAboveMeansEntryPointOrOrphanNotBoth(t *testing.T) {
	res := build(t, map[string]string{
		"package.json":  `{"name":"p","main":"src/index.ts"}`,
		"src/index.ts":  `import "./used";`,
		"src/used.ts":   `export const u = 1;`,
		"src/unused.ts": `export const stranded = 1;`,
	})

	entry := ladder(t, res, "src/index.ts")
	if !entry.Entry || entry.Orphan {
		t.Errorf("index.ts is the declared main; entry=%v orphan=%v", entry.Entry, entry.Orphan)
	}
	if !strings.Contains(entry.Verdict, "entry point") {
		t.Errorf("verdict should name it an entry point, got %q", entry.Verdict)
	}

	orphan := ladder(t, res, "src/unused.ts")
	if orphan.Entry || !orphan.Orphan {
		t.Errorf("unused.ts is reached by nothing; entry=%v orphan=%v", orphan.Entry, orphan.Orphan)
	}
	if strings.Contains(orphan.Verdict, "entry point") {
		t.Errorf("an orphan must not be described as an entry point: %q", orphan.Verdict)
	}
}

// A leaf must read as cheap to change, or the verdict is noise.
func TestALeafIsNamedAsOne(t *testing.T) {
	res := build(t, map[string]string{
		"main.ts": `import "./leaf";`,
		"leaf.ts": `export const l = 1;`,
	})
	l := ladder(t, res, "leaf.ts")
	if !strings.Contains(l.Verdict, "leaf") {
		t.Errorf("one importer, nothing below — expected a leaf verdict, got %q", l.Verdict)
	}
}

func TestAMissingFileSaysSo(t *testing.T) {
	res := build(t, map[string]string{"a.ts": `export const a = 1;`})
	if _, err := BuildLadder(res, nil, "nope.ts"); err == nil {
		t.Error("expected an error for a file that is not in the graph")
	}
}

// Counts and lists must agree, or one of them is lying.
func TestCountsMatchTheFilesListed(t *testing.T) {
	res := build(t, map[string]string{
		"a.ts": `import "./b"; import "./c";`,
		"b.ts": `import "./d";`,
		"c.ts": `import "./d";`,
		"d.ts": `export const d = 1;`,
	})
	l := ladder(t, res, "a.ts")
	for _, lv := range append(append([]Level{}, l.Up...), l.Down...) {
		if lv.Count != len(lv.Files) {
			t.Errorf("rung %d says %d files but lists %d", lv.Distance, lv.Count, len(lv.Files))
		}
	}
}
