// file: internal/database/retired_hash_lookup_test.go
// version: 1.0.0
// guid: 6a3d9f12-7c48-4e05-b1a9-3d58e0c47b61
// last-edited: 2026-10-24

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// While no retired version exists, RetiredHoldersOfHash answers "none" without
// memdb: a new import must not be refused for warmup when there is nothing
// retired to find.
func TestRetiredHoldersOfHash_NoRetiredVersionsNeedsNoMemdb(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)
	ps.WaitForWarmup()
	book, err := ps.CreateBook(&Book{Title: "Live", FilePath: "/lib/live"})
	require.NoError(t, err)
	require.NoError(t, ps.CreateBookFile(&BookFile{BookID: book.ID, FilePath: "/lib/live/01.mp3", FileHash: "h-live"}))

	has, err := ps.HasRetiredVersions()
	require.NoError(t, err)
	require.False(t, has)

	for _, useMem := range []bool{true, false} {
		ps.UseMemDB = useMem
		if !useMem {
			_, werr := ps.BookFilesWithHash("h-live")
			require.ErrorIs(t, werr, ErrBookFilesWithHashUnavailable, "the complete read refuses without memdb")
		}
		retired, liveHolds, err := RetiredHoldersOfHash(ps, "h-live")
		require.NoError(t, err, "useMem=%v", useMem)
		require.Empty(t, retired)
		require.False(t, liveHolds, "no retired version exists, so the answer is none and the live lookups own the rest")
	}
}

// With a retired row, the answer is the same whether memdb is serving (the
// indexed complete read) or not (the Pebble-only fallback), and a live row that
// also holds the hash is reported.
func TestRetiredHoldersOfHash_RetiredRowsReadyAndNotReady(t *testing.T) {
	for _, useMem := range []bool{true, false} {
		t.Run(map[bool]string{true: "memdb-ready", false: "memdb-not-ready"}[useMem], func(t *testing.T) {
			fx := newRetiredFixture(t)
			only := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/shell/01.mp3", FileHash: "h-only", TrackNumber: 1})
			orig := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/shell/02.mp3", FileHash: "h-cur", OriginalFileHash: "h-orig", TrackNumber: 2})
			shared := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/shell/03.mp3", FileHash: "h-shared", TrackNumber: 3})
			require.NoError(t, fx.retire(t, only, orig, shared))
			fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/holder/03.mp3", FileHash: "h-shared", TrackNumber: 3})
			fx.ps.UseMemDB = useMem
			if !useMem {
				_, werr := fx.ps.BookFilesWithHash("h-only")
				require.ErrorIs(t, werr, ErrBookFilesWithHashUnavailable)
			}

			retired, liveHolds, err := RetiredHoldersOfHash(fx.ps, "h-only")
			require.NoError(t, err)
			require.Len(t, retired, 1)
			require.Equal(t, only.ID, retired[0].ID)
			require.Equal(t, fx.holder.ID, retired[0].BookID)
			require.Equal(t, fx.version.ID, retired[0].VersionID)
			require.False(t, liveHolds)

			retired, liveHolds, err = RetiredHoldersOfHash(fx.ps, "h-orig")
			require.NoError(t, err)
			require.Len(t, retired, 1, "the original hash counts")
			require.Equal(t, orig.ID, retired[0].ID)
			require.False(t, liveHolds)

			retired, liveHolds, err = RetiredHoldersOfHash(fx.ps, "h-shared")
			require.NoError(t, err)
			require.Len(t, retired, 1)
			require.True(t, liveHolds, "a live row holding the same content is reported")

			retired, liveHolds, err = RetiredHoldersOfHash(fx.ps, "h-nobody")
			require.NoError(t, err)
			require.Empty(t, retired)
			require.False(t, liveHolds)
		})
	}
}

// A store that cannot say whether retired versions exist and cannot read the
// per-hash rows keeps failing closed; a refusal that is not the memdb refusal
// is never swallowed.
func TestRetiredHoldersOfHash_FailsClosedWithoutAnyReader(t *testing.T) {
	_, _, err := RetiredHoldersOfHash(struct{}{}, "h")
	require.ErrorIs(t, err, ErrBookFilesWithHashUnavailable)

	boom := errors.New("boom")
	_, _, err = RetiredHoldersOfHash(failingHashStore{err: boom}, "h")
	require.ErrorIs(t, err, boom)
}

type failingHashStore struct{ err error }

func (s failingHashStore) BookFilesWithHash(string) ([]BookFile, error) { return nil, s.err }
