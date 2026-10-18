// file: internal/database/review_apply_outcome.go
// version: 1.0.0
// guid: 5c1e8b7a-3d92-4f60-a4b8-2e7d90c6f315
// last-edited: 2026-10-18

package database

import "errors"

// NothingWrittenError marks a review-apply failure that provably changed nothing:
// the apply refused or failed before its first persistent write. It is the ONLY way
// an apply failure is handed back (the review item released to the status it was
// claimed from, decidable again). Every other failure is HELD: the item stays
// applying with its claim blanked, exactly like a failed re-run, until an approve
// finishes it or an operator abandons it (ReviewItemApplyClaimer.HoldReviewItemClaim).
//
// 🔴 THE DEFAULT IS HELD, ON PURPOSE. A plain error, an opaque call that may or may
// not have written, and a panic all mean "cannot tell", and the safe direction is to
// assume the library changed. A handler therefore has to ASSERT cleanliness, and
// asserts it only at a point it can show no write has landed; a handler that forgets
// to classify a new failure path holds the item rather than hand a half-changed
// library back as decidable.
type NothingWrittenError struct {
	// Err is the underlying failure; never nil on an error made by NothingWritten.
	Err error
}

func (e *NothingWrittenError) Error() string {
	if e.Err == nil {
		return "apply failed before writing anything"
	}
	return e.Err.Error()
}

func (e *NothingWrittenError) Unwrap() error { return e.Err }

// NothingWritten marks err as an apply failure that wrote nothing. A nil err stays
// nil and an already-marked one is returned as is.
func NothingWritten(err error) error {
	if err == nil || IsNothingWritten(err) {
		return err
	}
	return &NothingWrittenError{Err: err}
}

// IsNothingWritten reports whether err carries the nothing-written marker anywhere
// in its chain. A nil error is not one.
func IsNothingWritten(err error) bool {
	_, ok := errors.AsType[*NothingWrittenError](err)
	return ok
}
