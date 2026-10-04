// file: internal/database/pebble_store_legacy_seed_test.go
// version: 1.0.1
// guid: 0a0ee936-5fe1-42bb-9d1a-8739a9561bbe
// last-edited: 2026-10-03

package database

import "testing"

// SeedLegacyBookRowForTest writes the row shape older builds left (Series
// object kept with SeriesID nil), every read sees it, and the next ordinary
// write normalizes it. It skips exactly the Series rule: other fields fn
// sets land as with ModifyBook.
func TestSeedLegacyBookRowForTest_WritesAVisibleLegacyRowThatTheNextWriteNormalizes(t *testing.T) {
	store, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	series, err := store.CreateSeries("The Expanse", nil)
	if err != nil {
		t.Fatal(err)
	}
	book, err := store.CreateBook(&Book{Title: "Leviathan Wakes", FilePath: "/library/lw.m4b"})
	if err != nil {
		t.Fatal(err)
	}

	emb := *series
	if _, err := store.SeedLegacyBookRowForTest(book.ID, func(b *Book) error {
		b.SeriesID, b.Series = nil, &emb
		b.Title = "Leviathan Wakes (seeded)"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	row, err := store.GetBookByID(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SeriesID != nil || row.Series == nil || row.Series.ID != series.ID {
		t.Fatalf("legacy row not stored as seeded: id=%v series=%+v", row.SeriesID, row.Series)
	}
	if row.Title != "Leviathan Wakes (seeded)" {
		t.Fatalf("other fields must land as with ModifyBook: title=%q", row.Title)
	}
	cores, err := store.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, c := range cores {
		if c.ID == book.ID {
			seen = c.SeriesID == nil && c.Title == "Leviathan Wakes (seeded)"
		}
	}
	if !seen {
		t.Fatalf("the library listing does not show the seeded row")
	}

	// An ordinary write holds the object to SeriesID again.
	if _, err := store.ModifyBook(book.ID, func(b *Book) error {
		b.Title = "Leviathan Wakes"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBookByID(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SeriesID != nil || row.Series != nil {
		t.Fatalf("the next ordinary write kept the legacy object: id=%v series=%+v", row.SeriesID, row.Series)
	}
}

// A missing book is (nil, nil), as with ModifyBook.
func TestSeedLegacyBookRowForTest_MissingBook(t *testing.T) {
	store, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, err := store.SeedLegacyBookRowForTest("no-such-book", func(*Book) error {
		t.Fatal("fn must not run for a missing book")
		return nil
	})
	if err != nil || got != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", got, err)
	}
}
