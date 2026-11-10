// file: scripts/r2corpus/shaped/main.go
// version: 1.0.0
// guid: 09051bcd-d562-402f-877c-d91939009221
// last-edited: 2026-11-10

//go:build r2corpus_shaped

// Command shaped grows a copy of F2-12's stage-1 store to the production
// counts and sizes the owner recorded as P1 (F2-14; docs/specs/platform-core/
// 09-abo-on-core.md §9.3 item 1 "production-shaped store", §9.7 P1).
//
// Python cannot write Pebble rows and R0's API is too slow to reach production
// counts (43.7 million `opv2:log:` rows), so scripts/r2corpus/gen_production_
// shaped.py drives this program. It is the row writer only: the generator has
// already verified the stage-1 store against manifest.json, copied it, and
// afterwards hashes the result into store_manifest.json.
//
// Unlike F2-13's overlay program it needs no R0 checkout: it writes no row
// through any store code. Every generated row is a stage-1 row (or, for a
// family stage 1 has none of, an F2-13 overlay row) of the same family with a
// fresh key and a value padded to the P1 average size, so it decodes as R0
// wrote it. The only package it imports from the repository is the key-family
// registry, so a key is counted under the same family the P1 census counted it
// under. The build constraint keeps every build and vet of main away from it.
//
// Usage:
//
//	shaped -mode generate -store <pebble dir> -counts p1_counts.json \
//	       [-scale 0.01] [-templates <overlay pebble dir>] [-report out.json]
//	shaped -mode scan -store <pebble dir> -counts p1_counts.json
//
// generate writes the rows into -store (a copy of stage 1; it is modified) and
// prints a JSON report of every family. scan reads a store and prints, per
// counts family, the keys, raw key bytes and raw value bytes it holds. The two
// are separate code paths on purpose: the generator's tests check what generate
// wrote with scan.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
)

func main() {
	mode := flag.String("mode", "", "generate | scan")
	storePath := flag.String("store", "", "Pebble directory (generate: modified in place)")
	countsPath := flag.String("counts", "", "p1_counts.json")
	scale := flag.Float64("scale", 1, "fraction of the P1 counts to generate (local runs; CI uses 1)")
	templatesPath := flag.String("templates", "", "Pebble directory of F2-13's overlay store, a template source for families stage 1 lacks")
	reportPath := flag.String("report", "", "write the JSON report here as well as to stdout")
	workers := flag.Int("workers", runtime.NumCPU(), "concurrent batch writers")
	flag.Parse()

	if err := run(*mode, *storePath, *countsPath, *scale, *templatesPath, *reportPath, *workers); err != nil {
		fmt.Fprintln(os.Stderr, "shaped:", err)
		os.Exit(1)
	}
}

func run(mode, storePath, countsPath string, scale float64, templatesPath, reportPath string, workers int) error {
	if storePath == "" || countsPath == "" {
		return fmt.Errorf("-store and -counts are required")
	}
	counts, err := loadCounts(countsPath)
	if err != nil {
		return err
	}
	var out any
	switch mode {
	case "scan":
		out, err = scanStore(storePath, counts)
	case "generate":
		if scale <= 0 {
			return fmt.Errorf("-scale %v: must be positive", scale)
		}
		out, err = generate(storePath, templatesPath, counts, scale, workers)
	default:
		return fmt.Errorf("-mode %q: want generate or scan", mode)
	}
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if reportPath != "" {
		if err := os.WriteFile(reportPath, raw, 0o644); err != nil {
			return err
		}
	}
	_, err = os.Stdout.Write(raw)
	return err
}
