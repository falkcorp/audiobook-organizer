// file: internal/database/tombstone_file_stamp_test.go
// version: 1.0.0
// guid: c4a81e5d-7f20-4b39-8d16-3e92b05a7f48
// last-edited: 2026-10-30

package database

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stamped tombstone is the same value under the same key as a plain one: every
// existing reader (GetBookTombstone, ListBookTombstones) still decodes it as a
// Book, the stamp reads back, and DeleteBookTombstone removes both together.
func TestStampedTombstone_RoundTripsAndIsAnOrdinaryTombstoneToOldReaders(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	stamp := TombstoneFileStamp{HasInode: true, Ino: 4242, Size: 1234, MtimeNS: 1_730_000_000_000_000_000}
	require.NoError(t, store.CreateStampedBookTombstone(&Book{ID: "knightfall", Title: "Batman: Knightfall", FilePath: "/lib/knightfall.m4b"}, stamp))
	require.NoError(t, store.CreateBookTombstone(&Book{ID: "hush", Title: "Batman: Hush", FilePath: "/lib/hush.m4b"}))

	got, err := store.GetBookTombstoneStamp("knightfall")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, stamp, *got)

	plain, err := store.GetBookTombstoneStamp("hush")
	require.NoError(t, err)
	assert.Nil(t, plain, "a tombstone written without a stamp (or before stamps existed) has none")

	missing, err := store.GetBookTombstoneStamp("no-such-tombstone")
	require.NoError(t, err)
	assert.Nil(t, missing)

	book, err := store.GetBookTombstone("knightfall")
	require.NoError(t, err)
	require.NotNil(t, book)
	assert.Equal(t, "Batman: Knightfall", book.Title)
	assert.Equal(t, "/lib/knightfall.m4b", book.FilePath)

	listed, err := store.ListBookTombstones(0)
	require.NoError(t, err)
	ids := map[string]string{}
	for _, b := range listed {
		ids[b.ID] = b.FilePath
	}
	assert.Equal(t, map[string]string{"knightfall": "/lib/knightfall.m4b", "hush": "/lib/hush.m4b"}, ids)

	// Rewriting the tombstone plainly drops the stamp: a stamp never outlives the
	// purge decision that wrote it.
	require.NoError(t, store.CreateBookTombstone(&Book{ID: "knightfall", FilePath: "/lib/knightfall.m4b"}))
	again, err := store.GetBookTombstoneStamp("knightfall")
	require.NoError(t, err)
	assert.Nil(t, again)

	require.NoError(t, store.CreateStampedBookTombstone(&Book{ID: "knightfall", FilePath: "/lib/knightfall.m4b"}, stamp))
	require.NoError(t, store.DeleteBookTombstone("knightfall"))
	gone, err := store.GetBookTombstoneStamp("knightfall")
	require.NoError(t, err)
	assert.Nil(t, gone, "deleting the tombstone deletes its stamp")
}

func TestStampedTombstone_NilBookIsRefused(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	assert.Error(t, store.CreateStampedBookTombstone(nil, TombstoneFileStamp{}))
}
