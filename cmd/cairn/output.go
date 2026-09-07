package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/MihaiPosea/cairn/internal/affected"
	"github.com/MihaiPosea/cairn/internal/agent"
	"github.com/MihaiPosea/cairn/internal/drift"
	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/modules"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
	"github.com/MihaiPosea/cairn/internal/verify"
	"github.com/MihaiPosea/cairn/internal/web"
)

// ── scan ────────────────────────────────────────────────────────────────────

type summaryOut struct {
	Root            string  `json:"root"`
	FilesScanned    int     `json:"files_scanned"`
	FileNodes       int     `json:"file_nodes"`
	Imports         int     `json:"imports"`
	FileEdges       int     `json:"file_edges"`
	Packages        int     `json:"packages"`
	Builtins        int     `json:"builtins"`
	Unresolved      int     `json:"unresolved"`
	UnresolvedRate  float64 `json:"unresolved_rate"`
	Unanalyzable    int     `json:"unanalyzable"`
	ParseFailures   int     `json:"parse_failures"`
	AliasesLoaded   bool    `json:"tsconfig_aliases_loaded"`
	CacheHits       int     `json:"cache_hits"`
	CacheMisses     int     `json:"cache_misses"`
	CaseMismatches  int     `json:"case_mismatches"`
	FromBuildOutput int     `json:"from_build_output"`

	PackageSource       string   `json:"package_source,omitempty"`
	Declared            int      `json:"declared,omitempty"`
	Locked              int      `json:"locked,omitempty"`
	Installed           int      `json:"installed,omitempty"`
	UnusedDeclared      []string `json:"unused_declared,omitempty"`
	ImportedNotDeclared []string `json:"imported_not_declared,omitempty"`

	// UnresolvedList is every unresolved specifier, not the truncated preview
	// the human output shows. Diagnosing a high rate needs all of them.
	UnresolvedList []unresolvedOut `json:"unresolved_list,omitempty"`

	// UnresolvedByCategory counts them by cause, which is what turns a rate
	// into a verdict: "0.9% unresolved, all of it test fixtures" is a healthy
	// repo, and the same number of genuinely missing files is not.
	UnresolvedByCategory map[string]int `json:"unresolved_by_category,omitempty"`
}

type unresolvedOut struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Specifier string `json:"specifier"`
	Reason    string `json:"reason"`
}

func summary(res *scan.Result) summaryOut {
	s := res.Graph.Stats()
	out := summaryOut{
		Root:            res.Root,
		FilesScanned:    res.FilesScanned,
		FileNodes:       s[graph.File],
		Imports:         res.ImportsFound,
		FileEdges:       res.Graph.EdgeCount(graph.File),
		Packages:        s[graph.Package],
		Builtins:        s[graph.Builtin],
		Unresolved:      len(res.Unresolved),
		UnresolvedRate:  res.UnresolvedRate(),
		Unanalyzable:    len(res.Unanalyzable),
		ParseFailures:   len(res.ParseFailures),
		AliasesLoaded:   res.AliasesLoaded,
		CacheHits:       res.CacheHits,
		CacheMisses:     res.CacheMisses,
		CaseMismatches:  len(res.CaseMismatches),
		FromBuildOutput: res.FromBuildOutput,
	}
	if res.Packages != nil {
		out.PackageSource = res.Packages.Source
		out.Declared = res.Packages.DeclaredCount()
		out.Locked = len(res.Packages.Packages)
	}
	if res.Disagree != nil {
		out.Installed = res.Disagree.InstalledCount
	}
	if res.Join != nil {
		out.UnusedDeclared = res.Join.UnusedDeclared
		out.ImportedNotDeclared = res.Join.ImportedNotDeclared
	}
	for _, u := range res.Unresolved {
		out.UnresolvedList = append(out.UnresolvedList, unresolvedOut{
			File: u.File, Line: u.Line, Specifier: u.Specifier, Reason: u.Reason,
		})
	}
	if len(res.Unresolved) > 0 {
		out.UnresolvedByCategory = map[string]int{}
		for _, c := range scan.ClusterUnresolved(res.Unresolved) {
			out.UnresolvedByCategory[c.Category] += c.Count
		}
	}
	return out
}

func printSummary(res *scan.Result) {
	s := res.Graph.Stats()
	fmt.Printf("%s\n\n", res.Root)
	fmt.Printf("  %-22s %d\n", "files scanned", res.FilesScanned)
	if assets := s[graph.File] - res.FilesScanned; assets > 0 {
		fmt.Printf("  %-22s %d (css, json, other imported assets)\n", "other files reached", assets)
	}
	fmt.Printf("  %-22s %d\n", "imports", res.ImportsFound)
	fmt.Printf("  %-22s %d\n", "file -> file edges", res.Graph.EdgeCount(graph.File))
	fmt.Printf("  %-22s %d\n", "packages in graph", s[graph.Package])
	fmt.Printf("  %-22s %d\n", "runtime builtins", s[graph.Builtin])
	if res.AliasesLoaded {
		fmt.Printf("  %-22s %s\n", "tsconfig aliases", "loaded")
	}
	if total := res.CacheHits + res.CacheMisses; total > 0 {
		fmt.Printf("  %-22s %d of %d files (%d reparsed)\n", "served from cache",
			res.CacheHits, total, res.CacheMisses)
	}
	if n := len(res.ParseFailures); n > 0 {
		fmt.Printf("  %-22s %d\n", "files that failed", n)
	}

	fmt.Printf("\n  %-22s %d (%.1f%% of imports)\n", "unresolved", len(res.Unresolved), res.UnresolvedRate()*100)
	if len(res.Unresolved) > 0 {
		clusters := scan.ClusterUnresolved(res.Unresolved)
		// Grouped by shared cause rather than listed one by one. A repo that
		// generates files during its build can have thousands of unresolved
		// imports for a single reason, and a wall of "not found" lines reads
		// as a broken tool rather than as a fact about the repo.
		for i, c := range clusters {
			if i == 6 {
				fmt.Printf("      … and %d more groups\n", len(clusters)-6)
				break
			}
			fmt.Printf("      %5d  %-34s %s\n", c.Count, c.Prefix, c.Category)
		}
		if s := res.UnresolvedSummary(); s != "" {
			fmt.Printf("\n      %s\n", s)
		}
		fmt.Println("      run with --json for the full list")
	}

	if n := res.FromBuildOutput; n > 0 {
		fmt.Printf("\n  %-22s %d\n", "unbuilt output", n)
		fmt.Println("      imports name compiled files that do not exist yet; resolved to the")
		fmt.Println("      source they are built from, which is the same edge and an openable file")
	}

	if n := len(res.CaseMismatches); n > 0 {
		fmt.Printf("\n  %-22s %d\n", "case mismatches", n)
		fmt.Println("      these work on macOS and Windows and fail on Linux")
		for i, c := range res.CaseMismatches {
			if i == 5 {
				fmt.Printf("      … and %d more\n", n-5)
				break
			}
			fmt.Printf("      %s:%d  %s\n", c.File, c.Line, c.Reason)
		}
	}

	printPackages(res)

	if n := len(res.Unanalyzable); n > 0 {
		fmt.Printf("\n  %-22s %d\n", "computed imports", n)
		fmt.Println("      import() with a path built at runtime — cairn cannot follow these,")
		fmt.Println("      so dead-file results in this repo are reported with lower confidence")
		for i, u := range res.Unanalyzable {
			if i == 5 {
				break
			}
			fmt.Printf("      %s:%d  %s\n", u.File, u.Line, u.Reason)
		}
	}
}

func printPackages(res *scan.Result) {
	if res.Packages == nil {
		return
	}
	p := res.Packages
	fmt.Printf("\n  packages (read from %s)\n", p.Source)
	fmt.Printf("      %-20s %d\n", "declared", p.DeclaredCount())
	fmt.Printf("      %-20s %d\n", "in lockfile", len(p.Packages))
	if res.Disagree != nil {
		fmt.Printf("      %-20s %d\n", "on disk", res.Disagree.InstalledCount)
		if n := len(res.Disagree.LockedNotInstalled); n > 0 {
			fmt.Printf("      %-20s %d (optional or platform-specific)\n", "locked, not on disk", n)
		}
		if n := len(res.Disagree.InstalledNotLocked); n > 0 {
			fmt.Printf("      %-20s %d\n", "on disk, not locked", n)
		}
	}
	if res.Join == nil {
		return
	}
	if n := len(res.Join.ImportedNotDeclared); n > 0 {
		fmt.Printf("\n  imported but not in package.json  (%d)\n", n)
		fmt.Println("      these work by accident and will break for the next person")
		for _, name := range res.Join.ImportedNotDeclared {
			fmt.Printf("      %s\n", name)
		}
	}
	if n := len(res.Join.UnusedDeclared); n > 0 {
		fmt.Printf("\n  declared but never imported  (%d)\n", n)
		fmt.Println("      a hint, not proof — config files and plugins load packages by name")
		for i, name := range res.Join.UnusedDeclared {
			if i == 8 {
				fmt.Printf("      … and %d more\n", n-8)
				break
			}
			fmt.Printf("      %s\n", name)
		}
	}
}

// ── blast ───────────────────────────────────────────────────────────────────

func runBlast(res *scan.Result, target string, asJSON bool) error {
	id := graph.NodeID(graph.File, strings.TrimPrefix(target, "./"))
	if _, ok := res.Graph.Nodes[id]; !ok {
		return fmt.Errorf("%s is not a file in this repo (paths are relative to the repo root)", target)
	}

	b := query.BlastRadius(res.Graph, id)
	if asJSON {
		return emit(map[string]any{
			"file":     short(id),
			"affected": namesOf(b.Affected),
			"direct":   shortAll(b.Direct),
			"count":    len(b.Affected),
		})
	}

	fmt.Printf("%s\n\n", short(id))
	if len(b.Affected) == 0 {
		fmt.Println("  nothing imports this file")
		fmt.Println("  changing it is safe — or it is dead. try `cairn dead`.")
		return nil
	}
	verb := "depend"
	if len(b.Affected) == 1 {
		verb = "depends"
	}
	fmt.Printf("  %s %s on this\n\n", plural(len(b.Affected), "file"), verb)
	byDepth := map[int][]string{}
	maxDepth := 0
	for id, d := range b.Affected {
		byDepth[d] = append(byDepth[d], short(id))
		if d > maxDepth {
			maxDepth = d
		}
	}
	for d := 1; d <= maxDepth; d++ {
		names := byDepth[d]
		if len(names) == 0 {
			continue
		}
		label := fmt.Sprintf("%d hop", d)
		if d > 1 {
			label += "s"
		}
		fmt.Printf("  %-8s away\n", label)
		for _, n := range sorted(names) {
			fmt.Printf("      %s\n", n)
		}
	}
	return nil
}

// ── dead ────────────────────────────────────────────────────────────────────

func runDead(res *scan.Result, asJSON bool) error {
	rep := query.DeadFilesWith(res.Graph, res.ManifestEntries, len(res.Unanalyzable) > 0)
	dead, entries := rep.Files, rep.Entries

	if asJSON {
		list := make([]map[string]string, 0, len(dead))
		for _, d := range dead {
			list = append(list, map[string]string{"file": short(d.File), "why": d.Why})
		}
		return emit(map[string]any{
			"dead": list, "entry_points": len(entries), "count": len(dead), "bail": rep.Bail,
		})
	}

	fmt.Printf("%s\n\n", res.Root)
	if rep.Bail != "" {
		fmt.Println("  cannot answer")
		fmt.Printf("      %s\n", rep.Bail)
		return nil
	}
	fmt.Printf("  %d entry points found\n", len(entries))
	for i, e := range entries {
		if i == 6 {
			fmt.Printf("      … and %d more\n", len(entries)-6)
			break
		}
		fmt.Printf("      %-34s %s\n", short(e.File), e.Reason)
	}

	if len(dead) == 0 {
		fmt.Println("\n  every file is reachable from an entry point")
		return nil
	}
	fmt.Printf("\n  %d files nothing reaches\n", len(dead))
	for _, d := range dead {
		fmt.Printf("      %s\n", short(d.File))
	}
	fmt.Printf("\n  %s\n", dead[0].Why)
	fmt.Println("  check before deleting — a file loaded by name at runtime looks identical to a dead one")
	return nil
}

// ── why ─────────────────────────────────────────────────────────────────────

func runWhy(res *scan.Result, pkg string, asJSON bool) error {
	var declared []string
	if res.Packages != nil {
		for name := range res.Packages.Declared {
			declared = append(declared, name)
		}
		for name := range res.Packages.DeclaredDev {
			declared = append(declared, name)
		}
		declared = sorted(declared)
	}

	w := query.WhyPackage(res.Graph, pkg, declared)
	if w == nil {
		return fmt.Errorf("%s is not in this repo's dependency graph", pkg)
	}
	if asJSON {
		return emit(map[string]any{"package": pkg, "path": shortAll(w.Path), "direct": w.Direct, "from": w.From})
	}

	if len(w.Path) == 0 {
		fmt.Printf("%s is installed, but nothing in this repo reaches it.\n", pkg)
		fmt.Println("it may be a transitive dependency of something not in the lockfile, or genuinely orphaned.")
		return nil
	}
	switch {
	case w.Direct:
		fmt.Printf("%s is imported directly by your code.\n\n", pkg)
	case w.From == "package.json":
		fmt.Printf("%s arrives through %s. Nothing in your code imports it —\n", pkg, plural(len(w.Path)-1, "hop"))
		fmt.Printf("it comes in behind a package you declared.\n\n")
	default:
		fmt.Printf("%s arrives through %s.\n\n", pkg, plural(len(w.Path)-1, "hop"))
	}
	for i, id := range w.Path {
		prefix := "  "
		if i > 0 {
			prefix = "  " + strings.Repeat("  ", i) + "└─ "
		}
		fmt.Printf("%s%s\n", prefix, short(id))
	}
	return nil
}

// ── cycles ──────────────────────────────────────────────────────────────────

func runCycles(res *scan.Result, asJSON bool) error {
	cycles := query.Cycles(res.Graph, query.AllEdges)
	if asJSON {
		list := make([][]string, 0, len(cycles))
		for _, c := range cycles {
			list = append(list, shortAll(c.Nodes))
		}
		return emit(map[string]any{"cycles": list, "count": len(cycles)})
	}

	if len(cycles) == 0 {
		fmt.Println("no import cycles")
		return nil
	}
	fmt.Printf("%d import cycles\n\n", len(cycles))
	fmt.Println("  JavaScript allows these and many are harmless. They matter when a")
	fmt.Println("  module reads a value from a partner at import time and gets undefined.")
	fmt.Println()
	for _, c := range cycles {
		fmt.Printf("  %d files\n", len(c.Nodes))
		for _, n := range c.Nodes {
			fmt.Printf("      %s\n", short(n))
		}
		fmt.Println()
	}
	return nil
}

// ── cost ────────────────────────────────────────────────────────────────────

func runCost(res *scan.Result, pkg string, asJSON bool) error {
	c := query.PackageCost(res.Graph, pkg)
	if c == nil {
		return fmt.Errorf("%s is not in this repo's dependency graph", pkg)
	}
	if asJSON {
		return emit(map[string]any{
			"package": pkg, "packages": shortAll(c.Packages),
			"package_count": len(c.Packages), "bytes": c.Bytes, "measured": c.Measured,
		})
	}

	fmt.Printf("%s\n\n", pkg)
	fmt.Printf("  %-22s %d\n", "packages pulled in", len(c.Packages))
	if c.Measured {
		fmt.Printf("  %-22s %s\n", "installed size", humanBytes(c.Bytes))
	} else {
		fmt.Printf("  %-22s %s\n", "installed size", "not measured (node_modules missing?)")
	}
	fmt.Println()
	for i, p := range c.Packages {
		if i == 20 {
			fmt.Printf("      … and %d more\n", len(c.Packages)-20)
			break
		}
		n := res.Graph.Nodes[p]
		if n.Bytes > 0 {
			fmt.Printf("      %-40s %s\n", short(p), humanBytes(n.Bytes))
		} else {
			fmt.Printf("      %s\n", short(p))
		}
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

func namesOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, short(id))
	}
	return sorted(out)
}

func shortAll(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, short(id))
	}
	return out
}

func keysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ── verify ──────────────────────────────────────────────────────────────────

func runVerify(root string, asJSON bool) error {
	rep, err := verify.Run(root)
	if err != nil {
		return err
	}
	if !rep.Available {
		return fmt.Errorf("cannot verify: %s (verification needs node and a typescript install in the repo)", rep.Reason)
	}

	if asJSON {
		return emit(map[string]any{
			"ts_version": rep.TSVersion,
			"compared":   rep.Compared,
			"agreed":     rep.Agreed,
			"precision":  rep.Precision(),
			"recall":     rep.Recall(),
			"wrong":      len(rep.Wrong),
			"missed":     len(rep.CairnMissed),
			"extra":      len(rep.CairnExtra),
			"explained":  len(rep.Explained),
			"rules":      rep.RulesExercised,
			// The disagreements themselves, not only how many there were.
			// A score with no way to see what produced it can be read but
			// not acted on, and the terminal view stops at eight.
			"disagreements": map[string]any{
				"wrong":  rep.Wrong,
				"missed": rep.CairnMissed,
				"extra":  rep.CairnExtra,
			},
		})
	}

	fmt.Printf("%s\n\n", root)
	fmt.Printf("  checked against TypeScript %s, specifier by specifier\n\n", rep.TSVersion)
	fmt.Printf("  %-22s %d\n", "imports compared", rep.Compared)
	fmt.Printf("  %-22s %d\n", "agreed", rep.Agreed)
	fmt.Printf("  %-22s %.2f%%\n", "precision", rep.Precision()*100)
	fmt.Printf("  %-22s %.2f%%\n", "recall", rep.Recall()*100)

	section := func(title string, list []verify.Disagreement, note string) {
		if len(list) == 0 {
			return
		}
		fmt.Printf("\n  %s (%d)\n", title, len(list))
		if note != "" {
			fmt.Printf("      %s\n", note)
		}
		for i, d := range list {
			if i == 8 {
				fmt.Printf("      … and %d more\n", len(list)-8)
				break
			}
			fmt.Printf("      %s  %q\n          cairn: %s\n          tsc:   %s\n",
				d.File, d.Specifier, d.Cairn, d.Oracle)
		}
	}

	if len(rep.RulesExercised) > 0 {
		fmt.Printf("\n  rules this repo actually exercised\n")
		fmt.Println("      a perfect score only covers the rules that ran")
		for _, rule := range sorted(keysOf(rep.RulesExercised)) {
			fmt.Printf("      %-20s %d\n", rule, rep.RulesExercised[rule])
		}
	}

	section("resolved differently", rep.Wrong, "these are real bugs")
	section("imports cairn did not find", rep.CairnMissed, "holes in the parser")
	section("imports TypeScript did not report", rep.CairnExtra,
		"usually require() calls, which its preprocessor treats differently")

	if n := len(rep.Explained); n > 0 {
		fmt.Printf("\n  known differences (%d)\n", n)
		fmt.Println("      counted as agreement, listed so the number is not hidden")
		for i, d := range rep.Explained {
			if i == 4 {
				fmt.Printf("      … and %d more\n", n-4)
				break
			}
			fmt.Printf("      %s  %q — %s\n", d.File, d.Specifier, d.Class)
		}
	}
	return nil
}

// ── serve / export ──────────────────────────────────────────────────────────

func serveGraph(res *scan.Result, addr string, withPackages bool) error {
	p := web.Build(res, withPackages)
	describeView(p)
	return web.Serve(p, addr)
}

func exportGraph(res *scan.Result, path string, withPackages bool) error {
	p := web.Build(res, withPackages)
	if err := web.Export(p, path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	describeView(p)
	fmt.Printf("\nwrote %s (%s)\n", path, humanBytes(info.Size()))
	fmt.Println("one file, no server, no dependencies — send it to anyone")
	return nil
}

func describeView(p *web.Payload) {
	fmt.Printf("%s\n\n", p.Root)
	fmt.Printf("  %-22s %d\n", "nodes drawn", len(p.Nodes))
	fmt.Printf("  %-22s %d\n", "edges drawn", len(p.Edges))
	if p.Truncated > 0 {
		fmt.Printf("  %-22s %d (kept the most-depended-on)\n", "not drawn", p.Truncated)
	}
}

// ── affected ────────────────────────────────────────────────────────────────

func runAffected(root string, res *scan.Result, base string, asJSON bool) error {
	changed, err := affected.ChangedFiles(root, base)
	if err != nil {
		return fmt.Errorf("asking git what changed: %w", err)
	}
	a := affected.Compute(res, changed)

	if asJSON {
		return emit(map[string]any{
			"changed": a.Changed, "unknown": a.Unknown,
			"affected": a.Affected, "tests": a.Tests, "entries": a.Entries,
			"total_files": a.TotalFiles, "total_tests": a.TotalTests,
			"reduction": a.Reduction(), "test_reduction": a.TestReduction(),
			"bail": a.Bail, "safe": a.Bail == "",
		})
	}

	fmt.Printf("%s\n\n", root)
	if len(a.Changed) == 0 {
		fmt.Println("  nothing changed against " + base)
		return nil
	}

	fmt.Printf("  %s changed\n", plural(len(a.Changed), "file"))
	for i, f := range a.Changed {
		if i == 6 {
			fmt.Printf("      … and %d more\n", len(a.Changed)-6)
			break
		}
		fmt.Printf("      %s\n", f)
	}

	if a.Bail != "" {
		fmt.Printf("\n  run everything\n")
		fmt.Printf("      %s\n", a.Bail)
		fmt.Println("      a test that should have run and did not is worse than a slow build")
		return nil
	}

	fmt.Printf("\n  %d of %d files affected  (%.0f%% of the repo untouched)\n",
		len(a.Affected), a.TotalFiles, a.Reduction()*100)

	if a.TotalTests > 0 {
		fmt.Printf("\n  %d of %d tests need to run  (%.0f%% skippable)\n",
			len(a.Tests), a.TotalTests, a.TestReduction()*100)
		for _, t := range a.Tests {
			fmt.Printf("      %s\n", t)
		}
	}
	if len(a.Entries) > 0 {
		fmt.Printf("\n  %s reached\n", plural(len(a.Entries), "entry point"))
		for i, e := range a.Entries {
			if i == 6 {
				fmt.Printf("      … and %d more\n", len(a.Entries)-6)
				break
			}
			fmt.Printf("      %s\n", e)
		}
	}

	fmt.Println("\n  soundness: this follows import edges only. Tests that share a database,")
	fmt.Println("  a fixture file, or global state are coupled in ways no import graph sees.")
	return nil
}

// runContext answers "I am about to change this file, what else must I read".
//
// The plain output is deliberately shaped for a terminal and the --json for a
// program, because both readers are real: a person checking whether the answer
// is sane, and an agent consuming it.
func runContext(res *scan.Result, target string, budget int, asJSON bool) error {
	mm := modules.Build(res)
	c, err := agent.Build(res, mm, target, agent.Options{Budget: budget})
	if err != nil {
		return err
	}
	if asJSON {
		return emit(c)
	}

	fmt.Printf("%s\n", c.Target)
	if c.Module != "" {
		fmt.Printf("in %s · %d files depend on it\n", c.Module, c.Blast)
	} else {
		fmt.Printf("%d files depend on it\n", c.Blast)
	}
	fmt.Println()
	fmt.Printf("read these %d files (~%s tokens)\n", len(c.Read), thousands(c.Tokens))
	for _, f := range c.Read {
		fmt.Printf("  %-52s %s\n", f.Path, f.Why)
	}
	if c.Omitted > 0 {
		fmt.Printf("\n%d more related files did not fit in the budget (~%s tokens)\n",
			c.Omitted, thousands(c.OmittedTokens))
	}
	// Against the number of files in the graph, not the number scanned: those
	// differ, and quoting the wrong one produced "scope: 888 files, not 668".
	total := 0
	for _, n := range res.Graph.Nodes {
		if n.Kind == graph.File {
			total++
		}
	}
	fmt.Printf("\nsearch scope: %d of %d files — narrow grep to these\n", len(c.Scope), total)
	fmt.Printf("  cairn scope %s | xargs rg <pattern>\n", c.Target)
	if c.Warning != "" {
		fmt.Printf("\nnote: %s\n", c.Warning)
	}
	return nil
}

// runScope prints the files a search could possibly need to look at.
//
// This is the half that makes the graph and grep work together rather than
// compete. grep is excellent at finding words and has no idea which files
// matter; the graph knows exactly which files could be involved and nothing
// about words. Piping one into the other gives each the thing it lacks.
func runScope(res *scan.Result, target string, asJSON bool) error {
	mm := modules.Build(res)
	c, err := agent.Build(res, mm, target, agent.Options{})
	if err != nil {
		return err
	}
	if asJSON {
		total := 0
		for _, n := range res.Graph.Nodes {
			if n.Kind == graph.File {
				total++
			}
		}
		return emit(map[string]any{
			"target": c.Target, "scope": c.Scope,
			"files": len(c.Scope), "of": total,
		})
	}
	for _, p := range c.Scope {
		fmt.Println(p)
	}
	return nil
}

func thousands(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.Itoa(n/1000) + "k"
}

// runDrift reports what a change did to the shape of the program.
func runDrift(root, base string, asJSON bool) error {
	rep, err := drift.Compare(root, base)
	if err != nil {
		return err
	}
	if asJSON {
		if err := emit(rep); err != nil {
			return err
		}
	} else {
		printDrift(rep)
	}
	// A non-zero exit is what makes this usable as a gate. Only regressions
	// count: a tool that fails a build because something got better would be
	// turned off within a day.
	if rep.Regressions > 0 {
		os.Exit(1)
	}
	return nil
}

func printDrift(r *drift.Report) {
	if r.Bail != "" {
		fmt.Println(r.Bail)
		return
	}
	fmt.Printf("%s → working tree · %d files changed\n\n", r.Base, r.FilesChanged)
	if len(r.Findings) == 0 {
		fmt.Println("nothing changed about the shape of the program.")
	}
	for _, f := range r.Findings {
		mark := "  •"
		switch f.Severity {
		case drift.Regression:
			mark = "  ✗"
		case drift.Improvement:
			mark = "  ✓"
		}
		fmt.Printf("%s %s\n", mark, f.Title)
		fmt.Printf("     %s\n", f.Detail)
		for i, it := range f.Items {
			if i >= 6 {
				fmt.Printf("     … and %d more\n", len(f.Items)-6)
				break
			}
			fmt.Printf("     %s\n", it)
		}
		fmt.Println()
	}
	fmt.Printf("coupling: a change reaches %.0f%% of the repository (was %.0f%%), median over %d files\n",
		r.Coupling.Head*100, r.Coupling.Base*100, r.Coupling.Sampled)
	if r.Regressions > 0 {
		fmt.Printf("\n%s\n", plural(r.Regressions, "regression"))
	}
}

// runGrep searches, ordered by the dependency graph.
func runGrep(res *scan.Result, pattern, anchor string, connected, ignoreCase bool, asJSON bool) error {
	r, err := agent.Grep(res, pattern, agent.SearchOptions{
		Anchor: anchor, ConnectedOnly: connected, IgnoreCase: ignoreCase,
	})
	if err != nil {
		return err
	}
	if asJSON {
		return emit(r)
	}

	if anchor == "" {
		for _, h := range r.Hits {
			fmt.Printf("%s:%d: %s\n", h.Path, h.Line, h.Text)
		}
		fmt.Printf("\n%d matches in %d files. Pass --from <file> to order them by what is "+
			"actually connected to it.\n", len(r.Hits), r.Searched)
		return nil
	}

	shown := ""
	for _, h := range r.Hits {
		// A rule between the connected matches and the rest, because that is
		// the line a reader needs: above it, files that can reach the anchor;
		// below it, files that merely share a word.
		if h.Hops < 0 && shown != "unrelated" {
			shown = "unrelated"
			fmt.Printf("\n— not connected to %s —\n\n", anchor)
		} else if h.Hops >= 0 && shown == "" {
			shown = "connected"
		}
		tag := "·"
		switch {
		case h.Hops == 0:
			tag = "the file itself"
		case h.Hops > 0 && h.Direction == "upstream":
			tag = plural(h.Hops, "hop") + " up — breaks if you change it"
		case h.Hops > 0:
			tag = plural(h.Hops, "hop") + " down — the target uses it"
		}
		fmt.Printf("%s:%d  %s\n", h.Path, h.Line, tag)
		fmt.Printf("    %s\n", h.Text)
		if len(h.Via) > 2 {
			fmt.Printf("    via %s\n", strings.Join(h.Via, " → "))
		}
	}

	fmt.Printf("\n%d connected, %d unrelated, out of %d files searched (%d in the repo)\n",
		r.Connected, r.Unrelated, r.Searched, r.OfFiles)
	return nil
}

// runLadder shows where a file sits: two levels up, two levels down.
func runLadder(res *scan.Result, file string, asJSON bool) error {
	mm := modules.Build(res)
	l, err := agent.BuildLadder(res, mm, file)
	if err != nil {
		return err
	}
	if asJSON {
		return emit(l)
	}

	name := file
	if i := strings.LastIndexByte(file, '/'); i >= 0 {
		name = file[i+1:]
	}
	fmt.Printf("\n  %s", name)
	if l.Module != "" {
		fmt.Printf("   in %s", l.Module)
	}
	fmt.Println()
	fmt.Println()

	rung := func(lv agent.Level, mark, label string) {
		fmt.Printf("  %-4s %4d   %s\n", mark, lv.Count, label)
		for i, f := range lv.Files {
			if i >= 4 {
				fmt.Printf("            … and %d more\n", lv.Count-4)
				break
			}
			fmt.Printf("            %s\n", f)
		}
		if lv.Count > 4 && len(lv.Modules) > 1 {
			fmt.Printf("            across %s\n", strings.Join(lv.Modules, ", "))
		}
	}

	// Up first, printed above the file, because that is where it sits: the
	// things standing on it are drawn over it, the things it stands on below.
	rung(l.Up[1], "▲▲", "two levels up — who needs the things that need this")
	rung(l.Up[0], "▲", "one level up — who needs this directly")
	fmt.Printf("\n  ●         %s\n\n", file)
	rung(l.Down[0], "▼", "one level down — what this stands on")
	rung(l.Down[1], "▼▼", "two levels down — what those stand on")

	fmt.Printf("\n  %s\n", l.Verdict)
	fmt.Printf("  %d files above it in total, %d below.\n\n", l.Reach, l.Depends)
	return nil
}
