// file: internal/books/store/series_delete_if_unreferenced.go
// version: 1.4.0
// guid: 70d67056-7e94-4c44-b37f-39897839efed
// last-edited: 2026-11-18

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// seriesDeleteLog carries DeleteSeriesIfUnreferenced's diagnostics through the
// log-injection barrier.
var seriesDeleteLog = logger.New("database.series-delete")

// ErrSeriesNotFound: no series has that id.
var ErrSeriesNotFound = errors.New("series not found")

// ErrSeriesChanged: the series' name or author is not what the caller expected,
// so the caller's snapshot of the row (an undo row, say) is not the row that
// would have been deleted. Nothing is written.
var ErrSeriesChanged = errors.New("series changed since it was read")

// SeriesUnreferencedDeleter is the delete a maintenance op uses when it has
// emptied a series itself and wants the row gone only if that is still true.
// A separate one-method capability, resolved with AsCapability, for the reason
// SeriesBookRefCounter is: adding it to Store would grow every implementation
// and generated mock for one caller. *PebbleStore implements it.
type SeriesUnreferencedDeleter interface {
	// DeleteSeriesIfUnreferenced deletes series id if no book row of any state
	// names it at the moment of deletion, and reports whether it did. A series
	// something still references is kept: (false, nil). Nothing is written then.
	// expectName and expectAuthorID are the row the caller read (and, for an
	// undo journal, snapshotted): the delete happens only if the row still has
	// exactly that name and author, compared under the series lock.
	// It returns ErrSeriesNotFound for an id with no row, ErrSeriesChanged for a
	// row whose name or author is not the expected one, ErrSeriesHeld for a
	// held row, and an error when the reference count cannot be trusted (a
	// memdb known to be missing rows, or not serving): never a delete on a
	// guess. An error that wraps ErrMemdbIncomplete or
	// ErrSeriesDeleteMemdbNotServing is about the whole store, so every other
	// delete would fail the same way; any other error (a row that will not
	// decode, a failed write) is about this row.
	DeleteSeriesIfUnreferenced(id int, expectName string, expectAuthorID *int) (deleted bool, err error)
}

var _ SeriesUnreferencedDeleter = (*PebbleStore)(nil)

// SeriesDeleteReadiness lets a caller that will write BEFORE it deletes ask,
// once and up front, whether DeleteSeriesIfUnreferenced can count references at
// all right now. A caller that unlinks or re-homes books and only then deletes
// the emptied series would otherwise do the writes and meet the refusal after.
// A separate capability for the reason SeriesUnreferencedDeleter is. *PebbleStore
// implements it.
type SeriesDeleteReadiness interface {
	// SeriesDeleteReady returns nil when DeleteSeriesIfUnreferenced can count
	// right now, and the error it would refuse with otherwise (a memdb that is
	// not serving, or is known to be missing book rows). It writes nothing. It is a
	// snapshot, not a promise: the state can change after it returns, so a caller
	// still handles a refusal from the delete itself.
	SeriesDeleteReady() error
}

var _ SeriesDeleteReadiness = (*PebbleStore)(nil)

// SeriesDeleteReady implements SeriesDeleteReadiness. It asks the two questions
// countRefsForDelete asks before it counts, in the same order, and no more: is
// the memdb serving (or absent by configuration), and is its book table intact.
func (p *PebbleStore) SeriesDeleteReady() error {
	if m := p.memOrRefuse("SeriesDeleteReady"); m != nil {
		return m.requireTablesComplete("series reference count", memTableBooks)
	}
	if p.UseMemDB {
		return ErrSeriesDeleteMemdbNotServing
	}
	return nil
}

// ErrSeriesDeleteMemdbNotServing is wrapped by DeleteSeriesIfUnreferenced and
// SeriesDeleteReady when the store is meant to have a memdb but it is not
// serving, so the reference count would need a library scan under every book
// lock, which is refused. Like ErrMemdbIncomplete it says the store cannot count
// references to ANY series right now, not that one row is bad: a caller that
// drives many deletes stops on either, and carries on past any other error.
var ErrSeriesDeleteMemdbNotServing = errors.New("the memdb is not serving, and a library scan under every book lock is refused")

// seriesDeleteTestHookAfterCount and seriesDeleteTestHookBeforeDelete, when
// non-nil, run in DeleteSeriesIfUnreferenced with every lock the method takes
// held: the first right after the reference count, the second right before the
// row is deleted (only when the count was zero). Tests use them to put a
// competing book write into each window the locks close: count-to-lock
// acquisition, and check-to-delete. Always nil in production.
var (
	seriesDeleteTestHookAfterCount   func(id int)
	seriesDeleteTestHookBeforeDelete func(id int)
)

// DeleteSeriesIfUnreferenced implements SeriesUnreferencedDeleter.
//
// It exists because "count the references, then DeleteSeries" is two store
// calls with nothing between them: a book written into the series after the
// count and before the delete is left naming a series id that no longer
// resolves, and every reader renders that book with no series.
//
// WHAT IT TAKES, in this order: every book write stripe (lockEveryBook), then
// nameIdx.series. The stripes come first because a book writer holding one can
// be waiting on nameIdx.series (a ModifyBook callback that creates a series);
// the reverse order would deadlock with it. nameIdx.series is the lock
// DeleteSeries, CreateSeries, RenameSeriesIf and the rest of the series
// writers take. The book stripes are the lock every writer of a book row takes
// around its read and its commit. The `book:<id>` row is written by three
// functions and removed by one, and each runs with the row's lockBook stripe
// held: createBook (the row's first write); updateBookLockedMode (every update:
// UpdateBook, ModifyBook, ModifyBookCredits, ClearBookSignature and the helpers
// built on them, all of which take the stripe before calling it);
// commitBookSigMigration (rewrites the row it read under the stripe with the
// signature bytes stripped, series_id untouched; reached only from
// MigrateBookSigToSidecar); and deleteBook (the removal, which takes the stripe
// before the owner stripe). The credit writers (SetBookAuthors and its
// narrator twin) take their own stripes and do not touch the book row or its
// memdb entry's series_id. series_id lives only in the book row.
//
// WHY THE REVERSE ORDER IS NOT USED, and why this one cannot deadlock: no holder
// of nameIdx.series (CreateSeries, UpdateSeriesName, RenameSeriesIf,
// RenameSeriesIfCurrent, DeleteSeries, HoldSeries, RestoreSeries) takes a book
// stripe, an owner stripe or a credit stripe: they read and write series rows,
// the name index and the series memdb table only. The change observer they fire
// is the one place outside this package's control; the server's implementation
// queues its fan-out and returns at once, so it takes no book lock either. The
// only edge between the two families is stripe -> series (a ModifyBook callback
// that creates a series), which is the order taken here.
//
// WHAT IT IS ATOMIC AGAINST: every one of those writers. While the count and
// the delete run, no book row can be created, repointed to this series,
// repointed away or deleted, and the memdb the count reads is updated inside
// the same stripe, so the count and the rows agree. A write that was waiting
// runs after the delete.
//
// It is atomic against every write of the series row too (rename, hold,
// restore and delete all hold nameIdx.series): the name and author the caller
// passes are compared with the row under that lock, and the delete follows under
// the same hold, so a snapshot the caller built from them (an undo row) is
// exactly the row that is deleted. The compare is a defensive check, not a
// second kind of writer: the only non-test writers of a series row's author are
// CreateSeries and RestoreSeries (they set it when the row is made), and no store
// method changes it afterwards, so in practice a mismatch means a rename. A mismatch is
// ErrSeriesChanged and writes nothing; a held row is refused with ErrSeriesHeld
// by the delete.
//
// WHAT IT IS NOT ATOMIC AGAINST:
//   - A writer that read the series row EARLIER and writes series_id after the
//     delete. Book writes do not check that the series they name exists, so a
//     writer holding a stale "series 7 exists" runs its commit once the
//     stripes are free and strands its own book. The window this method closes
//     is count-to-delete; the window between a caller's own read of the series
//     and its write is the caller's, and closing it needs the book write to
//     verify the series under nameIdx.series (a change to every writer, not
//     made here).
//   - Another process on the same database, and any store that does not
//     implement this method (it is not part of Store).
//
// COST. Every book write waits while the count and the delete run: one memdb
// index read and one Pebble delete, a few milliseconds. The count is taken from
// the memdb ONLY. A store whose memdb is not serving, or is known to be missing
// rows, refuses rather than falling through to a whole-library Pebble scan
// with every book write blocked behind it (GetSeriesBookRefCount's fall-through
// is right for a caller holding no locks, and wrong here). A store built
// without a memdb (UseMemDB false: tests) counts with the Pebble scan.
//
// The rename notification fires after every lock is released.
func (p *PebbleStore) DeleteSeriesIfUnreferenced(id int, expectName string, expectAuthorID *int) (bool, error) {
	deleted, err := p.deleteSeriesIfUnreferencedLocked(id, expectName, expectAuthorID)
	if deleted {
		p.notifySeriesRenamed(id)
	}
	return deleted, err
}

func (p *PebbleStore) deleteSeriesIfUnreferencedLocked(id int, expectName string, expectAuthorID *int) (bool, error) {
	unlockBooks := p.lockEveryBook()
	defer unlockBooks()
	p.nameIdx.series.Lock()
	defer p.nameIdx.series.Unlock()

	// The row as it is NOW, read from Pebble (the authority the rename writers
	// write first), compared with the row the caller read and journaled. A change
	// between that read and this point (in practice a rename) is one the caller's
	// snapshot does not describe.
	val, closer, err := p.db.Get([]byte(fmt.Sprintf("series:%d", id)))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, fmt.Errorf("series %d: %w", id, ErrSeriesNotFound)
		}
		return false, fmt.Errorf("read series %d: %w", id, err)
	}
	var cur Series
	uerr := json.Unmarshal(val, &cur)
	closer.Close()
	if uerr != nil {
		return false, fmt.Errorf("decode series %d: %w", id, uerr)
	}
	if cur.Name != expectName || !sameSeriesAuthor(cur.AuthorID, expectAuthorID) {
		return false, fmt.Errorf("series %d is %q under %s, expected %q under %s: %w",
			id, cur.Name, authorLabel(cur.AuthorID), expectName, authorLabel(expectAuthorID), ErrSeriesChanged)
	}

	refs, err := p.seriesRefCountForDelete(id)
	if err != nil {
		return false, err
	}
	if refs != 0 {
		return false, nil
	}
	if seriesDeleteTestHookBeforeDelete != nil {
		seriesDeleteTestHookBeforeDelete(id)
	}
	if err := p.deleteSeriesRowLocked(id); err != nil {
		return false, err
	}
	return true, nil
}

// seriesRefCountForDelete is the unfiltered count of book rows naming id, read
// while every book stripe is held, so it must never be a whole-library scan on
// a store that is meant to have a memdb. The after-count test hook fires from
// in here, so it runs wherever the count runs: a count hoisted out from under
// the locks takes the hook (and the lock-held assertions made in it) with it.
func (p *PebbleStore) seriesRefCountForDelete(id int) (int, error) {
	n, err := p.countRefsForDelete(id)
	if err == nil && seriesDeleteTestHookAfterCount != nil {
		seriesDeleteTestHookAfterCount(id)
	}
	return n, err
}

func (p *PebbleStore) countRefsForDelete(id int) (int, error) {
	if m := p.memOrRefuse("DeleteSeriesIfUnreferenced"); m != nil {
		n, err := m.GetSeriesBookRefCount(id)
		if err != nil {
			if errors.Is(err, ErrMemdbIncomplete) {
				seriesDeleteLog.Error("series delete refused: the memdb is missing book rows, so its reference count could answer low: series_id=%d lost_rows=%v error=%v",
					id, m.LostRows(), err)
			}
			return 0, fmt.Errorf("count references to series %d: %w", id, err)
		}
		return n, nil
	}
	if p.UseMemDB {
		return 0, fmt.Errorf("count references to series %d: %w", id, ErrSeriesDeleteMemdbNotServing)
	}
	counts, err := p.getAllSeriesBookRefCountsPebble()
	if err != nil {
		return 0, fmt.Errorf("count references to series %d: %w", id, err)
	}
	return counts[id], nil
}

// authorLabel renders an optional author id for an error message.
func authorLabel(id *int) string {
	if id == nil {
		return "no author"
	}
	return "author " + strconv.Itoa(*id)
}
