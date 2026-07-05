// file: internal/metadata/enhanced_concurrency_test.go
// version: 1.0.0
// guid: 3f4a5b6c-7d8e-49f0-a1b2-c3d4e5f60718
// last-edited: 2026-07-05

package metadata

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/mock"
)

// TestBatchUpdateMetadata_ParallelManyItems exercises BatchUpdateMetadata's
// registry.RunItems-based worker pool (CONC-13) with enough items to force
// all metadataBulkOpConcurrency workers active concurrently, mixing success,
// validation-failure, and not-found outcomes so both the successCount++ and
// errs append aggregation paths race across goroutines. Run with `go test
// -race` (see gate in TASK-02) to confirm the mutex-guarded aggregation has
// no data race, and that the parallel pass produces the same result set as
// the original serial loop: each item's outcome depends only on its own
// BookID/Updates, never on execution order, so the expected counts below are
// exactly what the old serial `for i, update := range updates` loop would
// have produced too.
func TestBatchUpdateMetadata_ParallelManyItems(t *testing.T) {
	store := newMockStore(t)

	const (
		numOK          = 12
		numValidateErr = 5
		numNotFound    = 5
	)
	total := numOK + numValidateErr + numNotFound

	updates := make([]MetadataUpdate, 0, total)

	// numOK: real books present in the store, title update should apply and
	// succeed for every one of them regardless of goroutine interleaving.
	for i := 0; i < numOK; i++ {
		id := fmt.Sprintf("ok-book-%d", i)
		newTitle := fmt.Sprintf("Updated Title %d", i)
		book := &database.Book{ID: id, Title: fmt.Sprintf("Old Title %d", i), Format: "mp3"}
		store.EXPECT().GetBookByID(id).Return(book, nil).Once()
		store.EXPECT().UpdateBook(id, mock.MatchedBy(func(b *database.Book) bool {
			return b != nil && b.Title == newTitle
		})).Return(book, nil).Once()
		updates = append(updates, MetadataUpdate{
			BookID:  id,
			Updates: map[string]interface{}{"title": newTitle},
		})
	}

	// numValidateErr: Validate:true with an empty (required) title, fails
	// ValidateMetadata before ever touching the store.
	for i := 0; i < numValidateErr; i++ {
		id := fmt.Sprintf("bad-validate-%d", i)
		updates = append(updates, MetadataUpdate{
			BookID:   id,
			Validate: true,
			Updates:  map[string]interface{}{"title": ""},
		})
	}

	// numNotFound: GetBookByID fails for these IDs.
	for i := 0; i < numNotFound; i++ {
		id := fmt.Sprintf("missing-%d", i)
		store.EXPECT().GetBookByID(id).Return(nil, fmt.Errorf("book not found")).Once()
		updates = append(updates, MetadataUpdate{
			BookID:  id,
			Updates: map[string]interface{}{"title": "won't matter"},
		})
	}

	errs, successCount := BatchUpdateMetadata(updates, store, false)

	if successCount != numOK {
		t.Errorf("successCount = %d, want %d", successCount, numOK)
	}
	if len(errs) != numValidateErr+numNotFound {
		t.Errorf("len(errs) = %d, want %d (errs=%v)", len(errs), numValidateErr+numNotFound, errs)
	}
}

// TestImportMetadata_ParallelManyItems is the ImportMetadata analogue of
// TestBatchUpdateMetadata_ParallelManyItems: enough items to keep all
// metadataBulkOpConcurrency workers busy, mixing successful creates,
// validation failures, and malformed entries so the mutex-guarded
// importCount/errs aggregation is exercised on every path under -race.
func TestImportMetadata_ParallelManyItems(t *testing.T) {
	store := newMockStore(t)

	const (
		numOK      = 12
		numBadData = 5
		numInvalid = 5
	)

	books := make([]interface{}, 0, numOK+numBadData+numInvalid)

	for i := 0; i < numOK; i++ {
		title := fmt.Sprintf("Imported Book %d", i)
		store.EXPECT().CreateBook(mock.MatchedBy(func(b *database.Book) bool {
			return b != nil && b.Title == title
		})).Return(&database.Book{ID: fmt.Sprintf("created-%d", i), Title: title}, nil).Once()
		books = append(books, map[string]interface{}{"title": title, "format": "m4b"})
	}

	// numBadData: not a map — fails the type assertion before any store call.
	for i := 0; i < numBadData; i++ {
		books = append(books, fmt.Sprintf("not-a-map-%d", i))
	}

	// numInvalid: required "title" validation fails.
	for i := 0; i < numInvalid; i++ {
		books = append(books, map[string]interface{}{"title": ""})
	}

	data := map[string]interface{}{"books": books}

	count, errs := ImportMetadata(data, store, true)

	if count != numOK {
		t.Errorf("count = %d, want %d", count, numOK)
	}
	if len(errs) != numBadData+numInvalid {
		t.Errorf("len(errs) = %d, want %d (errs=%v)", len(errs), numBadData+numInvalid, errs)
	}
}
