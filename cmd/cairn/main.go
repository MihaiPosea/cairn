// Command cairn reads a repository and shows you what it actually depends on.
//
// It builds one graph with two halves joined: the files in your repo importing
// each other, and the packages those imports drag in. See DESIGN.md for the
// decisions behind it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MihaiPosea/cairn/internal/graph"
	"github.com/MihaiPosea/cairn/internal/scan"
)

const usage = `cairn — see what your project actually depends on

usage:
  cairn scan [dir]              build the graph and summarise it
  cairn blast <file>            what breaks if you change this file
  cairn dead                    files nothing reaches from an entry point
  cairn why <package>           the path that dragged this package in
  cairn cycles                  import cycles, as readable chains
  cairn cost <specifier>        packages and bytes this one import pulls in

flags:
  --json                        machine-readable output
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cairn: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	fs := flag.NewFlagSet("cairn", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	sizes := fs.Bool("sizes", false, "measure installed package sizes (walks node_modules)")
	cmd := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	rest := fs.Args()

	switch cmd {
	case "scan":
		dir := "."
		if len(rest) > 0 {
			dir = rest[0]
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		return scan_(abs, *asJSON, *sizes)

	case "blast", "dead", "why", "cycles", "cost":
		return fmt.Errorf("%s: not implemented yet (milestone M4)", cmd)

	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil

	default:
		return fmt.Errorf("unknown command %q — run `cairn help`", cmd)
	}
}

// scan walks the repo, parses what it finds, resolves the imports, and prints
// a summary of the resulting graph.
func scan_(dir string, asJSON, sizes bool) error {
	res, err := scan.RunWith(dir, scan.Options{MeasureSizes: sizes})
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(summary(res))
	}
	printSummary(res)
	return nil
}

type summaryOut struct {
	Root           string  `json:"root"`
	Files          int     `json:"files_scanned"`
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
		Files:          res.FilesScanned,
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
	fmt.Printf("  %-22s %d\n", "packages referenced", s[graph.Package])
	fmt.Printf("  %-22s %d\n", "runtime builtins", s[graph.Builtin])

	if res.AliasesLoaded {
		fmt.Printf("  %-22s %s\n", "tsconfig aliases", "loaded")
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
		fmt.Printf("      %s:%d  %s\n", u.File, u.Line, u.Specifier)
	}

	printPackages(res)

	if n := len(res.Unanalyzable); n > 0 {
		fmt.Printf("\n  %-22s %d (import() with a computed path)\n", "unanalyzable", n)
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
	fmt.Printf("\n  packages (%s)\n", p.Source)
	fmt.Printf("      %-18s %d\n", "declared", p.DeclaredCount())
	fmt.Printf("      %-18s %d\n", "in lockfile", len(p.Packages))
	if res.Disagree != nil {
		fmt.Printf("      %-18s %d\n", "on disk", res.Disagree.InstalledCount)
		if n := len(res.Disagree.LockedNotInstalled); n > 0 {
			fmt.Printf("      %-18s %d (optional or platform-specific)\n", "locked, not on disk", n)
		}
		if n := len(res.Disagree.InstalledNotLocked); n > 0 {
			fmt.Printf("      %-18s %d\n", "on disk, not locked", n)
		}
	}

	if res.Join == nil {
		return
	}
	if n := len(res.Join.ImportedNotDeclared); n > 0 {
		fmt.Printf("\n  imported but not declared in package.json  (%d)\n", n)
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
