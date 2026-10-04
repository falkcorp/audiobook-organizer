// file: internal/database/pebble_store_create_series_test.go
// version: 1.0.0
// guid: 7d0b6f1e-3c52-4a8e-9b14-5e2f8a6c1d93
// last-edited: 2026-10-03

package database

import "testing"

// CreateBook holds Book.Series to SeriesID exactly as UpdateBook does, so a
// create cannot write the legacy shapes the stale-series relink repairs.
func TestCreateBook_HoldsSeriesToSeriesID(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s, err := store.CreateSeries("The Expanse", nil)
	if err != nil {
		t.Fatal(err)
	}
	o, err := store.CreateSeries("Other", nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("object without id is dropped", func(t *testing.T) {
		emb := *s
		b, err := store.CreateBook(&Book{Title: "X", FilePath: "/l/x.m4b", Series: &emb})
		if err != nil {
			t.Fatal(err)
		}
		row, _ := store.GetBookByID(b.ID)
		if row.SeriesID != nil || row.Series != nil {
			t.Fatalf("SeriesID=%v Series=%+v, want both nil", row.SeriesID, row.Series)
		}
	})
	t.Run("object naming another id is dropped", func(t *testing.T) {
		oid := o.ID
		emb := *s
		b, err := store.CreateBook(&Book{Title: "Y", FilePath: "/l/y.m4b", SeriesID: &oid, Series: &emb})
		if err != nil {
			t.Fatal(err)
		}
		row, _ := store.GetBookByID(b.ID)
		if row.SeriesID == nil || *row.SeriesID != o.ID {
			t.Fatalf("SeriesID=%v, want %d", row.SeriesID, o.ID)
		}
		if row.Series != nil && row.Series.ID != o.ID {
			t.Fatalf("Series names %d, want nil or %d", row.Series.ID, o.ID)
		}
	})
	t.Run("matching object is kept", func(t *testing.T) {
		sid := s.ID
		emb := *s
		b, err := store.CreateBook(&Book{Title: "Z", FilePath: "/l/z.m4b", SeriesID: &sid, Series: &emb})
		if err != nil {
			t.Fatal(err)
		}
		row, _ := store.GetBookByID(b.ID)
		if row.Series == nil || row.Series.ID != s.ID {
			t.Fatalf("Series=%+v, want id %d", row.Series, s.ID)
		}
	})
}
