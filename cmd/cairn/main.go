// Command cairn reads a repository and shows you what it actually depends on.
//
// It builds one graph with two halves joined: the files in your repo importing
// each other, and the packages those imports drag in. See DESIGN.md for the
// decisions behind it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
		return scan(abs, *asJSON)

	case "blast", "dead", "why", "cycles", "cost":
		return fmt.Errorf("%s: not implemented yet (milestone M4)", cmd)

	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil

	default:
		return fmt.Errorf("unknown command %q — run `cairn help`", cmd)
	}
}

// scan walks the repo, parses what it finds, resolves the imports, and prints a
// summary of the resulting graph.
//
// M1 fills this in. For now it exists so the wiring is real and every later
// milestone has somewhere to land.
func scan(dir string, asJSON bool) error {
	return fmt.Errorf("scan %s: not implemented yet (milestone M1)", dir)
}
