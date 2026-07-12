// file: internal/metafetch/cache_test.go
// version: 1.0.0
// guid: 7f2a1c4e-9b3d-4e6a-8c1f-2d5b8a0e3f61
// last-edited: 2026-07-11

// Package metafetch tests for INIT-3-T5: ValidateCachedIdentity makes the
// cache's existing SourceHash load-bearing at apply time, closing the TOCTOU
// window where the book's search inputs drift between the cache write and the
// apply. The three-case fail-open/fail-closed semantics are exercised here,
// where the unexported hashSearchInputs is reachable to build matching hashes.
package metafetch

import (
	"errors"
	"testing"
)

func TestValidateCachedIdentity(t *testing.T) {
	var mfs *Service // nil Service is fine — ValidateCachedIdentity never touches mfs.db.

	const (
		bookID   = "book-1"
		query    = "The Stable Book"
		author   = "Stable Author"
		narrator = "Sam Narrator"
		series   = "Stable Series"
	)

	t.Run("match returns nil", func(t *testing.T) {
		entry := &MetadataCandidateCache{
			BookID:     bookID,
			SourceHash: hashSearchInputs(bookID, query, author, narrator, series),
		}
		if err := mfs.ValidateCachedIdentity(entry, bookID, query, author, narrator, series); err != nil {
			t.Fatalf("ValidateCachedIdentity() = %v, want nil on matching hash", err)
		}
	})

	t.Run("mismatch fails closed with ErrStaleMetadataCache", func(t *testing.T) {
		entry := &MetadataCandidateCache{
			BookID:     bookID,
			SourceHash: hashSearchInputs(bookID, query, author, narrator, series),
		}
		// The book's author drifted since the cache write.
		err := mfs.ValidateCachedIdentity(entry, bookID, query, "A Different Author", narrator, series)
		if err == nil {
			t.Fatal("ValidateCachedIdentity() = nil, want ErrStaleMetadataCache on drifted inputs")
		}
		if !errors.Is(err, ErrStaleMetadataCache) {
			t.Fatalf("ValidateCachedIdentity() error = %v, want errors.Is ErrStaleMetadataCache", err)
		}
	})

	t.Run("legacy empty hash fails open", func(t *testing.T) {
		entry := &MetadataCandidateCache{BookID: bookID, SourceHash: ""}
		// Inputs are arbitrary — an empty stored hash must always apply (nil).
		if err := mfs.ValidateCachedIdentity(entry, bookID, query, author, narrator, series); err != nil {
			t.Fatalf("ValidateCachedIdentity() = %v, want nil (fail-open) on empty legacy hash", err)
		}
	})
}
