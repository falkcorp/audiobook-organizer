// file: cmd/pid-census/main.go
// version: 1.0.0
// guid: 8f2b0d61-4a37-4c95-9e12-7d3a6b1c0e58
// last-edited: 2026-07-23
//
// READ-ONLY book_file iTunes-PID integrity census. Point it at a COPY of the
// production Pebble DB (never the live dir — Pebble opens read-write and wants the
// LOCK): ZFS-snapshot, clone, cp the pebble dir to a writable jdfalk-owned path,
// then run this. Optionally pass a copy of the writeback .itl to mark which
// duplicate PIDs are present in the library. Nothing here mutates the DB beyond
// Pebble's own open (on the throwaway copy). See
// docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md.
//
//	go run ./cmd/pid-census --db /tmp/pebble-copy [--itl /tmp/iTunes\ Library.itl]

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

func main() {
	dbPath := flag.String("db", "", "path to a COPY of the Pebble DB directory (required)")
	itlPath := flag.String("itl", "", "optional path to a copy of the writeback iTunes Library.itl")
	full := flag.Bool("full", false, "print the full JSON report incl. samples (default: summary only)")
	repair := flag.Bool("repair", false, "also print the pid-repair PLAN preview (read-only; needs --itl for diff_file)")
	mapFrom := flag.String("map-from", "W:", "path-mapping source prefix (Windows drive)")
	mapTo := flag.String("map-to", "/mnt/bigdata/books", "path-mapping target prefix (local mount)")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "error: --db is required (a COPY of the Pebble dir, not the live one)")
		os.Exit(2)
	}

	store, err := database.NewPebbleStore(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open pebble %q: %v\n", *dbPath, err)
		os.Exit(1)
	}
	defer store.Close()

	report, err := itunes.ComputePIDIntegrity(store, *itlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "census: %v\n", err)
		os.Exit(1)
	}

	// Summary line (always) — the decision-relevant counts.
	fmt.Printf("tracks_in_itl=%d files_with_pid=%d distinct_pids=%d duplicate_pids=%d\n",
		report.TracksInITL, report.FilesWithPID, report.DistinctPIDs, report.DuplicatePIDs)
	fmt.Printf("  dup_same_file=%d dup_diff_file=%d dup_in_itl=%d files_to_clear=%d\n",
		report.DupSameFile, report.DupDiffFile, report.DupInITL, report.FilesToClear)
	fmt.Printf("  pids_on_multiple_primaries_diff_path=%d (relocate-correctness probe: 0 = relocate provably correct)\n",
		report.PIDsOnMultiplePrimariesDiffPath)

	if *repair {
		mappings := []itunes.PathMapping{{From: *mapFrom, To: *mapTo}}
		_, preview, err := itunes.ComputePIDRepairPlan(store, *itlPath, mappings)
		if err != nil {
			fmt.Fprintf(os.Stderr, "repair plan: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("=== REPAIR PLAN (dry-run) ===\n")
		fmt.Printf("duplicate_pids=%d same_file_groups=%d diff_file_groups=%d ambiguous_groups=%d files_to_clear=%d\n",
			preview.DuplicatePIDs, preview.SameFileGroups, preview.DiffFileGroups,
			preview.AmbiguousGroups, preview.FilesToClear)
		if *full {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(preview)
		}
		return
	}

	if *full {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "encode: %v\n", err)
			os.Exit(1)
		}
	}
}
