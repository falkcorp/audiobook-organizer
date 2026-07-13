// file: internal/metafetch/service_fetch_followups_test.go
// version: 1.0.0
// guid: 4d9a2f6b-1c83-4e57-9b0a-7f2e6c1d8a34
// last-edited: 2026-07-13

package metafetch

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingSource is a metadata source whose Search* calls block until the
// caller's context is cancelled, then return ctx.Err(). It is used to prove
// that FetchMetadataForBook honors context cancellation end-to-end (TODO 1):
// before ctx was threaded, an in-flight external fetch could not be interrupted
// until the ~90s Audnexus region-loop bound elapsed.
type blockingSource struct {
	name    string
	started chan struct{}
	once    sync.Once
}

func (b *blockingSource) Name() string { return b.name }

func (b *blockingSource) signalStarted() { b.once.Do(func() { close(b.started) }) }

func (b *blockingSource) SearchByTitle(ctx context.Context, _ string) ([]metadata.BookMetadata, error) {
	b.signalStarted()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingSource) SearchByTitleAndAuthor(ctx context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	b.signalStarted()
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestFetchMetadataForBook_CancelledContextAborts verifies that cancelling the
// context aborts an in-flight FetchMetadataForBook promptly (well under the ~90s
// Audnexus bound) rather than blocking indefinitely on the source call.
func TestFetchMetadataForBook_CancelledContextAborts(t *testing.T) {
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			// No author → the fetch flow lands directly on SearchByTitle.
			return &database.Book{ID: id, Title: "Some Searchable Title"}, nil
		},
	}
	svc := NewService(mock)

	// Same instance listed twice: the first source blocks (and we cancel while
	// it's blocked); the loop's top-of-iteration ctx.Err() check then returns
	// context.Canceled directly before the second iteration issues any call.
	bs := &blockingSource{name: "blocking", started: make(chan struct{})}
	svc.SetOverrideSources([]metadata.MetadataSource{bs, bs})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := svc.FetchMetadataForBook(ctx, "b1")
		errCh <- err
	}()

	// Wait until we're actually inside the blocking source call, then cancel.
	select {
	case <-bs.started:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking source was never entered")
	}
	cancel()

	select {
	case err := <-errCh:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("FetchMetadataForBook did not abort promptly after context cancel")
	}
}

// TestFetchMetadataForBook_StaleCacheYearKindSelfCorrected verifies TODO 2: an
// Audible/Audnexus fetch-cache entry that was serialized BEFORE the
// PublishYearIsAudiobookRelease flag shipped (#1940) — so the flag deserializes
// as false — is self-corrected on cache READ by re-deriving the flag from the
// entry's source, routing the year to AudiobookReleaseYear (release) rather than
// PrintYear (print).
func TestFetchMetadataForBook_StaleCacheYearKindSelfCorrected(t *testing.T) {
	raw := map[string][]byte{}
	var updatedBook *database.Book
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Mistborn"}, nil
		},
		GetAuthorByNameFunc: func(name string) (*database.Author, error) {
			return &database.Author{ID: 1, Name: name}, nil
		},
		UpdateBookFunc: func(_ string, book *database.Book) (*database.Book, error) {
			updatedBook = book
			return book, nil
		},
		RecordMetadataChangeFunc: func(_ *database.MetadataChangeRecord) error { return nil },
		GetSeriesByNameFunc: func(name string, _ *int) (*database.Series, error) {
			return &database.Series{ID: 1, Name: name}, nil
		},
		GetRawFunc:    func(k string) ([]byte, error) { return raw[k], nil },
		SetRawFunc:    func(k string, v []byte) error { raw[k] = v; return nil },
		DeleteRawFunc: func(k string) error { delete(raw, k); return nil },
	}

	// The source name MUST match a real release-year source so
	// SourceProducesAudiobookReleaseYear returns true — reference the client's
	// own Name() rather than hardcoding a string.
	audibleName := (&metadata.AudibleClient{}).Name()

	// A pre-#1940 cached entry: flag defaults to false even though the source
	// (Audible) reports an audiobook RELEASE year.
	blob, err := json.Marshal([]metadata.BookMetadata{{
		Title:                         "Mistborn",
		Author:                        "Brandon Sanderson",
		PublishYear:                   2015,
		PublishYearIsAudiobookRelease: false,
	}})
	require.NoError(t, err)
	require.NoError(t, database.PutCachedMetadataFetch(mock, "b1", audibleName, blob, 1.0))

	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{
		&mockMetadataSource{name: audibleName},
	})

	resp, err := svc.FetchMetadataForBook(context.Background(), "b1")
	require.NoError(t, err)
	assert.Equal(t, audibleName, resp.Source)
	require.NotNil(t, updatedBook)

	// The corrected flag routes 2015 to the audiobook release year, NOT PrintYear.
	require.NotNil(t, updatedBook.AudiobookReleaseYear, "release year should be set from the cached Audible entry")
	assert.Equal(t, 2015, *updatedBook.AudiobookReleaseYear)
	if updatedBook.PrintYear != nil {
		assert.Equal(t, 0, *updatedBook.PrintYear, "print year must not receive the release year")
	}
}
