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

	"github.com/MihaiPosea/cairn/internal/mcp"
	"github.com/MihaiPosea/cairn/internal/scan"
)

const usage = `cairn - see what your project actually depends on

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
  cairn mcp                     serve the graph to a coding agent over stdio
  cairn ladder <file>           where this file sits: two levels up, two down
  cairn grep <pat> --from <f>   search, ordered by what is connected to <f>
  cairn drift --base main       what this change did to the architecture
  cairn context <file>          what to read before changing this file
  cairn scope <file>            the files a search must cover - pipe into grep
  cairn affected [--base ref]   what needs re-running after your changes

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

// splitArgs separates flags from positional arguments so flags are accepted
// anywhere on the line. takesValue names the flags whose value is a separate
// token; a bool flag must not swallow the argument after it.
func splitArgs(args []string, takesValue map[string]bool) (flags, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.ContainsRune(name, '=') {
			continue
		}
		if takesValue[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positional
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
	from := fs.String("from", "", "anchor file for `cairn grep` - results are ordered by distance from it")
	connected := fs.Bool("connected", false, "`cairn grep`: drop matches the graph cannot connect to --from")
	ignoreCase := fs.Bool("i", false, "case-insensitive search")
	budget := fs.Int("budget", 0, "token budget for `cairn context`")
	base := fs.String("base", "origin/main", "git ref to compare against for `cairn affected`")
	// Go's flag package stops parsing at the first non-flag argument, so
	// `cairn export graph.html --dir ~/repo` silently dropped --dir and
	// exported the current directory instead. Four different repositories
	// exported the same 40,547 bytes and nothing said a word. Every command
	// here takes both a target and a --dir, so that shape is the normal one.
	flags, rest := splitArgs(args[1:], map[string]bool{"dir": true, "addr": true, "base": true, "budget": true, "from": true})
	if err := fs.Parse(append(flags, rest...)); err != nil {
		return err
	}
	rest = fs.Args()

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
			return "", fmt.Errorf("%s needs an argument - see `cairn help`", cmd)
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

	case "affected":
		return withScan(root, false, func(res *scan.Result) error {
			return runAffected(root, res, *base, *asJSON)
		})

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

	case "mcp":
		srv, err := mcp.New(root)
		if err != nil {
			return err
		}
		return srv.Serve()

	case "ladder":
		target, err := needsArg()
		if err != nil {
			return err
		}
		return withScan(root, false, func(res *scan.Result) error {
			return runLadder(res, target, *asJSON)
		})

	case "grep":
		pattern, err := needsArg()
		if err != nil {
			return err
		}
		return withScan(root, false, func(res *scan.Result) error {
			return runGrep(res, pattern, *from, *connected, *ignoreCase, *asJSON)
		})

	case "drift":
		return runDrift(root, *base, *asJSON)

	case "context":
		target, err := needsArg()
		if err != nil {
			return err
		}
		return withScan(root, false, func(res *scan.Result) error {
			return runContext(res, target, *budget, *asJSON)
		})

	case "scope":
		target, err := needsArg()
		if err != nil {
			return err
		}
		return withScan(root, false, func(res *scan.Result) error {
			return runScope(res, target, *asJSON)
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
		return fmt.Errorf("unknown command %q - run `cairn help`", cmd)
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
