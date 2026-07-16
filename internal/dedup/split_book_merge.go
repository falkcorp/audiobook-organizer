// file: internal/dedup/split_book_merge.go
// version: 1.1.0
// guid: 3b5d7f9a-2e4c-6b8d-0f1a-3c5e7d9f1b3e
// last-edited: 2026-07-16

// Split-book cluster merge — portable across SQLite and Pebble.
//
// The existing `MergeChapterBooks` store method is SQLite-only
// (PebbleStore returns nil without doing anything). The existing
// `merge.Service.MergeBooks` soft-deletes losers but does NOT move
// their BookFiles to the keeper — for chapter merges that would orphan
// every chapter file.
//
// This function uses the portable `MoveBookFilesToBook` (implemented on
// both stores) and only soft-deletes after the files have been
// reassigned. Duration is recomputed as the sum of moved-file durations.

package dedup

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// SplitBookMergeResult summarises a successful merge.
type SplitBookMergeResult struct {
	KeepID         string `json:"keep_id"`
	MergedSrcCount int    `json:"merged_src_count"`
	FilesMoved     int    `json:"files_moved"`
	NewDuration    int    `json:"new_duration"`
	Errors         []string `json:"errors,omitempty"`
}

// MergeSplitBookCluster absorbs every srcID into keepID:
//
//  1. For each src: GetBookFiles, MoveBookFilesToBook(ids, src, keep).
//  2. Recompute keep duration as sum of all bookfile durations.
//  3. Optionally update keep.Title to suggestedTitle (when non-empty).
//  4. Soft-delete each src.
//
// Per-src errors are collected but do not abort — the remaining sources
// still get processed so the operator doesn't end up with a half-merged
// cluster.
func MergeSplitBookCluster(store database.Store, keepID string, srcIDs []string, suggestedTitle string) (*SplitBookMergeResult, error) {
	if keepID == "" {
		return nil, fmt.Errorf("MergeSplitBookCluster: empty keepID")
	}
	if len(srcIDs) == 0 {
		return nil, fmt.Errorf("MergeSplitBookCluster: no srcIDs")
	}
	keep, err := store.GetBookByID(keepID)
	if err != nil || keep == nil {
		return nil, fmt.Errorf("keep book %s not found: %w", keepID, err)
	}

	result := &SplitBookMergeResult{KeepID: keepID}

	// safeToDelete tracks srcs that are PROVABLY empty after Step 1 — either they
	// had no files to begin with, or their files were successfully moved to keep.
	// A src whose GetBookFiles or MoveBookFilesToBook errored is NOT recorded, so
	// Step 4 leaves it intact rather than soft-deleting a book that still owns its
	// files (which would orphan that audio — a deleted book still referenced by
	// live BookFile rows). The operator can retry the whole cluster.
	safeToDelete := make(map[string]bool, len(srcIDs))

	// Step 1: move bookfiles from each src to keep.
	for _, srcID := range srcIDs {
		if srcID == keepID {
			continue
		}
		files, err := store.GetBookFiles(srcID)
		if err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("get files for %s: %v", srcID, err))
			continue
		}
		if len(files) == 0 {
			safeToDelete[srcID] = true // nothing to orphan
			continue
		}
		ids := make([]string, 0, len(files))
		for _, f := range files {
			ids = append(ids, f.ID)
		}
		if err := store.MoveBookFilesToBook(ids, srcID, keepID); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("move files from %s: %v", srcID, err))
			continue
		}
		result.FilesMoved += len(files)
		safeToDelete[srcID] = true // files reassigned to keep
	}

	// Step 2: recompute keep duration as sum of all bookfile durations.
	allFiles, err := store.GetBookFiles(keepID)
	if err != nil {
		result.Errors = append(result.Errors,
			fmt.Sprintf("recount files on keep: %v", err))
	} else {
		var total int
		for _, f := range allFiles {
			total += int(f.Duration)
		}
		if total > 0 {
			result.NewDuration = total
			keep.Duration = &total
		}
	}

	// Step 3: update keep title if a non-empty suggested title was given.
	if suggestedTitle != "" && suggestedTitle != keep.Title {
		keep.Title = suggestedTitle
	}
	if _, err := store.UpdateBook(keep.ID, keep); err != nil {
		result.Errors = append(result.Errors,
			fmt.Sprintf("update keep book: %v", err))
	}

	// Step 4: soft-delete each src whose files were provably moved away. A src
	// that errored in Step 1 still owns its files and is deliberately left intact
	// to avoid orphaning that audio.
	for _, srcID := range srcIDs {
		if srcID == keepID {
			continue
		}
		if !safeToDelete[srcID] {
			continue // Step 1 failed for this src; leave it (and its files) intact
		}
		if err := merge.SoftDeleteBook(store, srcID); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("soft-delete %s: %v", srcID, err))
			slog.Warn("split-book merge soft-delete failed", "src", srcID, "err", err)
			continue
		}
		result.MergedSrcCount++
	}

	return result, nil
}
