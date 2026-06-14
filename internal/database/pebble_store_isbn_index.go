// file: internal/database/pebble_store_isbn_index.go
// version: 1.0.0
// guid: 7a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-06-14

// ISBN/ASIN secondary index for PebbleStore.
//
// # Key layout
//
//	book:isbn10:<value>:<bookID>  → []byte{}  (presence = membership)
//	book:isbn13:<value>:<bookID>  → []byte{}
//	book:asin:<value>:<bookID>    → []byte{}
//
// This is a set-layout index (bookID in the key, not the value) because
// multiple books can legitimately share the same ISBN/ASIN — that is precisely
// the dedup signal. Compare with the single-value book:hash: index where the
// value is the bookID.
//
// # Maintenance
//
// Index rows are written and deleted inside the same atomic pebble.Batch as
// the book row itself (CreateBook / UpdateBook / DeleteBook).  UpdateBook
// loads the prior book first (it already does this for path-index maintenance)
// and uses updateISBNIndex to delete stale rows and write fresh ones when a
// value changes.
//
// # Build flag
//
// GetBookIDsByISBNASIN should only be called when IsISBNIndexBuilt() returns
// true.  Before the backfill op runs, the index is absent for all pre-existing
// books.  Callers gate on this flag; the build_isbn_index op sets it.

package database

import (
	"fmt"
	"log/slog"

	"github.com/cockroachdb/pebble/v2"
)

// isbnIndexBuiltFlagKey is stored in Settings when the isbn-index-build op
// completes.  The v1 suffix lets us force a re-run by bumping to v2.
const isbnIndexBuiltFlagKey = "system:flag:book_isbn_index_v1_done"

// derefStrISBN is a nil-safe string pointer deref used in ISBN index maintenance.
// Returns "" for nil pointers.
func derefStrISBN(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// IsISBNIndexBuilt reports whether the isbn-index-build backfill op has
// completed on this database.  Callers that use GetBookIDsByISBNASIN must
// gate on this flag; an unbuilt index silently returns empty results.
func (p *PebbleStore) IsISBNIndexBuilt() bool {
	setting, err := p.GetSetting(isbnIndexBuiltFlagKey)
	return err == nil && setting != nil && setting.Value == "true"
}

// SetISBNIndexBuilt marks the isbn-index-build op complete.
// Called by the build_isbn_index backfill op when the full scan finishes.
func (p *PebbleStore) SetISBNIndexBuilt() error {
	return p.SetSetting(isbnIndexBuiltFlagKey, "true", "bool", false)
}

// isbnIndexKey builds one row key for the ISBN/ASIN secondary index.
// keyspace is "isbn10", "isbn13", or "asin".
func isbnIndexKey(keyspace, value, bookID string) []byte {
	return []byte(fmt.Sprintf("book:%s:%s:%s", keyspace, value, bookID))
}

// writeISBNIndexRows adds index rows for all non-empty ISBN10/ISBN13/ASIN
// values to the supplied batch.  No-op for empty values.
func writeISBNIndexRows(batch *pebble.Batch, bookID, isbn10, isbn13, asin string) error {
	if isbn10 != "" {
		if err := batch.Set(isbnIndexKey("isbn10", isbn10, bookID), []byte{}, nil); err != nil {
			return fmt.Errorf("pebble isbn10 index set: %w", err)
		}
	}
	if isbn13 != "" {
		if err := batch.Set(isbnIndexKey("isbn13", isbn13, bookID), []byte{}, nil); err != nil {
			return fmt.Errorf("pebble isbn13 index set: %w", err)
		}
	}
	if asin != "" {
		if err := batch.Set(isbnIndexKey("asin", asin, bookID), []byte{}, nil); err != nil {
			return fmt.Errorf("pebble asin index set: %w", err)
		}
	}
	return nil
}

// deleteISBNIndexRows removes index rows for all non-empty ISBN10/ISBN13/ASIN
// values from the supplied batch.  Missing keys are a no-op in pebble.
func deleteISBNIndexRows(batch *pebble.Batch, bookID, isbn10, isbn13, asin string) error {
	if isbn10 != "" {
		if err := batch.Delete(isbnIndexKey("isbn10", isbn10, bookID), nil); err != nil {
			return fmt.Errorf("pebble isbn10 index delete: %w", err)
		}
	}
	if isbn13 != "" {
		if err := batch.Delete(isbnIndexKey("isbn13", isbn13, bookID), nil); err != nil {
			return fmt.Errorf("pebble isbn13 index delete: %w", err)
		}
	}
	if asin != "" {
		if err := batch.Delete(isbnIndexKey("asin", asin, bookID), nil); err != nil {
			return fmt.Errorf("pebble asin index delete: %w", err)
		}
	}
	return nil
}

// updateISBNIndex is the UPDATE helper: deletes stale rows for old values and
// writes fresh rows for new values, only when a value has actually changed.
// Mirrors the updateHashIndex closure in UpdateBook.
func updateISBNIndex(batch *pebble.Batch, bookID string, oldISBN10, newISBN10, oldISBN13, newISBN13, oldASIN, newASIN string) error {
	updateField := func(keyspace, oldVal, newVal string) error {
		if oldVal == newVal {
			return nil // unchanged — no index work needed
		}
		if oldVal != "" {
			if err := batch.Delete(isbnIndexKey(keyspace, oldVal, bookID), nil); err != nil {
				return fmt.Errorf("pebble %s index delete old: %w", keyspace, err)
			}
		}
		if newVal != "" {
			if err := batch.Set(isbnIndexKey(keyspace, newVal, bookID), []byte{}, nil); err != nil {
				return fmt.Errorf("pebble %s index set new: %w", keyspace, err)
			}
		}
		return nil
	}

	if err := updateField("isbn10", oldISBN10, newISBN10); err != nil {
		return err
	}
	if err := updateField("isbn13", oldISBN13, newISBN13); err != nil {
		return err
	}
	return updateField("asin", oldASIN, newASIN)
}

// WriteISBNIndexForBook writes the isbn10/isbn13/asin secondary-index rows for
// a single book using a one-shot Pebble batch.  Intended for the backfill op
// only; ongoing index maintenance goes through CreateBook/UpdateBook/DeleteBook.
// Calling this on a book that already has index rows is harmless (idempotent
// Set).  Empty values are skipped.
func (p *PebbleStore) WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error {
	batch := p.db.NewBatch()
	if err := writeISBNIndexRows(batch, bookID, isbn10, isbn13, asin); err != nil {
		batch.Close()
		return fmt.Errorf("WriteISBNIndexForBook %s: %w", bookID, err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("WriteISBNIndexForBook %s commit: %w", bookID, err)
	}
	return nil
}

// GetBookIDsByISBNASIN returns the union of book IDs whose ISBN10, ISBN13, or
// ASIN matches any of the supplied non-empty values.  Each non-empty argument
// triggers a prefix scan of its index namespace; results are deduplicated.
//
// This method should only be called when IsISBNIndexBuilt() is true.  Before
// the backfill op runs, books created before the index existed have no index
// rows and will be silently missed.
func (p *PebbleStore) GetBookIDsByISBNASIN(isbn10, isbn13, asin string) ([]string, error) {
	seen := make(map[string]struct{})

	scanPrefix := func(keyspace, value string) error {
		if value == "" {
			return nil
		}
		// Key format: book:<keyspace>:<value>:<bookID>
		// We scan from "book:<keyspace>:<value>:" to "book:<keyspace>:<value>:~"
		// (prefixEnd appends +1 to last byte).
		prefix := []byte(fmt.Sprintf("book:%s:%s:", keyspace, value))
		upper := prefixEnd(prefix)

		iter, err := p.db.NewIter(&pebble.IterOptions{
			LowerBound: prefix,
			UpperBound: upper,
		})
		if err != nil {
			return fmt.Errorf("pebble isbn index iter %s=%q: %w", keyspace, value, err)
		}
		defer func() {
			if cerr := iter.Close(); cerr != nil {
				slog.Warn("pebble isbn index iter close", "keyspace", keyspace, "error", cerr)
			}
		}()

		for iter.First(); iter.Valid(); iter.Next() {
			key := string(iter.Key())
			// key = "book:<keyspace>:<value>:<bookID>"
			// bookID is everything after the last ':'
			// We already know the prefix is "book:<keyspace>:<value>:", so we can
			// strip the prefix directly.
			if len(key) <= len(prefix) {
				continue // malformed key — skip
			}
			bookID := key[len(prefix):]
			if bookID != "" {
				seen[bookID] = struct{}{}
			}
		}
		return nil
	}

	if err := scanPrefix("isbn10", isbn10); err != nil {
		return nil, err
	}
	if err := scanPrefix("isbn13", isbn13); err != nil {
		return nil, err
	}
	if err := scanPrefix("asin", asin); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}
