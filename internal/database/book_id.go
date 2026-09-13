// file: internal/database/book_id.go
// version: 1.0.0
// guid: 3f8a1c52-7d4e-4b09-9e61-2a5c0d8b7f14
// last-edited: 2026-09-13

package database

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidBookID is returned (wrapped) by CreateBook when a caller-supplied
// book ID cannot be stored safely as a Pebble key. Match it with errors.Is.
var ErrInvalidBookID = errors.New("invalid book id")

// ValidateBookID reports whether id is safe to use as the <id> in a
// "book:<id>" Pebble key.
//
// A book ID must not contain ':'. Every book-keyed scan in this package
// assumes the ID is the single segment after "book:" — the version-group
// backfill's structural filter skips any key with more than one colon
// (strings.Count(key, ":") != 1), and secondary index keys of the form
// "book:<id>:<suffix>" share the "book:" keyspace, so an ID like "a:b" is
// indistinguishable from the index row for book "a". Minted ULIDs never
// contain a colon; this only rejects caller-supplied IDs (PEBBLE-KEY-BOUND-
// CENSUS, #2896). Enforcing it at the one place IDs enter the store makes the
// invariant true by construction instead of by luck at every reader.
//
// The empty string is not validated here: CreateBook mints a ULID for it.
func ValidateBookID(id string) error {
	if strings.Contains(id, ":") {
		return fmt.Errorf("%w %q: must not contain ':' (it is the Pebble key separator)", ErrInvalidBookID, id)
	}
	return nil
}
