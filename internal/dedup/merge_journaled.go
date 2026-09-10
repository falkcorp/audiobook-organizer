// file: internal/dedup/merge_journaled.go
// version: 1.3.0
// guid: 1d7c3e58-4a09-42b6-8f31-5c0e9b247a63
// last-edited: 2026-09-10

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
// merge.ElectPrimary.
//
// Callers may run their own post-merge side effects (tagging, candidate status,
// candidate cleanup) after this returns. Those write further book_ver
// snapshots, which is harmless: step 4 looks for the EARLIEST snapshot after
// the baseline, so later ones cannot displace it.
func (de *Engine) MergeJournaled(candidateID int64, aID, bID, keepID, tag string) (*merge.Result, string, error) {
	if aID == "" || bID == "" {
		return nil, "", fmt.Errorf("merge-journaled: both book ids are required")
	}
	result, keys, err := de.MergeBooksJournaled(candidateID, []string{aID, bID}, keepID, tag)
	journalKey := ""
	if len(keys) > 0 {
		journalKey = keys[0]
	}
	return result, journalKey, err
}

// MergeBooksJournaled is the N-ary form of MergeJournaled: it merges an
// arbitrary cluster of books and guarantees an undo key exists for EVERY loser
// before the merge happens. It returns the merge result and one journal key per
// loser, in the same order as the losers were journaled.
//
// WHY N-ARY (DA-02). MergeJournaled was strictly pairwise and candidate-keyed,
// so the merge paths that had neither shape — the exact-file-hash auto-merge
// and the LLM high-confidence auto-merge (no candidate row / a candidate but no
// review), and the HTTP cluster-merge endpoints (N books, no candidate row at
// all) — could not route through it and went on calling
// mergeService.MergeBooks directly, writing no journal. Those were precisely
// the merges nobody reviewed. This is the shared core; MergeJournaled is now a
// thin pairwise wrapper over it, so `auto_resolve.go` and the single-candidate
// review endpoint keep their exact signature and behaviour.
//
// candidateID may be 0 when no dedup candidate triggered the merge (the cluster
// endpoints, the file-hash auto-merge); the entry then records the books alone.
// keepID may be empty to let MergeBooks auto-pick the primary via
// merge.ElectPrimary.
//
// WHY N-1 ENTRIES RATHER THAN ONE. database.AutoMergeJournalEntry holds exactly
// one WinnerID/LoserID pair and UnmergeAuto reverts exactly that pair, so a
// cluster of N books is recorded as N-1 pairwise entries sharing one winner.
// Reverting the whole cluster means calling UnmergeAuto on each key, which
// reverts the winner N-1 times: that is safe because RevertBookToVersion reads
// an IMMUTABLE book_ver snapshot key (book_ver:<id>:<nanos>) and re-applies it,
// so every repeat lands the winner in the identical state. The extra snapshots
// each revert writes cannot disturb the recorded pre-merge timestamps, which
// were fixed here at merge time.
//
// Journal keys are derived from MergedAt nanoseconds, so each loser's entry is
// offset by its index to keep the keys distinct (and chronologically ordered);
// nothing reads MergedAt as anything but the key.
func (de *Engine) MergeBooksJournaled(candidateID int64, bookIDs []string, keepID, tag string) (*merge.Result, []string, error) {
	if de == nil || de.embedStore == nil || de.bookStore == nil || de.mergeService == nil {
		return nil, nil, fmt.Errorf("merge-journaled: engine not fully initialised")
	}

	// De-duplicate while preserving order. A repeated id would otherwise
	// produce a journal entry whose winner and loser are the same book.
	ids := make([]string, 0, len(bookIDs))
	seen := make(map[string]struct{}, len(bookIDs))
	for _, id := range bookIDs {
		if id == "" {
			return nil, nil, fmt.Errorf("merge-journaled: book ids must not be empty")
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) < 2 {
		return nil, nil, fmt.Errorf("merge-journaled: need at least 2 distinct book ids, got %d", len(ids))
	}
	if keepID != "" {
		if _, ok := seen[keepID]; !ok {
			return nil, nil, fmt.Errorf("merge-journaled: keep id %s is not one of the books being merged", keepID)
		}
	}

	baselines := make(map[string]int64, len(ids))
	for _, id := range ids {
		baselines[id] = de.newestSnapshotNanos(id)
	}

	// Predict the winner the same way MergeBooks will, so the provisional
	// entries are meaningful on their own. MergeBooks re-derives the
	// authoritative winner and the patch below overwrites the same keys, so a
	// wrong prediction costs nothing — the ENTRY COUNT is N-1 either way.
	predWinner := keepID
	if predWinner == "" {
		best, err := de.predictPrimary(ids)
		if err != nil {
			return nil, nil, err
		}
		predWinner = best
	}

	// mergedAt fixes the journal keys so the post-merge patch overwrites the
	// same entries rather than creating a second set.
	mergedAt := time.Now().UnixNano()
	journalKeys := make([]string, 0, len(ids)-1)
	for _, loserID := range othersThan(ids, predWinner) {
		key, err := de.embedStore.PutAutoMergeJournalEntry(database.AutoMergeJournalEntry{
			CandidateID: candidateID,
			WinnerID:    predWinner,
			LoserID:     loserID,
			Tag:         tag,
			MergedAt:    mergedAt + int64(len(journalKeys)),
		})
		if err != nil {
			// A provisional entry that cannot be written means this merge would
			// be irreversible, which is a worse outcome than no merge at all.
			// Any entries already written name only books that were never
			// merged; they are inert and an operator can ignore them.
			return nil, journalKeys, fmt.Errorf("merge-journaled: write provisional journal entry (merge skipped): %w", err)
		}
		journalKeys = append(journalKeys, key)
	}

	result, mergeErr := de.mergeService.MergeBooks(ids, keepID)
	if mergeErr != nil {
		return nil, journalKeys, fmt.Errorf("merge-journaled: merge books: %w", mergeErr)
	}
	if result == nil || result.PrimaryID == "" {
		return nil, journalKeys, fmt.Errorf("merge-journaled: merge returned no primary id")
	}

	winnerID := result.PrimaryID
	winnerTS := de.preMergeSnapshotNanos(winnerID, baselines[winnerID])
	for i, loserID := range othersThan(ids, winnerID) {
		if i >= len(journalKeys) {
			break // unreachable: len(losers) == len(ids)-1 == len(journalKeys)
		}
		if _, err := de.embedStore.PutAutoMergeJournalEntry(database.AutoMergeJournalEntry{
			CandidateID:      candidateID,
			WinnerID:         winnerID,
			LoserID:          loserID,
			WinnerPreMergeTS: winnerTS,
			LoserPreMergeTS:  de.preMergeSnapshotNanos(loserID, baselines[loserID]),
			Tag:              tag,
			MergedAt:         mergedAt + int64(i),
		}); err != nil {
			// The merge is complete. The provisional entry already names the
			// candidate and both books, so log rather than fail a done merge.
			slog.Error("merge-journaled: patch journal entry failed (provisional entry stands)",
				"candidate", candidateID, "journal", journalKeys[i], "err", err)
		}
	}

	return result, journalKeys, nil
}

// predictPrimary picks the book merge.MergeBooks will elect as primary when no
// keepID is supplied, by calling the exported merge.ElectPrimary on the same
// two inputs MergeBooks feeds it: the books and their file rows.
//
// Reusing the real election rather than re-deriving one matters even though the
// prediction is provisional. ElectPrimary is NOT a merge.BookIsBetter
// reduction: a book with an audio route beats one without BEFORE BookIsBetter
// is consulted at all (merge.HasAudioRoute), and exact ties fall to the
// existing primary then the older ULID rather than to argument order. A
// BookIsBetter reduction therefore names a different winner on exactly the
// clusters that mix file-bearing and file-less rows — the common shape here.
func (de *Engine) predictPrimary(ids []string) (string, error) {
	books := make([]*database.Book, 0, len(ids))
	filesByID := make(map[string][]database.BookFile, len(ids))
	for _, id := range ids {
		book, err := de.bookStore.GetBookByID(id)
		if err != nil {
			return "", fmt.Errorf("merge-journaled: load book %s before merge: %w", id, err)
		}
		// (nil, nil) is the store's "no such row". Surface it as the same typed
		// error MergeBooks would, so the handler's stale-candidate branch sees
		// one shape whether the book vanished before or during the merge.
		if book == nil {
			return "", fmt.Errorf("merge-journaled: load books before merge: %w", &merge.BookNotFoundError{BookID: id})
		}
		files, err := de.bookStore.GetBookFiles(id)
		if err != nil {
			return "", fmt.Errorf("merge-journaled: load files for book %s before merge: %w", id, err)
		}
		books = append(books, book)
		filesByID[id] = files
	}
	if best := merge.ElectPrimary(books, filesByID); best >= 0 {
		return books[best].ID, nil
	}
	// -1 means every participant is soft-deleted, so MergeBooks is about to
	// refuse this merge outright and the entries written for it are inert. Name
	// the first book so the entry count stays N-1, and let MergeBooks produce
	// the authoritative (typed) error rather than inventing one here.
	return ids[0], nil
}

// othersThan returns every id except exclude, preserving order.
func othersThan(ids []string, exclude string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == exclude {
			continue
		}
		out = append(out, id)
	}
	return out
}
