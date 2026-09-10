// file: internal/dedup/book_dedup_audio_route_test.go
// version: 1.0.0
// guid: e8415b62-fc53-4bd4-abd2-7f2306af7da7
// last-edited: 2026-09-10

package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// TestMergeBooks_RefusesFilelessKeeperWithFileBearingLoser is the regression
// test for TODO.md:2304. This package's MergeBooks HARD-deletes every loser
// (store.DeleteBook, not a soft delete), so it took keepID exactly as the
// caller handed it over: a keeper with no book_file rows and an empty
// FilePath collapsed a loser whose book_file rows were the only route to the
// audio, and the rows that reached it were gone for good.
//
// merge.Service.MergeBooks has refused that shape since the #3053 review
// (FilelessPrimaryError, elected with HasAudioRoute); this asserts the legacy
// hard-collapse path refuses it the same way and, above all, that nothing is
// deleted when it does.
func TestMergeBooks_RefusesFilelessKeeperWithFileBearingLoser(t *testing.T) {
	store := newConcurrentTestStore(t)

	keepID := ulid.Make().String()
	loserID := ulid.Make().String()

	// The keeper is the ghost row: no book_file rows, empty FilePath.
	_, err := store.CreateBook(&database.Book{ID: keepID, Title: "Ghost Keeper", Format: "m4b"})
	require.NoError(t, err)

	// The loser's book_file row is the group's ONLY route to the audio.
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "File Bearing Loser", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		BookID:   loserID,
		FilePath: "/audio/file-bearing-loser.m4b",
		FileHash: "hash-file-bearing-loser",
		FileSize: 5 << 20,
		Duration: 3600,
	}))

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)

	// Headline assertion: the row that carries the audio is still there.
	survivor, getErr := store.GetBookByID(loserID)
	require.NoError(t, getErr)
	require.NotNil(t, survivor, "the file-bearing loser must NOT be hard-deleted for a keeper that has no audio route")

	files, filesErr := store.GetBookFiles(loserID)
	require.NoError(t, filesErr)
	require.Len(t, files, 1, "the loser's book_file rows must survive the refused merge")

	require.Equal(t, 0, res.MergedCount, "a refused merge must not count a merge")

	var fileless *merge.FilelessPrimaryError
	require.True(t, errors.As(err, &fileless), "want *merge.FilelessPrimaryError, got %T: %v", err, err)
	require.Equal(t, keepID, fileless.PrimaryID)
	require.Equal(t, []string{loserID}, fileless.FileBearing)
	require.True(t, merge.IsRefusal(err), "the refusal must be typed as a refusal, not a server fault")
}

// TestMergeBooks_AllowsMergeWhenNoParticipantHasAudioRoute pins the other side
// of the guard. Collapsing rows that ALL lack an audio route loses no audio,
// and refusing it would make the file-less ghost class (the exact rows the
// iTunes heal exists to tidy) impossible to collapse. This mirrors the
// "Merging books that ALL lack file rows is still allowed" rule in
// merge.Service.MergeBooks — a future tightening of the guard into "refuse
// whenever any loser has files" would silently disable the heal, and this test
// is what stops it.
func TestMergeBooks_AllowsMergeWhenNoParticipantHasAudioRoute(t *testing.T) {
	store := newConcurrentTestStore(t)

	keepID := ulid.Make().String()
	loserID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: keepID, Title: "Ghost Keeper", Format: "m4b"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "Ghost Loser", Format: "m4b"})
	require.NoError(t, err)

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.MergedCount)
	require.Empty(t, res.Errors)

	gone, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	require.Nil(t, gone, "with no audio to lose, the hard collapse still runs")
}

// TestMergeBooks_AllowsFilePathOnlyKeeper covers the 20.4% of prod books that
// have a FilePath and no book_file rows (chapter consolidation was disabled
// when they were scanned). merge.HasAudioRoute counts FilePath as a route on
// purpose, so such a keeper is NOT file-less and must not be refused.
func TestMergeBooks_AllowsFilePathOnlyKeeper(t *testing.T) {
	store := newConcurrentTestStore(t)

	keepID := ulid.Make().String()
	loserID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{
		ID: keepID, Title: "Single File Keeper", Format: "m4b",
		FilePath: "/audio/single-file-keeper.m4b",
	})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "File Bearing Loser", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		BookID:   loserID,
		FilePath: "/audio/file-bearing-loser.m4b",
		FileHash: "hash-file-bearing-loser-2",
		FileSize: 5 << 20,
		Duration: 3600,
	}))

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.MergedCount)
}
