// file: internal/database/series_object_drop_history_test.go
// version: 1.0.0
// guid: 988e17ab-3613-427f-a33a-da7c6215bc39
// last-edited: 2026-10-04

package database

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The store's series invariant (enforceSeriesInvariant) drops or replaces an
// embedded Series object that does not match SeriesID. When that object was
// the only record of a series, the drop is recorded as one series_object
// history row in the same batch as the book write. These tests pin: one row
// per lost object, the right old value, and no row for an ordinary series
// edit (the writer records that as its own "series" row).

type dropFixture struct {
	t     *testing.T
	store *PebbleStore
	a, b  *Series // two real series
}

func newDropFixture(t *testing.T) *dropFixture {
	t.Helper()
	st, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	a, err := st.CreateSeries("The Expanse", nil)
	require.NoError(t, err)
	b, err := st.CreateSeries("Discworld", nil)
	require.NoError(t, err)
	return &dropFixture{t: t, store: st, a: a, b: b}
}

// book creates a consistent book (linked to s with the matching object, or
// series-less when s is nil).
func (f *dropFixture) book(path string, s *Series) *Book {
	f.t.Helper()
	bk := &Book{Title: "T " + path, FilePath: path, Format: "m4b"}
	if s != nil {
		id, obj := s.ID, *s
		bk.SeriesID, bk.Series = &id, &obj
	}
	out, err := f.store.CreateBook(bk)
	require.NoError(f.t, err)
	return out
}

// legacy seeds the row an older build left: the given link and object.
func (f *dropFixture) legacy(id string, seriesID *int, obj *Series) {
	f.t.Helper()
	_, err := f.store.SeedLegacyBookRowForTest(id, func(row *Book) error {
		row.SeriesID, row.Series = seriesID, obj
		return nil
	})
	require.NoError(f.t, err)
}

func (f *dropFixture) reread(id string) *Book {
	f.t.Helper()
	b, err := f.store.GetBookByID(id)
	require.NoError(f.t, err)
	return b
}

func (f *dropFixture) drops(id string) []MetadataChangeRecord {
	f.t.Helper()
	rows, err := f.store.GetMetadataChangeHistory(id, HistoryFieldSeriesObject, 1<<30)
	require.NoError(f.t, err)
	return rows
}

func (f *dropFixture) retitle(id, title string) {
	f.t.Helper()
	row := f.reread(id)
	row.Title = title
	_, err := f.store.UpdateBook(id, row)
	require.NoError(f.t, err)
}

func decodeName(t *testing.T, raw *string) string {
	t.Helper()
	require.NotNil(t, raw)
	var s string
	require.NoError(t, json.Unmarshal([]byte(*raw), &s))
	return s
}

func requireDrop(t *testing.T, r MetadataChangeRecord, oldID int, oldName string, newID *int, newName string) {
	t.Helper()
	require.Equal(t, HistoryFieldSeriesObject, r.Field)
	require.Equal(t, ChangeTypeSeriesObjectDrop, r.ChangeType)
	require.Equal(t, SeriesObjectDropSource, r.Source)
	require.NotZero(t, r.ID)
	require.False(t, r.ChangedAt.IsZero())
	require.Equal(t, oldName, decodeName(t, r.PreviousValue))
	require.Equal(t, newName, decodeName(t, r.NewValue))
	require.NotNil(t, r.PreviousRef)
	require.NotNil(t, r.PreviousRef.SeriesID)
	require.Equal(t, oldID, *r.PreviousRef.SeriesID)
	require.NotNil(t, r.NewRef)
	if newID == nil {
		require.Nil(t, r.NewRef.SeriesID)
	} else {
		require.NotNil(t, r.NewRef.SeriesID)
		require.Equal(t, *newID, *r.NewRef.SeriesID)
	}
}

// Drop path 1: SeriesID nil with an object (the orphan/stale class). A
// round-trip write that carries the object drops it: one row, then none on
// the next write.
func TestSeriesObjectDrop_OrphanRoundTripRecordsOnce(t *testing.T) {
	f := newDropFixture(t)
	bk := f.book("/lib/orphan.m4b", nil)
	f.legacy(bk.ID, nil, &Series{ID: 9004, Name: "Vanished Series"})

	f.retitle(bk.ID, "retitled")
	require.Nil(t, f.reread(bk.ID).Series, "the invariant still drops the object")
	rows := f.drops(bk.ID)
	require.Len(t, rows, 1)
	requireDrop(t, rows[0], 9004, "Vanished Series", nil, "")

	f.retitle(bk.ID, "retitled again")
	require.Len(t, f.drops(bk.ID), 1, "nothing is dropped twice")
}

// Drop path 1b: a writer that sourced its book from the memdb projection
// passes no object at all. The stored stale object is still lost, and still
// recorded.
func TestSeriesObjectDrop_ProjectionWriteRecordsTheStoredObject(t *testing.T) {
	f := newDropFixture(t)
	bk := f.book("/lib/proj.m4b", nil)
	f.legacy(bk.ID, nil, &Series{ID: f.a.ID, Name: f.a.Name})

	proj := stripBookForMemdb(f.reread(bk.ID))
	require.Nil(t, proj.Series)
	proj.Title = "projection write"
	_, err := f.store.UpdateBook(bk.ID, proj)
	require.NoError(t, err)

	rows := f.drops(bk.ID)
	require.Len(t, rows, 1)
	requireDrop(t, rows[0], f.a.ID, f.a.Name, nil, "")
}

// Drop path 2: SeriesID names one series, the object another (the id-mismatch
// class). The object is dropped (the stored object is not the linked
// series'), recorded with the linked id as the new ref.
func TestSeriesObjectDrop_IDMismatchRecordsTheStaleObject(t *testing.T) {
	f := newDropFixture(t)
	bk := f.book("/lib/mismatch.m4b", f.b)
	bID := f.b.ID
	f.legacy(bk.ID, &bID, &Series{ID: f.a.ID, Name: f.a.Name})

	f.retitle(bk.ID, "retitled")
	require.Nil(t, f.reread(bk.ID).Series)
	rows := f.drops(bk.ID)
	require.Len(t, rows, 1)
	requireDrop(t, rows[0], f.a.ID, f.a.Name, &bID, "")
}

// Replace path: the stored row is consistent, the writer passes an object
// for another series. The stored object replaces it; the writer's object is
// recorded, the kept one is not.
func TestSeriesObjectDrop_ReplaceRecordsTheWritersObject(t *testing.T) {
	f := newDropFixture(t)
	bk := f.book("/lib/replace.m4b", f.b)
	row := f.reread(bk.ID)
	row.Series = &Series{ID: 777, Name: "Wrong Object"}
	_, err := f.store.UpdateBook(bk.ID, row)
	require.NoError(t, err)

	got := f.reread(bk.ID)
	require.NotNil(t, got.Series)
	require.Equal(t, f.b.ID, got.Series.ID, "the linked series' object is kept")
	rows := f.drops(bk.ID)
	require.Len(t, rows, 1)
	bID := f.b.ID
	requireDrop(t, rows[0], 777, "Wrong Object", &bID, f.b.Name)
}

// Two different objects lost by one write get two rows (distinct keys).
func TestSeriesObjectDrop_TwoLostObjectsTwoRows(t *testing.T) {
	f := newDropFixture(t)
	bk := f.book("/lib/two.m4b", f.b)
	bID := f.b.ID
	f.legacy(bk.ID, &bID, &Series{ID: f.a.ID, Name: f.a.Name})
	row := f.reread(bk.ID)
	row.Series = &Series{ID: 778, Name: "Writer Object"}
	_, err := f.store.UpdateBook(bk.ID, row)
	require.NoError(t, err)

	rows := f.drops(bk.ID)
	require.Len(t, rows, 2)
	got := map[int]string{}
	for _, r := range rows {
		got[*r.PreviousRef.SeriesID] = decodeName(t, r.PreviousValue)
	}
	require.Equal(t, map[int]string{778: "Writer Object", f.a.ID: f.a.Name}, got)
}

// Create: a new book written with an object but no SeriesID drops it, and
// the drop is recorded.
func TestSeriesObjectDrop_CreateRecords(t *testing.T) {
	f := newDropFixture(t)
	bk, err := f.store.CreateBook(&Book{Title: "C", FilePath: "/lib/create.m4b", Format: "m4b",
		Series: &Series{ID: f.a.ID, Name: f.a.Name}})
	require.NoError(t, err)
	require.Nil(t, f.reread(bk.ID).Series)
	rows := f.drops(bk.ID)
	require.Len(t, rows, 1)
	requireDrop(t, rows[0], f.a.ID, f.a.Name, nil, "")
}

// No row when nothing is lost: an ordinary write of a consistent book, a
// series clear, a move by id alone and a consistent create. The clear and the
// move drop the old series' own object, which is the writer's series edit
// (recorded by the writer as a "series" row), not a lost record.
func TestSeriesObjectDrop_NoRowForOrdinaryWrites(t *testing.T) {
	f := newDropFixture(t)
	bk := f.book("/lib/plain.m4b", f.a)
	plain := f.book("/lib/none.m4b", nil)

	f.retitle(bk.ID, "retitled")
	f.retitle(plain.ID, "retitled")

	// Projection write keeps the object: nothing lost.
	proj := stripBookForMemdb(f.reread(bk.ID))
	proj.Title = "projection"
	_, err := f.store.UpdateBook(bk.ID, proj)
	require.NoError(t, err)

	// Move by id alone (the old object rides along).
	row := f.reread(bk.ID)
	bID := f.b.ID
	row.SeriesID = &bID
	_, err = f.store.UpdateBook(bk.ID, row)
	require.NoError(t, err)

	// Clear by id alone.
	row = f.reread(bk.ID)
	row.SeriesID = nil
	_, err = f.store.UpdateBook(bk.ID, row)
	require.NoError(t, err)
	require.Nil(t, f.reread(bk.ID).Series)

	require.Empty(t, f.drops(bk.ID))
	require.Empty(t, f.drops(plain.ID))
	hist, err := f.store.GetBookChangeHistory(bk.ID, 1<<30)
	require.NoError(t, err)
	require.Empty(t, hist, "the store records nothing for an ordinary write")
}

// A revert to a snapshot that held the stale object writes that object back
// through the invariant: it is dropped again and the drop recorded again.
// The revert cannot restore a stale object; the relink does (by setting the
// series id the object names).
func TestSeriesObjectDrop_RevertToStaleSnapshotRecordsAgain(t *testing.T) {
	f := newDropFixture(t)
	bk := f.book("/lib/revert.m4b", nil)
	f.legacy(bk.ID, nil, &Series{ID: 9005, Name: "Snapshot Series"})
	f.retitle(bk.ID, "first") // snapshot of the stale row taken here; drop #1
	require.Len(t, f.drops(bk.ID), 1)

	snaps, err := f.store.GetBookSnapshots(bk.ID, 100)
	require.NoError(t, err)
	var staleAt time.Time
	for _, s := range snaps {
		var b Book
		if json.Unmarshal(s.Data, &b) == nil && b.Series != nil && b.Series.ID == 9005 {
			staleAt = s.Timestamp
		}
	}
	require.False(t, staleAt.IsZero(), "a snapshot holds the stale object")
	_, err = f.store.RevertBookToVersion(bk.ID, staleAt)
	require.NoError(t, err)
	require.Nil(t, f.reread(bk.ID).Series)
	require.Len(t, f.drops(bk.ID), 2)
}

// The legacy seed path writes the object as given and records nothing.
func TestSeriesObjectDrop_LegacySeedRecordsNothing(t *testing.T) {
	f := newDropFixture(t)
	bk := f.book("/lib/seed.m4b", nil)
	f.legacy(bk.ID, nil, &Series{ID: 1, Name: "x"})
	require.Empty(t, f.drops(bk.ID))
}
