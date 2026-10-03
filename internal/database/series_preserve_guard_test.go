// file: internal/database/series_preserve_guard_test.go
// version: 1.0.0
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
