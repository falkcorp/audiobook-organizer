// file: internal/server/owner_rejected_rerank_test.go
// version: 1.0.0
// guid: 118c8e73-9354-4107-840e-af173540ebc3
// last-edited: 2026-10-10

package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func sortedRows(rows []json.RawMessage) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = string(r)
	}
	sort.Strings(out)
	return out
}

// The one-time re-order moves every rejected slot-0 candidate down, drops
// none, marks itself done, and does nothing on a second run.
func TestRerankOwnerRejectedRows_OneShot(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	svc := metafetch.NewService(store)
	old := ownerRejectedRerankPageSize
	ownerRejectedRerankPageSize = 2 // page through the keyspace
	t.Cleanup(func() { ownerRejectedRerankPageSize = old })

	var books []*database.Book
	before := map[string][]json.RawMessage{}
	for _, title := range []string{"Quiet Harbor", "Long Field", "Grey Orchard"} {
		b, cerr := store.CreateBook(&database.Book{Title: title, Format: "m4b"})
		require.NoError(t, cerr)
		books = append(books, b)
		rej := metafetch.MetadataCandidate{Title: title, Source: "Audible", Score: 0.97}
		ok := metafetch.MetadataCandidate{Title: title + " (Unabridged)", Source: "Audible", Score: 0.93}
		rows := append(candidateJSON(t, rej), candidateJSON(t, ok)...)
		before[b.ID] = rows
		require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: b.ID, Candidates: rows, FetchedAt: time.Now()}))
		require.NoError(t, store.SetRaw(metafetch.RejectedCandidateStoreKey(b.ID, rej.Source, rej.Title), []byte("1")))
	}
	// A second rejection of the last book (two keys, one book), a rejection
	// of a book with no row, and one for a book that no longer exists.
	require.NoError(t, store.SetRaw(metafetch.RejectedCandidateStoreKey(books[2].ID, "Audible", "Something Else"), []byte("1")))
	nr, err := store.CreateBook(&database.Book{Title: "No Row", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.SetRaw(metafetch.RejectedCandidateStoreKey(nr.ID, "Audible", "No Row"), []byte("1")))
	require.NoError(t, store.SetRaw(metafetch.RejectedCandidateStoreKey("gone-book", "Audible", "Gone"), []byte("1")))

	res, err := rerankOwnerRejectedRows(context.Background(), store, svc, time.Now())
	require.NoError(t, err)
	require.Equal(t, 6, res.Keys)
	require.EqualValues(t, 5, res.Counts.Books)
	require.EqualValues(t, 3, res.Counts.Reordered)
	require.EqualValues(t, 1, res.Counts.NoRow)
	require.EqualValues(t, 1, res.Counts.BookMissing)
	require.EqualValues(t, 0, res.Counts.Errors)
	require.True(t, res.FlagSet)
	for _, b := range books {
		row, gerr := store.GetMetadataCache(b.ID)
		require.NoError(t, gerr)
		require.Equal(t, sortedRows(before[b.ID]), sortedRows(row.Candidates), "a re-order drops nothing")
		require.Equal(t, b.Title+" (Unabridged)", candidateTitles(t, row.Candidates)[0])
	}

	again, err := rerankOwnerRejectedRows(context.Background(), store, svc, time.Now())
	require.NoError(t, err)
	require.True(t, again.AlreadyDone)
	require.Nil(t, again.Counts)
}

type failingReranker struct{}

func (failingReranker) RerankCachedCandidates(string, func(metafetch.MetadataCandidate) int) (metafetch.RerankOutcome, error) {
	return metafetch.RerankUnchanged, errors.New("write failed")
}

// A failed book leaves the marker unset, so the next start retries.
func TestRerankOwnerRejectedRows_FailureLeavesMarkerUnset(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	b, err := store.CreateBook(&database.Book{Title: "Quiet Harbor", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.SetRaw(metafetch.RejectedCandidateStoreKey(b.ID, "Audible", "Quiet Harbor"), []byte("1")))

	res, err := rerankOwnerRejectedRows(context.Background(), store, failingReranker{}, time.Now())
	require.NoError(t, err)
	require.False(t, res.FlagSet)
	require.EqualValues(t, 1, res.Counts.Errors)
	s, err := store.GetSetting(ownerRejectedRerankDoneKey)
	if err == nil && s != nil {
		require.Empty(t, s.Value)
	}
}

// scanFailingStore fails every per-book rejection read (ScanPrefix); the
// one-time pass's paged keyspace scan still works.
type scanFailingStore struct{ database.Store }

func (scanFailingStore) ScanPrefix(string) ([]database.KVPair, error) {
	return nil, errors.New("scan failed")
}

// A book whose rejections cannot be read is an error, not a row ranked as
// if nothing were rejected: the marker stays unset and the row untouched.
func TestRerankOwnerRejectedRows_UnreadableRejectionsFailClosed(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	b, err := store.CreateBook(&database.Book{Title: "Quiet Harbor", Format: "m4b"})
	require.NoError(t, err)
	rows := append(candidateJSON(t, metafetch.MetadataCandidate{Title: "Quiet Harbor", Source: "Audible", Score: 0.97}),
		candidateJSON(t, metafetch.MetadataCandidate{Title: "Quiet Harbor (Unabridged)", Source: "Audible", Score: 0.93})...)
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: b.ID, Candidates: rows, FetchedAt: time.Now()}))
	require.NoError(t, store.SetRaw(metafetch.RejectedCandidateStoreKey(b.ID, "Audible", "Quiet Harbor"), []byte("1")))

	res, err := rerankOwnerRejectedRows(context.Background(), scanFailingStore{store}, metafetch.NewService(store), time.Now())
	require.NoError(t, err)
	require.False(t, res.FlagSet)
	require.EqualValues(t, 1, res.Counts.Errors)
	row, err := store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.Equal(t, "Quiet Harbor", candidateTitles(t, row.Candidates)[0])
}
