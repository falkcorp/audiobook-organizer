// file: internal/audiobooks/undo_hold.go
// version: 1.1.0
// guid: 7b3e1c94-5a28-4d60-9f17-c2d84e60a3b5
// last-edited: 2026-10-22

package audiobooks

import (
	"errors"
	"fmt"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// undoHold answers the purge's one undo question: is this soft-deleted book
// still needed by an undo that can run, so that deleting its row would make
// that undo refuse or apply only part of itself?
//
// Three kinds of undo need a trashed book's row, and each has its own record:
//
//  1. a combine (merge.CombineJournal): UndoCombine restores the absorbed
//     book from its shell (undoShells, a snapshot of the combine journals);
//  2. an operation-ledger undo: a maintenance op (fs-regroup-xml's fragments
//     apply, retire-into, the folder-books retire) soft-deletes a shell and
//     journals it as a book_soft_delete row; reverting the op restores the
//     shell, and the revert's check (undo.CheckRestoreBook) refuses "no
//     longer exists" for a purged one, leaving the op half-reverted. The
//     rows moved onto the survivor by such an op name the shell too, so the
//     shell has to be there for the whole revert, not only its own row;
//  3. an auto-merge (database.AutoMergeJournalEntry): Engine.UnmergeAuto
//     reverts the loser from its pre-merge snapshot, and the winner from its
//     own when the entry recorded one, and refuses (writing nothing) when
//     either row is gone. A purged loser or winner therefore makes the undo
//     impossible, so both are held while the entry is open (a provisional
//     entry cannot be undone: only its loser, which is restored from the
//     trash, is held).
//
// What a hold does NOT keep, and what a purge of a held-then-released book
// costs: a purged auto-merge loser or combine shell takes its listening state
// (read status, progress, positions) with it. The combine journal and the
// auto-merge snapshot restore the BOOK RECORD and the user state they
// captured at merge time (CombineUserProgress); anything the listener did on
// that book after the merge is on the survivor, and a purge after the undo can
// no longer run does not bring it back. The hold exists so the undo, while it
// can run, is never made to refuse or half-apply; it does not make a purged
// book's later listening history recoverable.
//
// Holds fails CLOSED: any record it cannot read is an error, and the caller
// keeps the book. An unfinished by-book ledger index is the same: see
// checkLedgerReady.
//
// The ledger kind is deliberately limited to book_soft_delete rows. "Any
// unreverted change that names the book" would hold nearly every trashed book
// (every historical metadata edit is an unreverted change), and a metadata
// revert of a book that no longer exists does not damage anything it already
// reverted: those rows are refused one by one. The soft-delete row is the one
// whose revert is what brings the book back.
//
// Every writer of the records above holds the merge lock across the write
// and records BEFORE it soft-deletes: fs-regroup's retire (under
// merge.LockMergeRMW) journals its book_soft_delete row first and keeps the
// shell live when that row cannot be written, as the combine, retire-into and
// folder-books writers do; the auto-merge journal entry is written before the
// merge, with a generation counter. So a Holds called under the merge lock is
// exact.
//
// A hold is permanent until the undo runs or is retired: the operation ledger
// and the combine journals are never pruned by the log retention. That is the
// cost of "a purge must not make a revertible operation un-revertable", and it
// is shown in the trash as "kept: undo possible".
type undoHold struct {
	combine *undoShells
	store   opChangeReader
	// emb is the auto-merge journal. nil on a backend with no Pebble DB: the
	// dedup engine's journal lives in that DB (database.NewEmbeddingStoreFromStore),
	// so such a backend has no auto-merge entries to hold.
	emb *database.EmbeddingStore

	autoMu sync.Mutex
	auto   *database.AutoMergeOpenLosers
}

// opChangeReader is the operation-ledger read the hold needs. GetBookChanges
// reads the opchange_by_book: index while the store trusts it, and scans every
// journal row otherwise (see checkLedgerReady for why that is not acceptable
// here).
type opChangeReader interface {
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

// opChangeIndexState is the capability of a store whose GetBookChanges can be
// unindexed (database.PebbleStore).
type opChangeIndexState interface {
	OpChangeByBookIndexUsable() bool
}

// ErrUndoLedgerNotReady is why a purge refuses to run: the per-book index of
// the operation ledger is not built (or not verified this boot) yet, so the
// purge cannot ask "does an operation still need this book" for each trashed
// book without scanning the whole ledger once per book. Nothing is purged.
var ErrUndoLedgerNotReady = errors.New("the operation-journal by-book index is not built and verified yet, so the purge cannot tell which trashed books a maintenance operation's undo still needs; " +
	"it is built by maintenance.opchange-book-index-rebuild, which the server enqueues on every start: wait for that to finish, or start it with dry_run=false, then purge again")

// checkLedgerReady fails closed until the by-book ledger index is usable.
//
// The ledger is the only record that says a trashed shell is needed by an
// operation's undo, and it is keyed by operation, not by book. Without the
// index GetBookChanges falls back to a scan of every journal row for each
// call, which for a library-scale trash (thousands of books, each looked up
// under the merge lock) is not a slower purge but one that never finishes. And
// a store that cannot say whether its index is ready is not assumed to be:
// only a backend that has no index at all (the capability is absent) reads its
// ledger directly.
func (svc *AudiobookService) checkLedgerReady() error {
	st, ok := database.AsCapability[opChangeIndexState](svc.store)
	if !ok {
		return nil
	}
	if !st.OpChangeByBookIndexUsable() {
		return ErrUndoLedgerNotReady
	}
	return nil
}

// newUndoHold reads what the hold needs once, failing closed on any read.
func (svc *AudiobookService) newUndoHold() (*undoHold, error) {
	if err := svc.checkLedgerReady(); err != nil {
		return nil, err
	}
	ledger, ok := database.AsCapability[opChangeReader](svc.store)
	if !ok {
		return nil, errors.New("the store cannot read the operation journal, so the books a maintenance operation's undo still needs cannot be told apart")
	}
	combine, err := svc.newUndoShells()
	if err != nil {
		return nil, err
	}
	h := &undoHold{combine: combine, store: ledger, emb: database.NewEmbeddingStoreFromStore(svc.store)}
	if h.emb != nil {
		if err := h.refreshAuto(); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// refreshAuto re-reads the auto-merge journal. The caller holds autoMu (or is
// the constructor).
func (h *undoHold) refreshAuto() error {
	open, err := h.emb.OpenAutoMergeLosers()
	if err != nil {
		return fmt.Errorf("read the auto-merge journal: %w", err)
	}
	h.auto = open
	return nil
}

// Holds reports whether b is still needed by an undo that can run, and which
// record holds it (for the log). A non-nil error means it could not be
// decided: keep the book. b must be the row as it is stored now (a purge
// re-reads it under the merge lock): the checks compare its current
// marked_for_deletion_at against the stamps the records hold.
func (h *undoHold) Holds(b *database.Book) (reason string, held bool, err error) {
	if b == nil || !b.IsSoftDeleted() {
		return "", false, nil
	}
	if id, held, err := h.combine.Holds(b); err != nil || held {
		if err != nil {
			return "", false, err
		}
		return "combine journal " + id, true, nil
	}
	if key, role, held, err := h.autoHolds(b.ID); err != nil || held {
		if err != nil {
			return "", false, err
		}
		return "auto-merge journal entry " + key + " (" + role + ")", true, nil
	}
	if opID, held, err := h.ledgerHolds(b); err != nil || held {
		if err != nil {
			return "", false, err
		}
		return "operation " + opID + " (book_soft_delete)", true, nil
	}
	return "", false, nil
}

// autoHolds reports whether id is the loser (or the snapshot-restored winner)
// of an auto-merge that has not been undone, rebuilding its snapshot first when
// an entry was written since. role is "loser" or "winner".
func (h *undoHold) autoHolds(id string) (key, role string, held bool, err error) {
	if h.emb == nil {
		return "", "", false, nil
	}
	h.autoMu.Lock()
	defer h.autoMu.Unlock()
	if h.auto == nil || h.auto.Gen != database.AutoMergeJournalGeneration() {
		if err := h.refreshAuto(); err != nil {
			return "", "", false, fmt.Errorf("an auto-merge was journaled since this pass began and the journal cannot be re-read: %w", err)
		}
	}
	if key, held = h.auto.Losers[id]; held {
		return key, "loser", true, nil
	}
	if key, held = h.auto.Winners[id]; held {
		return key, "winner", true, nil
	}
	return "", "", false, nil
}

// ledgerHolds reports whether an operation's ledger still holds b: an
// unreverted, unvoided book_soft_delete row for b whose revert would act on it
// as it is now. It asks the revert's own check (undo.CheckSoftDeleteCurrent)
// so the two cannot drift: a row whose stamp no longer matches the book's
// (restored and deleted again later) is not held, and a row with no stamp (the
// note fs-regroup-xml journals) is held while the book is in the trash.
func (h *undoHold) ledgerHolds(b *database.Book) (opID string, held bool, err error) {
	if err := h.recheckLedgerReady(); err != nil {
		return "", false, err
	}
	changes, err := h.store.GetBookChanges(b.ID)
	if err != nil {
		return "", false, fmt.Errorf("read the operation journal rows of %s: %w", b.ID, err)
	}
	// A revert compares against every stamp ITS operation journaled for the
	// book, so the stamps are gathered per operation.
	byOp := map[string][]*database.OperationChange{}
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeBookSoftDelete && c.BookID == b.ID {
			byOp[c.OperationID] = append(byOp[c.OperationID], c)
		}
	}
	for op, rows := range byOp {
		stamps := undo.OpSoftDeleteStamps(rows)
		for _, c := range rows {
			if c.RevertedAt != nil || c.Voided {
				continue
			}
			if undo.CheckSoftDeleteCurrent(b, c, stamps) == nil {
				return op, true, nil
			}
		}
	}
	return "", false, nil
}

// recheckLedgerReady is checkLedgerReady against the store the hold was built
// from: a rebuild that started mid-pass clears the trust, and the lookups then
// scan the whole journal.
func (h *undoHold) recheckLedgerReady() error {
	if st, ok := database.AsCapability[opChangeIndexState](h.store); ok && !st.OpChangeByBookIndexUsable() {
		return ErrUndoLedgerNotReady
	}
	return nil
}
