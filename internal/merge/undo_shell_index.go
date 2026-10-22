// file: internal/merge/undo_shell_index.go
// version: 1.2.0
// guid: 3a8c5e72-1f94-4b06-9d3e-6c7a2b0f8e51
// last-edited: 2026-10-22

package merge

import (
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// UndoShellIndex answers one question for the purge: is this soft-deleted book
// the shell of a combine whose undo is still possible?
//
// UndoCombine restores an absorbed book only when its row is still the shell
// that combine retired (retiredByJournal: soft-deleted, carrying exactly the
// stamp the journal recorded), and an applied journal REFUSES the whole undo
// when a shell is gone. A combine shell owns no book_file rows (its files moved
// to the survivor), so the purge's owns-files rule does not protect it, and on
// 2026-10-21 a manual purge hard-deleted the shells of that day's 21 combines
// (2,520 of 2,523), which made every one of those combines un-revertable.
//
// A journal holds its shells while its undo can still run: applied, undo_failed
// (an undo that stopped part way re-runs), or pending and StepJournaled (a
// merge that stopped part way, which undo replays). Undone journals hold
// nothing: their shells were restored, or are the trash's to purge.
//
// Combine journals never expire, so a shell held here stays in the trash until
// its combine is undone. That is the intended cost of "purge must not make a
// revertible operation un-revertable". (This index covers combine journals
// only; the purge's hold also covers operation-ledger soft-deletes and
// auto-merge losers, see audiobooks.undoHold.)
//
// The index is a snapshot of one prefix scan of the journals and is read-only,
// so workers share it without a lock. A snapshot goes stale when a combine
// journals afterwards: Stale reports that (CombineJournalGeneration moved since
// the scan began), and a caller that decides under the merge lock rebuilds
// before trusting it. A purge that trusted a stale snapshot would delete the
// shell of a combine journaled during the pass.
type UndoShellIndex struct {
	// gen is CombineJournalGeneration read BEFORE the scan began, so a journal
	// written while or after the scan ran changes it.
	gen uint64
	// stamps maps an absorbed book id to the soft-delete stamps open journals
	// recorded for it (more than one when it was retired by several combines).
	stamps map[string][]stampedShell
}

type stampedShell struct {
	stamp     time.Time
	journalID string
}

// NewUndoShellIndex scans the combine journals once and indexes the shells of
// every journal whose undo can still run. A read error is returned: the purge
// must fail closed rather than treat "cannot tell" as "nothing is held".
func NewUndoShellIndex(db journalKVReader) (*UndoShellIndex, error) {
	gen := CombineJournalGeneration()
	journals, err := listCombineJournals(db, 0)
	if err != nil {
		return nil, fmt.Errorf("list combine journals to find shells whose undo is still possible: %w", err)
	}
	idx := &UndoShellIndex{gen: gen, stamps: map[string][]stampedShell{}}
	for i := range journals {
		j := &journals[i]
		if !combineJournalUndoable(j) {
			continue
		}
		for _, a := range j.Absorbed {
			if a.LeftLive || a.MarkedForDeletionAt == nil {
				continue // never retired, or not stamped: undo restores nothing for it
			}
			idx.stamps[a.BookID] = append(idx.stamps[a.BookID], stampedShell{stamp: *a.MarkedForDeletionAt, journalID: j.ID})
		}
	}
	return idx, nil
}

// combineJournalUndoable reports whether UndoCombine can still run for j:
// the statuses undoPreconditions accepts (applied, undo_failed, and a pending
// journal that recorded every step it ran).
func combineJournalUndoable(j *CombineJournal) bool {
	switch j.Status {
	case CombineJournalApplied, CombineJournalUndoFailed:
		return true
	case CombineJournalPending:
		return j.StepJournaled
	}
	return false
}

// Holds reports whether b is the retired shell of a combine that can still be
// undone, and the journal that holds it. b must be the row as it is stored now
// (the purge re-reads it under the merge lock): a book restored or re-deleted
// since the combine carries another stamp, and is not held.
func (x *UndoShellIndex) Holds(b *database.Book) (journalID string, held bool) {
	if x == nil || b == nil || !b.IsSoftDeleted() || b.MarkedForDeletionAt == nil {
		return "", false
	}
	for _, s := range x.stamps[b.ID] {
		if b.MarkedForDeletionAt.Equal(s.stamp) {
			return s.journalID, true
		}
	}
	return "", false
}

// Stale reports whether a combine journal was written after this snapshot's
// scan began, so it may be missing a held shell. A nil index is stale.
func (x *UndoShellIndex) Stale() bool {
	return x == nil || x.gen != CombineJournalGeneration()
}

// Len is how many books the index holds.
func (x *UndoShellIndex) Len() int {
	if x == nil {
		return 0
	}
	return len(x.stamps)
}
