// file: internal/readstatus/rebuild_test.go
// version: 1.1.0
// guid: 8b2d6f4a-3c19-4e57-a6d0-f1e9c7b5a382
// last-edited: 2026-09-19

package readstatus

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// corruptStateStore returns a real store for u1/b1 with a readable position
// at 300 s on s1 (of a 3 × 600 s book) and an UNDECODABLE ubs: row.
func corruptStateStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "db")
	store, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "B", FilePath: "/tmp/b1"}); err != nil {
		t.Fatal(err)
	}
	for i, seg := range []string{"s1", "s2", "s3"} {
		if err := store.CreateBookFile(&database.BookFile{ID: seg, BookID: "b1", FilePath: "/tmp/" + seg, TrackNumber: i + 1, Duration: 600}); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.SetUserPosition("u1", "b1", "s1", 300)
	store.Close()
	db, err := pebble.Open(dir, &pebble.Options{FormatMajorVersion: pebble.FormatNewest})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Set([]byte("ubs:u1:b1"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err = database.NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// An unreadable ubs: row made every state write for the book 503 forever.
// Dry run (the default) reports it and writes nothing.
func TestRebuild_DryRunReportsAndWritesNothing(t *testing.T) {
	store := corruptStateStore(t)
	rep, err := RebuildUserBookState(store, "u1", "b1", false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.StateReadable || rep.StateError == "" || !rep.WouldRebuild || rep.Applied || rep.Rebuilt == nil {
		t.Fatalf("dry-run report = %+v", rep)
	}
	if _, err := store.GetUserBookState("u1", "b1"); err == nil {
		t.Fatal("dry run rewrote the row")
	}
}

func TestRebuild_ApplyRebuildsFromPositions(t *testing.T) {
	store := corruptStateStore(t)
	rep, err := RebuildUserBookState(store, "u1", "b1", true)
	if err != nil || !rep.Applied {
		t.Fatalf("apply = %+v, %v", rep, err)
	}
	st, err := store.GetUserBookState("u1", "b1")
	if err != nil || st == nil || st.Status != database.UserBookStatusInProgress || st.TotalListenedSeconds != 300 {
		t.Fatalf("rebuilt state = %+v, %v; want in_progress with 300 s listened", st, err)
	}
	// And the book is writable again.
	if _, err := RecomputeUserBookState(store, "u1", "b1"); err != nil {
		t.Fatalf("recompute after repair: %v", err)
	}
}

// A readable row is never touched.
func TestRebuild_ReadableStateIsLeftAlone(t *testing.T) {
	store := seedTombstoned(t)
	rep, err := RebuildUserBookState(store, "u1", "b1", true)
	if err != nil || !rep.StateReadable || rep.WouldRebuild || rep.Applied {
		t.Fatalf("report for a readable row = %+v, %v", rep, err)
	}
	assertStateIntact(t, store)
}

type transientStateStore struct{ countingStore }

func (*transientStateStore) GetUserBookState(string, string) (*database.UserBookState, error) {
	return nil, errors.New("transient I/O failure")
}

// Only a row that fails to DECODE is rebuilt. A transient read error says
// nothing about the row, which may be perfectly good: apply must not
// overwrite it. The error maps to 503 (ErrStateUnreadable).
func TestRebuild_TransientReadErrorWritesNothing(t *testing.T) {
	store := seedTombstoned(t)
	ts := &transientStateStore{countingStore{Store: store}}
	rep, err := RebuildUserBookState(ts, "u1", "b1", true)
	if !errors.Is(err, ErrStateUnreadable) || rep.Applied || ts.writes != 0 {
		t.Fatalf("rebuild after a transient read error = %+v, %v, writes=%d; want ErrStateUnreadable and no write", rep, err, ts.writes)
	}
	assertStateIntact(t, store)
}
