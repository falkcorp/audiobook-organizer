// file: internal/dedup/merge_journaled.go
// version: 1.13.0
// guid: 1d7c3e58-4a09-42b6-8f31-5c0e9b247a63
// last-edited: 2026-11-17

package dedup

import (
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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
//  4. Patch the SAME journal key with the authoritative winner/loser, their
//     pre-merge snapshot timestamps and the sibling-move journal. A failure
//     here is logged, not returned — the merge is already done. The entry
//     stays marked Provisional, which UnmergeAuto refuses; the log names the
//     sibling-move journal so the siblings can still be put back.
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

var journaledMergeLog = logger.New("dedup.merge-journaled")

// putJournal writes one auto-merge journal entry, replacing whatever is at its
// key (see Engine.journalPut). Only the post-merge patch uses it: a row's first
// write goes through createJournal, which never replaces another merge's row.
func (de *Engine) putJournal(e database.AutoMergeJournalEntry) (string, error) {
	if de.journalPut != nil {
		return de.journalPut(e)
	}
	return de.embedStore.PutAutoMergeJournalEntry(e)
}

// createJournal writes a provisional row only if its key is free. created=false
// means another row holds the key and nothing was written. The journalPut seam,
// when set, takes the write (and always reports it created): a test that injects
// a failure sees the same sequence of writes it always did.
func (de *Engine) createJournal(e database.AutoMergeJournalEntry) (key string, created bool, err error) {
	if de.journalPut != nil {
		key, err = de.journalPut(e)
		return key, err == nil, err
	}
	return de.embedStore.PutAutoMergeJournalEntryIfAbsent(e)
}

// closeJournal closes the provisional row e (written by this call, so e.Key and
// e.RowID are set) unless the stored row is no longer the one this call wrote.
// closed=false with a nil error means it was left alone for that reason.
func (de *Engine) closeJournal(e database.AutoMergeJournalEntry, at int64) (closed bool, err error) {
	if de.journalPut != nil {
		e.AbortedAt = at
		_, err = de.journalPut(e)
		return err == nil, err
	}
	return de.embedStore.CloseAutoMergeJournalEntry(e.Key, e.RowID, at)
}

// journalNanos is the clock that stamps journal keys.
func (de *Engine) journalNanos() int64 {
	if de.journalNow != nil {
		return de.journalNow()
	}
	return time.Now().UnixNano()
}

// provisionalKeyAttempts bounds how often a provisional row asks for a new key
// after the one it wanted turned out to be taken. A key is a nanosecond, so one
// retry is already unlikely and a second is not expected.
const provisionalKeyAttempts = 8

// createProvisional writes e as a new row, on a new MergedAt (so a new key) each
// time the key it wanted is taken by another merge's row. It never replaces an
// existing row. The returned entry carries the key it was stored at and the
// MergedAt that key was made from.
func (de *Engine) createProvisional(e database.AutoMergeJournalEntry) (database.AutoMergeJournalEntry, error) {
	for range provisionalKeyAttempts {
		key, created, err := de.createJournal(e)
		if err != nil {
			return e, err
		}
		if created {
			e.Key = key
			return e, nil
		}
		// The clock can repeat a value, so the retry is forced above the last one.
		e.MergedAt = max(de.journalNanos(), e.MergedAt+1)
	}
	return e, fmt.Errorf("the journal key was taken %d times in a row", provisionalKeyAttempts)
}

// journalSiblings is the part of result.MovedSiblings that left with loserID:
// the siblings of the group loserID was in. A sibling of a group several
// losers shared is recorded on each of their entries; restoring it is
// idempotent, so undoing any one of them puts it back.
func journalSiblings(result *merge.Result, loserID string) []database.AutoMergeJournalSibling {
	var out []database.AutoMergeJournalSibling
	for _, sib := range result.MovedSiblings {
		if slices.Contains(sib.WithLosers, loserID) {
			out = append(out, database.AutoMergeJournalSibling{
				BookID:      sib.BookID,
				FromGroupID: sib.FromGroupID,
				IntoGroupID: result.VersionGroupID,
				WasPrimary:  sib.WasPrimary,
			})
		}
	}
	return out
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

	// Refuse an iTunes-library participant BEFORE the provisional journal
	// entries are written, so a refused merge leaves nothing behind. The
	// authoritative check still runs inside MergeBooks under the merge lock;
	// this early one only keeps the refusal write-free.
	if err := merge.GuardITunesProtected(de.bookStore, ids); err != nil {
		return nil, nil, fmt.Errorf("merge-journaled: %w", err)
	}

	// Each book's newest snapshot is recorded in its provisional row as the floor
	// its pin starts from. A snapshot list that cannot be read is not "no
	// snapshot" (that is a real baseline of 0), so the merge is refused here,
	// before the first journal write and before MergeBooks: no row, no pin.
	baselines := make(map[string]int64, len(ids))
	for _, id := range ids {
		nanos, err := de.newestSnapshotNanos(id)
		if err != nil {
			return nil, nil, &BaselineReadError{BookID: id, Err: err}
		}
		baselines[id] = nanos
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

	// Every provisional row lists EVERY participant with its baseline, so a row that
	// survives a failed patch still covers the whole merge, whichever book turns
	// out to have won (see AutoMergeJournalEntry.Participants).
	participants := make([]database.AutoMergeJournalParticipant, len(ids))
	for i, id := range ids {
		participants[i] = database.AutoMergeJournalParticipant{BookID: id, BaselineTS: baselines[id]}
	}

	// One row per loser of the PREDICTED winner. MergeBooks re-derives the
	// authoritative winner and the patch below matches the rows to the actual
	// losers by book id, so a wrong prediction costs nothing: the row COUNT is N-1
	// either way. Each row's key is its own (MergedAt, +i to keep a merge's rows
	// apart) and is written only if free, so a concurrent merge that asked for the
	// same nanosecond takes another key instead of overwriting this row.
	mergedAt := de.journalNanos()
	journalKeys := make([]string, 0, len(ids)-1)
	provisional := make([]database.AutoMergeJournalEntry, 0, len(ids)-1)
	for _, loserID := range othersThan(ids, predWinner) {
		entry := database.AutoMergeJournalEntry{
			CandidateID: candidateID,
			WinnerID:    predWinner,
			LoserID:     loserID,
			Tag:         tag,
			MergedAt:    mergedAt + int64(len(journalKeys)),
			Provisional: true,
			RowID:       rand.Text(),
			// While the entry is provisional the snapshot prune keeps ONE
			// snapshot per book: the earliest one newer than its baseline, the
			// pre-merge record the patch below will name (the merge's own writes
			// land after the entry does).
			WinnerBaselineTS:  baselines[predWinner],
			LoserBaselineTS:   baselines[loserID],
			BaselinesRecorded: true,
			Participants:      participants,
		}
		written, err := de.createProvisional(entry)
		if err != nil {
			// A provisional entry that cannot be written means this merge would
			// be irreversible, which is a worse outcome than no merge at all.
			// Entries already written for it are closed here when that can be
			// proven harmless: the merge never started, so they describe nothing.
			de.abortProvisional(provisional, "the next provisional entry could not be written")
			return nil, journalKeys, fmt.Errorf("merge-journaled: write provisional journal entry (merge skipped): %w", err)
		}
		journalKeys = append(journalKeys, written.Key)
		provisional = append(provisional, written)
	}

	result, mergeErr := de.mergeService.MergeBooks(ids, keepID)
	if mergeErr != nil {
		// A refusal from the merge's read-only phase proves it wrote nothing, so
		// the entries describe a merge that never happened: close them, which
		// releases their snapshot pins. Only that proof closes a row. Any other
		// failure (a plain error, a typed one raised after writes began, a
		// part-way merge) may have written, so its entries stay provisional and
		// keep pinning the one snapshot per book an operator would need.
		var refused *merge.RefusedBeforeWriteError
		if errors.As(mergeErr, &refused) {
			de.abortProvisional(provisional, "the merge was refused before it wrote anything")
			return nil, journalKeys, fmt.Errorf("merge-journaled: merge books: %w", mergeErr)
		}
		// A merge that failed after its first write may have moved siblings.
		// The provisional entries stay provisional (UnmergeAuto refuses
		// them); the sibling journal is the undo for those moves.
		var partial *merge.PartialMergeError
		if errors.As(mergeErr, &partial) && partial.SiblingJournalID != "" {
			journaledMergeLog.Error("merge failed part way; undo its sibling moves by reverting operation %s, which names sibling-move journal %s (candidate=%d version_group=%s moved_siblings=%d)",
				logger.SanitizeLogValue(partial.SiblingOperationID), logger.SanitizeLogValue(partial.SiblingJournalID), candidateID,
				logger.SanitizeLogValue(partial.VersionGroupID), len(partial.MovedSiblings))
		}
		return nil, journalKeys, fmt.Errorf("merge-journaled: merge books: %w", mergeErr)
	}
	if result == nil || result.PrimaryID == "" {
		return nil, journalKeys, fmt.Errorf("merge-journaled: merge returned no primary id")
	}

	winnerID := result.PrimaryID
	// The patch names, for each side, the earliest snapshot above its baseline. A
	// snapshot list that cannot be read is NOT "no snapshot": writing a 0 would drop
	// the row's floor (a finalized row pins exactly what it names, and a 0 names
	// nothing) and leave an undo that skips that side. So a side that cannot be
	// resolved leaves its row provisional, with its floors, and is logged.
	winnerTS, winnerErr := de.preMergeSnapshotNanos(winnerID, baselines[winnerID])
	rowOf := make(map[string]int, len(provisional)) // loser id -> its provisional row
	for i, row := range provisional {
		rowOf[row.LoserID] = i
	}
	for _, loserID := range othersThan(ids, winnerID) {
		// Rows are matched to actual losers by book id. When the winner was
		// predicted right every loser has its own row. When it was not, the
		// predicted winner is a real loser with no row of its own, and the row
		// whose loser is the actual winner is the spare: it takes that place.
		idx, ok := rowOf[loserID]
		if !ok {
			idx, ok = rowOf[winnerID]
		}
		if !ok {
			journaledMergeLog.Error("merge-journaled: no provisional row for loser %s of a finished merge (candidate %d); it is covered by the other rows' participant floors",
				logger.SanitizeLogValue(loserID), candidateID)
			continue
		}
		row := provisional[idx]
		if winnerErr != nil {
			journaledMergeLog.Error("merge-journaled: cannot read the snapshots of winner %s to finalize journal row %s (candidate %d); the row stays provisional and keeps pinning the snapshots, UnmergeAuto will refuse it: %v",
				logger.SanitizeLogValue(winnerID), logger.SanitizeLogValue(row.Key), candidateID, winnerErr)
			continue
		}
		loserTS, loserErr := de.preMergeSnapshotNanos(loserID, baselines[loserID])
		if loserErr != nil {
			journaledMergeLog.Error("merge-journaled: cannot read the snapshots of loser %s to finalize journal row %s (candidate %d); the row stays provisional and keeps pinning the snapshots, UnmergeAuto will refuse it: %v",
				logger.SanitizeLogValue(loserID), logger.SanitizeLogValue(row.Key), candidateID, loserErr)
			continue
		}
		sibs := journalSiblings(result, loserID)
		entry := database.AutoMergeJournalEntry{
			CandidateID:      candidateID,
			WinnerID:         winnerID,
			LoserID:          loserID,
			WinnerPreMergeTS: winnerTS,
			LoserPreMergeTS:  loserTS,
			Tag:              tag,
			MergedAt:         row.MergedAt, // the key this row was created at
			Siblings:         sibs,
			IntoGroupID:      result.VersionGroupID,
		}
		// Named on every entry whenever the merge wrote a sibling-move
		// journal, not only on a loser some siblings left with: the journal
		// also records each loser's user-state follow onto a flag holder that
		// is not the survivor (a reused-group member, or a sibling that moved
		// with ANOTHER loser), and UnmergeAuto reverses that follow through
		// it (UndoSiblingMoveForLoser). An entry without it brought the loser
		// back with its state left on the flag holder and its ABS id still
		// redirected there.
		entry.SiblingJournalID = result.SiblingJournalID
		if _, err := de.putJournal(entry); err != nil {
			// The merge is complete, so log rather than fail a done merge.
			// The provisional entry stands and UnmergeAuto refuses it (it
			// records no snapshot and no siblings); the loser is still
			// restorable from the trash, and the siblings that left with it
			// through the sibling-move journal logged here (reverting the
			// operation logged with it).
			journaledMergeLog.Error("merge-journaled: patch journal entry failed (provisional entry stands; UnmergeAuto will refuse it) (candidate %d, journal %s, loser %s, sibling journal %s, sibling operation %s, siblings %d): %v",
				candidateID, logger.SanitizeLogValue(row.Key), logger.SanitizeLogValue(loserID),
				logger.SanitizeLogValue(result.SiblingJournalID), logger.SanitizeLogValue(result.SiblingOperationID), len(sibs), err)
		}
	}

	return result, journalKeys, nil
}

// BaselineReadError is a merge refused because a participant's snapshot list
// could not be read, so its baseline could not be recorded. Nothing was written:
// the refusal comes before the provisional journal rows and before MergeBooks.
type BaselineReadError struct {
	BookID string
	Err    error
}

func (e *BaselineReadError) Error() string {
	return fmt.Sprintf("merge-journaled: cannot read the snapshots of book %s to record the merge's baseline, merge refused before anything was written: %v", e.BookID, e.Err)
}
func (e *BaselineReadError) Unwrap() error { return e.Err }

// abortProvisional closes provisional entries whose merge provably never
// started (AbortedAt): UnmergeAuto and the snapshot pins both ignore a closed
// entry. A row is closed only while the stored row is still the one this call
// wrote (its RowID): a different merge that took the key since is left alone. A
// failure to close is logged and leaves the entry provisional, which is the safe
// state: it keeps its pin and UnmergeAuto refuses it.
func (de *Engine) abortProvisional(entries []database.AutoMergeJournalEntry, why string) {
	now := de.journalNanos()
	for _, e := range entries {
		closed, err := de.closeJournal(e, now)
		switch {
		case err != nil:
			journaledMergeLog.Warn("merge-journaled: could not close a provisional journal entry for a merge that never wrote anything (candidate %d, winner %s, loser %s, %s); it stays provisional and keeps pinning one snapshot per book: %v",
				e.CandidateID, logger.SanitizeLogValue(e.WinnerID), logger.SanitizeLogValue(e.LoserID), why, err)
		case !closed:
			journaledMergeLog.Warn("merge-journaled: left a journal row alone because it is no longer the provisional row this merge wrote (candidate %d, journal %s, winner %s, loser %s, %s)",
				e.CandidateID, logger.SanitizeLogValue(e.Key), logger.SanitizeLogValue(e.WinnerID), logger.SanitizeLogValue(e.LoserID), why)
		}
	}
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
	// The same election MergeBooks makes: audio quality alone (owner
	// decision 2026-10-05 13:00; which copy a user listened to plays no part,
	// the state follows the flag holder).
	if best := merge.ElectPrimary(books, filesByID); best >= 0 {
		return books[best].ID, nil
	}
	// -1 means every participant is soft-deleted, so MergeBooks is about to
	// refuse this merge outright. Its refusal comes from the read-only phase, so
	// MergeBooksJournaled closes the entries written for it (they are NOT inert
	// until then: each pins a snapshot per book). Name the first book so the entry
	// count stays N-1, and let MergeBooks produce the authoritative (typed) error
	// rather than inventing one here.
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
