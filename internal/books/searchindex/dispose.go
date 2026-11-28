// file: internal/books/searchindex/dispose.go
// version: 0.1.0
// guid: 5e0a8c3b-2f64-4d19-9b7e-84c1d6a3f205
// last-edited: 2026-11-28

package searchindex

import (
	"errors"

	"github.com/falkcorp/mediacore/storage/index"
)

// Disposition is what the search handler does with an error from the index's
// Search, Count or Facets.
type Disposition uint8

const (
	// Failed is an error the search cannot answer around: a 5xx.
	Failed Disposition = iota
	// Narrow is a query that is valid but expands past the engine's term cap
	// (index.ErrTooBroad: a short prefix or leading wildcard over a large field).
	// The handler answers it with the 400-class bad-query answer, with
	// NarrowMessage. It is not a failure: a narrower query answers, so there is
	// no log-as-error and no fallback.
	Narrow
	// Fallback is an index that cannot answer at all (index.ErrDegraded: it
	// failed to open). The handler serves the substring path, as it does while
	// the index is rebuilding.
	Fallback
)

// NarrowMessage is the text of the Narrow answer.
const NarrowMessage = "the search matches too many terms to run; narrow your search (use a longer prefix or more specific words)"

// Dispose classifies err from the index.
//
// index.ErrTooBroad is checked first and is never a Fallback: serving the
// substring path for it would run the very scan the engine just refused, over
// every book, and would hide from the person that their query is the problem.
// Only an index that is itself unavailable falls back.
func Dispose(err error) Disposition {
	switch {
	case errors.Is(err, index.ErrTooBroad):
		return Narrow
	case errors.Is(err, index.ErrDegraded):
		return Fallback
	}
	return Failed
}
