// file: internal/server/series_prune_held_test.go
// version: 1.0.0
// guid: 010f0a3d-2158-471b-8424-64e39fbdf9c5
// last-edited: 2026-10-06

package server

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A held series row (database.SeriesHeld) is referenced by nothing while a
// repair's undo may link books back to it; the orphan prune keeps it and
// still deletes an unheld orphan beside it.
func TestExecuteSeriesPrune_KeepsHeldSeries(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	held, err := store.CreateSeries("Brandon Sanderson", nil)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := store.CreateSeries("Nobody Reads This", nil)
	if err != nil {
		t.Fatal(err)
	}
	holder := database.AsSeriesHolder(store)
	if holder == nil {
		t.Fatal("the store cannot hold series rows")
	}
	if err := holder.HoldSeries(held.ID, "maintenance.author-named-series"); err != nil {
		t.Fatal(err)
	}

	s := newSeriesPruneServer(t)
	if err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, ""); err != nil {
		t.Fatalf("executeSeriesPrune: %v", err)
	}
	if got, _ := store.GetSeriesByID(held.ID); got == nil {
		t.Fatal("the held series was pruned; an undo can no longer link its books back")
	}
	if got, _ := store.GetSeriesByID(orphan.ID); got != nil {
		t.Fatal("the unheld orphan must still be pruned (positive control)")
	}
}
