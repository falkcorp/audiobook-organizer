// file: internal/database/live_series_ref_test.go
// version: 1.0.0
// guid: 7a1c5e93-2d84-4f60-b8a7-0e39d1c62b45
// last-edited: 2026-08-14

package database

import "testing"

// TestDropDanglingSeriesRef pins C610: copies of SeriesID must not propagate
// refs whose series has been deleted, while live refs and nil refs pass
// through untouched.
func TestDropDanglingSeriesRef(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p := store.(*PebbleStore)

	live, err := p.CreateSeries("Living Series", nil)
	if err != nil {
		t.Fatalf("CreateSeries: %v", err)
	}
	doomed, err := p.CreateSeries("Doomed Series", nil)
	if err != nil {
		t.Fatalf("CreateSeries doomed: %v", err)
	}
	doomedID := doomed.ID
	if err := p.DeleteSeries(doomedID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}

	// Live ref: kept.
	b := &Book{ID: "c610-live", SeriesID: &live.ID}
	if DropDanglingSeriesRef(p, b, "test") {
		t.Fatal("live series ref must be kept")
	}
	if b.SeriesID == nil || *b.SeriesID != live.ID {
		t.Fatalf("live ref mutated: %v", b.SeriesID)
	}

	// Dangling ref: dropped.
	b2 := &Book{ID: "c610-dangling", SeriesID: &doomedID}
	if !DropDanglingSeriesRef(p, b2, "test") {
		t.Fatal("dangling series ref must be dropped")
	}
	if b2.SeriesID != nil {
		t.Fatalf("dangling ref survived: %v", *b2.SeriesID)
	}

	// Nil ref: no-op.
	b3 := &Book{ID: "c610-nil"}
	if DropDanglingSeriesRef(p, b3, "test") {
		t.Fatal("nil ref must be a no-op")
	}

	// Capability missing: fail-open (keep the ref).
	b4 := &Book{ID: "c610-nocap", SeriesID: &doomedID}
	if DropDanglingSeriesRef(struct{}{}, b4, "test") {
		t.Fatal("store without series lookup must keep the ref")
	}
	if b4.SeriesID == nil {
		t.Fatal("fail-open path must not drop the ref")
	}
}
