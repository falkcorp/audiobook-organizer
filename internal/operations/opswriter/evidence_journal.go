// file: internal/operations/opswriter/evidence_journal.go
// version: 1.0.0
// guid: 3d79460b-a54c-4691-ba04-dc81be729043
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeScanner is the whole-journal read RowApplied falls back to while
// the opchange_by_book index is not usable (database.PebbleStore). Until
// EnsureOpChangeByBookIndex trusts the index at boot, every GetBookChanges
// is itself a scan of every opchange row -- and a resumed run reaches its
// resume checks before that, one RowApplied per row, up to a chunk's worth
// in a burst. Optional: without it every book is read with GetBookChanges.
type ChangeScanner interface {
	OpChangeByBookIndexUsable() bool
	ScanOperationChanges(fn func(*database.OperationChange) error) error
}

// journalRow is the part of an operation journal row the evidence check
// compares: who wrote it, on which book, what, and when. The values are
// left out on purpose: some writers store a whole book_file row there, and
// the memo holds every row of the journal.
type journalRow struct {
	op, id, book, field string
	at                  time.Time
}

func journalRowOf(c *database.OperationChange) journalRow {
	return journalRow{op: c.OperationID, id: c.ID, book: c.BookID, field: c.FieldName, at: c.CreatedAt}
}

// journalMemo is one whole-journal scan, grouped by book, kept for the
// Writer's run while the index is not usable (bookJournal).
type journalMemo struct {
	mu     sync.Mutex
	built  bool
	err    error // a decode failure the scan stopped on, kept: rescanning finds it again
	byBook map[string][]journalRow
	scans  int // scans run, for tests
}

// journalScanCtxEvery is how many rows the scan reads between checks of the
// caller's context.
const journalScanCtxEvery = 4096

// bookJournal returns every operation journal row of bookID. While the
// store's opchange_by_book index is usable it is one GetBookChanges (index
// reads, fresh every call). While it is not, and the store has a
// ChangeScanner, the first call scans the whole journal once and every
// later call of this Writer reuses that scan, until the index becomes
// usable. The memo does not see journal rows recorded after its scan; a
// write the run itself makes in the meantime only matters to rows whose
// fresh values it already changed, and a foreign one is missed until the
// index is trusted -- the price of not scanning 300k rows per row checked.
// A scan the context stops is not kept (the next call scans again); one a
// row that does not decode stops is kept as that error.
func (w *Writer) bookJournal(ctx context.Context, bookID string) ([]journalRow, error) {
	sc := w.cfg.ChangeScan
	if sc == nil || sc.OpChangeByBookIndexUsable() {
		rows, err := w.cfg.Changes.GetBookChanges(bookID)
		if err != nil {
			return nil, err
		}
		out := make([]journalRow, len(rows))
		for i, c := range rows {
			out[i] = journalRowOf(c)
		}
		return out, nil
	}
	m := &w.journalMemo
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.built {
		byBook := map[string][]journalRow{}
		n := 0
		m.scans++
		err := sc.ScanOperationChanges(func(c *database.OperationChange) error {
			if n++; n%journalScanCtxEvery == 0 && ctx != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if c.BookID != "" {
				byBook[c.BookID] = append(byBook[c.BookID], journalRowOf(c))
			}
			return nil
		})
		switch {
		case err == nil:
			m.byBook = byBook
		case undecodable(err):
			m.err = err
		default:
			return nil, err
		}
		m.built = true
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.byBook[bookID], nil
}
