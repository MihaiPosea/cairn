package main

import (
	"fmt"
	"strings"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/query"
	"github.com/MihaiPosea/cairn/internal/scan"
)

// ── scan ────────────────────────────────────────────────────────────────────

type summaryOut struct {
	Root           string  `json:"root"`
	FilesScanned   int     `json:"files_scanned"`
	FileNodes      int     `json:"file_nodes"`
	Imports        int     `json:"imports"`
	FileEdges      int     `json:"file_edges"`
	Packages       int     `json:"packages"`
	Builtins       int     `json:"builtins"`
	Unresolved     int     `json:"unresolved"`
	UnresolvedRate float64 `json:"unresolved_rate"`
	Unanalyzable   int     `json:"unanalyzable"`
	ParseFailures  int     `json:"parse_failures"`
	AliasesLoaded  bool    `json:"tsconfig_aliases_loaded"`
	CacheHits      int     `json:"cache_hits"`
	CacheMisses    int     `json:"cache_misses"`

	PackageSource       string   `json:"package_source,omitempty"`
	Declared            int      `json:"declared,omitempty"`
	Locked              int      `json:"locked,omitempty"`
	Installed           int      `json:"installed,omitempty"`
	UnusedDeclared      []string `json:"unused_declared,omitempty"`
	ImportedNotDeclared []string `json:"imported_not_declared,omitempty"`
}

func summary(res *scan.Result) summaryOut {
	s := res.Graph.Stats()
	out := summaryOut{
		Root:           res.Root,
		FilesScanned:   res.FilesScanned,
		FileNodes:      s[graph.File],
		Imports:        res.ImportsFound,
		FileEdges:      res.Graph.EdgeCount(graph.File),
		Packages:       s[graph.Package],
		Builtins:       s[graph.Builtin],
		Unresolved:     len(res.Unresolved),
		UnresolvedRate: res.UnresolvedRate(),
		Unanalyzable:   len(res.Unanalyzable),
		ParseFailures:  len(res.ParseFailures),
		AliasesLoaded:  res.AliasesLoaded,
		CacheHits:      res.CacheHits,
		CacheMisses:    res.CacheMisses,
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
	for i, u := range res.Unresolved {
		if i == 10 {
			fmt.Printf("      … and %d more\n", len(res.Unresolved)-10)
			break
		}
		fmt.Printf("      %s:%d  %s — %s\n", u.File, u.Line, u.Specifier, u.Reason)
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
	fmt.Printf("  %d files depend on this\n\n", len(b.Affected))
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
	dead := query.DeadFiles(res.Graph, len(res.Unanalyzable) > 0)
	entries := query.EntryPoints(res.Graph)

	if asJSON {
		list := make([]map[string]string, 0, len(dead))
		for _, d := range dead {
			list = append(list, map[string]string{"file": short(d.File), "why": d.Why})
		}
		return emit(map[string]any{"dead": list, "entry_points": len(entries), "count": len(dead)})
	}

	fmt.Printf("%s\n\n", res.Root)
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
