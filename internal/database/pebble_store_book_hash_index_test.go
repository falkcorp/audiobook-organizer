// file: internal/database/pebble_store_book_hash_index_test.go
// version: 2.0.0
// guid: 4e8b2d63-9a17-4c50-b3f6-1d7a0c5e9b82
// last-edited: 2026-10-22

package database

import (
	"context"
	"fmt"
	"sync"
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

// hashOwners is what the three lookups resolve to for the hash hashBook wrote
// ("" for none).
func hashOwners(t *testing.T, s *PebbleStore, hash string) [3]string {
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

func setHashes(t *testing.T, s *PebbleStore, id, hash string) {
	t.Helper()
	require.NoError(t, setHashesErr(s, id, hash))
}

// setHashesErr is setHashes for goroutines other than the test's.
func setHashesErr(s *PebbleStore, id, hash string) error {
	b, err := s.GetBookByID(id)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("book %s not found", id)
	}
	b.FileHash, b.OriginalFileHash, b.OrganizedFileHash = &hash, new("o-"+hash), new("g-"+hash)
	_, err = s.UpdateBook(id, b)
	return err
}

func setRawKey(t *testing.T, s *PebbleStore, key, value string) {
	t.Helper()
	require.NoError(t, s.db.Set([]byte(key), []byte(value), pebble.Sync))
}

func rawKeyExists(t *testing.T, s *PebbleStore, key []byte) bool {
	t.Helper()
	_, closer, err := s.db.Get(key)
	if err != nil {
		require.ErrorIs(t, err, pebble.ErrNotFound)
		return false
	}
	closer.Close()
	return true
}

func kindOf(rep BookHashIndexReport, name string) BookHashKindReport {
	for _, k := range rep.Kinds {
		if k.Kind == name {
			return k
		}
	}
	return BookHashKindReport{}
}

// Two books holding one file each have their OWN key, so deleting either one
// leaves the other found. The single-valued index named one of them and the
// delete of the other erased it (the 2026-10-21 purge of combine shells).
func TestBookHashIndex_DeletingOneHolderKeepsTheOther(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a", "H", false)
	hashBook(t, s, "b", "H", false)
	require.Equal(t, [3]string{"a", "a", "a"}, hashOwners(t, s, "H"), "the smallest live id, not the last writer")

	require.NoError(t, s.DeleteBook("a"))
	assert.Equal(t, [3]string{"b", "b", "b"}, hashOwners(t, s, "H"))

	hashBook(t, s, "c", "H", false)
	require.NoError(t, s.DeleteBook("c"), "deleting the LAST writer must not erase b either")
	assert.Equal(t, [3]string{"b", "b", "b"}, hashOwners(t, s, "H"))

	require.NoError(t, s.DeleteBook("b"))
	assert.Equal(t, [3]string{"", "", ""}, hashOwners(t, s, "H"))
	assert.False(t, rawKeyExists(t, s, bookHashKey(bookHashKindFile, "H", "b")), "a deleted book leaves no key")
}

// Changing one holder's hash moves only that holder's keys.
func TestBookHashIndex_UpdatingOneHolderKeepsTheOther(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a", "H", false)
	hashBook(t, s, "b", "H", false)

	setHashes(t, s, "a", "H2")
	assert.Equal(t, [3]string{"b", "b", "b"}, hashOwners(t, s, "H"), "a no longer holds H; b still does")
	assert.Equal(t, [3]string{"a", "a", "a"}, hashOwners(t, s, "H2"))

	setHashes(t, s, "b", "H3")
	assert.Equal(t, [3]string{"", "", ""}, hashOwners(t, s, "H"))
	assert.Equal(t, [3]string{"b", "b", "b"}, hashOwners(t, s, "H3"))
	assert.False(t, rawKeyExists(t, s, bookHashKey(bookHashKindFile, "H", "a")))
	assert.False(t, rawKeyExists(t, s, bookHashKey(bookHashKindFile, "H", "b")))
}

// UpdateBook sets the current keys every time, so a key that went missing heals
// on the book's next write even when its hash did not change.
func TestBookHashIndex_UpdateBookHealsAMissingKey(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a", "H", false)
	key := bookHashKey(bookHashKindFile, "H", "a")
	require.NoError(t, s.db.Delete(key, pebble.Sync))
	got, err := s.GetBookByFileHash("H")
	require.NoError(t, err)
	require.Nil(t, got, "no key, no legacy key: not found")

	setHashes(t, s, "a", "H")
	assert.True(t, rawKeyExists(t, s, key))
	assert.Equal(t, "a", hashOwners(t, s, "H")[0])
}

// The choice among holders is deterministic: the live holder with the smallest
// id, whatever the write order; a trashed holder only when no live one exists.
func TestBookHashIndex_ChoosesLiveSmallestThenTrashedSmallest(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a-trashed", "H", true)
	hashBook(t, s, "z-live", "H", false)
	hashBook(t, s, "m-live", "H", false)
	assert.Equal(t, [3]string{"m-live", "m-live", "m-live"}, hashOwners(t, s, "H"), "live beats a trashed holder with a smaller id")

	require.NoError(t, s.DeleteBook("m-live"))
	require.NoError(t, s.DeleteBook("z-live"))
	assert.Equal(t, [3]string{"a-trashed", "a-trashed", "a-trashed"}, hashOwners(t, s, "H"), "a trashed holder is the fallback")

	hashBook(t, s, "b-trashed", "T", true)
	hashBook(t, s, "a-trashed2", "T", true)
	assert.Equal(t, "a-trashed2", hashOwners(t, s, "T")[0], "smallest trashed id")
}

// Every candidate is verified against its own row: an extra key (a book that
// changed its hash behind a stale key, a row that is gone) is ignored.
func TestBookHashIndex_ReaderIgnoresKeysTheRowDoesNotBack(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "real", "H", false)
	hashBook(t, s, "other", "X", false)
	// "other" does not hold H, "ghost" has no row; both sort before "real".
	require.NoError(t, s.db.Set(bookHashKey(bookHashKindFile, "H", "aa-ghost"), []byte{}, pebble.Sync))
	require.NoError(t, s.db.Set(bookHashKey(bookHashKindFile, "H", "aa-other"), []byte{}, pebble.Sync))
	require.NoError(t, s.db.Set(bookHashKey(bookHashKindFile, "H", "other"), []byte{}, pebble.Sync))

	got, err := s.GetBookByFileHash("H")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "real", got.ID)
	got, err = s.GetBookByFileHash("nobody-holds-this")
	require.NoError(t, err)
	assert.Nil(t, got)
	got, err = s.GetBookByFileHash("")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// A candidate whose row cannot be decoded is skipped when another resolves, and
// is an error (never "no such book") when nothing else does.
func TestBookHashIndex_UndecodableCandidate(t *testing.T) {
	s := setupTestPebbleStore(t)
	setRawKey(t, s, "book:bad", "{not json")
	require.NoError(t, s.db.Set(bookHashKey(bookHashKindFile, "H", "bad"), []byte{}, pebble.Sync))

	got, err := s.GetBookByFileHash("H")
	require.Error(t, err, "a lookup that cannot tell must not answer no")
	assert.Nil(t, got)

	hashBook(t, s, "good", "H", false)
	got, err = s.GetBookByFileHash("H")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "good", got.ID)
}

// Until the index is trusted a lookup is the UNION of the new keys and the
// legacy single-valued key, so a book not yet backfilled is found as it always
// was. The repair then makes the index complete, sets the sentinel and deletes
// the legacy keys.
func TestBookHashIndex_UntrustedReadsTheLegacyKeyAndRepairTrustsAndSweeps(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "old", "H", false)
	// "old" predates the new layout: it has only the legacy key.
	for i := range bookHashKinds {
		require.NoError(t, s.db.Delete(bookHashKey(i, map[int]string{0: "H", 1: "o-H", 2: "g-H"}[i], "old"), pebble.Sync))
	}
	setRawKey(t, s, "book:hash:H", "old")
	setRawKey(t, s, "book:originalhash:o-H", "old")
	setRawKey(t, s, "book:organizedhash:g-H", "old")
	trusted, err := s.bookHashIndexTrusted()
	require.NoError(t, err)
	require.False(t, trusted)
	assert.Equal(t, [3]string{"old", "old", "old"}, hashOwners(t, s, "H"), "found through the legacy key")

	// A book written after the cut-over has only new keys; the union returns
	// the smaller id of the two holders.
	hashBook(t, s, "new", "H", false)
	assert.Equal(t, "new", hashOwners(t, s, "H")[0])

	rep, err := s.RepairBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 3, rep.Missing(), "old's three keys")
	assert.Equal(t, 3, rep.Written())
	assert.True(t, rep.Trusted)
	assert.True(t, rep.LegacySwept)
	assert.False(t, rawKeyExists(t, s, []byte("book:hash:H")))
	assert.False(t, rawKeyExists(t, s, []byte("book:originalhash:o-H")))
	assert.False(t, rawKeyExists(t, s, []byte("book:organizedhash:g-H")))
	assert.Equal(t, [3]string{"new", "new", "new"}, hashOwners(t, s, "H"))
	require.NoError(t, s.DeleteBook("new"))
	assert.Equal(t, [3]string{"old", "old", "old"}, hashOwners(t, s, "H"), "old's own keys now back it")

	// Trusted: a legacy-shaped key written afterwards is not consulted.
	setRawKey(t, s, "book:hash:ghosted", "old")
	got, err := s.GetBookByFileHash("ghosted")
	require.NoError(t, err)
	assert.Nil(t, got)

	again, err := s.RepairBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.Zero(t, again.Missing(), "%+v", again)
	assert.Zero(t, again.Written())
}

func TestCheckBookHashIndex_ReportsAndWritesNothing(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a", "H", false)
	hashBook(t, s, "b", "H", true)
	hashBook(t, s, "c", "C", false)
	hashBook(t, s, "nohash", "", false)
	require.NoError(t, s.db.Delete(bookHashKey(bookHashKindFile, "H", "b"), pebble.Sync))
	require.NoError(t, s.db.Delete(bookHashKey(bookHashKindOrganized, "g-C", "c"), pebble.Sync))

	rep, err := s.CheckBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, rep.Apply)
	assert.Equal(t, 4, rep.Books)
	assert.Equal(t, 3, rep.LiveBooks)
	assert.Equal(t, BookHashKindReport{Kind: "hash", Checked: 3, OK: 2, Missing: 1}, kindOf(rep, "hash"))
	assert.Equal(t, BookHashKindReport{Kind: "originalhash", Checked: 3, OK: 3}, kindOf(rep, "originalhash"))
	assert.Equal(t, BookHashKindReport{Kind: "organizedhash", Checked: 3, OK: 2, Missing: 1}, kindOf(rep, "organizedhash"))
	assert.Equal(t, 2, rep.Missing())
	assert.Zero(t, rep.Written())
	assert.Len(t, rep.Samples, 2)
	assert.False(t, rep.Trusted)
	assert.False(t, rep.LegacySwept)

	assert.False(t, rawKeyExists(t, s, bookHashKey(bookHashKindFile, "H", "b")), "a check writes nothing")
	trusted, err := s.bookHashIndexTrusted()
	require.NoError(t, err)
	assert.False(t, trusted, "a check does not set the sentinel")
}

// A book row that cannot be decoded cannot have its hashes indexed, so the
// repair writes the rest, reports it, and leaves the sentinel and the legacy
// keys alone.
func TestRepairBookHashIndex_UndecodableRowKeepsTheIndexUntrusted(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a", "H", false)
	require.NoError(t, s.db.Delete(bookHashKey(bookHashKindFile, "H", "a"), pebble.Sync))
	setRawKey(t, s, "book:zz-bad", "{not json")
	setRawKey(t, s, "book:hash:H", "a")

	rep, err := s.RepairBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Unreadable)
	assert.Equal(t, []string{"zz-bad"}, rep.SampleUnreadable)
	assert.Equal(t, 1, rep.Written(), "the readable book is still repaired")
	assert.False(t, rep.Trusted)
	assert.False(t, rep.LegacySwept)
	assert.True(t, rawKeyExists(t, s, []byte("book:hash:H")), "the legacy key stays while the index is untrusted")
	assert.True(t, rawKeyExists(t, s, bookHashKey(bookHashKindFile, "H", "a")))
}

func TestRepairBookHashIndex_StopsWhenCancelled(t *testing.T) {
	s := setupTestPebbleStore(t)
	hashBook(t, s, "a", "H", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.RepairBookHashIndex(ctx, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	trusted, terr := s.bookHashIndexTrusted()
	require.NoError(t, terr)
	assert.False(t, trusted, "a cancelled repair never sets the sentinel")
}

// Concurrent writers over books that share hashes: per-book keys mean no
// writer can erase another's entry, so afterwards every surviving book is found
// under every hash it holds and no deleted book is ever returned. Run under
// -race.
func TestBookHashIndex_ConcurrentWritersOnSharedHashes(t *testing.T) {
	s := setupTestPebbleStore(t)
	const books, hashes, rounds = 48, 4, 6
	id := func(i int) string { return fmt.Sprintf("b%02d", i) }
	for i := range books {
		hashBook(t, s, id(i), fmt.Sprintf("h%d", i%hashes), false)
	}

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for r := range rounds {
				for i := w; i < books; i += 8 {
					assert.NoError(t, setHashesErr(s, id(i), fmt.Sprintf("h%d", (i+r+1)%hashes)))
				}
			}
			for i := w; i < books; i += 8 {
				if i%3 == 0 {
					assert.NoError(t, s.DeleteBook(id(i)))
				}
			}
		})
	}
	wg.Wait()

	live := map[string][]string{} // hash -> surviving ids in ascending order
	for i := range books {
		if i%3 == 0 {
			continue
		}
		b, err := s.GetBookByID(id(i))
		require.NoError(t, err)
		require.NotNil(t, b)
		live[*b.FileHash] = append(live[*b.FileHash], b.ID)
	}
	for h := range hashes {
		hash := fmt.Sprintf("h%d", h)
		got, err := s.GetBookByFileHash(hash)
		require.NoError(t, err)
		if ids := live[hash]; len(ids) > 0 {
			require.NotNil(t, got, "hash %s has holders %v", hash, ids)
			assert.Equal(t, ids[0], got.ID, "hash %s", hash)
		} else {
			assert.Nil(t, got)
		}
	}
	rep, err := s.CheckBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.Zero(t, rep.Missing(), "writers kept every key: %+v", rep)
}

// The repair running beside live writers loses nothing: keys it writes for a
// book that has since changed or been deleted are extras the reader ignores, and
// a later run finds nothing missing.
func TestRepairBookHashIndex_RacingLiveWriters(t *testing.T) {
	s := setupTestPebbleStore(t)
	const books = 60
	id := func(i int) string { return fmt.Sprintf("b%02d", i) }
	for i := range books {
		hashBook(t, s, id(i), fmt.Sprintf("h%d", i%5), false)
		if i%2 == 0 { // as if written before the cut-over
			for k := range bookHashKinds {
				h := bookHashKinds[k].ofBook(hashTestBook(t, s, id(i)))
				require.NoError(t, s.db.Delete(bookHashKey(k, h, id(i)), pebble.Sync))
			}
		}
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 3 {
			_, err := s.RepairBookHashIndex(context.Background(), nil)
			assert.NoError(t, err)
		}
	})
	for w := range 4 {
		wg.Go(func() {
			for i := w; i < books; i += 4 {
				assert.NoError(t, setHashesErr(s, id(i), fmt.Sprintf("n%d", i%7)))
				if i%5 == 0 {
					assert.NoError(t, s.DeleteBook(id(i)))
				}
			}
		})
	}
	wg.Wait()

	last, err := s.RepairBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, last.Trusted)
	after, err := s.CheckBookHashIndex(context.Background(), nil)
	require.NoError(t, err)
	assert.Zero(t, after.Missing(), "%+v", after)
	for i := range books {
		b, err := s.GetBookByID(id(i))
		require.NoError(t, err)
		if b == nil {
			continue
		}
		got, err := s.GetBookByFileHash(*b.FileHash)
		require.NoError(t, err)
		require.NotNil(t, got, "book %s must be found under its own hash", b.ID)
		assert.LessOrEqual(t, got.ID, b.ID, "the choice is the smallest live holder")
		assert.Equal(t, *b.FileHash, *got.FileHash)
	}
}

func hashTestBook(t *testing.T, s *PebbleStore, id string) *Book {
	t.Helper()
	b, err := s.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}
