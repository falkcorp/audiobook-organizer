// file: internal/operations/opswriter/evidence_journal.go
// version: 1.1.0
// guid: 3d79460b-a54c-4691-ba04-dc81be729043
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"math"
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
// compares: what it wrote, when, and -- only for a row of this Writer's own
// operation -- its id (a named row is recognized by it). Everything else
// is left out on purpose, because the memo holds every row of the journal:
// the values (some writers store a whole book_file row there), the book
// (the memo's map key), and a foreign row's op and id (only "not this run"
// matters). That is 48 bytes a row plus the field name's bytes (and the
// id's, on the run's own rows); it was 88 with the book, the op and a
// time.Time. Measured with synthetic rows whose strings the store already
// holds, so string bytes excluded: the kept scan went from 34 MB to 19 MB
// at 300k rows and from 126 MB to 75 MB at 1M. A real scan decodes each
// field name into its own allocation, which adds roughly its length (about
// 45 bytes for a book_file:<id> name) a row on top.
type journalRow struct {
	id, field string
	at        int64 // CreatedAt, Unix nanoseconds; math.MinInt64 for none
	own       bool  // written under this Writer's op id
}

func journalRowOf(c *database.OperationChange, opID string) journalRow {
	r := journalRow{field: c.FieldName, at: math.MinInt64}
	if !c.CreatedAt.IsZero() {
		r.at = c.CreatedAt.UnixNano()
	}
	if c.OperationID == opID {
		r.own, r.id = true, c.ID
	}
	return r
}

// stamp is the row's CreatedAt (the oldest instant when it had none).
func (r journalRow) stamp() time.Time { return time.Unix(0, r.at) }

// journalMemoMaxAge is how long one whole-journal scan serves a Writer
// before the next read scans again. The scan cannot see rows recorded after
// it, so this bounds how stale a check can be while the index stays
// untrusted -- for the whole run if trust fails at boot. The cost it bounds
// on the other side: one scan reads every opchange row, measured at about
// 0.06 s for 300k rows and 0.15 s for 1M (synthetic, without the JSON
// decode a real scan adds, which multiplies it a few times). One minute keeps a
// resumed run that checks rows continuously at a few percent of its time in
// scans, and a foreign write made during the run is seen within a minute.
const journalMemoMaxAge = time.Minute

// journalMemo is the whole-journal scan, grouped by book, a Writer keeps
// while the index is not usable (bookJournal). One scan runs at a time;
// other readers wait for it, each on its own context.
type journalMemo struct {
	mu      sync.Mutex
	built   bool
	builtAt time.Time
	err     error // a decode failure the scan stopped on, kept like a result
	byBook  map[string][]journalRow
	gen     uint64      // bumped by reset: a scan started before it is not kept
	flight  *memoFlight // the scan in progress, nil when none
	scans   int         // scans run, for tests
	now     func() time.Time
}

// memoFlight is one scan in progress; done is closed when it ends.
type memoFlight struct{ done chan struct{} }

func (m *journalMemo) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// reset drops a kept scan, so the next read while the index is not usable
// scans again. A scan in progress finishes for its own reader but is not
// kept.
func (m *journalMemo) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	m.built, m.err, m.byBook = false, nil, nil
}

// journalScanCtxEvery is how many rows the scan reads between checks of the
// caller's context.
const journalScanCtxEvery = 4096

// bookJournal returns every operation journal row of bookID. While the
// store's opchange_by_book index is usable it is one GetBookChanges (index
// reads, fresh every call), and any kept scan is dropped. While it is not,
// and the store has a ChangeScanner, it is served from one whole-journal
// scan (journalMemoBook).
func (w *Writer) bookJournal(ctx context.Context, bookID string) ([]journalRow, error) {
	sc := w.cfg.ChangeScan
	if sc == nil || sc.OpChangeByBookIndexUsable() {
		if sc != nil {
			// The index is usable now: drop the scan (tens of MB on a
			// journal of several hundred thousand rows; journalRow) rather
			// than hold it.
			w.journalMemo.reset()
		}
		rows, err := w.cfg.Changes.GetBookChanges(bookID)
		if err != nil {
			return nil, err
		}
		out := make([]journalRow, len(rows))
		for i, c := range rows {
			out[i] = journalRowOf(c, w.cfg.OpID)
		}
		return out, nil
	}
	return w.journalMemoBook(ctx, sc, bookID)
}

// journalMemoBook serves bookID from the memo. A scan younger than
// journalMemoMaxAge is reused; an older one, or none, is replaced by a new
// scan, which the first reader to need it runs (outside the lock, the old
// scan dropped first so two never coexist) while later readers wait for it
// or for their own context, whichever ends first. The memo cannot see rows
// recorded after its scan began: a foreign write in that window is missed
// by checks served from it, for up to journalMemoMaxAge. A scan whose
// reader's context stopped it is not kept and is not an answer for the
// readers waiting on it: they retry, and one of them scans. One a row that
// does not decode stops is kept as that error, until it ages out.
func (w *Writer) journalMemoBook(ctx context.Context, sc ChangeScanner, bookID string) ([]journalRow, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m := &w.journalMemo
	for {
		m.mu.Lock()
		if m.built && m.clock().Sub(m.builtAt) < journalMemoMaxAge {
			rows, err := m.byBook[bookID], m.err
			m.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return rows, nil
		}
		if f := m.flight; f != nil {
			m.mu.Unlock()
			select {
			case <-f.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		f := &memoFlight{done: make(chan struct{})}
		m.flight = f
		m.built, m.err, m.byBook = false, nil, nil
		gen, at := m.gen, m.clock()
		m.scans++
		m.mu.Unlock()

		byBook, err := scanJournal(ctx, sc, w.cfg.OpID)

		m.mu.Lock()
		m.flight = nil
		if m.gen == gen && (err == nil || undecodable(err)) {
			m.built, m.builtAt, m.err, m.byBook = true, at, err, byBook
		}
		close(f.done)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return byBook[bookID], nil
	}
}

// scanJournal reads the whole journal into journalRows grouped by book,
// checking ctx every journalScanCtxEvery rows. A row with no book is left
// out: nothing reads it by book.
func scanJournal(ctx context.Context, sc ChangeScanner, opID string) (map[string][]journalRow, error) {
	byBook := map[string][]journalRow{}
	n := 0
	err := sc.ScanOperationChanges(func(c *database.OperationChange) error {
		if n++; n%journalScanCtxEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if c.BookID != "" {
			byBook[c.BookID] = append(byBook[c.BookID], journalRowOf(c, opID))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return byBook, nil
}
