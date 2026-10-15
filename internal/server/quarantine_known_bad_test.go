// file: internal/server/quarantine_known_bad_test.go
// version: 1.1.0
// guid: 2e7c9b51-8a4f-4d36-b1e0-6f3a5d8c2b94
// last-edited: 2026-10-15

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/quarantine"
	"github.com/falkcorp/audiobook-organizer/internal/remux"
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

type p6NopEvents struct{}

func (p6NopEvents) Publish(context.Context, plugin.Event) {}

// A book whose file the transcode marked permanently unreadable
// (transcode_skip_*) is quarantined by the real QuarantineService; an unmarked
// book beside it is left alone.
func TestQuarantineKnownBadFiles_QuarantinesAMarkedBook(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()

	ids := map[string]string{}
	for _, name := range []string{"marked", "clean"} {
		path := filepath.Join(root, "Synthetic Author", name, name+".m4b")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic audio "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := store.CreateBook(&database.Book{Title: "Synthetic " + name, FilePath: path})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = b.ID
		if name == "marked" {
			if err := store.SetSetting(remux.TranscodeSkipKey(path), "true", "bool", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	srv := &Server{store: store, quarantineSvc: quarantine.NewQuarantineService(store, &config.Config{RootDir: root}, p6NopEvents{})}

	n, done, err := srv.quarantineKnownBadFiles(context.Background())
	if err != nil || done || n != 1 {
		t.Fatalf("quarantined=%d done=%t err=%v, want 1/false/nil", n, done, err)
	}
	if b, _ := store.GetBookByID(ids["marked"]); b == nil || b.QuarantinedAt == nil {
		t.Errorf("marked book not quarantined: %+v", b)
	}
	if b, _ := store.GetBookByID(ids["clean"]); b == nil || b.QuarantinedAt != nil {
		t.Errorf("clean book quarantined: %+v", b)
	}
}
