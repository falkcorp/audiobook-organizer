// file: internal/metafetch/book_runtime.go
// version: 1.1.2
// guid: ebab5e13-aa16-4d7d-84e0-773a72bc717e
// last-edited: 2026-10-29

package metafetch

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// bookRuntimeSec is the local runtime every candidate-scoring path compares a
// candidate's runtime against: the book's canonical runtime
// (database.LoadBookRuntime — the sum over its file rows) when it is COMPLETE,
// else 0, which every scoring function here reads as "unknown". A partial
// runtime stays unknown as a TOTAL, but bookRuntime also carries the upper
// bound the file sizes put on it (see localRuntime).
//
// It used to be Book.Duration, which for a multi-file book is the sum of only
// the files whose durations were probed. A 10 h book with two probed
// 20-minute chapters scored a 10 h candidate as a 93%-off runtime mismatch and
// a 40-minute one as a match. A partial runtime is a lower bound, not a total,
// so it scores as unknown: no bonus, no penalty, with one exception: a
// candidate longer than the sizes of ALL the book's files can plausibly hold
// (localRuntime.exceedsUpper) is still a mismatch.
func (mfs *Service) bookRuntimeSec(book *database.Book) int {
	return mfs.bookRuntime(book).Sec
}

// partialBound is what a partial runtime still proves: the measured lower
// bound, the bytes-per-second estimate of the whole book, and the conservative
// upper bound (database.BookRuntime.EstimatedUpperSeconds). The zero value
// means no bound could be established (sizes missing, nothing measured).
type partialBound struct {
	LowerSec     int
	EstimateSec  int
	UpperSec     int
	FilesKnown   int
	FilesCounted int
}

// localRuntime is the local book's runtime as candidate scoring sees it: Sec is
// the complete total (0 = unknown, which includes a partial runtime), Partial
// the bound a partial runtime puts on the total. A partial runtime is never
// compared as a total -- a 10 h book with two probed chapters must not read as
// a 40-minute book -- but a candidate beyond Partial.UpperSec is longer than
// the files on disk could hold and is scored as the >100% mismatch it is.
type localRuntime struct {
	Sec     int
	Partial partialBound
	// FilesKnown / FilesCounted say how much of the book Sec covers, set even
	// when no size-based bound exists (Partial is then the zero value). Used
	// to explain WHY a runtime is unknown.
	FilesKnown, FilesCounted int
	// MeasuredSec is the summed duration of the measured files: a LOWER bound
	// on the total when partial (never a total). TotalBytes / SizesComplete
	// are the files' summed size and whether every row carried one; they let
	// an "unknown" runtime say whether sizes were the missing piece.
	MeasuredSec   int
	TotalBytes    int64
	SizesComplete bool
}

// bookRuntime is bookRuntimeSec plus the partial bound; see localRuntime.
func (mfs *Service) bookRuntime(book *database.Book) localRuntime {
	if book == nil {
		return localRuntime{}
	}
	var files database.BookFilesGetter
	if mfs != nil && mfs.db != nil {
		files = mfs.db
	}
	rt, err := database.LoadBookRuntime(files, book)
	if err != nil {
		logging.Warn(context.Background(), "metafetch: book files unreadable; runtime treated as unknown",
			"book_id", book.ID, "err", err)
	}
	return localRuntimeOf(rt)
}

// localRuntimeOf converts the canonical runtime to its scoring view.
func localRuntimeOf(rt database.BookRuntime) localRuntime {
	sec, _ := rt.KnownSeconds()
	out := localRuntime{
		Sec: sec, FilesKnown: rt.FilesKnown, FilesCounted: rt.FilesCounted,
		MeasuredSec: rt.Seconds, TotalBytes: rt.TotalBytes, SizesComplete: rt.SizesComplete,
	}
	if upper, ok := rt.EstimatedUpperSeconds(); ok {
		est, _ := rt.EstimatedSeconds()
		out.Partial = partialBound{
			LowerSec: rt.Seconds, EstimateSec: est, UpperSec: upper,
			FilesKnown: rt.FilesKnown, FilesCounted: rt.FilesCounted,
		}
	}
	return out
}

// exceedsUpper reports whether a candidate runtime is longer than the upper
// bound a PARTIAL runtime's file sizes allow. False for a complete runtime
// (compared as a total instead), for an unknown candidate runtime, and when no
// bound could be established.
func (r localRuntime) exceedsUpper(candSec int) bool {
	return r.Sec <= 0 && r.Partial.UpperSec > 0 && candSec > r.Partial.UpperSec
}

// informative reports whether the local runtime can change a candidate's
// score at all: a complete total, or a partial runtime with an upper bound.
func (r localRuntime) informative() bool { return r.Sec > 0 || r.Partial.UpperSec > 0 }
