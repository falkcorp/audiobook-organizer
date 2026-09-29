// file: internal/server/transcription_identity_parity_test.go
// version: 1.0.0
// guid: 0c9b742a-6fd1-4ae1-bd10-454a241b3df7
// last-edited: 2026-09-28
//
// maintenance.auto-match-transcribed's apply (ApplyTranscriptionCandidate)
// must judge a cached row's identity exactly as the bulk-apply planner
// (planCachedApply) does. Until 2026-09-28 it validated with the Book.Author
// snapshot and the full narrator/series shape, so it refused rows the planner
// accepted (a batch row hashed with the live author or a transcribed query).

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

type parityFixture struct {
	t     *testing.T
	store *database.PebbleStore
	mfs   *metafetch.Service
	s     *Server
}

func newParityFixture(t *testing.T, results []metadata.BookMetadata) *parityFixture {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))
	t.Cleanup(func() { _ = store.Close() })
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{&countingSource{name: "Src", results: results}})
	return &parityFixture{t: t, store: store, mfs: mfs, s: &Server{store: store, metadataFetchService: mfs}}
}

// verdicts returns whether the planner and ApplyTranscriptionCandidate each
// read the book's cache row as identity_stale, and the apply's error.
func (f *parityFixture) verdicts(id, gatedTitle, gatedAuthor string) (plannerStale, applyStale bool, applyErr error) {
	f.t.Helper()
	plan := planCachedApply(f.mfs, f.store, id, nil, nil)
	require.NotNil(f.t, plan.Gate, "planner skipped before the gate: %s %v", plan.Reason, plan.Err)
	applyErr = f.s.ApplyTranscriptionCandidate(context.Background(), id, gatedTitle, gatedAuthor)
	return plan.Gate.Reason == applygate.ReasonIdentityStale, errors.Is(applyErr, metafetch.ErrStaleMetadataCache), applyErr
}

// The Valis rows from F1: a row the batch fetch wrote with no author hint
// before 2026-09-28, for a book whose author is Philip K. Dick. Fresh (its
// fingerprint proves the author), stripped of its fingerprint, and after the
// author changed -- both readers must agree on every one.
func TestApplyTranscriptionCandidate_IdentityParityWithPlanner_Valis(t *testing.T) {
	f := newParityFixture(t, []metadata.BookMetadata{{Title: "Valis", Author: "Philip K. Dick", Series: "Valis Trilogy"}})
	pkd, err := f.store.CreateAuthor("Philip K. Dick")
	require.NoError(t, err)
	b, err := f.store.CreateBook(&database.Book{Title: "Valis", FilePath: "/lib/Unknown Author/Valis/Valis.m4b", AuthorID: &pkd.ID})
	require.NoError(t, err)
	_, err = f.mfs.FetchAndCacheLimited(context.Background(), nil, b.ID, "Valis", "", "", "", metafetch.SearchOptions{})
	require.NoError(t, err)

	top, found, err := f.s.SearchTranscriptionCandidate(context.Background(), b.ID, "", "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "Valis Trilogy", top.Series, "the op's pre-check needs the candidate's series")

	plannerStale, applyStale, applyErr := f.verdicts(b.ID, "Valis", "Philip K. Dick")
	require.False(t, plannerStale, "fresh: planner")
	require.False(t, applyStale, "fresh: apply (%v)", applyErr)

	entry, err := f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	saved := entry.SearchFingerprint
	entry.SearchFingerprint = ""
	require.NoError(t, f.store.PutMetadataCache(entry))
	plannerStale, applyStale, applyErr = f.verdicts(b.ID, "Valis", "Philip K. Dick")
	require.True(t, plannerStale, "no fingerprint: planner")
	require.True(t, applyStale, "no fingerprint: apply (%v)", applyErr)
	entry.SearchFingerprint = saved

	leguin, err := f.store.CreateAuthor("Ursula K. Le Guin")
	require.NoError(t, err)
	book, err := f.store.GetBookByID(b.ID)
	require.NoError(t, err)
	book.AuthorID = &leguin.ID
	_, err = f.store.UpdateBook(book.ID, book)
	require.NoError(t, err)
	require.NoError(t, f.store.PutMetadataCache(entry)) // the store drops the row on an author change
	plannerStale, applyStale, applyErr = f.verdicts(b.ID, "Valis", "Philip K. Dick")
	require.True(t, plannerStale, "author changed: planner")
	require.True(t, applyStale, "author changed: apply (%v)", applyErr)
}

// A blank-titled book whose batch row was fetched by its transcribed title
// with its live author -- the shape the batch fetch writes today, and the
// book this op exists for. The planner lifts identity_stale on the
// transcription; the apply must too, and fills the title. The old check
// (Book.Author snapshot, stored title) refused it.
func TestApplyTranscriptionCandidate_IdentityParityWithPlanner_TranscribedQuery(t *testing.T) {
	f := newParityFixture(t, []metadata.BookMetadata{{Title: "Valis", Author: "Philip K. Dick"}})
	pkd, err := f.store.CreateAuthor("Philip K. Dick")
	require.NoError(t, err)
	heard := "Valis"
	b, err := f.store.CreateBook(&database.Book{Title: "", FilePath: "/lib/Unknown Author/Unknown Title/book.m4b",
		AuthorID: &pkd.ID, TranscribedTitle: &heard})
	require.NoError(t, err)
	_, err = f.mfs.FetchAndCacheLimited(context.Background(), nil, b.ID, heard, "Philip K. Dick", "", "", metafetch.SearchOptions{})
	require.NoError(t, err)

	plannerStale, applyStale, applyErr := f.verdicts(b.ID, "Valis", "Philip K. Dick")
	require.False(t, plannerStale, "planner lifts the transcribed query")
	require.False(t, applyStale, "apply must lift it too (%v)", applyErr)
	require.NoError(t, applyErr)
	got, err := f.store.GetBookByID(b.ID)
	require.NoError(t, err)
	require.Equal(t, "Valis", got.Title)
}
