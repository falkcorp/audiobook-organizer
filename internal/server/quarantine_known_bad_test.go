// file: internal/server/quarantine_known_bad_test.go
// version: 1.0.0
// guid: 2e7c9b51-8a4f-4d36-b1e0-6f3a5d8c2b94
// last-edited: 2026-10-15

package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestQuarantineKnownBadFiles_CancelLeavesFlagUnsetAndRunsOnce: the quarantine
// step now runs inside maintenance.malformed-m4b-transcode on the op's ctx. A
// cancelled run (the registry's shutdown drain) returns at once without
// setting the done flag; a full run sets it; the run after that is a no-op.
func TestQuarantineKnownBadFiles_CancelLeavesFlagUnsetAndRunsOnce(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Unmarked books exercise the read pool without quarantining anything
	// (quarantineSvc is nil here, so a marked book would panic the test).
	for i := range 6 {
		if _, err := store.CreateBook(&database.Book{Title: fmt.Sprintf("Synthetic %d", i), FilePath: fmt.Sprintf("/library/synthetic-%d.m4b", i)}); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{store: store}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, done, err := srv.quarantineKnownBadFiles(ctx); !errors.Is(err, context.Canceled) || done {
		t.Fatalf("cancelled run: done=%t err=%v, want context.Canceled", done, err)
	}
	if s, _ := store.GetSetting(quarantineKnownBadKey); s != nil && s.Value == "true" {
		t.Fatal("a cancelled run set the done flag")
	}

	n, done, err := srv.quarantineKnownBadFiles(context.Background())
	if err != nil || done || n != 0 {
		t.Fatalf("first full run: quarantined=%d done=%t err=%v, want 0/false/nil", n, done, err)
	}
	if _, done, err := srv.quarantineKnownBadFiles(context.Background()); err != nil || !done {
		t.Fatalf("run after completion: done=%t err=%v, want already done", done, err)
	}
}
