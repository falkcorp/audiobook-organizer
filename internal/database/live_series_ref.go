// file: internal/database/live_series_ref.go
// version: 1.0.0
// guid: 3f8b1d64-9a25-4c07-b6e1-5d20c8f47a93
// last-edited: 2026-08-14

package database

import "log/slog"

// seriesRefGetter is the narrow capability DropDanglingSeriesRef needs,
// declared here rather than widening database.Store (capability-assertion
// convention, matching SoftDeletedCountStore et al).
type seriesRefGetter interface {
	GetSeriesByID(id int) (*Series, error)
}

// AsSeriesRefGetter resolves the series-lookup capability from any store
// value, unwrapping decorators, mirroring the other As*Store helpers.
func AsSeriesRefGetter(s any) seriesRefGetter {
	for s != nil {
		if g, ok := s.(seriesRefGetter); ok {
			return g
		}
		u, ok := s.(StoreUnwrapper)
		if !ok {
			return nil
		}
		s = u.Unwrap()
	}
	return nil
}

// DropDanglingSeriesRef clears book.SeriesID when it references a series that
// no longer exists, and reports whether it dropped anything (C610).
//
// WHY: several paths COPY SeriesID between book rows — the organizer's
// organized-copy CreateBook, reconcile's MergeBookMetadata fill, and
// dedup-books' keeper fill. None of them checked that the series still
// existed, so a dangling ref on any source row propagated onto freshly
// written rows forever (~12K dangling refs accumulated in production; no NEW
// phantom IDs are minted since resolveSeriesID creates-by-name, but copies
// kept spreading the old ones). Call this on the destination row before the
// write.
//
// SeriesSequence is left untouched: a sequence without a series is inert, and
// the sequence remains meaningful if the operator later re-links the series.
// A store read error is treated as "unknown, keep the ref" — dropping data on
// a transient read failure is the worse direction.
func DropDanglingSeriesRef(s any, book *Book, caller string) bool {
	if book == nil || book.SeriesID == nil {
		return false
	}
	getter := AsSeriesRefGetter(s)
	if getter == nil {
		// A store without series lookup cannot validate the ref; keeping it is
		// the recoverable direction, but say so — a silent no-op here is how
		// the copies spread unnoticed in the first place.
		slog.Warn("DropDanglingSeriesRef: store cannot look up series; keeping ref unvalidated",
			"book_id", book.ID, "caller", caller)
		return false
	}
	ser, err := getter.GetSeriesByID(*book.SeriesID)
	if err != nil {
		return false
	}
	if ser != nil {
		return false
	}
	slog.Info("dropping dangling series ref on copy",
		"book_id", book.ID, "series_id", *book.SeriesID, "caller", caller)
	book.SeriesID = nil
	return true
}
