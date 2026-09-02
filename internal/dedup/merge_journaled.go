// file: internal/dedup/merge_journaled.go
// version: 1.1.0
// guid: 1d7c3e58-4a09-42b6-8f31-5c0e9b247a63
// last-edited: 2026-09-02

package dedup

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// MergeJournaled merges the two books of a dedup candidate and guarantees that
// an undo key exists before the merge happens.
//
// This sequence used to live only inside autoMergeCertain, which meant the
// Tier-1 auto path was the ONLY merge in the system that could be reversed. A
// merge dispatched from the review lane wrote no journal entry, so
// UnmergeAuto had nothing to revert to — the merges a human triggers by hand,
// at speed, were exactly the ones with no undo. Centralising it here is what
// makes "every merge is reversible" a property of the engine rather than a
// habit of one caller.
//
// The order is load-bearing:
//
//  1. Capture the newest existing book_ver snapshot for each side. MergeBooks
//     calls UpdateBook on every book, so the earliest snapshot NEWER than this
//     baseline is the genuine pre-merge record. "Newest after the merge" is
//     wrong, because the loser gets a second snapshot when it is soft-deleted.
//  2. Write a provisional journal entry. A failure here is a HARD error and
//     the merge does not happen: an irreversible merge with no undo key is a
//     worse outcome than no merge at all.
//  3. Merge.
//  4. Patch the SAME journal key with the authoritative winner/loser and their
//     pre-merge snapshot timestamps. A failure here is logged, not returned —
//     the merge is already done, and the provisional entry still names the
//     candidate and both books, so an operator can recover by hand.
//
// keepID may be empty to let MergeBooks auto-pick the primary via
// merge.BookIsBetter.
//
// Callers may run their own post-merge side effects (tagging, candidate status,
// candidate cleanup) after this returns. Those write further book_ver
// snapshots, which is harmless: step 4 looks for the EARLIEST snapshot after
// the baseline, so later ones cannot displace it.
func (de *Engine) MergeJournaled(candidateID int64, aID, bID, keepID, tag string) (*merge.Result, string, error) {
	if de == nil || de.embedStore == nil || de.bookStore == nil || de.mergeService == nil {
		return nil, "", fmt.Errorf("merge-journaled: engine not fully initialised")
	}
	if aID == "" || bID == "" {
		return nil, "", fmt.Errorf("merge-journaled: both book ids are required")
	}

	baseA := de.newestSnapshotNanos(aID)
	baseB := de.newestSnapshotNanos(bID)

	// Predict the winner the same way MergeBooks will, so the provisional entry
	// is meaningful on its own. MergeBooks re-derives the authoritative winner
	// and step 4 overwrites this, so a wrong prediction costs nothing.
	predWinner, predLoser := aID, bID
	if keepID != "" {
		predWinner = keepID
		predLoser = aID
		if predLoser == predWinner {
			predLoser = bID
		}
	} else {
		bookA, errA := de.bookStore.GetBookByID(aID)
		if errA != nil {
			return nil, "", fmt.Errorf("merge-journaled: load book %s before merge: %w", aID, errA)
		}
		bookB, errB := de.bookStore.GetBookByID(bID)
		if errB != nil {
			return nil, "", fmt.Errorf("merge-journaled: load book %s before merge: %w", bID, errB)
		}
		// (nil, nil) is the store's "no such row". Surface it as the same typed
		// error MergeBooks would, so the handler's stale-candidate branch sees
		// one shape whether the book vanished before or during the merge.
		if bookA == nil {
			return nil, "", fmt.Errorf("merge-journaled: load books before merge: %w", &merge.BookNotFoundError{BookID: aID})
		}
		if bookB == nil {
			return nil, "", fmt.Errorf("merge-journaled: load books before merge: %w", &merge.BookNotFoundError{BookID: bID})
		}
		if merge.BookIsBetter(bookB, bookA) {
			predWinner, predLoser = bID, aID
		}
	}

	// mergedAt fixes the journal key so the post-merge patch overwrites the same
	// entry rather than creating a second one.
	mergedAt := time.Now().UnixNano()
	journalKey, err := de.embedStore.PutAutoMergeJournalEntry(database.AutoMergeJournalEntry{
		CandidateID: candidateID,
		WinnerID:    predWinner,
		LoserID:     predLoser,
		Tag:         tag,
		MergedAt:    mergedAt,
	})
	if err != nil {
		return nil, "", fmt.Errorf("merge-journaled: write provisional journal entry (merge skipped): %w", err)
	}

	result, mergeErr := de.mergeService.MergeBooks([]string{aID, bID}, keepID)
	if mergeErr != nil {
		return nil, journalKey, fmt.Errorf("merge-journaled: merge books: %w", mergeErr)
	}
	if result == nil || result.PrimaryID == "" {
		return nil, journalKey, fmt.Errorf("merge-journaled: merge returned no primary id")
	}

	winnerID := result.PrimaryID
	loserID := aID
	if loserID == winnerID {
		loserID = bID
	}

	if _, err := de.embedStore.PutAutoMergeJournalEntry(database.AutoMergeJournalEntry{
		CandidateID:      candidateID,
		WinnerID:         winnerID,
		LoserID:          loserID,
		WinnerPreMergeTS: de.preMergeSnapshotNanos(winnerID, baselineFor(winnerID, aID, baseA, baseB)),
		LoserPreMergeTS:  de.preMergeSnapshotNanos(loserID, baselineFor(loserID, aID, baseA, baseB)),
		Tag:              tag,
		MergedAt:         mergedAt,
	}); err != nil {
		// The merge is complete. The provisional entry already names the
		// candidate and both books, so log rather than fail a done merge.
		slog.Error("merge-journaled: patch journal entry failed (provisional entry stands)",
			"candidate", candidateID, "journal", journalKey, "err", err)
	}

	return result, journalKey, nil
}
