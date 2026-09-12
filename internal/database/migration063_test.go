// file: internal/database/migration063_test.go
// version: 1.1.0
// guid: e059ecc0-ab71-4ae2-8d64-0f07261fabc2
// last-edited: 2026-09-12

package database

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// writeRawJunction stores a junction value exactly as a pre-fix setter would
// have, bypassing the stamp SetBookAuthors/SetBookNarrators now apply.
func writeRawJunction(t *testing.T, s *PebbleStore, prefix, bookID string, rows any) {
	t.Helper()
	data, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NoError(t, s.db.Set([]byte(prefix+bookID), data, pebble.Sync))
}

func rawJunctionBytes(t *testing.T, s *PebbleStore, key string) []byte {
	t.Helper()
	val, closer, err := s.db.Get([]byte(key))
	require.NoError(t, err)
	defer closer.Close()
	return append([]byte(nil), val...)
}

// TestMigration063StampsDamagedJunctionRows seeds one book_id-less narrator
// row and one author row carrying ANOTHER book's id, runs the migration, and
// checks the stored bytes (not the getters, which stamp on read), memdb, and a
// second run.
func TestMigration063StampsDamagedJunctionRows(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	book := mkAuthorRefBook(t, store, "Mig63Damaged", 0, true, false)
	other := mkAuthorRefBook(t, store, "Mig63Other", 0, true, false)
	healthy := mkAuthorRefBook(t, store, "Mig63Healthy", 0, true, false)
	narrator, err := store.CreateNarrator("Mig63 Narrator")
	require.NoError(t, err)
	author, err := store.CreateAuthor("Mig63 Author")
	require.NoError(t, err)

	writeRawJunction(t, store, "book_narrators:", book.ID, []BookNarrator{{NarratorID: narrator.ID, Role: "narrator"}})
	writeRawJunction(t, store, "book_authors:", book.ID, []BookAuthor{{BookID: other.ID, AuthorID: author.ID, Role: "author"}})
	require.NoError(t, store.SetBookNarrators(healthy.ID, []BookNarrator{{BookID: healthy.ID, NarratorID: narrator.ID}}))
	healthyBefore := rawJunctionBytes(t, store, "book_narrators:"+healthy.ID)

	require.Empty(t, rawJunctionRows[BookNarrator](t, store, "book_narrators:", book.ID)[0].BookID, "fixture must start damaged")

	require.NoError(t, migration063Up(store))

	nr := rawJunctionRows[BookNarrator](t, store, "book_narrators:", book.ID)
	require.Len(t, nr, 1)
	require.Equal(t, book.ID, nr[0].BookID)
	require.Equal(t, narrator.ID, nr[0].NarratorID, "only book_id may change")
	require.Equal(t, "narrator", nr[0].Role, "only book_id may change")

	ar := rawJunctionRows[BookAuthor](t, store, "book_authors:", book.ID)
	require.Len(t, ar, 1)
	require.Equal(t, book.ID, ar[0].BookID, "a row filed under this key belongs to this book")
	require.Equal(t, author.ID, ar[0].AuthorID)

	require.Equal(t, healthyBefore, rawJunctionBytes(t, store, "book_narrators:"+healthy.ID),
		"a key whose rows already agree must not be rewritten")

	// memdb: the narrator row is present under the right book, and the author
	// row is indexed under book, not under other.
	var sawNarrator bool
	for _, r := range memBookNarratorRows(t, store) {
		if r.NarratorID == narrator.ID && r.BookID == book.ID {
			sawNarrator = true
		}
	}
	require.True(t, sawNarrator, "memdb must hold the repaired narrator row under its book")
	for _, r := range memBookAuthorRows(t, store) {
		if r.AuthorID == author.ID {
			require.Equal(t, book.ID, r.BookID, "memdb must not index the credit under another book")
		}
	}
	requireUpdateReachesMemDB(t, store, book.ID, "Mig63Damaged (renamed)")

	// Idempotency: a second pass finds nothing and writes nothing.
	nBefore := rawJunctionBytes(t, store, "book_narrators:"+book.ID)
	aBefore := rawJunctionBytes(t, store, "book_authors:"+book.ID)
	res, err := store.RepairJunctionBookIDs()
	require.NoError(t, err)
	require.Zero(t, res.AuthorKeysRepaired+res.NarratorKeysRepaired, "second pass must repair nothing")
	require.Equal(t, 2, res.NarratorKeysScanned)
	require.NoError(t, migration063Up(store))
	require.True(t, bytes.Equal(nBefore, rawJunctionBytes(t, store, "book_narrators:"+book.ID)))
	require.True(t, bytes.Equal(aBefore, rawJunctionBytes(t, store, "book_authors:"+book.ID)))
}

// pendingOps reads the warmup write-through buffer length under its lock.
func pendingOps(s *PebbleStore) int {
	s.memPending.mu.Lock()
	defer s.memPending.mu.Unlock()
	return len(s.memPending.ops)
}

// TestMigration063SendsNoMemdbWritesWhileWarmupBuffers covers the startup
// path. RunMigrations runs while the async warmup is still in flight, and
// every memSync issued then is buffered, up to memPendingOpCap. Overflowing the
// cap abandons memdb for the process, so one buffered write per repaired key
// could switch memdb off on a heavily damaged library. The migration must
// repair Pebble and leave the buffer alone; warmup's own key stamping loads the
// right rows.
func TestMigration063SendsNoMemdbWritesWhileWarmupBuffers(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	book := mkAuthorRefBook(t, store, "Mig63Buffering", 0, true, false)
	control := mkAuthorRefBook(t, store, "Mig63BufferingControl", 0, true, false)
	narrator, err := store.CreateNarrator("Mig63 Buffering Narrator")
	require.NoError(t, err)
	author, err := store.CreateAuthor("Mig63 Buffering Author")
	require.NoError(t, err)
	writeRawJunction(t, store, "book_narrators:", book.ID, []BookNarrator{{NarratorID: narrator.ID}})
	writeRawJunction(t, store, "book_authors:", book.ID, []BookAuthor{{AuthorID: author.ID}})

	// Put the store into the state NewPebbleStore leaves it in while warmup
	// scans. Always disarm, so Close does not wait on a warmup that never runs.
	store.beginMemWarmupBuffering()
	t.Cleanup(store.endMemWarmupBuffering)
	require.Zero(t, pendingOps(store))

	require.NoError(t, migration063Up(store))
	require.Zero(t, pendingOps(store), "the migration must not queue memdb writes while warmup buffers")
	require.Equal(t, book.ID, rawJunctionRows[BookNarrator](t, store, "book_narrators:", book.ID)[0].BookID,
		"Pebble must still be repaired while memdb is buffering")
	require.Equal(t, book.ID, rawJunctionRows[BookAuthor](t, store, "book_authors:", book.ID)[0].BookID)

	// Control: in this state an ordinary write-through IS queued, so the zero
	// above is not an artefact of a buffer that never fills.
	require.NoError(t, store.SetBookNarrators(control.ID, []BookNarrator{{NarratorID: narrator.ID}}))
	require.Equal(t, 1, pendingOps(store), "fixture must actually be in the buffering state")
}

// TestMigration063CountsWhatItRepaired pins the report: the startup log is the
// only record of what the migration touched in production.
func TestMigration063CountsWhatItRepaired(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	book := mkAuthorRefBook(t, store, "Mig63Counts", 0, true, false)
	writeRawJunction(t, store, "book_narrators:", book.ID, []BookNarrator{{NarratorID: 1}, {NarratorID: 2}, {BookID: book.ID, NarratorID: 3}})
	require.NoError(t, store.db.Set([]byte("book_authors:"+book.ID), []byte("not json"), pebble.Sync))

	res, err := store.RepairJunctionBookIDs()
	require.NoError(t, err)
	require.Equal(t, 1, res.NarratorKeysRepaired)
	require.Equal(t, 2, res.NarratorRowsRepaired, "the row that already agreed is not counted")
	require.Equal(t, 1, res.Undecodable)
	require.Equal(t, []byte("not json"), rawJunctionBytes(t, store, "book_authors:"+book.ID),
		"an undecodable value has no correct replacement and must be left as found")
}

// TestWarmupAdmitsJunctionRowWithoutBookID covers the restart path: warmup is
// async and races the startup migrations, so it must not depend on migration
// 63 having run. Before the fix it rejected the row and flagged the table
// incomplete for the whole process lifetime.
func TestWarmupAdmitsJunctionRowWithoutBookID(t *testing.T) {
	dir := t.TempDir()
	first, err := NewPebbleStore(dir)
	require.NoError(t, err)
	first.WaitForWarmup()
	book := mkAuthorRefBook(t, first, "WarmupDamaged", 0, true, false)
	narrator, err := first.CreateNarrator("Warmup Narrator")
	require.NoError(t, err)
	writeRawJunction(t, first, "book_narrators:", book.ID, []BookNarrator{{NarratorID: narrator.ID}})
	require.NoError(t, first.Close())

	store := seedAuthorRefStore(t, dir) // reopens and waits for warmup; no migrations run
	require.Empty(t, store.mem().LostRows(), "warmup must admit the row, not reject it")
	rows := memBookNarratorRows(t, store)
	require.Len(t, rows, 1)
	require.Equal(t, book.ID, rows[0].BookID)
	requireUpdateReachesMemDB(t, store, book.ID, "WarmupDamaged (renamed)")
}
