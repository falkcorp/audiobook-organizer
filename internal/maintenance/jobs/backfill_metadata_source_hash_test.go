// file: internal/maintenance/jobs/backfill_metadata_source_hash_test.go
// version: 1.0.1
// guid: c3d4e5f6-a7b8-9012-cdef-012345678901
// last-edited: 2026-07-07

package jobs_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackfillMetadataSourceHashJob_Registered(t *testing.T) {
	assertJobRegistered(t, "backfill-metadata-source-hash")
}

func TestBackfillMetadataSourceHashJob_Metadata(t *testing.T) {
	j, err := maintenance.Get("backfill-metadata-source-hash")
	require.NoError(t, err)
	assert.Equal(t, "backfill-metadata-source-hash", j.ID())
	assert.NotEmpty(t, j.Name())
	assert.NotEmpty(t, j.Description())
	assert.NotNil(t, j.DefaultParams())
}

func TestBackfillMetadataSourceHashJob_SkipsAlreadyHashed(t *testing.T) {
	existing := "sha256:abc123"
	src := "audible"
	asin := "B001234567"
	book := database.Book{
		ID:                 "book-1",
		Title:              "Already Hashed",
		MetadataSourceHash: &existing,
		MetadataSource:     &src,
		ASIN:               &asin,
	}
	var updateCalled bool
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return []database.BookCore{book.Core()}, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updateCalled = true
			return b, nil
		},
	}

	j, err := maintenance.Get("backfill-metadata-source-hash")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	assert.False(t, updateCalled, "UpdateBook must not be called for already-hashed books")
}

func TestBackfillMetadataSourceHashJob_SkipsNilSourceAndID(t *testing.T) {
	book := database.Book{ID: "book-noid", Title: "No Source"}
	var updateCalled bool
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return []database.BookCore{book.Core()}, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updateCalled = true
			return b, nil
		},
	}

	j, err := maintenance.Get("backfill-metadata-source-hash")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	assert.False(t, updateCalled, "UpdateBook must not be called for books with no source/ID")
}

func TestBackfillMetadataSourceHashJob_HashesAudibleBook(t *testing.T) {
	src := "audible"
	asin := "B001234567"
	book := database.Book{
		ID:             "book-audible",
		Title:          "Audible Book",
		MetadataSource: &src,
		ASIN:           &asin,
	}
	var writtenHash string
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return []database.BookCore{book.Core()}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b := book
			return &b, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			if b.MetadataSourceHash != nil {
				writtenHash = *b.MetadataSourceHash
			}
			return b, nil
		},
	}

	j, err := maintenance.Get("backfill-metadata-source-hash")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	assert.NotEmpty(t, writtenHash, "MetadataSourceHash should be written for an audible book with ASIN")
	// Hash is deterministic: same input → same output.
	assert.Len(t, writtenHash, 64, "expected a 64-char SHA-256 hex digest")
}

func TestBackfillMetadataSourceHashJob_DryRun(t *testing.T) {
	src := "audible"
	asin := "B009876543"
	book := database.Book{
		ID:             "book-dry",
		Title:          "Dry Run Book",
		MetadataSource: &src,
		ASIN:           &asin,
	}
	var updateCalled bool
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return []database.BookCore{book.Core()}, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updateCalled = true
			return b, nil
		},
	}

	j, err := maintenance.Get("backfill-metadata-source-hash")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, true /* dryRun */))
	assert.False(t, updateCalled, "dry_run=true: UpdateBook must not be called")
}

func TestBackfillMetadataSourceHashJob_Cancellation(t *testing.T) {
	src := "audible"
	asin := "B000000001"
	books := make([]database.BookCore, 5)
	for i := range books {
		books[i] = database.BookCore{
			ID:             fmt.Sprintf("b%d", i),
			Title:          "Book",
			MetadataSource: &src,
			ASIN:           &asin,
		}
	}
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return books, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) { return b, nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	j, err := maintenance.Get("backfill-metadata-source-hash")
	require.NoError(t, err)
	err = j.Run(ctx, store, &noopReporter{}, false)
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	}
}
