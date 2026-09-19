// file: internal/dedup/book_runtime.go
// version: 1.0.1
// guid: 1c5335bc-2f9e-4f1a-829a-f8ed1a96ebc5
// last-edited: 2026-09-19

package dedup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// bookRuntimeMemo caches canonical runtimes for the length of one run
// (FullScan). GetBookFiles is a Pebble range scan plus a JSON decode per row,
// not a memdb read, and the duration collectors, the min-duration gate and
// the part-vs-whole gate each want the same book's runtime — per candidate
// pair, for every same-author survivor. Keyed by book ID and validated
// against the book's UpdatedAt, so a book whose aggregate was rewritten
// mid-run (a file change recomputes it) is read again. Only the small
// BookRuntime and the row count are kept, never the rows.
type bookRuntimeMemo struct {
	mu    sync.Mutex
	byID  map[string]memoEntry
	reads atomic.Int64 // GetBookFiles calls made through the memo (for tests/logs)
}

type memoEntry struct {
	updatedAt time.Time
	rt        database.BookRuntime
	rows      int
	err       error
}

func newBookRuntimeMemo() *bookRuntimeMemo {
	return &bookRuntimeMemo{byID: make(map[string]memoEntry)}
}

// size is the number of books cached.
func (m *bookRuntimeMemo) size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byID)
}

// runtimeAndRows is the canonical runtime of book and its file-row count.
// ok is false when the rows could not be read (the runtime is then unknown).
func runtimeAndRows(store database.BookFilesGetter, memo *bookRuntimeMemo, book *database.Book) (database.BookRuntime, int, bool) {
	var stamp time.Time
	if book.UpdatedAt != nil {
		stamp = *book.UpdatedAt
	}
	if memo != nil {
		memo.mu.Lock()
		e, hit := memo.byID[book.ID]
		memo.mu.Unlock()
		if hit && e.updatedAt.Equal(stamp) {
			return e.rt, e.rows, e.err == nil
		}
	}
	var (
		files []database.BookFile
		err   error
	)
	if store == nil {
		err = errNoRuntimeStore
	} else {
		files, err = store.GetBookFiles(book.ID)
		if memo != nil {
			memo.reads.Add(1)
		}
	}
	rt := database.ComputeBookRuntime(book, files)
	if err != nil {
		logging.Warn(context.Background(), "dedup: book files unreadable; runtime treated as unknown",
			"book_id", book.ID, "err", err)
		rt = database.BookRuntime{Source: database.RuntimeSourceNone, BookAggregateSec: rt.BookAggregateSec}
	}
	if memo != nil {
		memo.mu.Lock()
		memo.byID[book.ID] = memoEntry{updatedAt: stamp, rt: rt, rows: len(files), err: err}
		memo.mu.Unlock()
	}
	return rt, len(files), err == nil
}

var errNoRuntimeStore = errors.New("no book file store")

// knownRuntimeSec is the book's canonical runtime in seconds, and whether it
// is COMPLETE. The duration collectors compare only complete runtimes:
// Book.Duration is a partial sum for a multi-file book whose chapters were
// not all probed (or are missing from disk), and comparing a 40-minute lower
// bound of a 10 h book against a 40-minute book is a false duplicate, while
// comparing it against the book's own 10 h copy misses the real one.
func knownRuntimeSec(store database.BookFilesGetter, memo *bookRuntimeMemo, book *database.Book) (float64, bool) {
	rt, _, _ := runtimeAndRows(store, memo, book)
	sec, ok := rt.KnownSeconds()
	return float64(sec), ok
}

// runtimeMemo is the engine's run-scoped memo, or nil outside a run.
func (de *Engine) runtimeMemo() *bookRuntimeMemo { return de.rtMemo.Load() }

// shortRuntime reports whether a canonical runtime is COMPLETE and strictly
// under minFingerprintMatchSeconds. Unknown and partial runtimes are never
// short: a partial runtime is a lower bound, and a book with one probed
// 45-second intro and its chapters unprobed or missing is not a 45-second
// book.
func shortRuntime(rt database.BookRuntime) bool {
	sec, known := rt.KnownSeconds()
	return known && sec < minFingerprintMatchSeconds
}

// partVsWholeRuntime reports whether one side is a single-file PART of the
// other side's multi-file WHOLE by runtime: the part (exactly one row) has a
// COMPLETE runtime under partVsWholeDurationRatioMax of the whole's (at least
// two rows). The whole may be partial — its Seconds is the sum of its known
// counted rows, a lower bound whenever its unmatched missing rows are real
// chapters — so the test fires no more often than an exact measurement would.
func partVsWholeRuntime(rtA database.BookRuntime, rowsA int, rtB database.BookRuntime, rowsB int) bool {
	var part, whole database.BookRuntime
	switch {
	case rowsA == 1 && rowsB >= 2:
		part, whole = rtA, rtB
	case rowsB == 1 && rowsA >= 2:
		part, whole = rtB, rtA
	default:
		return false
	}
	partSec, partKnown := part.KnownSeconds()
	if !partKnown || whole.Source != database.RuntimeSourceFiles || whole.Seconds <= 0 {
		return false
	}
	return float64(partSec) < partVsWholeDurationRatioMax*float64(whole.Seconds)
}

// durationPct is the symmetric fractional difference of two runtimes.
func durationPct(a, b float64) float64 {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	base := a
	if b > base {
		base = b
	}
	return diff / base
}

// prefilterOtherDuration is the cheap in-memory screen the duration
// collectors run over an author's whole catalogue before paying a file read:
// the other book's stored aggregate against this book's canonical runtime.
// It can only DROP pairs. A book whose aggregate is a partial sum screens out
// as "too different", which loses a possible signal but can never create a
// false one, because every survivor is re-measured on its canonical runtime.
func prefilterOtherDuration(bookDur float64, other *database.Book, threshold float64) bool {
	if other.Duration == nil || *other.Duration <= 0 {
		return false
	}
	return durationPct(bookDur, float64(*other.Duration)) < threshold
}
