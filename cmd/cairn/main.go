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
	"strings"

	"github.com/MihaiPosea/cairn/internal/scan"
)

const usage = `cairn — see what your project actually depends on

usage:
  cairn scan [dir]              build the graph and summarise it
  cairn blast <file>            what breaks if you change this file
  cairn dead                    files nothing reaches from an entry point
  cairn why <package>           the path that dragged this package in
  cairn cycles                  import cycles, as readable chains
  cairn cost <package>          packages and bytes this one import pulls in
  cairn verify                  check cairn's graph against TypeScript's own resolver
  cairn serve                   open the graph in a browser
  cairn export <file.html>      write a standalone page you can send someone

flags:
  --dir <path>                  repo to scan (default: .)
  --json                        machine-readable output
  --sizes                       measure installed package sizes (walks node_modules)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cairn: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}

	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	dir := fs.String("dir", ".", "repo to scan")
	asJSON := fs.Bool("json", false, "machine-readable output")
	sizes := fs.Bool("sizes", false, "measure installed package sizes")
	addr := fs.String("addr", "localhost:7777", "address for `cairn serve`")
	withPkgs := fs.Bool("packages", false, "include packages in the graph view")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	rest := fs.Args()

	// `cairn scan ~/repo` is the shape people expect, so a positional argument
	// to scan is also treated as the directory.
	if cmd == "scan" && len(rest) > 0 {
		*dir = rest[0]
		rest = rest[1:]
	}

	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}

	needsArg := func() (string, error) {
		if len(rest) == 0 {
			return "", fmt.Errorf("%s needs an argument — see `cairn help`", cmd)
		}
		return rest[0], nil
	}

	switch cmd {
	case "scan":
		return withScan(root, *sizes, func(res *scan.Result) error {
			if *asJSON {
				return emit(summary(res))
			}
			printSummary(res)
			return nil
		})

	case "blast":
		target, err := needsArg()
		if err != nil {
			return err
		}
		return withScan(root, false, func(res *scan.Result) error {
			return runBlast(res, target, *asJSON)
		})

	case "dead":
		return withScan(root, false, func(res *scan.Result) error {
			return runDead(res, *asJSON)
		})

	case "why":
		target, err := needsArg()
		if err != nil {
			return err
		}
		return withScan(root, false, func(res *scan.Result) error {
			return runWhy(res, target, *asJSON)
		})

	case "cycles":
		return withScan(root, false, func(res *scan.Result) error {
			return runCycles(res, *asJSON)
		})

	case "verify":
		return runVerify(root, *asJSON)

	case "serve":
		return withScan(root, *sizes, func(res *scan.Result) error {
			return serveGraph(res, *addr, *withPkgs)
		})

	case "export":
		target, err := needsArg()
		if err != nil {
			return err
		}
		return withScan(root, *sizes, func(res *scan.Result) error {
			return exportGraph(res, target, *withPkgs)
		})

	case "cost":
		target, err := needsArg()
		if err != nil {
			return err
		}
		// Cost without sizes is only half an answer, so it opts in by default.
		return withScan(root, true, func(res *scan.Result) error {
			return runCost(res, target, *asJSON)
		})

	default:
		return fmt.Errorf("unknown command %q — run `cairn help`", cmd)
	}
}

func withScan(root string, sizes bool, fn func(*scan.Result) error) error {
	res, err := scan.RunWith(root, scan.Options{MeasureSizes: sizes})
	if err != nil {
		return err
	}
	return fn(res)
}

func emit(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// short strips the "file:" / "pkg:" prefix for display.
func short(id string) string {
	if i := strings.Index(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGT"[exp])
}
