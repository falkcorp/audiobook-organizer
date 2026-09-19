// file: internal/dedup/book_runtime.go
// version: 1.0.0
// guid: 1c5335bc-2f9e-4f1a-829a-f8ed1a96ebc5
// last-edited: 2026-09-19

package dedup

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// knownRuntimeSec is the book's canonical runtime (database.LoadBookRuntime:
// the sum over its file rows) in seconds, and whether it is COMPLETE. The
// duration collectors compare only complete runtimes: Book.Duration is a
// partial sum for a multi-file book whose chapters were not all probed, and
// comparing a 40-minute lower bound of a 10 h book against a 40-minute book
// is a false duplicate, while comparing it against the book's own 10 h copy
// misses the real one.
func knownRuntimeSec(store database.BookFilesGetter, book *database.Book) (float64, bool) {
	rt, err := database.LoadBookRuntime(store, book)
	if err != nil {
		logging.Warn(context.Background(), "dedup: book files unreadable; runtime treated as unknown",
			"book_id", book.ID, "err", err)
	}
	sec, ok := rt.KnownSeconds()
	return float64(sec), ok
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
