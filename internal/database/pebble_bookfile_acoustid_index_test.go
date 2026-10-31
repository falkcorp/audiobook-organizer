// file: internal/database/pebble_bookfile_acoustid_index_test.go
// version: 1.0.0
// guid: 5d8c2f61-3a97-4b04-8e15-c0f7a9d2b346
// last-edited: 2026-10-30

package database

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for TODO-BF-ACOUSTID-SEG-KEYS-NOT-PERSISTED: the book_file_acoustid:
// index must hold exactly the segments the stored row holds, and the repair
// removes the keys the old writers left behind.

const (
	segRiddler = "AQADtAcSRYriddler"
	segJoker   = "AQADtAcSRZjoker"
	segPenguin = "AQADtAcSRApenguin"
)

// acoustIDKeys lists every book_file_acoustid: key and its value.
func acoustIDKeys(t *testing.T, ps *PebbleStore) map[string]string {
	t.Helper()
	pairs, err := ps.ScanPrefix("book_file_acoustid:")
	require.NoError(t, err)
	out := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		out[kv.Key] = string(kv.Value)
	}
	return out
}

// plantLegacyAcoustIDRow writes a pre-T020 row the way an old binary did: the
// segments in the stored bytes AND one index key per segment.
func plantLegacyAcoustIDRow(t *testing.T, ps *PebbleStore, f *BookFile) {
	t.Helper()
	writeLegacyBookFileRow(t, ps, f)
	ref := []byte(f.BookID + ":" + f.ID)
	for _, seg := range []string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
		f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
		if seg != "" {
			require.NoError(t, ps.db.Set([]byte("book_file_acoustid:"+seg), ref, nil))
		}
	}
}

func acoustIDTestBook(t *testing.T, ps *PebbleStore, title string) *Book {
	t.Helper()
	b, err := ps.CreateBook(&Book{Title: title, FilePath: "/lib/" + title})
	require.NoError(t, err)
	return b
}

// A create, an update and an upsert that carry AcoustIDSeg values the row does
// not store must not leave book_file_acoustid: keys behind. Before the fix each
// wrote one per segment, and nothing could ever delete them.
func TestBookFileWrites_DroppedSegsLeaveNoAcoustIDKeys(t *testing.T) {
	ps := setupTestPebbleStore(t)
	book := acoustIDTestBook(t, ps, "Gotham Year One")

	created := &BookFile{BookID: book.ID, FilePath: "/lib/gotham/01.mp3", AcoustIDSeg0: segRiddler, AcoustIDSeg1: segJoker}
	require.NoError(t, ps.CreateBookFile(created))
	assert.Empty(t, acoustIDKeys(t, ps), "CreateBookFile indexed segments its row does not store")

	updated := *created
	updated.AcoustIDSeg0 = segPenguin
	updated.AcoustIDSeg6 = segJoker
	require.NoError(t, ps.UpdateBookFile(created.ID, &updated))
	assert.Empty(t, acoustIDKeys(t, ps), "UpdateBookFile indexed segments its row does not store")

	upserted := &BookFile{BookID: book.ID, FilePath: "/lib/gotham/02.mp3", AcoustIDSeg0: segRiddler}
	require.NoError(t, ps.UpsertBookFile(upserted))
	assert.Empty(t, acoustIDKeys(t, ps), "UpsertBookFile indexed segments its row does not store")

	got, err := ps.GetBookFileByAcoustID(segPenguin)
	require.NoError(t, err)
	assert.Nil(t, got, "no key, so the exact lookup names nobody")
}

// A RETIRED row's update writes its single-owner keys through its own branch
// (writeBookFileSecondaryIndexesFor); it must not index dropped segments either.
func TestUpdateRetiredBookFile_DroppedSegsLeaveNoAcoustIDKeys(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/ac/robin.mp3"})
	require.NoError(t, fx.retire(t, x))

	row, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.True(t, row.Retired, "premise: the row is retired")
	row.AcoustIDSeg0 = segRiddler
	row.AcoustIDSeg3 = segJoker
	require.NoError(t, fx.ps.UpdateBookFile(row.ID, row))

	assert.Empty(t, acoustIDKeys(t, fx.ps), "a retired row's update indexed segments it does not store")
}

// The shape that hurt: a LEGACY row (segments stored, keys present) is read,
// edited and written back by an unrelated caller. The update drops the
// segments from the row, so its keys must go too, not be re-created from the
// struct the caller read.
func TestUpdateBookFile_LegacyRowLosesItsAcoustIDKeysWithItsSegs(t *testing.T) {
	ps := setupTestPebbleStore(t)
	book := acoustIDTestBook(t, ps, "Arkham Asylum")
	legacy := &BookFile{ID: "01LEGACYUPDATE0000000000A", BookID: book.ID, FilePath: "/lib/arkham/01.mp3",
		AcoustIDSeg0: segRiddler, AcoustIDSeg2: segJoker}
	plantLegacyAcoustIDRow(t, ps, legacy)
	require.Len(t, acoustIDKeys(t, ps), 2, "premise: two legacy keys")

	read, err := ps.GetBookFileByID(book.ID, legacy.ID)
	require.NoError(t, err)
	require.NotNil(t, read)
	require.Equal(t, segRiddler, read.AcoustIDSeg0, "premise: a Pebble read still carries the legacy segment")
	read.TrackNumber = 7
	require.NoError(t, ps.UpdateBookFile(read.ID, read))

	assert.Empty(t, acoustIDKeys(t, ps), "the row dropped its segments; their keys must be gone, not rewritten")
	raw, err := ps.GetRaw(string(bookFileKey(book.ID, legacy.ID)))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "acoustid_seg")
}

// A move rewrites the row under another book. It used to marshal the whole
// struct (legacy segments kept) and re-index them; the row is now written in
// its stored form like every other write, and its keys go with the segments.
func TestMoveBookFilesToBook_LegacyRowIsWrittenInStoredForm(t *testing.T) {
	ps := setupTestPebbleStore(t)
	src := acoustIDTestBook(t, ps, "Batcave")
	dst := acoustIDTestBook(t, ps, "Wayne Manor")
	legacy := &BookFile{ID: "01LEGACYMOVE00000000000A1", BookID: src.ID, FilePath: "/lib/batcave/01.mp3", AcoustIDSeg0: segPenguin}
	plantLegacyAcoustIDRow(t, ps, legacy)
	require.Len(t, acoustIDKeys(t, ps), 1)

	require.NoError(t, ps.MoveBookFilesToBook([]string{legacy.ID}, src.ID, dst.ID))

	raw, err := ps.GetRaw(string(bookFileKey(dst.ID, legacy.ID)))
	require.NoError(t, err)
	require.NotNil(t, raw, "the row moved")
	assert.NotContains(t, string(raw), "acoustid_seg", "a moved row is written in its stored form")
	assert.Empty(t, acoustIDKeys(t, ps), "the moved row holds no segment, so no key may name it")
}

// repairFixture: one orphan per shape the old writers produced, one live legacy
// key the repair must keep, and one malformed key it must report and keep.
//
//	segRiddler: the row exists and has no segment (the common orphan).
//	"gone":      the row was deleted.
//	segJoker:   a legacy row that still stores its segment (live).
//	"broken":    a value that is not a row reference.
type repairFixture struct {
	ps          *PebbleStore
	live, bare  *BookFile
	goneSegment string
}

func newRepairFixture(t *testing.T) *repairFixture {
	t.Helper()
	ps := setupTestPebbleStore(t)
	book := acoustIDTestBook(t, ps, "Court of Owls")
	fx := &repairFixture{ps: ps, goneSegment: "AQADgone"}

	fx.bare = &BookFile{BookID: book.ID, FilePath: "/lib/owls/bare.mp3"}
	require.NoError(t, ps.CreateBookFile(fx.bare))
	require.NoError(t, ps.db.Set([]byte("book_file_acoustid:"+segRiddler), []byte(book.ID+":"+fx.bare.ID), nil))

	require.NoError(t, ps.db.Set([]byte("book_file_acoustid:"+fx.goneSegment), []byte(book.ID+":01GONE000000000000000000A"), nil))

	fx.live = &BookFile{ID: "01LIVELEGACY000000000000A", BookID: book.ID, FilePath: "/lib/owls/legacy.mp3", AcoustIDSeg0: segJoker}
	plantLegacyAcoustIDRow(t, ps, fx.live)

	require.NoError(t, ps.db.Set([]byte("book_file_acoustid:broken"), []byte("not-a-ref"), nil))
	return fx
}

func scanAllAcoustIDEntries(t *testing.T, ps *PebbleStore, page int) []BookFileAcoustIDEntry {
	t.Helper()
	var all []BookFileAcoustIDEntry
	after := ""
	for {
		got, next, err := ps.ScanBookFileAcoustIDIndex(after, page)
		require.NoError(t, err)
		all = append(all, got...)
		if next == "" {
			return all
		}
		after = "book_file_acoustid:" + got[len(got)-1].Seg
		require.Equal(t, after, next, "the cursor is the last key of the page")
	}
}

func TestJudgeBookFileAcoustIDEntry_ClassifiesEveryShape(t *testing.T) {
	fx := newRepairFixture(t)
	want := map[string]BookFileAcoustIDVerdict{
		segRiddler:     AcoustIDEntryRowLacksSeg,
		fx.goneSegment: AcoustIDEntryRowGone,
		segJoker:       AcoustIDEntryLive,
		"broken":       AcoustIDEntryMalformed,
	}
	// Two entries per page forces the cursor across every boundary.
	entries := scanAllAcoustIDEntries(t, fx.ps, 2)
	require.Len(t, entries, len(want))
	for _, e := range entries {
		v, err := fx.ps.JudgeBookFileAcoustIDEntry(e)
		require.NoError(t, err)
		assert.Equal(t, want[e.Seg], v, "segment %s", e.Seg)
	}
}

// A key whose ref names a book the row has since left is judged by the row's
// ID, not declared gone.
func TestJudgeBookFileAcoustIDEntry_FollowsAMovedRowByID(t *testing.T) {
	fx := newRepairFixture(t)
	other := acoustIDTestBook(t, fx.ps, "Blackgate")
	// The legacy row is re-keyed under another book by a writer that left the
	// index key naming the old owner.
	raw, err := fx.ps.GetRaw(string(bookFileKey(fx.live.BookID, fx.live.ID)))
	require.NoError(t, err)
	require.NoError(t, fx.ps.DeleteRaw(string(bookFileKey(fx.live.BookID, fx.live.ID))))
	require.NoError(t, fx.ps.SetRaw(string(bookFileKey(other.ID, fx.live.ID)), raw))
	require.NoError(t, fx.ps.db.Set([]byte("book_file_id:"+fx.live.ID), bookFileKey(other.ID, fx.live.ID), nil))

	v, err := fx.ps.JudgeBookFileAcoustIDEntry(BookFileAcoustIDEntry{Seg: segJoker, Ref: fx.live.BookID + ":" + fx.live.ID})
	require.NoError(t, err)
	assert.Equal(t, AcoustIDEntryLive, v, "the row still holds the segment under its new book")
}

func TestDeleteOrphanBookFileAcoustIDEntries_DeletesOrphansKeepsLiveAndMalformed(t *testing.T) {
	fx := newRepairFixture(t)
	entries := scanAllAcoustIDEntries(t, fx.ps, 100)
	require.Len(t, entries, 4)

	deleted, err := fx.ps.DeleteOrphanBookFileAcoustIDEntries(context.Background(), entries)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted, "the row-lacks-segment key and the row-gone key")

	left := acoustIDKeys(t, fx.ps)
	assert.Contains(t, left, "book_file_acoustid:"+segJoker, "a key whose row still holds its segment is live")
	assert.Contains(t, left, "book_file_acoustid:broken", "a malformed ref is reported, never deleted")
	assert.NotContains(t, left, "book_file_acoustid:"+segRiddler)
	assert.NotContains(t, left, "book_file_acoustid:"+fx.goneSegment)

	// Idempotent: nothing left to delete.
	deleted, err = fx.ps.DeleteOrphanBookFileAcoustIDEntries(context.Background(), entries)
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

// The delete judges again under the single-owner index mutex: a key the scan
// saw as an orphan, then repointed (as a retire or an un-retire does) to a row
// that holds the segment, must survive. This is the stale-index race the
// retired-version writers close with indexCAS.
func TestDeleteOrphanBookFileAcoustIDEntries_JudgesAgainUnderTheLock(t *testing.T) {
	fx := newRepairFixture(t)
	stale := BookFileAcoustIDEntry{Seg: segRiddler, Ref: fx.live.BookID + ":" + fx.bare.ID}
	v, err := fx.ps.JudgeBookFileAcoustIDEntry(stale)
	require.NoError(t, err)
	require.Equal(t, AcoustIDEntryRowLacksSeg, v, "premise: the scan saw an orphan")

	// Between the scan and the delete, the key now belongs to a row that holds
	// the segment.
	holder := &BookFile{ID: "01HOLDER0000000000000000A", BookID: fx.live.BookID, FilePath: "/lib/owls/holder.mp3", AcoustIDSeg0: segRiddler}
	plantLegacyAcoustIDRow(t, fx.ps, holder)

	deleted, err := fx.ps.DeleteOrphanBookFileAcoustIDEntries(context.Background(), []BookFileAcoustIDEntry{stale})
	require.NoError(t, err)
	assert.Zero(t, deleted, "the key was repointed to a live row after the scan")
	assert.Equal(t, holder.BookID+":"+holder.ID, acoustIDKeys(t, fx.ps)["book_file_acoustid:"+segRiddler])
}

// SweepBookFileSegDrop still owns the live legacy keys: after the repair has
// run, the sweep rewrites the legacy row and removes its key.
func TestRepairThenSweep_LeavesNoAcoustIDKeys(t *testing.T) {
	fx := newRepairFixture(t)
	_, err := fx.ps.DeleteOrphanBookFileAcoustIDEntries(context.Background(), scanAllAcoustIDEntries(t, fx.ps, 100))
	require.NoError(t, err)
	res, err := fx.ps.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Rewrite)
	left := acoustIDKeys(t, fx.ps)
	assert.Len(t, left, 1, "only the malformed key remains: %v", left)
	assert.Contains(t, left, "book_file_acoustid:broken")
}

// The row bytes the probe sees are JSON; a segment with a character JSON
// escapes must still compare equal to the decoded field.
func TestRowStoresAcoustIDSeg_DecodesRatherThanMatchingBytes(t *testing.T) {
	const seg = "AQAD<tc>&x"
	raw, err := json.Marshal(&BookFile{ID: "x", AcoustIDSeg4: seg})
	require.NoError(t, err)
	has, err := rowStoresAcoustIDSeg(raw, seg)
	require.NoError(t, err)
	assert.True(t, has)
	has, err = rowStoresAcoustIDSeg(raw, seg+"z")
	require.NoError(t, err)
	assert.False(t, has)
	has, err = rowStoresAcoustIDSeg([]byte(`{"id":"x"}`), seg)
	require.NoError(t, err)
	assert.False(t, has)
}
