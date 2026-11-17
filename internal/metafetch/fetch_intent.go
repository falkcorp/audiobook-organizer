// file: internal/metafetch/fetch_intent.go
// version: 1.0.0
// guid: fd965b79-3063-458c-b45b-bfefdbef80af
// last-edited: 2026-11-17

package metafetch

import (
	"fmt"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// FetchIntent says who asked for one FetchMetadataForBook or
// FetchMetadataForBookByTitle call. Both functions search the providers and
// APPLY the best match with nobody picking it, so whether the book may be
// retired (in the trash, or absorbed into another book) depends on the caller,
// not on the function. Every call site states which it is: the argument is
// required, and it has no default.
type FetchIntent int

const (
	// Interactive is a person's own action on this one book (the Fetch
	// button, and its fetch queued behind a library scan). A retired book is
	// fetched like any other: the owner is working on it by hand.
	Interactive FetchIntent = iota + 1

	// Unattended is a job working through books nobody picked one by one
	// (organize's fetch-first, the iTunes import enrichment, the
	// production-author resolvers). A retired book is refused with
	// ErrBookRetired and nothing is searched or written. The caller reports it
	// as skipped, not failed.
	Unattended
)

// String names the intent for logs and errors.
func (i FetchIntent) String() string {
	switch i {
	case Interactive:
		return "interactive"
	case Unattended:
		return "unattended"
	default:
		return fmt.Sprintf("FetchIntent(%d)", int(i))
	}
}

// checkFetchIntent is the one place the intent is turned into a refusal. It
// runs on the row the fetch loaded, before any provider is searched. An intent
// that is neither constant (the zero value, a cast) is an error rather than a
// guess: a caller that did not state its intent must not get either behaviour.
func checkFetchIntent(intent FetchIntent, book *database.Book) error {
	switch intent {
	case Interactive:
		return nil
	case Unattended:
		if book.IsRetired() {
			return fmt.Errorf("book %q: %w", book.Title, ErrBookRetired)
		}
		return nil
	default:
		return fmt.Errorf("metafetch: invalid fetch intent %s", intent)
	}
}

// retiredCommitGuard re-checks an unattended fetch under the book's write
// stripe, on the row as it stands: a merge or trash that landed while the
// provider search ran is not written over. An interactive fetch has no such
// guard (nil).
func retiredCommitGuard(intent FetchIntent, id string) func(*database.Book) error {
	if intent != Unattended {
		return nil
	}
	return func(fresh *database.Book) error {
		if fresh.IsRetired() {
			return fmt.Errorf("book %s: %w", id, ErrBookRetired)
		}
		return nil
	}
}
