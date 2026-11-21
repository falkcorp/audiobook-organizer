// file: internal/books/roots/scan_real_test.go
// version: 0.1.0
// guid: 8e3f0b4d-2a6c-4f19-b7d5-4c1a9e6d3f28
// last-edited: 2026-11-20

package roots_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"

	"github.com/falkcorp/audiobook-organizer/internal/books/roots"
)

// The ScanStore over books' real LegacyRange: what the in-memory fake assumed
// about Get on a missing key, Scan's key shape and bounds, and Delete of an
// absent key is checked here against core/storage itself.
func TestScanStore_OverARealLegacyRange(t *testing.T) {
	ctx := context.Background()
	rg := newRig(t, nil)
	s := roots.NewScanStore(rg.rows)

	if _, ok, err := s.Get(ctx, "import-1"); ok || err != nil {
		t.Fatalf("Get of a missing row = %v, %v; want no row and no error", ok, err)
	}
	if err := s.Delete(ctx, "import-1"); err != nil {
		t.Fatalf("Delete of an absent row: %v", err)
	}
	at := time.Date(2026, 11, 20, 3, 4, 5, 0, time.UTC)
	if err := s.RecordScan(ctx, "import-1", 6, at); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "import-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(ctx, "import-2", false); err != nil {
		t.Fatal(err)
	}
	// An undecodable row beside the good ones, written under the claim.
	if err := rg.rows.Set(ctx, []byte("rootscan:import-3"), []byte(`{nope`)); err != nil {
		t.Fatal(err)
	}
	// A key outside the family but inside the claim's neighbourhood must not appear.
	all, err := s.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("All = %d entries, want 3: %+v", len(all), all)
	}
	if e := all["import-1"]; e.Err != nil || e.State.BookCount != 6 || e.State.LastScan == nil || !e.State.LastScan.Equal(at) {
		t.Errorf("import-1 = %+v", e)
	}
	if e := all["import-2"]; e.Err != nil || e.State.Enabled {
		t.Errorf("import-2 = %+v", e)
	}
	if !errors.Is(all["import-3"].Err, roots.ErrBadScanRow) {
		t.Errorf("import-3 = %+v; want ErrBadScanRow", all["import-3"])
	}
	if err := s.RecordScan(ctx, "import-3", 1, at); !errors.Is(err, roots.ErrBadScanRow) {
		t.Errorf("RecordScan over a bad row = %v", err)
	}
	if n, err := s.NextImportN(ctx, nil); err != nil || n != 4 {
		t.Errorf("NextImportN = %d, %v; want 4", n, err)
	}
	if err := s.Delete(ctx, "import-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "import-1"); ok {
		t.Error("the row is still there after Delete")
	}
}

func TestScanStore_RealRowsConcurrentFieldUpdates(t *testing.T) {
	ctx := context.Background()
	rg := newRig(t, nil)
	s := roots.NewScanStore(rg.rows)
	if err := s.Create(ctx, "import-1"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.RecordScan(ctx, "import-1", i+1, time.Now()) }()
		go func() { defer wg.Done(); _ = s.SetEnabled(ctx, "import-1", false) }()
	}
	wg.Wait()
	got, _, _ := s.Get(ctx, "import-1")
	if got.Enabled || got.BookCount == 0 || got.LastScan == nil {
		t.Errorf("after concurrent writers: %+v", got)
	}
}

// The whole read side over real storage and a real Scope.
func TestReader_OverRealScopeAndRealRows(t *testing.T) {
	ctx := context.Background()
	rg := newRig(t, nil)
	scans := roots.NewScanStore(rg.rows)
	r := roots.NewReader(rg.books, scans)
	if err := scans.RecordScan(ctx, "import-2", 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := r.ImportRoots(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("ImportRoots = %+v, %v", got, err)
	}
	var crime roots.ImportRoot
	for _, ir := range got {
		if ir.RootID == "import-2" {
			crime = ir
		}
	}
	if !crime.HasScan || crime.BookCount != 3 || !crime.Enabled {
		t.Errorf("import-2 = %+v", crime)
	}
	if ids, _ := r.WatchRoots(ctx); len(ids) != 1 || ids[0] != library.RootID("import-1") {
		t.Errorf("WatchRoots = %v (import-2 has watch off even though its row says enabled)", ids)
	}
}
