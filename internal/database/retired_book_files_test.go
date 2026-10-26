// file: internal/database/retired_book_files_test.go
// version: 1.3.1
// guid: 7e41c2b9-3d58-4f06-a1b7-9c20d6e85f13
// last-edited: 2026-10-26

package database

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// retiredFixture is a live holder book, a shell book whose files will be
// retired into it, and the retired version that will hold them.
type retiredFixture struct {
	ps      *PebbleStore
	holder  *Book
	shell   *Book
	version *BookVersion
}

func newRetiredFixture(t *testing.T) *retiredFixture {
	t.Helper()
	store, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := store.(*PebbleStore)
	ps.WaitForWarmup()

	holder, err := ps.CreateBook(&Book{Title: "Retire Holder", FilePath: "/lib/ret/holder"})
	require.NoError(t, err)
	shell, err := ps.CreateBook(&Book{Title: "Retire Shell", FilePath: "/lib/ret/shell"})
	require.NoError(t, err)
	v, err := ps.CreateBookVersion(&BookVersion{
		BookID: holder.ID, Status: BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	return &retiredFixture{ps: ps, holder: holder, shell: shell, version: v}
}

func (f *retiredFixture) create(t *testing.T, bookID string, row BookFile) *BookFile {
	t.Helper()
	row.BookID = bookID
	require.NoError(t, f.ps.CreateBookFile(&row))
	return &row
}

func (f *retiredFixture) retire(t *testing.T, rows ...*BookFile) error {
	t.Helper()
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return f.ps.RetireBookFiles([]BookFileMove{{FileIDs: ids, SourceBookID: f.shell.ID}}, f.holder.ID, f.version.ID)
}

// indexOwner is the raw value of a single-owner index key ("" when absent).
func indexOwner(t *testing.T, ps *PebbleStore, key string) string {
	t.Helper()
	v, closer, err := ps.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return ""
	}
	require.NoError(t, err)
	defer closer.Close()
	return string(v)
}

func countAllRows(t *testing.T, ps *PebbleStore) int {
	t.Helper()
	all, err := ps.getAllBookFilesPebbleScanFiltered(bookFilesAll)
	require.NoError(t, err)
	return len(all)
}

func TestRetireBookFiles_RepointsRowsAndDeletesNone(t *testing.T) {
	for _, useMem := range []bool{true, false} {
		t.Run(map[bool]string{true: "memdb", false: "pebble"}[useMem], func(t *testing.T) {
			fx := newRetiredFixture(t)
			live := fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/holder/01.mp3", Duration: 100, FileSize: 1000, TrackNumber: 1})
			x1 := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/shell/01.mp3", FileHash: "hash-x1", Duration: 111, FileSize: 111, TrackNumber: 1, ITunesPersistentID: "PID-X1"})
			x2 := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/shell/02.mp3", FileHash: "hash-x2", Duration: 222, FileSize: 222, TrackNumber: 2})
			before := countAllRows(t, fx.ps)
			require.Equal(t, 3, before)

			require.NoError(t, fx.retire(t, x1, x2))
			fx.ps.UseMemDB = useMem

			require.Equal(t, before, countAllRows(t, fx.ps), "retiring must not delete or add a book_file row")

			shellRows, err := fx.ps.GetBookFiles(fx.shell.ID)
			require.NoError(t, err)
			require.Empty(t, shellRows, "the shell no longer keys any row")

			holderLive, err := fx.ps.GetBookFiles(fx.holder.ID)
			require.NoError(t, err)
			require.Len(t, holderLive, 1, "the holder's own file list is unchanged by a retire")
			require.Equal(t, live.ID, holderLive[0].ID)

			all, err := fx.ps.GetBookFilesIncludingRetired(fx.holder.ID)
			require.NoError(t, err)
			require.Len(t, all, 3)

			retired, err := fx.ps.GetRetiredBookFiles(fx.holder.ID)
			require.NoError(t, err)
			require.Len(t, retired, 2)
			for _, r := range retired {
				require.True(t, r.Retired)
				require.Equal(t, fx.holder.ID, r.BookID)
				require.Equal(t, fx.version.ID, r.VersionID)
			}

			// The row is intact: content, hashes and ids survive the re-key.
			got, err := fx.ps.GetBookFileByID(fx.holder.ID, x1.ID)
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, "hash-x1", got.FileHash)
			require.Equal(t, 111, got.Duration)
			require.Equal(t, "PID-X1", got.ITunesPersistentID)
			old, err := fx.ps.GetBookFileByID(fx.shell.ID, x1.ID)
			require.NoError(t, err)
			require.Nil(t, old, "the old key is gone")
			require.Equal(t, "book_file:"+fx.holder.ID+":"+x1.ID, indexOwner(t, fx.ps, "book_file_id:"+x1.ID))

			// Keys nobody else carries stay with the retired row, re-pointed at
			// its new key. The single-identity lookups answer "which LIVE file",
			// so they answer nil; the complete and include-retired lookups still
			// resolve the retired row.
			require.Equal(t, fx.holder.ID+":"+x1.ID, indexOwner(t, fx.ps, "book_file_path:"+bookFilePathCRC("/lib/ret/shell/01.mp3")))
			hit, err := fx.ps.GetBookFileByPath("/lib/ret/shell/01.mp3")
			require.NoError(t, err)
			require.Nil(t, hit, "a path held only by a retired row has no live file")
			pid, err := fx.ps.GetBookFileByPID("PID-X1")
			require.NoError(t, err)
			require.Nil(t, pid, "a PID held only by a retired row has no live file")
			pidAny, err := fx.ps.GetBookFileByPIDIncludingRetired("PID-X1")
			require.NoError(t, err)
			require.NotNil(t, pidAny)
			require.Equal(t, x1.ID, pidAny.ID)
			require.True(t, pidAny.Retired)
			// The complete lookup needs memdb: it answers in the memdb arm and
			// refuses (rather than guessing) in the pebble arm. Asserting each arm
			// means neither can be skipped silently.
			atPath, aerr := fx.ps.BookFilesAtPath("/lib/ret/shell/01.mp3")
			if useMem {
				require.NoError(t, aerr)
				require.Len(t, atPath, 1, "BookFilesAtPath is the complete lookup and keeps retired rows")
				require.Equal(t, x1.ID, atPath[0].ID)
			} else {
				require.ErrorIs(t, aerr, ErrBookFilesAtPathUnavailable)
			}
			// The single-row twin sees the retired row in both modes, and is what
			// the ownership fallback reads while memdb is not serving.
			pathAny, err := fx.ps.GetBookFileByPathIncludingRetired("/lib/ret/shell/01.mp3")
			require.NoError(t, err)
			require.NotNil(t, pathAny)
			require.Equal(t, x1.ID, pathAny.ID)
			owners, err := BookFileRowsAtPath(fx.ps, "/lib/ret/shell/01.mp3")
			require.NoError(t, err)
			require.Len(t, owners, 1, "the scanner's ownership lookup counts the retired row in both modes (SF2)")
			require.Equal(t, x1.ID, owners[0].ID)

			// The holder's aggregates still describe only its live file.
			hb, err := fx.ps.GetBookByID(fx.holder.ID)
			require.NoError(t, err)
			require.NotNil(t, hb.Duration)
			require.Equal(t, 100, *hb.Duration)
		})
	}
}

// The single-owner index rule, in both creation orders: whoever wrote last
// owns the key before the retire, and after it the live row owns it.
func TestRetireBookFiles_IndexNeverHidesALiveRow(t *testing.T) {
	for _, order := range []string{"retired-row-created-first", "live-row-created-first"} {
		t.Run(order, func(t *testing.T) {
			fx := newRetiredFixture(t)
			const path = "/lib/ret/shared/01.mp3"
			mk := func(bookID string) *BookFile {
				return fx.create(t, bookID, BookFile{FilePath: path, FileHash: "shared-hash", OriginalFileHash: "shared-orig"})
			}
			var live, x *BookFile
			if order == "retired-row-created-first" {
				x = mk(fx.shell.ID)
				live = mk(fx.holder.ID)
			} else {
				live = mk(fx.holder.ID)
				x = mk(fx.shell.ID)
			}
			wantOwner := func(owner *BookFile) {
				t.Helper()
				ref := owner.BookID + ":" + owner.ID
				require.Equal(t, ref, indexOwner(t, fx.ps, "book_file_path:"+bookFilePathCRC(path)))
				require.Equal(t, ref, indexOwner(t, fx.ps, "book_file_hash:shared-hash"))
				require.Equal(t, ref, indexOwner(t, fx.ps, "book_file_orig_hash:shared-orig"))
			}
			if order == "retired-row-created-first" {
				wantOwner(live)
			} else {
				wantOwner(x)
			}

			require.NoError(t, fx.retire(t, x))

			wantOwner(live)
			hit, err := fx.ps.GetBookFileByPath(path)
			require.NoError(t, err)
			require.NotNil(t, hit)
			require.Equal(t, live.ID, hit.ID, "GetBookFileByPath must answer the live row")
			require.False(t, hit.Retired)

			// The complete lookup still shows both, the retired one flagged.
			rows, err := fx.ps.BookFilesAtPath(path)
			require.NoError(t, err)
			require.Len(t, rows, 2)
			for _, r := range rows {
				require.Equal(t, r.ID == x.ID, r.Retired)
			}
			withHash, err := fx.ps.BookFilesWithHash("shared-hash")
			require.NoError(t, err)
			require.Len(t, withHash, 2)

			// And the inverse hands nothing back that the live row owns.
			require.NoError(t, fx.ps.UnretireBookFiles(
				[]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))
			wantOwner(live)
			back, err := fx.ps.GetBookFileByID(fx.shell.ID, x.ID)
			require.NoError(t, err)
			require.NotNil(t, back)
			require.False(t, back.Retired)
		})
	}
}

// With no live row carrying the key, a retired row keeps it, and un-retiring
// gives it back to the shell. The key must never be left naming a key that no
// longer exists.
func TestRetireBookFiles_FreeKeyFollowsTheRowBothWays(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/only/01.mp3", FileHash: "only-hash", ITunesPersistentID: "PID-ONLY"})
	pathKey := "book_file_path:" + bookFilePathCRC("/lib/ret/only/01.mp3")

	require.NoError(t, fx.retire(t, x))
	for _, key := range []string{pathKey, "book_file_hash:only-hash", "book_file_pid:PID-ONLY"} {
		require.Equal(t, fx.holder.ID+":"+x.ID, indexOwner(t, fx.ps, key), key)
	}

	before, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
	require.NoError(t, err)
	require.NoError(t, fx.ps.UnretireBookFiles(
		[]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, map[string]string{x.ID: "v-before"}))

	for _, key := range []string{pathKey, "book_file_hash:only-hash", "book_file_pid:PID-ONLY"} {
		require.Equal(t, fx.shell.ID+":"+x.ID, indexOwner(t, fx.ps, key), key)
	}
	after, err := fx.ps.GetBookFileByID(fx.shell.ID, x.ID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.False(t, after.Retired)
	require.Equal(t, "v-before", after.VersionID)
	// Everything but the three fields the retire owns round-trips.
	before.BookID, before.Retired, before.VersionID, before.UpdatedAt = after.BookID, after.Retired, after.VersionID, after.UpdatedAt
	require.Equal(t, before, after)
	require.Equal(t, "book_file:"+fx.shell.ID+":"+x.ID, indexOwner(t, fx.ps, "book_file_id:"+x.ID))
}

// When the retiring row owns a key and the store cannot prove no other live
// row holds it (memdb not serving), the retire refuses and writes nothing.
func TestRetireBookFiles_RetryLaterWhenOwnershipCannotBeProven(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/retry/01.mp3", FileHash: "retry-hash"})
	pathKey := "book_file_path:" + bookFilePathCRC("/lib/ret/retry/01.mp3")
	ownerBefore := indexOwner(t, fx.ps, pathKey)
	rowsBefore := countAllRows(t, fx.ps)

	fx.ps.UseMemDB = false
	err := fx.retire(t, x)
	require.ErrorIs(t, err, ErrRetireIndexIncomplete)

	fx.ps.UseMemDB = true
	require.Equal(t, rowsBefore, countAllRows(t, fx.ps))
	require.Equal(t, ownerBefore, indexOwner(t, fx.ps, pathKey), "a refused retire must not touch the index")
	got, err := fx.ps.GetBookFileByID(fx.shell.ID, x.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "the row stays under the shell")
	require.False(t, got.Retired)

	require.NoError(t, fx.retire(t, x), "retry succeeds once memdb serves")
}

// A holder row carrying the key is proof enough: no memdb needed.
func TestRetireBookFiles_HolderRowIsAnAlternativeWithoutMemdb(t *testing.T) {
	fx := newRetiredFixture(t)
	live := fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/alt/01.mp3", FileHash: "alt-hash"})
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/alt/01.mp3", FileHash: "alt-hash"}) // x writes last: owns the keys
	require.Equal(t, fx.shell.ID+":"+x.ID, indexOwner(t, fx.ps, "book_file_hash:alt-hash"))

	fx.ps.UseMemDB = false
	require.NoError(t, fx.retire(t, x))
	require.Equal(t, fx.holder.ID+":"+live.ID, indexOwner(t, fx.ps, "book_file_hash:alt-hash"))
}

func TestRetireBookFiles_Refusals(t *testing.T) {
	t.Run("a row already retired", func(t *testing.T) {
		fx := newRetiredFixture(t)
		x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/r/01.mp3"})
		require.NoError(t, fx.retire(t, x))
		err := fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}}, fx.holder.ID+"x", fx.version.ID)
		require.Error(t, err)
		other, err := fx.ps.CreateBook(&Book{Title: "Other Holder", FilePath: "/lib/ret/other"})
		require.NoError(t, err)
		v2, err := fx.ps.CreateBookVersion(&BookVersion{BookID: other.ID, Status: BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
			RetiredFromBookID: fx.shell.ID, RetiredAt: new(time.Now())})
		require.NoError(t, err)
		err = fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}}, other.ID, v2.ID)
		require.ErrorIs(t, err, ErrBookFileRetired)
	})
	t.Run("a version that is not a retired version of the holder", func(t *testing.T) {
		fx := newRetiredFixture(t)
		x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/v/01.mp3"})
		alt, err := fx.ps.CreateBookVersion(&BookVersion{BookID: fx.holder.ID, Status: BookVersionStatusAlt, Format: "mp3", Source: "manual"})
		require.NoError(t, err)
		err = fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.shell.ID}}, fx.holder.ID, alt.ID)
		require.ErrorIs(t, err, ErrRetiredVersionMismatch)
		other, err := fx.ps.CreateBook(&Book{Title: "Other", FilePath: "/lib/ret/o2"})
		require.NoError(t, err)
		err = fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.shell.ID}}, other.ID, fx.version.ID)
		require.ErrorIs(t, err, ErrRetiredVersionMismatch, "the version belongs to another holder")
		err = fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.shell.ID}}, fx.holder.ID, "no-such-version")
		require.ErrorIs(t, err, ErrRetiredVersionMismatch)
	})
	t.Run("a missing or soft-deleted holder", func(t *testing.T) {
		fx := newRetiredFixture(t)
		x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/h/01.mp3"})
		deleted := true
		fx.holder.MarkedForDeletion = &deleted
		_, err := fx.ps.UpdateBook(fx.holder.ID, fx.holder)
		require.NoError(t, err)
		err = fx.retire(t, x)
		require.ErrorIs(t, err, ErrBookFileOwnerMissing)
		got, err := fx.ps.GetBookFileByID(fx.shell.ID, x.ID)
		require.NoError(t, err)
		require.NotNil(t, got, "nothing written")
	})
	t.Run("a row that is not under the stated source", func(t *testing.T) {
		fx := newRetiredFixture(t)
		err := fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{"nope"}, SourceBookID: fx.shell.ID}}, fx.holder.ID, fx.version.ID)
		require.Error(t, err)
	})
	t.Run("the holder as its own shell", func(t *testing.T) {
		fx := newRetiredFixture(t)
		x := fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/s/01.mp3"})
		err := fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}}, fx.holder.ID, fx.version.ID)
		require.Error(t, err)
	})
	t.Run("a batch with one bad row writes none of it", func(t *testing.T) {
		fx := newRetiredFixture(t)
		x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/b/01.mp3"})
		err := fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{x.ID, "nope"}, SourceBookID: fx.shell.ID}}, fx.holder.ID, fx.version.ID)
		require.Error(t, err)
		got, err := fx.ps.GetBookFileByID(fx.shell.ID, x.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.False(t, got.Retired)
	})
}

func TestUnretireBookFiles_Refusals(t *testing.T) {
	fx := newRetiredFixture(t)
	live := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/u/01.mp3"})
	moves := func(src string, ids ...string) []BookFileMove {
		return []BookFileMove{{FileIDs: ids, SourceBookID: src}}
	}
	err := fx.ps.UnretireBookFiles(moves(fx.shell.ID, live.ID), fx.holder.ID, fx.version.ID, nil)
	require.ErrorIs(t, err, ErrBookFileNotRetired)

	require.NoError(t, fx.retire(t, live))
	err = fx.ps.UnretireBookFiles(moves(fx.holder.ID, live.ID), fx.shell.ID, "some-other-version", nil)
	require.ErrorIs(t, err, ErrRetiredVersionMismatch)
	err = fx.ps.UnretireBookFiles(moves(fx.shell.ID, live.ID), fx.holder.ID, fx.version.ID, nil)
	require.Error(t, err, "the row is not under that source")
	got, err := fx.ps.GetBookFileByID(fx.holder.ID, live.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Retired, "a refused un-retire writes nothing")
}

// Rehoming a retired version follows the chain: the version row and its rows
// move together and the lookups keep resolving.
func TestRehomeRetiredVersion_MovesVersionAndRowsTogether(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/c/01.mp3", FileHash: "chain-hash"})
	require.NoError(t, fx.retire(t, x))
	newHolder, err := fx.ps.CreateBook(&Book{Title: "Chain Holder", FilePath: "/lib/ret/chain"})
	require.NoError(t, err)

	require.NoError(t, fx.ps.RehomeRetiredVersion(fx.version.ID, newHolder.ID))

	oldRows, err := fx.ps.GetBookFilesIncludingRetired(fx.holder.ID)
	require.NoError(t, err)
	require.Empty(t, oldRows)
	rows, err := fx.ps.GetRetiredBookFiles(newHolder.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, fx.version.ID, rows[0].VersionID)
	require.True(t, rows[0].Retired)
	require.Equal(t, newHolder.ID+":"+x.ID, indexOwner(t, fx.ps, "book_file_hash:chain-hash"))
	require.Equal(t, "book_file:"+newHolder.ID+":"+x.ID, indexOwner(t, fx.ps, "book_file_id:"+x.ID))

	v, err := fx.ps.GetBookVersion(fx.version.ID)
	require.NoError(t, err)
	require.Equal(t, newHolder.ID, v.BookID)
	oldList, err := fx.ps.GetBookVersionsByBookID(fx.holder.ID)
	require.NoError(t, err)
	require.Empty(t, oldList)
	newList, err := fx.ps.GetBookVersionsByBookID(newHolder.ID)
	require.NoError(t, err)
	require.Len(t, newList, 1)

	// Its own inverse.
	require.NoError(t, fx.ps.RehomeRetiredVersion(fx.version.ID, fx.holder.ID))
	back, err := fx.ps.GetRetiredBookFiles(fx.holder.ID)
	require.NoError(t, err)
	require.Len(t, back, 1)
	require.Equal(t, fx.holder.ID+":"+x.ID, indexOwner(t, fx.ps, "book_file_hash:chain-hash"))

	require.Error(t, fx.ps.RehomeRetiredVersion(fx.version.ID, fx.holder.ID), "already held there")
	alt, err := fx.ps.CreateBookVersion(&BookVersion{BookID: fx.holder.ID, Status: BookVersionStatusAlt, Format: "mp3", Source: "manual"})
	require.NoError(t, err)
	require.ErrorIs(t, fx.ps.RehomeRetiredVersion(alt.ID, newHolder.ID), ErrRetiredVersionMismatch)
}

func TestRetiredVersion_GuardsAndIndex(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/g/01.mp3"})

	from, err := fx.ps.GetRetiredVersionsFromBook(fx.shell.ID)
	require.NoError(t, err)
	require.Len(t, from, 1)
	require.Equal(t, fx.version.ID, from[0].ID)
	none, err := fx.ps.GetRetiredVersionsFromBook(fx.holder.ID)
	require.NoError(t, err)
	require.Empty(t, none)

	// The retired status is not a status a version is moved into.
	other, err := fx.ps.CreateBookVersion(&BookVersion{BookID: fx.holder.ID, Status: BookVersionStatusAlt, Format: "mp3", Source: "manual"})
	require.NoError(t, err)
	other.Status = BookVersionStatusRetired
	require.ErrorIs(t, fx.ps.UpdateBookVersion(other), ErrRetiredVersionMismatch)

	require.NoError(t, fx.retire(t, x))

	// While it holds rows, a retired version cannot be deleted, re-statused or moved.
	require.ErrorIs(t, fx.ps.DeleteBookVersion(fx.version.ID), ErrRetiredVersionHasFiles)
	v, err := fx.ps.GetBookVersion(fx.version.ID)
	require.NoError(t, err)
	v.Status = BookVersionStatusTrash
	require.ErrorIs(t, fx.ps.UpdateBookVersion(v), ErrRetiredVersionHasFiles)
	v.Status = BookVersionStatusRetired
	v.BookID = fx.shell.ID
	require.ErrorIs(t, fx.ps.UpdateBookVersion(v), ErrRetiredVersionHasFiles)
	// An ordinary update that keeps status and book is fine.
	v.BookID = fx.holder.ID
	v.MetadataJSON = `{"note":"kept"}`
	require.NoError(t, fx.ps.UpdateBookVersion(v))

	// Once its rows are back, the version can go, and takes its index entry.
	require.NoError(t, fx.ps.UnretireBookFiles(
		[]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))
	require.NoError(t, fx.ps.DeleteBookVersion(fx.version.ID))
	from, err = fx.ps.GetRetiredVersionsFromBook(fx.shell.ID)
	require.NoError(t, err)
	require.Empty(t, from)
}

// A retired version's provenance is fixed at creation. UpdateBookVersion keeps
// the idx:bv:from: key in step with RetiredFromBookID, so a change here would
// delete the only key HasRetiredVersions probes while the version still exists.
func TestRetiredVersion_UpdateRefusesProvenanceChange(t *testing.T) {
	for _, holdsRows := range []bool{true, false} {
		t.Run(map[bool]string{true: "holds rows", false: "no rows"}[holdsRows], func(t *testing.T) {
			fx := newRetiredFixture(t)
			if holdsRows {
				x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/prov/01.mp3"})
				require.NoError(t, fx.retire(t, x))
			}
			fromKey := string(retiredVersionFromKey(fx.shell.ID, fx.version.ID))
			requireFromKey := func(t *testing.T) {
				t.Helper()
				require.Equal(t, fx.version.ID, indexOwner(t, fx.ps, fromKey), "the from-key must survive a refused update")
				has, err := fx.ps.HasRetiredVersions()
				require.NoError(t, err)
				require.True(t, has)
			}

			clear := *fx.version
			clear.RetiredFromBookID = ""
			require.ErrorIs(t, fx.ps.UpdateBookVersion(&clear), ErrRetiredVersionMismatch)
			requireFromKey(t)

			moved := *fx.version
			moved.RetiredFromBookID = fx.holder.ID
			require.ErrorIs(t, fx.ps.UpdateBookVersion(&moved), ErrRetiredVersionMismatch)
			requireFromKey(t)

			restamped := *fx.version
			restamped.RetiredAt = new(fx.version.RetiredAt.Add(time.Hour))
			require.ErrorIs(t, fx.ps.UpdateBookVersion(&restamped), ErrRetiredVersionMismatch)
			cleared := *fx.version
			cleared.RetiredAt = nil
			require.ErrorIs(t, fx.ps.UpdateBookVersion(&cleared), ErrRetiredVersionMismatch)
			stored, err := fx.ps.GetBookVersion(fx.version.ID)
			require.NoError(t, err)
			require.True(t, fx.version.RetiredAt.Equal(*stored.RetiredAt), "a refused update must not change retired_at")

			// An update that leaves provenance alone still goes through.
			ok := *stored
			ok.MetadataJSON = `{"note":"kept"}`
			require.NoError(t, fx.ps.UpdateBookVersion(&ok))
			requireFromKey(t)
		})
	}
}

// RetireBookFiles refuses a version with no retired_from_book_id: it has no
// idx:bv:from: key, so rows filed under it would be invisible to
// HasRetiredVersions. CreateBookVersion cannot make such a version, and
// UpdateBookVersion no longer can either, so the row is written raw here, as a
// legacy or damaged one would be.
func TestRetireBookFiles_RefusesVersionWithoutProvenance(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/noprov/01.mp3"})

	stored, err := fx.ps.GetBookVersion(fx.version.ID)
	require.NoError(t, err)
	stored.RetiredFromBookID = ""
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, fx.ps.db.Set([]byte("bv:"+stored.ID), data, pebble.Sync))
	require.NoError(t, fx.ps.db.Delete(retiredVersionFromKey(fx.shell.ID, stored.ID), pebble.Sync))

	require.ErrorIs(t, fx.retire(t, x), ErrRetiredVersionMismatch)
	got, err := fx.ps.GetBookFileByID(fx.shell.ID, x.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "the row must still be under the shell")
	require.False(t, got.Retired, "a refused retire must leave the row live")
	has, err := fx.ps.HasRetiredVersions()
	require.NoError(t, err)
	require.False(t, has, "nothing was retired, so the probe's answer is still true")
}

func TestGenericPrimitivesRefuseRetiredRows(t *testing.T) {
	fx := newRetiredFixture(t)
	live := fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/p/live.mp3"})
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/p/x.mp3", FileHash: "prim-hash"})
	require.NoError(t, fx.retire(t, x))
	rows := countAllRows(t, fx.ps)

	third, err := fx.ps.CreateBook(&Book{Title: "Third", FilePath: "/lib/ret/third"})
	require.NoError(t, err)
	require.ErrorIs(t, fx.ps.MoveBookFilesToBook([]string{x.ID}, fx.holder.ID, third.ID), ErrBookFileRetired)
	require.ErrorIs(t, fx.ps.DeleteBookFile(x.ID), ErrBookFileRetired)
	require.ErrorIs(t, fx.ps.DeleteBookFilesByIDs([]string{live.ID, x.ID}), ErrBookFileRetired)
	require.ErrorIs(t, fx.ps.DeleteBookFilesForBook(fx.holder.ID), ErrBookFileRetired)
	require.ErrorIs(t, fx.ps.DeleteBook(fx.holder.ID), ErrBookOwnsFiles)

	require.Equal(t, rows, countAllRows(t, fx.ps), "every refusal left every row in place")
	stillLive, err := fx.ps.GetBookFiles(fx.holder.ID)
	require.NoError(t, err)
	require.Len(t, stillLive, 1, "the batch delete refused before deleting the live row riding with the retired one")
}

// An in-place rewrite of a retired row must not take a key from the live row,
// and a live row taking a PID from a retired owner leaves that row alone.
func TestRetiredRowRewriteNeverStealsALiveRowsKey(t *testing.T) {
	fx := newRetiredFixture(t)
	const path = "/lib/ret/steal/01.mp3"
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: path, FileHash: "steal-hash"})
	live := fx.create(t, fx.holder.ID, BookFile{FilePath: path, FileHash: "steal-hash"})
	require.NoError(t, fx.retire(t, x))
	require.Equal(t, fx.holder.ID+":"+live.ID, indexOwner(t, fx.ps, "book_file_hash:steal-hash"))

	retired, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
	require.NoError(t, err)
	retired.Title = "touched"
	require.NoError(t, fx.ps.UpdateBookFile(x.ID, retired))
	require.Equal(t, fx.holder.ID+":"+live.ID, indexOwner(t, fx.ps, "book_file_hash:steal-hash"), "UpdateBookFile on a retired row")
	require.Equal(t, fx.holder.ID+":"+live.ID, indexOwner(t, fx.ps, "book_file_path:"+bookFilePathCRC(path)))
	got, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
	require.NoError(t, err)
	require.True(t, got.Retired)
	require.Equal(t, "touched", got.Title)

	// PID: a retired row owns the key alone; a live row created with the same
	// PID takes the key and the retired row keeps its PID field.
	y := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/steal/02.mp3", ITunesPersistentID: "PID-STEAL"})
	require.NoError(t, fx.retire(t, y))
	newLive := fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/steal/03.mp3", ITunesPersistentID: "PID-STEAL"})
	require.Equal(t, fx.holder.ID+":"+newLive.ID, indexOwner(t, fx.ps, "book_file_pid:PID-STEAL"))
	gotY, err := fx.ps.GetBookFileByID(fx.holder.ID, y.ID)
	require.NoError(t, err)
	require.Equal(t, "PID-STEAL", gotY.ITunesPersistentID, "the retired row's own data is untouched")
	require.True(t, gotY.Retired)
}

// readSnapshot is every default read that must not see a retired row.
type readSnapshot struct {
	CountFiles        int
	TotalFiles        int
	TotalSize         int64
	TotalDuration     int64
	CoreIDs           []string
	VisitIDs          []string
	ForIDsCore        []string
	DupGroups         int
	FPFailures        int64
	SeriesFileCounts  map[int]int
	AuthorFileCounts  map[int]int
	AtPathLive        string
	HashIndexOwnerRow string
}

func takeReadSnapshot(t *testing.T, ps *PebbleStore, useMem bool, holderID string) readSnapshot {
	t.Helper()
	ps.UseMemDB = useMem
	var snap readSnapshot
	var err error

	snap.CountFiles, err = ps.CountFiles()
	require.NoError(t, err)
	stats, err := ps.computeLibraryStats()
	require.NoError(t, err)
	snap.TotalFiles, snap.TotalSize, snap.TotalDuration = stats.TotalFiles, stats.TotalSize, stats.TotalDuration

	core, err := ps.GetAllBookFilesCore()
	require.NoError(t, err)
	for _, c := range core {
		snap.CoreIDs = append(snap.CoreIDs, c.ID)
	}
	require.NoError(t, ps.VisitBookFiles(func(f *BookFile) { snap.VisitIDs = append(snap.VisitIDs, f.ID) }))
	byBook, err := ps.GetBookFilesForIDsCore([]string{holderID})
	require.NoError(t, err)
	for _, c := range byBook[holderID] {
		snap.ForIDsCore = append(snap.ForIDsCore, c.ID)
	}
	groups, err := ps.GetDuplicateFilesByHash(100)
	require.NoError(t, err)
	snap.DupGroups = len(groups)
	_, snap.FPFailures, err = ps.GetFilesWithFingerprintFailures("", 100, 0)
	require.NoError(t, err)
	snap.SeriesFileCounts, err = ps.GetAllSeriesFileCounts()
	require.NoError(t, err)
	snap.AuthorFileCounts, err = ps.GetAllAuthorFileCounts()
	require.NoError(t, err)
	if hit, err := ps.GetBookFileByPath("/lib/ret/par/shared.mp3"); err == nil && hit != nil {
		snap.AtPathLive = hit.ID
	}
	for _, s := range [][]string{snap.CoreIDs, snap.VisitIDs, snap.ForIDsCore} {
		sort.Strings(s)
	}
	return snap
}

// A retired row is invisible to every default read, in both read paths. The
// comparison is against a library where the retired file never existed, so
// the test fails for any read that still counts, lists, hashes or matches it.
func TestRetiredRowsAreInvisibleToDefaultReads(t *testing.T) {
	liveStamp, retiredStamp, ownStamp := int64(100), int64(200), int64(300)
	build := func(t *testing.T, withRetired bool) (*retiredFixture, string) {
		fx := newRetiredFixture(t)
		author, err := fx.ps.CreateAuthor("Parity Author")
		require.NoError(t, err)
		series, err := fx.ps.CreateSeries("Parity Series", &author.ID)
		require.NoError(t, err)
		fx.holder.AuthorID, fx.holder.SeriesID = &author.ID, &series.ID
		_, err = fx.ps.UpdateBook(fx.holder.ID, fx.holder)
		require.NoError(t, err)
		live := fx.create(t, fx.holder.ID, BookFile{
			FilePath: "/lib/ret/par/shared.mp3", FileHash: "par-hash", Duration: 100, FileSize: 1000,
			LastScanMtime: &liveStamp,
		})
		if withRetired {
			failedAt := time.Now()
			x := fx.create(t, fx.shell.ID, BookFile{
				FilePath: "/lib/ret/par/shared.mp3", FileHash: "par-hash", Duration: 555, FileSize: 5555,
				LastScanMtime:       &retiredStamp,
				FingerprintFailedAt: &failedAt,
			})
			// A path of its own so the retired row is not hidden just by sharing a key.
			x2 := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/par/own.mp3", FileHash: "par-hash", Duration: 7, FileSize: 77, LastScanMtime: &ownStamp})
			require.NoError(t, fx.retire(t, x, x2))
		}
		// The shells the apply targets are trashed (soft-deleted) books, which
		// the stats and counts already exclude; a live shell would keep its
		// stale book-level aggregates and say nothing about the file rows.
		gone := true
		fx.shell.MarkedForDeletion = &gone
		_, err = fx.ps.UpdateBook(fx.shell.ID, fx.shell)
		require.NoError(t, err)
		return fx, live.ID
	}

	for _, useMem := range []bool{true, false} {
		name := map[bool]string{true: "memdb", false: "pebble"}[useMem]
		t.Run(name, func(t *testing.T) {
			withFx, liveID := build(t, true)
			withoutFx, _ := build(t, false)

			with := takeReadSnapshot(t, withFx.ps, useMem, withFx.holder.ID)
			without := takeReadSnapshot(t, withoutFx.ps, useMem, withoutFx.holder.ID)

			// IDs differ between the two stores; compare shape, and name the live row.
			require.Equal(t, 1, with.CountFiles)
			require.Equal(t, without.CountFiles, with.CountFiles)
			require.Equal(t, without.TotalFiles, with.TotalFiles)
			require.Equal(t, without.TotalSize, with.TotalSize)
			require.Equal(t, without.TotalDuration, with.TotalDuration)
			require.Equal(t, []string{liveID}, with.CoreIDs)
			require.Equal(t, []string{liveID}, with.VisitIDs)
			require.Equal(t, []string{liveID}, with.ForIDsCore)
			require.Equal(t, without.DupGroups, with.DupGroups, "a hash shared with a retired row is not a duplicate")
			require.Zero(t, with.FPFailures)
			require.Equal(t, without.SeriesFileCounts, with.SeriesFileCounts)
			require.Equal(t, without.AuthorFileCounts, with.AuthorFileCounts)
			require.Equal(t, liveID, with.AtPathLive)

			// The include-retired twins still see everything.
			all, err := withFx.ps.GetAllBookFilesCoreIncludingRetired()
			require.NoError(t, err)
			require.Len(t, all, 3)
			withFx.ps.UseMemDB = useMem
			// Scan cache: a retired row never displaces the live row at its path.
			cache, err := withFx.ps.GetScanCacheMapContext(context.Background())
			require.NoError(t, err)
			require.Equal(t, liveStamp, cache["/lib/ret/par/shared.mp3"].Mtime,
				"the live row's stamp decides the scanner's skip, whichever row sorts first")
			require.Equal(t, ownStamp, cache["/lib/ret/par/own.mp3"].Mtime,
				"a retired-only path keeps its own stamp so an unchanged retired file is skipped")
		})
	}
}

// Retired survives every rewrite that does not name it: the scanner and the
// importers rebuild rows from a fresh struct that has Retired=false.
func TestRetiredFlagSurvivesGenericRewrites(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/rw/01.mp3", FileHash: "rw-hash", Duration: 5})
	require.NoError(t, fx.retire(t, x))
	assertStillRetired := func(label string) {
		t.Helper()
		got, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
		require.NoError(t, err, label)
		require.NotNil(t, got, label)
		require.True(t, got.Retired, label)
		require.Equal(t, fx.version.ID, got.VersionID, label)
		require.Equal(t, fx.holder.ID, got.BookID, label)
	}
	fresh := func() *BookFile {
		return &BookFile{ID: x.ID, BookID: fx.holder.ID, FilePath: "/lib/ret/rw/01.mp3", FileHash: "rw-hash", Duration: 9, Retired: false, VersionID: fx.version.ID}
	}
	require.NoError(t, fx.ps.UpdateBookFile(x.ID, fresh()))
	assertStillRetired("UpdateBookFile")
	require.NoError(t, fx.ps.UpsertBookFile(fresh()))
	assertStillRetired("UpsertBookFile")
	require.NoError(t, fx.ps.BatchUpsertBookFilesKeepPaths([]*BookFile{fresh()}))
	assertStillRetired("BatchUpsertBookFilesKeepPaths")
	require.NoError(t, fx.ps.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: fresh(), Present: true}}))
	assertStillRetired("BatchUpsertScannedBookFiles")
}

// Another holder's retired row carrying the key is not a live alternative: it
// must not be handed the key, the row being retired keeps it.
func TestRetireBookFiles_AnotherRetiredRowIsNotALiveAlternative(t *testing.T) {
	fx := newRetiredFixture(t)
	const path = "/lib/ret/two/01.mp3"
	x1 := fx.create(t, fx.shell.ID, BookFile{FilePath: path, FileHash: "two-hash"})
	require.NoError(t, fx.retire(t, x1))

	shell2, err := fx.ps.CreateBook(&Book{Title: "Retire Shell Two", FilePath: "/lib/ret/shell2"})
	require.NoError(t, err)
	holder2, err := fx.ps.CreateBook(&Book{Title: "Retire Holder Two", FilePath: "/lib/ret/holder2"})
	require.NoError(t, err)
	v2, err := fx.ps.CreateBookVersion(&BookVersion{
		BookID: holder2.ID, Status: BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell2.ID, RetireOpID: "op-2", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	x2 := fx.create(t, shell2.ID, BookFile{FilePath: path, FileHash: "two-hash"}) // writes last: owns the keys
	require.NoError(t, fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{x2.ID}, SourceBookID: shell2.ID}}, holder2.ID, v2.ID))

	require.Equal(t, holder2.ID+":"+x2.ID, indexOwner(t, fx.ps, "book_file_hash:two-hash"))
	require.Equal(t, holder2.ID+":"+x2.ID, indexOwner(t, fx.ps, "book_file_path:"+bookFilePathCRC(path)))
}

// A path-only key (no hash on the row) refuses on its own when ownership
// cannot be proven, so the path check is not shadowed by the hash check.
func TestRetireBookFiles_PathOnlyKeyRefusesWithoutMemdb(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/pathonly/01.mp3"})
	fx.ps.UseMemDB = false
	require.ErrorIs(t, fx.retire(t, x), ErrRetireIndexIncomplete)
	got, err := fx.ps.GetBookFileByID(fx.shell.ID, x.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
}

// Retired is bfStoredOwned: no caller-built row flips it, in either direction,
// and none is born with it.
func TestBookFileMerge_StoredOwnedFieldsAreImmutable(t *testing.T) {
	fx := newRetiredFixture(t)
	live := fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/imm/live.mp3", FileHash: "imm-live"})
	assertLive := func(label string) {
		t.Helper()
		got, err := fx.ps.GetBookFileByID(fx.holder.ID, live.ID)
		require.NoError(t, err, label)
		require.NotNil(t, got, label)
		require.False(t, got.Retired, label+": a generic write must not retire a live row")
	}
	attempt := func() *BookFile {
		return &BookFile{ID: live.ID, BookID: fx.holder.ID, FilePath: "/lib/ret/imm/live.mp3", FileHash: "imm-live", Retired: true}
	}
	require.NoError(t, fx.ps.UpdateBookFile(live.ID, attempt()))
	assertLive("UpdateBookFile")
	require.NoError(t, fx.ps.UpsertBookFile(attempt()))
	assertLive("UpsertBookFile")
	require.NoError(t, fx.ps.BatchUpsertBookFilesKeepPaths([]*BookFile{attempt()}))
	assertLive("BatchUpsertBookFilesKeepPaths")
	require.NoError(t, fx.ps.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: attempt(), Present: true}}))
	assertLive("BatchUpsertScannedBookFiles")

	rows := countAllRows(t, fx.ps)
	born := func() *BookFile {
		return &BookFile{BookID: fx.holder.ID, FilePath: "/lib/ret/imm/born.mp3", Retired: true}
	}
	require.ErrorIs(t, fx.ps.CreateBookFile(born()), ErrBookFileRetired)
	require.ErrorIs(t, fx.ps.BatchCreateBookFiles([]*BookFile{born()}), ErrBookFileRetired)
	require.ErrorIs(t, fx.ps.UpsertBookFile(born()), ErrBookFileRetired)
	require.ErrorIs(t, fx.ps.BatchUpsertBookFilesKeepPaths([]*BookFile{born()}), ErrBookFileRetired)
	require.Equal(t, rows, countAllRows(t, fx.ps), "a refused insert writes nothing")
}

// An upsert whose path or PID resolves to a retired row creates a live row: it
// does not rewrite the retired row, and the live row owns the key afterwards.
func TestUpsertIgnoresRetiredRowsAtItsPathAndPID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		upsert func(ps *PebbleStore, f *BookFile) error
	}{
		{"UpsertBookFile", func(ps *PebbleStore, f *BookFile) error { return ps.UpsertBookFile(f) }},
		{"BatchUpsertBookFilesKeepPaths", func(ps *PebbleStore, f *BookFile) error { return ps.BatchUpsertBookFilesKeepPaths([]*BookFile{f}) }},
		{"BatchUpsertScannedBookFiles", func(ps *PebbleStore, f *BookFile) error {
			return ps.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: f, Present: true}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newRetiredFixture(t)
			const path = "/lib/ret/ign/01.mp3"
			x := fx.create(t, fx.shell.ID, BookFile{FilePath: path, FileHash: "ign-hash", ITunesPersistentID: "PID-IGN", Duration: 42})
			require.NoError(t, fx.retire(t, x))
			rows := countAllRows(t, fx.ps)

			byPath := &BookFile{BookID: fx.holder.ID, FilePath: path, FileSize: 9}
			require.NoError(t, tc.upsert(fx.ps, byPath))
			require.NotEqual(t, x.ID, byPath.ID, "the retired row must not be the match")
			require.Equal(t, rows+1, countAllRows(t, fx.ps))
			hit, err := fx.ps.GetBookFileByPath(path)
			require.NoError(t, err)
			require.NotNil(t, hit)
			require.Equal(t, byPath.ID, hit.ID, "the live row owns the path key now")
			require.False(t, hit.Retired)

			byPID := &BookFile{BookID: fx.holder.ID, FilePath: "/lib/ret/ign/02.mp3", ITunesPersistentID: "PID-IGN"}
			require.NoError(t, tc.upsert(fx.ps, byPID))
			require.NotEqual(t, x.ID, byPID.ID)
			pidHit, err := fx.ps.GetBookFileByPID("PID-IGN")
			require.NoError(t, err)
			require.NotNil(t, pidHit)
			require.Equal(t, byPID.ID, pidHit.ID)

			old, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
			require.NoError(t, err)
			require.NotNil(t, old)
			require.True(t, old.Retired)
			require.Equal(t, 42, old.Duration, "the retired row's content is untouched")
			require.Equal(t, "PID-IGN", old.ITunesPersistentID)
		})
	}
}
