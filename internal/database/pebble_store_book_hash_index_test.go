// file: internal/database/pebble_store_book_hash_index_test.go
// version: 1.0.0
// guid: 4e8b2d63-9a17-4c50-b3f6-1d7a0c5e9b82
// last-edited: 2026-10-22

package database

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hashBook(t *testing.T, s *PebbleStore, id, hash string, trashed bool) {
	t.Helper()
	b := &Book{ID: id, Title: id, FilePath: "/p/" + id}
	if hash != "" {
		b.FileHash, b.OriginalFileHash, b.OrganizedFileHash = &hash, new("o-"+hash), new("g-"+hash)
	}
	if trashed {
		b.MarkedForDeletion = new(true)
	}
	_, err := s.CreateBook(b)
	require.NoError(t, err)
}

// hashKeyOwner is what the three lookups resolve to ("" for none).
func hashKeyOwners(t *testing.T, s *PebbleStore, hash string) [3]string {
	t.Helper()
	var out [3]string
	for i, get := range []func(string) (*Book, error){s.GetBookByFileHash, s.GetBookByOriginalHash, s.GetBookByOrganizedHash} {
		h := hash
		switch i {
		case 1:
			h = "o-" + hash
		case 2:
			h = "g-" + hash
		}
		b, err := get(h)
		require.NoError(t, err)
		if b != nil {
			out[i] = b.ID
		}
	}
	return out
}

// Two books holding one file share each single-valued hash key (last writer
// wins). Deleting the book that does NOT own a key must leave that key alone:
// DeleteBook used to delete it unconditionally and erased the live owner's
// lookup. Deleting the owner itself still removes its keys.
func TestDeleteBook_HashKeysAreDeletedOnlyWhileOwned(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a", "H", false)
	hashBook(t, s, "b", "H", false) // last writer: the keys name b
	require.Equal(t, [3]string{"b", "b", "b"}, hashKeyOwners(t, s, "H"))

	require.NoError(t, s.DeleteBook("a"))
	assert.Equal(t, [3]string{"b", "b", "b"}, hashKeyOwners(t, s, "H"), "deleting a non-owner must not erase the owner's keys")

	require.NoError(t, s.DeleteBook("b"))
	assert.Equal(t, [3]string{"", "", ""}, hashKeyOwners(t, s, "H"), "the owner's own delete removes its keys")
}

func setRawKey(t *testing.T, s *PebbleStore, key, value string) {
	t.Helper()
	require.NoError(t, s.db.Set([]byte(key), []byte(value), pebble.Sync))
}

func delRawKey(t *testing.T, s *PebbleStore, key string) {
	t.Helper()
	require.NoError(t, s.db.Delete([]byte(key), pebble.Sync))
}

// seedBrokenHashIndex builds a library where one live book per case has a
// broken entry in the plain file-hash index, and returns nothing: the cases
// are named by their hash.
func seedBrokenHashIndex(t *testing.T, s *PebbleStore) {
	t.Helper()
	hashBook(t, s, "ok", "h-ok", false)
	hashBook(t, s, "miss", "h-miss", false)
	delRawKey(t, s, "book:hash:h-miss")
	hashBook(t, s, "dang", "h-dang", false)
	setRawKey(t, s, "book:hash:h-dang", "ghost")
	hashBook(t, s, "other", "h-other", false)
	hashBook(t, s, "wrong", "h-wrong", false)
	setRawKey(t, s, "book:hash:h-wrong", "other") // names a live book that holds a different hash
	// A trashed duplicate that took the key from the live book created first.
	hashBook(t, s, "live-dup", "h-dup", false)
	hashBook(t, s, "trashed-dup", "h-dup", true)
	require.Equal(t, "trashed-dup", hashKeyOwners(t, s, "h-dup")[0])
	// Two live holders of one hash with the key deleted: the smaller id wins.
	hashBook(t, s, "m2", "h-two", false)
	hashBook(t, s, "m1", "h-two", false)
	delRawKey(t, s, "book:hash:h-two")
}

func kindOf(rep BookHashIndexReport, name string) BookHashKindReport {
	for _, k := range rep.Kinds {
		if k.Kind == name {
			return k
		}
	}
	return BookHashKindReport{}
}

func TestCheckBookHashIndex_ClassifiesAndWritesNothing(t *testing.T) {
	s := setupTestPebbleStore(t)
	seedBrokenHashIndex(t, s)

	rep, err := s.CheckBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, rep.Apply)
	assert.Equal(t, 9, rep.Books)
	assert.Equal(t, 8, rep.LiveBooks)

	k := kindOf(rep, "hash")
	assert.Equal(t, BookHashKindReport{Kind: "hash", Checked: 7, OK: 2, Missing: 2, Dangling: 1, WrongHolder: 1, TrashedOwner: 1}, k)
	// The duplicate's other two keys were taken by the trashed copy as well.
	for _, name := range []string{"originalhash", "organizedhash"} {
		assert.Equal(t, BookHashKindReport{Kind: name, Checked: 7, OK: 6, TrashedOwner: 1}, kindOf(rep, name))
	}
	assert.Equal(t, 7, rep.Broken())
	assert.Zero(t, rep.Repointed())
	assert.NotEmpty(t, rep.Samples)

	// Read-only: every key is exactly as seeded.
	assert.Equal(t, "", hashKeyOwners(t, s, "h-miss")[0])
	assert.Equal(t, "trashed-dup", hashKeyOwners(t, s, "h-dup")[0])
	v, closer, err := s.db.Get([]byte("book:hash:h-dang"))
	require.NoError(t, err)
	assert.Equal(t, "ghost", string(v))
	closer.Close()
}

func TestRepairBookHashIndex_PointsEveryBrokenKeyAtALiveHolder(t *testing.T) {
	s := setupTestPebbleStore(t)
	seedBrokenHashIndex(t, s)

	rep, err := s.RepairBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, rep.Apply)
	assert.Equal(t, 7, rep.Broken())
	assert.Equal(t, 7, rep.Repointed())
	assert.Zero(t, kindOf(rep, "hash").Raced)

	assert.Equal(t, "ok", hashKeyOwners(t, s, "h-ok")[0], "a good key is untouched")
	assert.Equal(t, "other", hashKeyOwners(t, s, "h-other")[0])
	assert.Equal(t, "miss", hashKeyOwners(t, s, "h-miss")[0], "missing")
	assert.Equal(t, "dang", hashKeyOwners(t, s, "h-dang")[0], "dangling")
	assert.Equal(t, "wrong", hashKeyOwners(t, s, "h-wrong")[0], "wrong holder")
	assert.Equal(t, [3]string{"live-dup", "live-dup", "live-dup"}, hashKeyOwners(t, s, "h-dup"), "a live holder replaces a trashed owner")
	assert.Equal(t, "m1", hashKeyOwners(t, s, "h-two")[0], "the smallest live id wins")

	// A second run finds nothing to do.
	again, err := s.RepairBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.Zero(t, again.Broken(), "%+v", again)
	assert.Zero(t, again.Repointed())
}

// A key naming a row that cannot be decoded is not overwritten on a guess.
func TestRepairBookHashIndex_LeavesAnUnreadableOwnerAlone(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "unr", "h-unr", false)
	setRawKey(t, s, "book:hash:h-unr", "garbage")
	setRawKey(t, s, "book:garbage", "{not json")

	rep, err := s.RepairBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, kindOf(rep, "hash").Unreadable, "%+v", rep)
	assert.Zero(t, rep.Repointed())
	v, closer, err := s.db.Get([]byte("book:hash:h-unr"))
	require.NoError(t, err)
	assert.Equal(t, "garbage", string(v))
	closer.Close()
}

// A key that changed between the check and the write is left as it is.
func TestRepointBookHashKey_LeavesAKeyThatChangedMeanwhile(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a", "H", false)
	raced, err := s.repointBookHashKey([]byte("book:hash:H"), "someone-else", true, "a")
	require.NoError(t, err)
	assert.True(t, raced)
	raced, err = s.repointBookHashKey([]byte("book:hash:H"), "", false, "a")
	require.NoError(t, err)
	assert.True(t, raced, "it was absent when read, present now")
	assert.Equal(t, "a", hashKeyOwners(t, s, "H")[0])
}

func TestCheckBookHashIndex_StopsWhenCancelled(t *testing.T) {
	s := setupTestPebbleStore(t)
	seedBrokenHashIndex(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.RepairBookHashIndex(ctx, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	// Nothing was written.
	assert.Equal(t, "trashed-dup", hashKeyOwners(t, s, "h-dup")[0])
}
