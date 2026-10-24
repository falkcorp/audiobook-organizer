// file: internal/database/book_trash_stamp_test.go
// version: 1.0.0
// guid: 2f6a9d03-b1c8-4e75-8a3d-5c0e7b94a1f6
// last-edited: 2026-10-24

package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tsPtr(t time.Time) *time.Time { return &t }

func boolPtr(b bool) *bool { return &b }

func TestEnforceTrashStamp(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	old := now.Add(-90 * 24 * time.Hour)
	lastWrite := now.Add(-3 * 24 * time.Hour)
	minted := now.Add(-time.Second)

	live := func(stamp *time.Time) *Book {
		return &Book{MarkedForDeletion: boolPtr(false), MarkedForDeletionAt: stamp, UpdatedAt: tsPtr(lastWrite)}
	}
	trashed := func(stamp *time.Time) *Book {
		return &Book{MarkedForDeletion: boolPtr(true), MarkedForDeletionAt: stamp, UpdatedAt: tsPtr(lastWrite)}
	}

	for _, tc := range []struct {
		name string
		book *Book
		old  *Book
		want *time.Time // nil: MarkedForDeletionAt must stay nil
	}{
		{"created trashed with no stamp", trashed(nil), nil, &now},
		{"created trashed with a stamp keeps it", trashed(&minted), nil, &minted},
		{"live to trashed with no stamp", trashed(nil), live(nil), &now},
		{"live to trashed over a stale stamp the restore left behind", trashed(&old), live(&old), &now},
		{"live to trashed with a stamp the writer minted keeps it", trashed(&minted), live(&old), &minted},
		{"still trashed, stamp dropped by the writer, keeps the stored stamp", trashed(nil), trashed(&old), &old},
		{"still trashed, legacy row with no stamp takes its last write", trashed(nil), trashed(nil), &lastWrite},
		{"still trashed with a stamp is untouched", trashed(&minted), trashed(&old), &minted},
		{"a live row is left as given, stale stamp included", live(&old), live(&old), &old},
		{"a restore that clears the stamp stays cleared", live(nil), trashed(&old), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enforceTrashStamp(tc.book, tc.old, now)
			if tc.want == nil {
				assert.Nil(t, tc.book.MarkedForDeletionAt)
				return
			}
			require.NotNil(t, tc.book.MarkedForDeletionAt)
			assert.True(t, tc.want.Equal(*tc.book.MarkedForDeletionAt), "got %v, want %v", tc.book.MarkedForDeletionAt, tc.want)
		})
	}

	// A legacy trashed row with neither a stamp nor a last write: now.
	b := &Book{MarkedForDeletion: boolPtr(true)}
	enforceTrashStamp(b, &Book{MarkedForDeletion: boolPtr(true)}, now)
	require.NotNil(t, b.MarkedForDeletionAt)
	assert.True(t, now.Equal(*b.MarkedForDeletionAt))
}

func TestTrashedAt(t *testing.T) {
	stamp, wrote := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	assert.Equal(t, &stamp, (&Book{MarkedForDeletionAt: &stamp, UpdatedAt: &wrote}).TrashedAt(), "the stamp is preferred over a later write")
	assert.Equal(t, &wrote, (&Book{UpdatedAt: &wrote}).TrashedAt(), "a row with no stamp falls back to its last write")
	assert.Nil(t, (&Book{}).TrashedAt())
	assert.Nil(t, (*Book)(nil).TrashedAt())
}

func trashStampStore(t *testing.T) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "the memdb path must be exercised")
	return p
}

// A writer that sets only the flag (the diagnostics delete_orphan fix did) gets
// a stamp from the store, and a stale stamp left on a live row by an older
// restore does not make the book look as old as its first trashing.
func TestStoreStampsATrashWriteAndIgnoresAStaleStamp(t *testing.T) {
	p := trashStampStore(t)
	old := time.Now().Add(-120 * 24 * time.Hour)
	_, err := p.CreateBook(&Book{ID: "stale", Title: "stale", FilePath: "/l/stale.m4b"})
	require.NoError(t, err)
	// A live row an older restore left the stamp on.
	_, err = p.SeedLegacyBookRowForTest("stale", func(b *Book) error {
		b.MarkedForDeletionAt = &old
		return nil
	})
	require.NoError(t, err)

	before := time.Now()
	_, err = p.ModifyBook("stale", func(b *Book) error {
		b.MarkedForDeletion = boolPtr(true)
		return nil
	})
	require.NoError(t, err)
	got, err := p.GetBookByID("stale")
	require.NoError(t, err)
	require.NotNil(t, got.MarkedForDeletionAt)
	assert.False(t, got.MarkedForDeletionAt.Before(before), "trashed now, not at the stale %v: %v", old, got.MarkedForDeletionAt)

	// And the retention filter sees it as just trashed.
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	list, err := p.ListSoftDeletedBooks(100, 0, &cutoff)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestCreateBookTrashedWithoutAStampIsStamped(t *testing.T) {
	p := trashStampStore(t)
	before := time.Now()
	created, err := p.CreateBook(&Book{ID: "t1", Title: "t1", FilePath: "/l/t1.m4b", MarkedForDeletion: boolPtr(true)})
	require.NoError(t, err)
	require.NotNil(t, created.MarkedForDeletionAt)
	assert.False(t, created.MarkedForDeletionAt.Before(before))
}

// The retention filter on a row older code trashed with no stamp: it is judged
// by its last write (the same answer in the listing and the count, memdb and
// Pebble alike), never waved through whatever its age.
func TestRetentionFilterJudgesAnUnstampedRowByItsLastWrite(t *testing.T) {
	p := trashStampStore(t)
	_, err := p.CreateBook(&Book{ID: "legacy", Title: "legacy", FilePath: "/l/legacy.m4b"})
	require.NoError(t, err)
	_, err = p.SeedLegacyBookRowForTest("legacy", func(b *Book) error {
		b.MarkedForDeletion = boolPtr(true)
		b.MarkedForDeletionAt = nil
		return nil
	})
	require.NoError(t, err)
	row, err := p.GetBookByID("legacy")
	require.NoError(t, err)
	require.Nil(t, row.MarkedForDeletionAt, "fixture: the seed leaves the row unstamped")

	recent := time.Now().Add(-30 * 24 * time.Hour)
	future := time.Now().Add(time.Hour)
	for _, usePebble := range []bool{false, true} {
		p.UseMemDB = !usePebble
		list, err := p.ListSoftDeletedBooks(100, 0, &recent)
		require.NoError(t, err)
		assert.Empty(t, list, "pebble=%v: written just now, not 30 days ago", usePebble)
		n, err := p.CountSoftDeletedBooks(&recent)
		require.NoError(t, err)
		assert.Zero(t, n, "pebble=%v: the count agrees with the listing", usePebble)

		list, err = p.ListSoftDeletedBooks(100, 0, &future)
		require.NoError(t, err)
		assert.Len(t, list, 1, "pebble=%v", usePebble)
		n, err = p.CountSoftDeletedBooks(&future)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "pebble=%v", usePebble)
	}
	p.UseMemDB = true
}
