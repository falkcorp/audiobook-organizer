// file: internal/itunes/cleanup_merged.go
// version: 1.0.0
// guid: 9c4e7a20-1b83-4d6f-a2e9-5c0d3b8f1a74
// last-edited: 2026-07-22
//
// P3 of the 2-way-sync writeback: remove stale duplicate audiobook tracks left
// in the library by books that were merged/superseded (the merge-cleanup that
// never applied while the writeback was broken). A superseded track = a library
// track whose PID belongs to a NON-primary book_file (a merged "loser" or
// alternate version) and does NOT also belong to any primary book_file. Removal
// goes through RemoveTracksByPIDLE, which excises the track AND cleans orphaned
// playlist references. Only ever targets audiobook PIDs sourced from the DB —
// music/podcast tracks are never in the candidate set. See
// docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md (P3).

package itunes

import (
	"fmt"
	"log/slog"
	"strings"
)

// MergedCleanupPreview summarizes the superseded-track removal without applying.
type MergedCleanupPreview struct {
	TracksInITL    int `json:"tracks_in_itl"`
	PrimaryPIDs    int `json:"primary_pids"`     // distinct primary book_file PIDs present in the ITL
	NonPrimaryPIDs int `json:"non_primary_pids"` // distinct non-primary book_file PIDs present in the ITL
	ToRemove       int `json:"to_remove"`        // non-primary PIDs that are NOT also a primary PID
	SharedSkipped  int `json:"shared_skipped"`   // non-primary PIDs also owned by a primary (kept, defensive)
}

// ComputeMergedTrackCleanup finds superseded audiobook tracks to remove. It emits
// ONLY Removes; never adds/relocates. A non-primary book_file PID is removed only
// if no primary book_file also carries it (so we never remove a live track).
func ComputeMergedTrackCleanup(store RebuildStore, itlPath string) (*ITLOperationSet, *MergedCleanupPreview, error) {
	lib, err := ParseITL(itlPath)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ITL: %w", err)
	}
	inITL := make(map[string]bool, len(lib.Tracks))
	for i := range lib.Tracks {
		inITL[strings.ToUpper(pidToHex(lib.Tracks[i].PersistentID))] = true
	}
	ops, preview := computeMergedCleanupFromInITL(inITL, store)
	return ops, preview, nil
}

// computeMergedCleanupFromInITL is the ITL-independent core: given the set of
// PIDs present in the library, it walks the DB's book_files and builds the
// remove set (non-primary PIDs not also owned by a primary). Split out for unit
// testing without minting a real .itl.
func computeMergedCleanupFromInITL(inITL map[string]bool, store RebuildStore) (*ITLOperationSet, *MergedCleanupPreview) {
	primary := make(map[string]bool)    // PID → is a primary book_file's PID
	nonPrimary := make(map[string]bool) // PID → is a non-primary book_file's PID (in ITL)

	const pageSize = 500
	afterID := ""
	for {
		books, err := store.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			// Fail closed: if we can't fully enumerate the DB we cannot safely
			// decide what is superseded, so remove nothing.
			slog.Error("cleanup-merged: get books failed, aborting (remove nothing)", "err", err)
			return &ITLOperationSet{Removes: map[string]bool{}}, &MergedCleanupPreview{TracksInITL: len(inITL)}
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			b := &books[i]
			isPrimary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
			files, ferr := store.GetBookFiles(b.ID)
			if ferr != nil {
				continue
			}
			for j := range files {
				pid := strings.ToUpper(files[j].ITunesPersistentID)
				if pid == "" || !inITL[pid] {
					continue
				}
				if isPrimary {
					primary[pid] = true
				} else {
					nonPrimary[pid] = true
				}
			}
		}
		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			break
		}
	}

	ops := ITLOperationSet{Removes: map[string]bool{}}
	preview := &MergedCleanupPreview{
		TracksInITL:    len(inITL),
		PrimaryPIDs:    len(primary),
		NonPrimaryPIDs: len(nonPrimary),
	}
	for pid := range nonPrimary {
		if primary[pid] {
			preview.SharedSkipped++ // a live primary also owns this PID — never remove
			continue
		}
		ops.Removes[pid] = true
		preview.ToRemove++
	}

	slog.Info("itunes cleanup-merged: computed superseded-track removal",
		"tracksInITL", preview.TracksInITL, "primaryPIDs", preview.PrimaryPIDs,
		"nonPrimaryPIDs", preview.NonPrimaryPIDs, "toRemove", preview.ToRemove,
		"sharedSkipped", preview.SharedSkipped)
	return &ops, preview
}
