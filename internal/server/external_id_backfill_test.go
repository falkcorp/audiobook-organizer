// file: internal/server/external_id_backfill_test.go
// version: 1.0.0
// guid: 4a8d1f63-2e7b-4c95-b0f4-9c3e6a1d7b28
// last-edited: 2026-10-15

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

func p6ExternalIDFixture(t *testing.T) (*Server, *database.PebbleStore, string) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pid := "SYNTHETICPID0001"
	b, err := store.CreateBook(&database.Book{Title: "Synthetic", FilePath: "/library/synthetic.m4b", ITunesPersistentID: &pid})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{store: store}, store, b.ID
}

func p6Mappings(t *testing.T, store *database.PebbleStore, id string) int {
	t.Helper()
	m, err := store.GetExternalIDsForBook(id)
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

// The maintenance op's two modes against a real store: ifNeeded=true honours
// the done flag (skipped, nothing written); ifNeeded=false runs the full pass
// even with the flag set; a cancelled ctx is reported as context.Canceled.
func TestBackfillExternalIDs_Modes(t *testing.T) {
	t.Run("if_needed with the done flag set skips", func(t *testing.T) {
		srv, store, id := p6ExternalIDFixture(t)
		if err := store.SetSetting(itunes.ExternalIDBackfillDoneKey, "true", "string", false); err != nil {
			t.Fatal(err)
		}
		skipped, err := srv.backfillExternalIDs(context.Background(), nil, true)
		if err != nil || !skipped {
			t.Fatalf("skipped=%t err=%v, want skipped", skipped, err)
		}
		if n := p6Mappings(t, store, id); n != 0 {
			t.Errorf("a skipped run wrote %d mapping(s)", n)
		}
	})
	t.Run("full run ignores the done flag", func(t *testing.T) {
		srv, store, id := p6ExternalIDFixture(t)
		if err := store.SetSetting(itunes.ExternalIDBackfillDoneKey, "true", "string", false); err != nil {
			t.Fatal(err)
		}
		skipped, err := srv.backfillExternalIDs(context.Background(), nil, false)
		if err != nil || skipped {
			t.Fatalf("skipped=%t err=%v, want a full run", skipped, err)
		}
		if n := p6Mappings(t, store, id); n == 0 {
			t.Error("a full run wrote no mapping for a book with an iTunes PID")
		}
	})
	t.Run("cancelled ctx", func(t *testing.T) {
		srv, store, id := p6ExternalIDFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := srv.backfillExternalIDs(ctx, nil, false); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if n := p6Mappings(t, store, id); n != 0 {
			t.Errorf("a cancelled run wrote %d mapping(s)", n)
		}
		if s, _ := store.GetSetting(itunes.ExternalIDBackfillDoneKey); s != nil {
			t.Errorf("a cancelled run set the done flag to %q", s.Value)
		}
	})
}
