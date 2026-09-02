package drift

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MihaiPosea/cairn/internal/modules"
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

func titles(fs []Finding, sev Severity) []string {
	var out []string
	for _, f := range fs {
		if f.Severity == sev {
			out = append(out, f.Title)
		}
	}
	return out
}

// One import that closes a loop is the archetypal change this exists to catch:
// the diff is a single plausible line and the architecture is meaningfully
// worse.
func TestANewCycleIsARegression(t *testing.T) {
	before := build(t, map[string]string{
		"a.ts": `import "./b"; export const a = 1;`,
		"b.ts": `export const b = 1;`,
	})
	after := build(t, map[string]string{
		"a.ts": `import "./b"; export const a = 1;`,
		"b.ts": `import "./a"; export const b = 1;`,
	})

	fs := cycleFindings(before, after)
	if got := titles(fs, Regression); len(got) != 1 {
		t.Fatalf("expected one regression for the new cycle, got %v", titles(fs, Regression))
	}
	if got := titles(fs, Improvement); len(got) != 0 {
		t.Errorf("nothing improved, but reported: %v", got)
	}
}

// The dishonest case. When one import merges several loops into a bigger one,
// the small loops vanish — and calling each of those an improvement turns a
// clear regression into a wash. Measured on vue: three cycles of 14, 13 and 62
// files became a single cycle of 104, which naively reads as one regression
// and three improvements.
func TestAnAbsorbedCycleIsNotAnImprovement(t *testing.T) {
	// Two independent loops before: (a,b) and (c,d).
	before := build(t, map[string]string{
		"a.ts": `import "./b"; export const a = 1;`,
		"b.ts": `import "./a"; export const b = 1;`,
		"c.ts": `import "./d"; export const c = 1;`,
		"d.ts": `import "./c"; export const d = 1;`,
	})
	// One added edge joins them into a single loop of four.
	after := build(t, map[string]string{
		"a.ts": `import "./b"; export const a = 1;`,
		"b.ts": `import "./a"; import "./c"; export const b = 1;`,
		"c.ts": `import "./d"; export const c = 1;`,
		"d.ts": `import "./c"; import "./a"; export const d = 1;`,
	})

	fs := cycleFindings(before, after)
	if imp := titles(fs, Improvement); len(imp) != 0 {
		t.Errorf("the small cycles were swallowed, not broken, but were reported as improvements: %v", imp)
	}
	reg := titles(fs, Regression)
	if len(reg) != 1 {
		t.Fatalf("expected exactly one regression for the merged cycle, got %v", reg)
	}
	var detail string
	for _, f := range fs {
		if f.Severity == Regression {
			detail = f.Detail
		}
	}
	if !strings.Contains(detail, "swallowed") {
		t.Errorf("the regression should say it absorbed smaller cycles; got %q", detail)
	}
}

// Breaking a loop for real must still be reported, or the tool only ever
// complains and gets switched off.
func TestBreakingACycleIsAnImprovement(t *testing.T) {
	before := build(t, map[string]string{
		"a.ts": `import "./b"; export const a = 1;`,
		"b.ts": `import "./a"; export const b = 1;`,
	})
	after := build(t, map[string]string{
		"a.ts": `import "./b"; export const a = 1;`,
		"b.ts": `export const b = 1;`,
	})
	fs := cycleFindings(before, after)
	if got := titles(fs, Improvement); len(got) != 1 {
		t.Errorf("expected the broken cycle to be reported, got %v", got)
	}
	if got := titles(fs, Regression); len(got) != 0 {
		t.Errorf("nothing regressed, but reported: %v", got)
	}
}

// The finding a line diff cannot produce: one import joins two parts of the
// repository that were deliberately independent.
func TestNewCouplingBetweenModulesIsARegression(t *testing.T) {
	shared := map[string]string{
		"apps/web/main.ts":   `export const w = 1;`,
		"apps/api/main.ts":   `export const a = 1;`,
		"libs/util/index.ts": `export const u = 1;`,
	}
	before := build(t, shared)

	after := map[string]string{}
	for k, v := range shared {
		after[k] = v
	}
	after["apps/api/main.ts"] = `import "../web/main"; export const a = 1;`
	afterRes := build(t, after)

	fs := couplingFindings(modules.Build(before), modules.Build(afterRes))
	if got := titles(fs, Regression); len(got) == 0 {
		t.Errorf("apps/api now imports apps/web; expected a coupling regression, got %v", fs)
	}
}

// Deleting the last import of a file leaves it on disk and out of the program.
// Nothing fails, which is exactly why it needs saying.
func TestAFileNothingReachesAnyMoreIsARegression(t *testing.T) {
	before := build(t, map[string]string{
		"src/index.ts":  `import "./helper"; export const i = 1;`,
		"src/helper.ts": `export const h = 1;`,
	})
	after := build(t, map[string]string{
		"src/index.ts":  `export const i = 1;`,
		"src/helper.ts": `export const h = 1;`,
	})

	fs := reachFindings(before, after)
	if len(fs) != 1 || fs[0].Severity != Regression {
		t.Fatalf("expected helper.ts to be reported as stranded, got %v", fs)
	}
	found := false
	for _, it := range fs[0].Items {
		if it == "src/helper.ts" {
			found = true
		}
	}
	if !found {
		t.Errorf("the stranded file should be named; got %v", fs[0].Items)
	}
}

// A run that reports nothing must be reachable, or every change looks alarming.
func TestAnUnchangedRepoDriftsNotAtAll(t *testing.T) {
	files := map[string]string{
		"src/index.ts": `import "./a"; export const i = 1;`,
		"src/a.ts":     `export const a = 1;`,
	}
	before, after := build(t, files), build(t, files)

	var all []Finding
	all = append(all, cycleFindings(before, after)...)
	all = append(all, couplingFindings(modules.Build(before), modules.Build(after))...)
	all = append(all, reachFindings(before, after)...)
	all = append(all, blastFindings(before, after)...)
	if len(all) != 0 {
		t.Errorf("nothing changed, but %d findings were reported: %v", len(all), all)
	}
}

// The gate has to be stable: the same commit measured twice must give the same
// number, or it cannot be used to fail a build.
func TestCouplingIsDeterministic(t *testing.T) {
	files := map[string]string{
		"src/index.ts": `import "./a"; import "./b";`,
		"src/a.ts":     `import "./b"; export const a = 1;`,
		"src/b.ts":     `export const b = 1;`,
	}
	a, b := medianReach(build(t, files)), medianReach(build(t, files))
	if a != b {
		t.Errorf("two runs of the same code gave %v and %v", a, b)
	}
	if a <= 0 || a > 1 {
		t.Errorf("coupling should be a share between 0 and 1, got %v", a)
	}
}
