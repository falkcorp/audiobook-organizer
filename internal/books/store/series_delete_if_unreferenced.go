// file: internal/books/store/series_delete_if_unreferenced.go
// version: 1.0.0
// guid: 70d67056-7e94-4c44-b37f-39897839efed
// last-edited: 2026-11-17

package store

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// seriesDeleteLog carries DeleteSeriesIfUnreferenced's diagnostics through the
// log-injection barrier.
var seriesDeleteLog = logger.New("database.series-delete")

// ErrSeriesNotFound: no series has that id.
var ErrSeriesNotFound = errors.New("series not found")

// SeriesUnreferencedDeleter is the delete a maintenance op uses when it has
// emptied a series itself and wants the row gone only if that is still true.
// A separate one-method capability, resolved with AsCapability, for the reason
// SeriesBookRefCounter is: adding it to Store would grow every implementation
// and generated mock for one caller. *PebbleStore implements it.
type SeriesUnreferencedDeleter interface {
	// DeleteSeriesIfUnreferenced deletes series id if no book row of any state
	// names it at the moment of deletion, and reports whether it did. A series
	// something still references is kept: (false, nil). Nothing is written then.
	// It returns ErrSeriesNotFound for an id with no row, ErrSeriesHeld for a
	// held row, and an error when the reference count cannot be trusted (a
	// memdb known to be missing rows, or not serving): never a delete on a
	// guess.
	DeleteSeriesIfUnreferenced(id int) (deleted bool, err error)
}

var _ SeriesUnreferencedDeleter = (*PebbleStore)(nil)

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
// WHAT IT IS NOT ATOMIC AGAINST:
//   - A writer that read the series row EARLIER and writes series_id after the
//     delete. Book writes do not check that the series they name exists, so a
//     writer holding a stale "series 7 exists" runs its commit once the
//     stripes are free and strands its own book. The window this method closes
//     is count-to-delete; the window between a caller's own read of the series
//     and its write is the caller's, and closing it needs the book write to
//     verify the series under nameIdx.series (a change to every writer, not
//     made here).
//   - The caller's earlier reads. A snapshot taken before this call (the name
//     and author an undo row records) can be out of date by the time the row
//     is deleted: a rename between the caller's read and this call is not seen.
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
func (p *PebbleStore) DeleteSeriesIfUnreferenced(id int) (bool, error) {
	deleted, err := p.deleteSeriesIfUnreferencedLocked(id)
	if deleted {
		p.notifySeriesRenamed(id)
	}
	return deleted, err
}

func (p *PebbleStore) deleteSeriesIfUnreferencedLocked(id int) (bool, error) {
	unlockBooks := p.lockEveryBook()
	defer unlockBooks()
	p.nameIdx.series.Lock()
	defer p.nameIdx.series.Unlock()

	if _, closer, err := p.db.Get([]byte(fmt.Sprintf("series:%d", id))); err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, fmt.Errorf("series %d: %w", id, ErrSeriesNotFound)
		}
		return false, fmt.Errorf("read series %d: %w", id, err)
	} else {
		closer.Close()
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
		return 0, fmt.Errorf("count references to series %d: the memdb is not serving, and a library scan under every book lock is refused", id)
	}
	counts, err := p.getAllSeriesBookRefCountsPebble()
	if err != nil {
		return 0, fmt.Errorf("count references to series %d: %w", id, err)
	}
	return counts[id], nil
}
