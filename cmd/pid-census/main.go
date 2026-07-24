// file: cmd/pid-census/main.go
// version: 1.1.0
// guid: 8f2b0d61-4a37-4c95-9e12-7d3a6b1c0e58
// last-edited: 2026-07-24
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
	mergeProv := flag.Bool("merge-provenance", false, "run the cleanup provenance census (P3 exit-gate); requires --itl")
	crossType := flag.Bool("cross-type", false, "run the cross-type PID-collision census (relocate disjointness backstop); requires --itl")
	syncDryRun := flag.Bool("sync-dry-run", false, "P2 relocate sync cycle DRY-RUN (plan + in-memory verify, NO write); requires --itl")
	syncRoot := flag.String("sync-writeback-root", "audiobook-organizer/.itunes-writeback/", "F7 AllowedWritebackRoot for the AO library's own media root")
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

	// P2 relocate sync cycle — DRY-RUN (plan + in-memory oracle verify; NO write).
	if *syncDryRun {
		if *itlPath == "" {
			fmt.Fprintln(os.Stderr, "error: --sync-dry-run requires --itl (a copy of the AO writeback .itl)")
			os.Exit(2)
		}
		mappings := []itunes.PathMapping{{From: *mapFrom, To: *mapTo}}
		r, serr := itunes.RunRelocateSyncCycle(store, itunes.SyncCycleConfig{
			ITLPath:              *itlPath,
			AllowedWritebackRoot: *syncRoot,
			Mappings:             mappings,
			Apply:                false, // DRY-RUN only
		})
		if serr != nil {
			fmt.Fprintf(os.Stderr, "sync dry-run: %v\n", serr)
			os.Exit(1)
		}
		fmt.Printf("=== P2 RELOCATE SYNC CYCLE — DRY-RUN (no write) ===\n")
		fmt.Printf("planned=%d already_correct=%d unmatched=%d unmappable=%d\n",
			r.Planned, r.AlreadyCorrect, r.Unmatched, r.Unmappable)
		fmt.Printf(">>> ORACLE_OK=%v relocated_verified=%d violations=%d\n",
			r.OracleOK, r.RelocatedVerified, len(r.OracleViolations))
		if len(r.OracleViolations) > 0 {
			for i, v := range r.OracleViolations {
				if i >= 5 {
					fmt.Printf("    ... +%d more\n", len(r.OracleViolations)-5)
					break
				}
				fmt.Printf("    VIOLATION pid=%s kind=%s %s\n", v.PID, v.Kind, v.Detail)
			}
		}
		fmt.Printf("    (DRY-RUN: nothing written. Review the plan + sample new locations before Apply=true.)\n")
		if *full {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(r)
		}
		return
	}

	// Cross-type PID-collision census (relocate disjointness backstop).
	if *crossType {
		if *itlPath == "" {
			fmt.Fprintln(os.Stderr, "error: --cross-type requires --itl (a copy of the AO writeback .itl)")
			os.Exit(2)
		}
		r, cerr := itunes.ComputeCrossTypeCollisions(store, *itlPath)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "cross-type census: %v\n", cerr)
			os.Exit(1)
		}
		fmt.Printf("=== CROSS-TYPE PID-COLLISION CENSUS (relocate disjointness backstop) ===\n")
		fmt.Printf("tracks_in_itl=%d  audiobook=%d  non_audiobook=%d\n",
			r.TracksInITL, r.AudiobookTracks, r.NonAudiobookTracks)
		fmt.Printf("  ab_owned=%d  ab_unowned=%d  non_ab_owned=%d  non_ab_unowned=%d\n",
			r.ABOwned, r.ABUnowned, r.NonABOwned, r.NonABUnowned)
		fmt.Printf(">>> CROSS_TYPE_COLLISIONS=%d  (live_primary_owner=%d)\n",
			r.CrossTypeCollisions, r.CollisionsLivePrimaryOwner)
		fmt.Printf("    MUST be 0 (or each offender explained) before the relocate op is armed.\n")
		if *full {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(r)
		}
		return
	}

	// Cleanup provenance census (P3 exit-gate) — reconciles BOTH loser sources:
	// the AutoMergeJournalEntry journal (production-authoritative) via the
	// EmbeddingStore sharing this store's raw Pebble handle, plus MergedIntoBookID
	// discovered per-owner inside the census.
	if *mergeProv {
		if *itlPath == "" {
			fmt.Fprintln(os.Stderr, "error: --merge-provenance requires --itl (a copy of the AO writeback .itl)")
			os.Exit(2)
		}
		emb := database.NewEmbeddingStore(store.DB())
		entries, jerr := emb.ListAutoMergeJournalEntries(0)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "list automerge journal: %v\n", jerr)
			os.Exit(1)
		}
		loserIDs := make([]string, 0, len(entries))
		for _, e := range entries {
			loserIDs = append(loserIDs, e.LoserID)
		}
		c, cerr := itunes.ComputeMergeOrphanCensus(store, *itlPath, loserIDs)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "merge-orphan census: %v\n", cerr)
			os.Exit(1)
		}
		fmt.Printf("=== CLEANUP PROVENANCE CENSUS (P3 exit-gate) ===\n")
		fmt.Printf("automerge_journal_entries=%d journal_loser_ids=%d\n", len(entries), c.JournalLoserIDs)
		fmt.Printf("tracks_in_itl=%d  healthy=%d  stale_owner=%d  no_live_owner=%d\n",
			c.TracksInITL, c.Healthy, c.StaleOwner, c.NoLiveOwner)
		fmt.Printf("merged_into_losers=%d distinct_loser_set(seen)=%d residual_duplicate_pids=%d\n",
			c.MergedIntoLosers, c.DistinctLoserSet, c.ResidualDuplicatePIDs)
		fmt.Printf(">>> PROVABLE_MERGE_ORPHANS=%d  (sha_gated_removable=%d)\n",
			c.ProvableMergeOrphans, c.SHAGatedRemovable)
		fmt.Printf("    NOTE: LOWER BOUND. no_live_owner tracks are unattributable (user non-AO imports\n")
		fmt.Printf("    OR merge orphans whose PID->loser link was severed). ~0 != 'no orphans exist'.\n")
		if *full {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(c)
		}
		return
	}

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
