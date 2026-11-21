// file: internal/server/handlers/metadata/prune_pin_test.go
// version: 1.1.0
// guid: 7c3f9a52-1e84-4d60-b2a7-9d5e0c8f4a31
// last-edited: 2026-11-17

package metadatahandler_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
)

// realPruneHandler is the handler over a real store whose embedding store shares
// its DB, as in production. The mock-store prune tests above prove only that the
// route calls PruneBookSnapshots; these prove what that call does to a snapshot a
// merge journal row points at.
func realPruneHandler(t *testing.T) (*metadatahandler.Handler, *database.PebbleStore, *database.EmbeddingStore) {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("prune_pin_" + t.Name())
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	es := database.NewEmbeddingStoreFromStore(s)
	require.NotNil(t, es)
	h := metadatahandler.New(s, nil, nil, nil, cache.New[gin.H]("prune-pin-test", time.Minute),
		func(b *database.Book) any { return gin.H{"id": b.ID} },
		func(string) bool { return false },
		func(context.Context, plugin.Event) {})
	return h, s, es
}

func snapshotNanos(t *testing.T, s *database.PebbleStore, id string) []int64 {
	t.Helper()
	snaps, err := s.GetBookSnapshots(id, 1000)
	require.NoError(t, err)
	out := make([]int64, 0, len(snaps))
	for _, snap := range slices.Backward(snaps) {
		out = append(out, snap.Timestamp.UnixNano())
	}
	return out
}

// The "Prune (keep 5)" route must keep a snapshot a live merge journal row needs,
// however old: it is the second way, besides the maintenance job, to run the prune.
func TestPruneBookCOWVersions_KeepsASnapshotAMergeJournalRowNeeds(t *testing.T) {
	h, s, es := realPruneHandler(t)
	b, err := s.CreateBook(&database.Book{Title: "Batman", FilePath: "/test/batman.m4b"})
	require.NoError(t, err)
	for i := range 8 {
		b.Title = fmt.Sprintf("Batman, printing %d", i)
		_, err := s.UpdateBook(b.ID, b)
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
	}
	ts := snapshotNanos(t, s, b.ID)
	require.Len(t, ts, 8)
	_, err = es.PutAutoMergeJournalEntry(database.AutoMergeJournalEntry{
		WinnerID: b.ID, LoserID: "robin", WinnerPreMergeTS: ts[0], MergedAt: time.Now().UnixNano(),
	})
	require.NoError(t, err)

	w := doReq(h.PruneBookCOWVersions, http.MethodPost, "/audiobooks/"+b.ID+"/cow-versions/prune",
		map[string]any{"keep_count": 5}, idParam(b.ID))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	left := snapshotNanos(t, s, b.ID)
	require.Equal(t, append([]int64{ts[0]}, ts[3:]...), left,
		"the pinned oldest snapshot survives the route, and so do the newest five")
}

// With the journal unreadable the route deletes nothing and says so.
func TestPruneBookCOWVersions_UnreadableJournalDeletesNothing(t *testing.T) {
	h, s, _ := realPruneHandler(t)
	b, err := s.CreateBook(&database.Book{Title: "Batman", FilePath: "/test/batman.m4b"})
	require.NoError(t, err)
	for i := range 6 {
		b.Title = fmt.Sprintf("Batman, printing %d", i)
		_, err := s.UpdateBook(b.ID, b)
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
	}
	// Written before the first prune builds the pin set, so no generation bump is needed.
	require.NoError(t, s.SetRaw("dedup:automerge:ffffffffffffffff", []byte("{not json")))

	w := doReq(h.PruneBookCOWVersions, http.MethodPost, "/audiobooks/"+b.ID+"/cow-versions/prune",
		map[string]any{"keep_count": 2}, idParam(b.ID))
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "a merge journal row is unreadable: dedup:automerge:ffffffffffffffff; prune is blocked until it is repaired",
		"the owner is told which row to repair, not a generic failure")
	require.Len(t, snapshotNanos(t, s, b.ID), 6, "nothing was deleted")
}
