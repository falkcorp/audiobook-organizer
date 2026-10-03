// file: internal/database/series_preserve_guard_test.go
// version: 1.1.0
// guid: d326551d-56ac-4803-a326-a38f5176bb35
// last-edited: 2026-10-03

package database

import "testing"

// UpdateBook keeps the stored Series display object when a write passes nil
// for it -- a memdb projection strips the object but never SeriesID. A write
// that also nils SeriesID is removing the series, and restoring the object
// there kept the series on display after a user cleared it (2026-10-03).
func TestUpdateBook_SeriesPreserveGuardFollowsSeriesID(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	series, err := store.CreateSeries("Redshirts", nil)
	if err != nil {
		t.Fatal(err)
	}
	book, err := store.CreateBook(&Book{Title: "Redshirts", FilePath: "/library/r.m4b", SeriesID: &series.ID, Series: series})
	if err != nil {
		t.Fatal(err)
	}

	// Projection-shaped write: object stripped, link intact -> object kept.
	proj := stripBookForMemdb(book)
	proj.Title = "Redshirts (retitled)"
	if _, err := store.UpdateBook(book.ID, proj); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBookByID(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Series == nil || row.Series.ID != series.ID {
		t.Fatalf("projection write wiped the series object: %+v", row.Series)
	}

	// Clear: link and object both nil -> both stay nil.
	row.SeriesID, row.Series = nil, nil
	if _, err := store.UpdateBook(book.ID, row); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBookByID(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SeriesID != nil || row.Series != nil {
		t.Fatalf("series clear did not stick: id=%v series=%+v", row.SeriesID, row.Series)
	}
}

// The store holds Series to SeriesID on every write, so a writer that changes
// only the link -- batch series_id null or another id, the cleanup/reconcile
// unlinks -- cannot leave the old series' object (which reads prefer) behind.
func TestUpdateBook_SeriesObjectFollowsALinkOnlyChange(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	oldSeries, err := store.CreateSeries("Redshirts", nil)
	if err != nil {
		t.Fatal(err)
	}
	newSeries, err := store.CreateSeries("Old Man's War", nil)
	if err != nil {
		t.Fatal(err)
	}
	book, err := store.CreateBook(&Book{Title: "R", FilePath: "/library/link.m4b", SeriesID: &oldSeries.ID, Series: oldSeries})
	if err != nil {
		t.Fatal(err)
	}
	reread := func() *Book {
		t.Helper()
		row, err := store.GetBookByID(book.ID)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}

	// Moved to another series by ID alone, stale object still attached.
	row := reread()
	row.SeriesID = &newSeries.ID
	if _, err := store.UpdateBook(book.ID, row); err != nil {
		t.Fatal(err)
	}
	if got := reread(); got.Series != nil && got.Series.ID != newSeries.ID {
		t.Fatalf("old series object survived a move to series %d: %+v", newSeries.ID, got.Series)
	}

	// A write that carries the right object keeps it.
	row = reread()
	row.Series = newSeries
	if _, err := store.UpdateBook(book.ID, row); err != nil {
		t.Fatal(err)
	}
	if got := reread(); got.Series == nil || got.Series.ID != newSeries.ID {
		t.Fatalf("matching series object was dropped: %+v", got.Series)
	}

	// Unlinked by ID alone (batch series_id null), stale object attached.
	row = reread()
	row.SeriesID = nil
	if _, err := store.UpdateBook(book.ID, row); err != nil {
		t.Fatal(err)
	}
	if got := reread(); got.SeriesID != nil || got.Series != nil {
		t.Fatalf("link-only unlink kept the series: id=%v series=%+v", got.SeriesID, got.Series)
	}
}
