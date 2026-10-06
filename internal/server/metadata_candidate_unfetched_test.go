// file: internal/server/metadata_candidate_unfetched_test.go
// version: 1.0.1
// guid: 6add5bfa-b425-4289-9274-72a2ee56ed84
// last-edited: 2026-10-06

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The scheduled candidate fetch (unfetched=true) selects only books nothing
// has fetched for -- no cache row, or an empty row answering questions a
// search no longer asks -- and FETCHES them: the book rows are not written
// and nothing is applied.
func TestCandidateFetch_UnfetchedSelectsNeverFetchedAndNeverApplies(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()
	strp := func(v string) *string { return &v }
	no := false

	mk := func(b database.Book) *database.Book {
		t.Helper()
		got, err := store.CreateBook(&b)
		require.NoError(t, err)
		return got
	}
	never := mk(database.Book{Title: "The Glass Orchard", FilePath: "/lib/a/The Glass Orchard/book.m4b"})
	staleEmpty := mk(database.Book{Title: "Copper Rain", FilePath: "/lib/a/Copper Rain/book.m4b"})
	currentEmpty := mk(database.Book{Title: "A Book Nobody Catalogued", FilePath: "/lib/a/Nobody/book.m4b"})
	withCands := mk(database.Book{Title: "Fetched Already", FilePath: "/lib/a/Fetched/book.m4b"})
	applied := mk(database.Book{Title: "Applied Already", FilePath: "/lib/a/Applied/book.m4b", MetadataReviewStatus: strp("matched")})
	noMatch := mk(database.Book{Title: "Owner Said No", FilePath: "/lib/a/No/book.m4b", MetadataReviewStatus: strp("no_match")})
	secondary := mk(database.Book{Title: "Second Copy", FilePath: "/lib/a/Second/book.m4b", IsPrimaryVersion: &no})
	untitled := mk(database.Book{Title: "", FilePath: "/lib/book.m4b"})
	legacyEmpty := mk(database.Book{Title: "Old Ladder Nothing", FilePath: "/lib/a/Old/book.m4b"})

	empty := &countingSource{name: "SrcA"}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{empty})
	s.metadataFetchService = mfs

	// currentEmpty: fetched, every provider answered nothing, for the
	// questions a search asks now.
	runCandidateFetch(t, s, "op-seed", []string{currentEmpty.ID}, false)
	// staleEmpty: an empty answer recorded for other questions.
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: staleEmpty.ID, FetchedAt: time.Now(),
		SourceHash: "h", SearchFingerprint: metafetch.FingerprintPrefix + "0000"}))
	// legacyEmpty: an empty answer from the version "1" ladder (bare hex).
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: legacyEmpty.ID, FetchedAt: time.Now(),
		SourceHash: "h", SearchFingerprint: "0000"}))
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: withCands.ID, FetchedAt: time.Now(),
		SourceHash: "h", SearchFingerprint: metafetch.FingerprintPrefix + "0000", Candidates: []json.RawMessage{json.RawMessage(`{"title":"Fetched Already"}`)}}))

	sel, err := unfetchedCandidateBookIDs(context.Background(), store, mfs, s.newFolderMemo(store), nil, 0)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{never.ID, staleEmpty.ID}, sel.IDs)
	assert.Equal(t, 1, sel.NoRow)
	assert.Equal(t, 1, sel.StaleEmpty)
	assert.Equal(t, 1, sel.Unsearchable, "the untitled book would only be skipped")
	for _, b := range []*database.Book{currentEmpty, withCands, applied, noMatch, secondary, untitled, legacyEmpty} {
		assert.NotContains(t, sel.IDs, b.ID, b.Title)
	}
	// A book another fetch holds is left to it.
	sel, err = unfetchedCandidateBookIDs(context.Background(), store, mfs, s.newFolderMemo(store), map[string]bool{never.ID: true}, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{staleEmpty.ID}, sel.IDs)

	// The op run: answers for the never-fetched book, fetches both selected
	// books and no other, and writes no book.
	answering := &countingSource{name: "SrcA", results: []metadata.BookMetadata{
		{Title: "The Glass Orchard", Author: "Mara Quill", Narrator: "Ada Penn", ASIN: "B0TEST0301", CoverURL: "c"}}}
	mfs.SetOverrideSources([]metadata.MetadataSource{answering})
	before := map[string]database.Book{}
	for _, b := range []*database.Book{never, staleEmpty} {
		got, err := store.GetBookByID(b.ID)
		require.NoError(t, err)
		before[b.ID] = *got
	}
	params, err := json.Marshal(metadataCandidateFetchOpParams{Unfetched: true})
	require.NoError(t, err)
	rec := &resumeRecorder{opID: "op-unfetched"}
	require.NoError(t, s.runMetadataCandidateFetchOp(context.Background(), params, rec))

	rows, err := store.GetOperationResults("op-unfetched")
	require.NoError(t, err)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.BookID)
	}
	assert.ElementsMatch(t, []string{never.ID, staleEmpty.ID}, ids)

	for id, b := range before {
		got, err := store.GetBookByID(id)
		require.NoError(t, err)
		assert.Equal(t, b.Title, got.Title, "fetch only: the title is not written")
		assert.Nil(t, got.MetadataReviewStatus, "fetch only: nothing is applied")
		assert.Nil(t, got.ASIN, "fetch only: no identifier is written")
	}
	entry, err := store.GetMetadataCache(never.ID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.NotEmpty(t, entry.Candidates, "the candidates land in the cache for review")

	// The selection is checkpointed before the first fetch with unfetched
	// false, so a resumed run is handed this list and never re-selects.
	require.NotEmpty(t, rec.states)
	var first map[string]any
	require.NoError(t, json.Unmarshal(rec.states[0], &first))
	assert.Equal(t, false, first["unfetched"])
	assert.Len(t, first["book_ids"], 2)
}
