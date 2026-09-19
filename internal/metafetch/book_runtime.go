// file: internal/metafetch/book_runtime.go
// version: 1.0.0
// guid: ebab5e13-aa16-4d7d-84e0-773a72bc717e
// last-edited: 2026-09-19

package metafetch

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// bookRuntimeSec is the local runtime every candidate-scoring path compares a
// candidate's runtime against: the book's canonical runtime
// (database.LoadBookRuntime — the sum over its file rows) when it is COMPLETE,
// else 0, which every scoring function here reads as "unknown".
//
// It used to be Book.Duration, which for a multi-file book is the sum of only
// the files whose durations were probed. A 10 h book with two probed
// 20-minute chapters scored a 10 h candidate as a 93%-off runtime mismatch and
// a 40-minute one as a match. A partial runtime is a lower bound, not a total,
// so it scores as unknown: no bonus, no penalty.
func (mfs *Service) bookRuntimeSec(book *database.Book) int {
	if book == nil {
		return 0
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
	sec, _ := rt.KnownSeconds()
	return sec
}
