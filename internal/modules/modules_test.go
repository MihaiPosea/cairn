package modules

import (
	"os"
	"path/filepath"
	"strings"
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

// The viewer scopes the graph by module, so a node with no module is an edge
// endpoint it cannot place. Losing edges silently is the exact failure this
// layer exists to prevent, which makes total coverage the first thing to
// check.
func TestEveryFileLandsInExactlyOneModule(t *testing.T) {
	res := build(t, map[string]string{
		"app/page.tsx":          `import {B} from "../components/Button";`,
		"components/Button.tsx": `import "../lib/utils"; export const B = 1;`,
		"lib/utils.ts":          `export const u = 1;`,
		"index.ts":              `import "./app/page";`,
	})
	m := Build(res)

	ids := map[string]bool{}
	for _, mod := range m.Modules {
		if ids[mod.ID] {
			t.Errorf("two modules share the id %q", mod.ID)
		}
		ids[mod.ID] = true
	}

	files := 0
	for id := range res.Graph.Nodes {
		if !strings.HasPrefix(id, "file:") {
			continue
		}
		files++
		mid, ok := m.Of[id]
		if !ok {
			t.Errorf("%s belongs to no module", id)
			continue
		}
		if !ids[mid] {
			t.Errorf("%s maps to module %q, which is not in the list", id, mid)
		}
	}

	sum := 0
	for _, mod := range m.Modules {
		sum += mod.Files
	}
	if sum != files {
		t.Errorf("module file counts sum to %d, want %d", sum, files)
	}
}

// Module ids are the ids the browser already computes for the same directory.
// Drift here does not fail loudly: the page simply finds no box for the module
// and draws an empty scope.
func TestModuleIDsAreTheViewersGroupIDs(t *testing.T) {
	res := build(t, map[string]string{
		"packages/ui/index.ts":   `export const a = 1;`,
		"packages/core/index.ts": `export const b = 1;`,
		"app/main.ts":            `import "../packages/ui";`,
	})
	for _, mod := range Build(res).Modules {
		if mod.Kind == "other" || mod.Kind == "external" {
			continue
		}
		want := "g:" + strings.TrimSuffix(mod.Path, "/")
		if mod.ID != want {
			t.Errorf("module %q has id %q, want %q", mod.Path, mod.ID, want)
		}
	}
}

// Every edge endpoint must be a module that exists, or the viewer draws a line
// to nowhere — the same invariant internal/web already asserts for file edges.
func TestModuleEdgeEndpointsExist(t *testing.T) {
	res := build(t, map[string]string{
		"app/page.tsx": `import "../lib/utils"; import "../components/Button";`,
		"components/Button.tsx": `import "../lib/utils";
export const B = 1;`,
		"lib/utils.ts": `export const u = 1;`,
	})
	m := Build(res)
	ids := map[string]bool{}
	for _, mod := range m.Modules {
		ids[mod.ID] = true
	}
	for _, e := range m.Edges {
		if !ids[e.From] || !ids[e.To] {
			t.Errorf("edge %s -> %s references a module that does not exist", e.From, e.To)
		}
		if e.From == e.To {
			t.Errorf("edge %s points at itself; imports inside a module are not module edges", e.From)
		}
		if e.Count < 1 {
			t.Errorf("edge %s -> %s stands for %d imports", e.From, e.To, e.Count)
		}
	}
	if len(m.Edges) == 0 {
		t.Error("expected module edges between app/, components/ and lib/")
	}
}

// A cycle between two files is often deliberate; a cycle between two modules
// means the boundary is not real. The report leads with these, so a missed one
// is a finding that never reaches the reader.
func TestModuleCycleIsFound(t *testing.T) {
	res := build(t, map[string]string{
		"a/index.ts": `import "../b/index";`,
		"b/index.ts": `import "../a/index";`,
	})
	m := Build(res)
	if len(m.Cycles) == 0 {
		t.Fatalf("a/ and b/ import each other; no module cycle reported (modules=%d edges=%d)",
			len(m.Modules), len(m.Edges))
	}
	found := false
	for _, c := range m.Cycles {
		if len(c) >= 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("cycle groups are all shorter than two modules: %v", m.Cycles)
	}
}

// The top level is a reading limit, not a rendering one: past a dozen boxes it
// stops being a picture you take in at once, which is all this level is for.
func TestTopLevelStaysWithinTheReadingLimit(t *testing.T) {
	files := map[string]string{}
	for _, d := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j",
		"k", "l", "m", "n", "o", "p", "q", "r", "s", "t"} {
		files[d+"/index.ts"] = "export const x = 1;"
	}
	m := Build(build(t, files))
	if len(m.Modules) > maxModules+1 { // +1 for the merged tail
		t.Errorf("20 top-level directories produced %d modules, want at most %d",
			len(m.Modules), maxModules+1)
	}
	total := 0
	for _, mod := range m.Modules {
		total += mod.Files
	}
	if total != 20 {
		t.Errorf("merging the tail lost files: %d of 20 accounted for", total)
	}
}

// A monorepo should show its real top-level shape rather than one box labelled
// "packages/". This is the rule that produced 4-14 modules across the nine
// measured repositories.
func TestADominantDirectoryIsSplitIntoItsChildren(t *testing.T) {
	files := map[string]string{"app/main.ts": `export const m = 1;`}
	for _, p := range []string{"ui", "core", "utils", "cli"} {
		for i := range 5 {
			files["packages/"+p+"/src/f"+string(rune('a'+i))+".ts"] = "export const x = 1;"
		}
	}
	m := Build(build(t, files))

	var paths []string
	for _, mod := range m.Modules {
		paths = append(paths, mod.Path)
	}
	for _, want := range []string{"packages/ui/", "packages/core/"} {
		found := false
		for _, p := range paths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("packages/ holds 20 of 21 files but was not split; got %v", paths)
			break
		}
	}
}

// A workspace is the truest module boundary a repo declares about itself, so
// its declared name and entry point must win over the directory name.
func TestWorkspaceNamesAndEntriesAreUsed(t *testing.T) {
	files := map[string]string{
		"package.json": `{"name":"root","workspaces":["packages/*"]}`,
		"app/main.ts":  `export const m = 1;`,
	}
	for _, p := range []string{"ui", "core", "utils", "cli"} {
		files["packages/"+p+"/package.json"] = `{"name":"@acme/` + p + `","main":"src/index.ts"}`
		for i := range 5 {
			files["packages/"+p+"/src/f"+string(rune('a'+i))+".ts"] = "export const x = 1;"
		}
		files["packages/"+p+"/src/index.ts"] = "export const i = 1;"
	}
	m := Build(build(t, files))

	for _, mod := range m.Modules {
		if mod.Path != "packages/ui/" {
			continue
		}
		if mod.Kind != "workspace" {
			t.Errorf("packages/ui/ has kind %q, want workspace", mod.Kind)
		}
		if mod.Name != "@acme/ui" {
			t.Errorf("packages/ui/ is named %q, want the declared @acme/ui", mod.Name)
		}
		if mod.Entry == "" {
			t.Error("packages/ui/ declares main but no entry was recorded")
		}
		return
	}
	t.Errorf("packages/ui/ is not among the modules")
}

// An empty repo must not panic or return nil slices the JSON encoder would
// render as null, which the page would then iterate.
func TestEmptyRepoProducesEmptyNotNil(t *testing.T) {
	m := Build(build(t, map[string]string{"README.md": "nothing"}))
	if m == nil {
		t.Fatal("Build returned nil")
	}
	if m.Modules == nil || m.Edges == nil || m.Of == nil {
		t.Errorf("nil slices/maps encode as JSON null: modules=%v edges=%v of=%v",
			m.Modules == nil, m.Edges == nil, m.Of == nil)
	}
}
