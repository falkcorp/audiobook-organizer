// file: tools/cmd/oplint/main.go
// version: 2.0.0
// guid: d0e1f2a3-b4c5-6789-3456-789abcdef012
// last-edited: 2026-10-16

// Command oplint checks ops v3 definitions with a type-aware go/analysis
// analyzer (analyzer.go; the rules are in Analyzer.Doc and sdk-api.md §11).
// It replaced, in 05-PR7, an import-path walker whose "walled garden" import
// rule reported 538 forbidden imports and which `make oplint` could not run
// (it lstat'ed ./internal/plugins/... as a path, finding F29).
//
// Usage:
//
//	go run ./tools/cmd/oplint [-probe-v2] [packages]   (default ./...)
//
// -probe-v2 also applies the writer-bypass rule to v2 registry.OperationDef
// Run funcs. It is informational (how much v2 code a port must move behind
// the Writer); make oplint never passes it.
//
// Every finding is printed as file:line:col: [category] message, then a
// count per category on stderr. The exit status is 1 when any finding other
// than a missing-help warning remains, 2 when the packages cannot be loaded
// or analyzed, and 0 otherwise.
//
// It drives the analyzer through go/analysis/checker rather than
// singlechecker because singlechecker fails on every diagnostic, and the
// missing-help rule is a warning.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [-probe-v2] [packages]   (default ./...)\n\n%s\n", os.Args[0], Analyzer.Doc)
	}
	Analyzer.Flags.VisitAll(func(f *flag.Flag) { flag.Var(f.Value, f.Name, f.Usage) })
	flag.Parse()
	os.Exit(run(flag.Args(), os.Stdout, os.Stderr))
}

// warnings are the categories that do not fail the run.
var warnings = map[string]bool{catMissingHelp: true}

func run(patterns []string, stdout, stderr io.Writer) int {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	cfg := &packages.Config{Mode: packages.LoadSyntax}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(stderr, "oplint: load: %v\n", err)
		return 2
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		fmt.Fprintf(stderr, "oplint: %d package error(s)\n", n)
		return 2
	}
	graph, err := checker.Analyze([]*analysis.Analyzer{Analyzer}, pkgs, nil)
	if err != nil {
		fmt.Fprintf(stderr, "oplint: %v\n", err)
		return 2
	}
	type finding struct {
		pos, cat, msg string
	}
	var found []finding
	counts := map[string]int{}
	for _, act := range graph.Roots {
		if act.Err != nil {
			fmt.Fprintf(stderr, "oplint: %s: %v\n", act.Package.PkgPath, act.Err)
			return 2
		}
		for _, d := range act.Diagnostics {
			found = append(found, finding{act.Package.Fset.Position(d.Pos).String(), d.Category, d.Message})
			counts[d.Category]++
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	failing := 0
	for _, f := range found {
		fmt.Fprintf(stdout, "%s: [%s] %s\n", f.pos, f.cat, f.msg)
		if !warnings[f.cat] {
			failing++
		}
	}
	cats := make([]string, 0, len(counts))
	for c := range counts {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	fmt.Fprintf(stderr, "oplint: %d package(s), %d finding(s), %d failing\n", len(pkgs), len(found), failing)
	for _, c := range cats {
		fmt.Fprintf(stderr, "oplint:   %s: %d\n", c, counts[c])
	}
	if failing > 0 {
		return 1
	}
	return 0
}
