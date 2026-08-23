// file: internal/server/handlers/entities/author_refcount.go
// version: 1.0.0
// guid: 2bde50e3-abc9-4f90-ba06-db303a3776f2
// last-edited: 2026-08-23

package entities

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// authorRefCounts returns, per author ID, how many book rows reference it in
// ANY state — including books in the trash, non-primary (duplicate) versions,
// and co-author credits that live only in the book_authors junction table. An
// author ID absent from the map is referenced by nothing and is the only thing
// safe to delete.
//
// This exists because the obvious getter is the wrong question. Both author
// delete handlers used to guard with GetBooksByAuthorIDCore, which is the
// listing behind the books shown for an author and therefore deliberately skips
// trashed and non-primary books. Those books still hold the author_id, so an
// author whose books were all trashed, or all alternate versions, counted as
// zero and the row was deleted out from under them — and an author's name lives
// only in that row, so the reference is unrecoverable afterwards.
//
// It fails CLOSED. If the store cannot answer the unfiltered question, the
// caller must refuse to delete rather than fall back to the filtered count,
// because that fallback is precisely the bug: it deletes rows while reporting
// success. Exact mirror of seriesRefCounts, which fixed the identical defect on
// the series side after it had stranded 13,322 books behind 6,893 deleted
// series IDs in production.
func authorRefCounts(store any) (map[int]int, error) {
	refCounter := database.AsAuthorBookRefStore(store)
	if refCounter == nil {
		return nil, fmt.Errorf("store cannot count unfiltered author references (got %T); "+
			"refusing to delete from a filtered count, which silently strands "+
			"books whose author is trashed, non-primary, or a junction-only co-author", store)
	}
	return refCounter.GetAllAuthorBookRefCounts()
}
