// file: internal/database/works_generation_test.go
// version: 1.0.0
// guid: 9c4a7e25-1f6b-4d83-b0e2-5a8d3c61f794
// last-edited: 2026-09-13

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// TestWorksGeneration_BumpedByEveryWorkWriter pins the invalidation contract
// the scanner's retained works map depends on: every writer of a work: key
// changes WorksGeneration. The writers are CreateWork, UpdateWork, DeleteWork,
// WipeByPrefixes and Reset — the complete set of work: key writers in this
// package (grep "work:" internal/database).
func TestWorksGeneration_BumpedByEveryWorkWriter(t *testing.T) {
	s, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p, ok := s.(*PebbleStore)
	if !ok {
		t.Fatalf("setupPebbleTestDB returned %T, want *PebbleStore", s)
	}

	expectBump := func(name string, write func() error) {
		t.Helper()
		before := p.WorksGeneration()
		if err := write(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if after := p.WorksGeneration(); after == before {
			t.Fatalf("%s did not change WorksGeneration (still %d)", name, after)
		}
	}

	var id string
	expectBump("CreateWork", func() error {
		w, err := p.CreateWork(&Work{Title: "Dune"})
		if err == nil {
			id = w.ID
		}
		return err
	})
	expectBump("UpdateWork", func() error {
		_, err := p.UpdateWork(id, &Work{Title: "Dune Messiah"})
		return err
	})
	expectBump("DeleteWork", func() error { return p.DeleteWork(id) })
	if _, err := p.CreateWork(&Work{Title: "Children of Dune"}); err != nil {
		t.Fatal(err)
	}
	expectBump("WipeByPrefixes", func() error {
		_, err := p.WipeByPrefixes([]string{"work:"})
		return err
	})
	if _, err := p.CreateWork(&Work{Title: "God Emperor"}); err != nil {
		t.Fatal(err)
	}
	expectBump("Reset", p.Reset)

	// Reads do not bump.
	before := p.WorksGeneration()
	if _, err := p.GetAllWorks(); err != nil {
		t.Fatal(err)
	}
	if p.WorksGeneration() != before {
		t.Fatal("GetAllWorks changed WorksGeneration")
	}
}

// TestForEachWork_StopsOnCanceledContext: the scanner's works load must stop
// on a canceled context instead of walking the whole table.
func TestForEachWork_StopsOnCanceledContext(t *testing.T) {
	s, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p := s.(*PebbleStore)
	const n = 3 * worksIterCtxEvery
	// Seed raw rows in one unsynced batch: 3,072 synced CreateWork calls took
	// ~46s, and only the row count matters here.
	batch := p.db.NewBatch()
	for i := 0; i < n; i++ {
		w := Work{ID: fmt.Sprintf("w%06d", i), Title: fmt.Sprintf("work %d", i)}
		data, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		if err := batch.Set([]byte("work:"+w.ID), data, nil); err != nil {
			t.Fatal(err)
		}
		if err := batch.Set([]byte("work:title:work "+w.ID+":"+w.ID), []byte(w.ID), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Commit(pebble.NoSync); err != nil {
		t.Fatal(err)
	}

	all := 0
	if err := p.ForEachWork(context.Background(), func(Work) error { all++; return nil }); err != nil {
		t.Fatal(err)
	}
	if all != n {
		t.Fatalf("ForEachWork visited %d works; want %d (index keys must not be visited)", all, n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	visited := 0
	err := p.ForEachWork(ctx, func(Work) error { visited++; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ForEachWork on a canceled ctx returned %v; want context.Canceled", err)
	}
	if visited >= n {
		t.Fatalf("ForEachWork visited all %d works despite a canceled ctx", visited)
	}

	if _, err := p.GetScanCacheMapContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("GetScanCacheMapContext: unexpected error %v", err)
	}
}
