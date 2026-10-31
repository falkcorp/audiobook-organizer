// file: internal/database/dbtest/newstore_test.go
// version: 1.2.0
// guid: c4a9d1e6-72b3-4f58-8e0a-1b6d3f9a2c57
// last-edited: 2026-10-31

package dbtest

import (
	"errors"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestNewStore_DefaultIsUsableAndWarm(t *testing.T) {
	s := NewStore(t)
	if !s.IsMemReady() {
		t.Fatal("store is not warm: IsMemReady() = false")
	}
	if !s.UseMemDB {
		t.Fatal("UseMemDB = false by default")
	}
	a, err := s.CreateAuthor("Synthetic Author")
	if err != nil || a == nil {
		t.Fatalf("CreateAuthor: %v, %v", a, err)
	}
}

func TestNewStore_TwoCallsAreIndependent(t *testing.T) {
	a := NewStore(t)
	b := NewStore(t)
	if _, err := a.CreateAuthor("Only In A"); err != nil {
		t.Fatal(err)
	}
	got, err := b.GetAuthorByName("Only In A")
	if err == nil && got != nil {
		t.Fatalf("store b sees store a's author: %+v", got)
	}
}

func TestNewStore_OnDiskIsUsableAndWarm(t *testing.T) {
	s := NewStore(t, OnDisk())
	if !s.IsMemReady() {
		t.Fatal("on-disk store is not warm")
	}
	if a, err := s.CreateAuthor("Synthetic Author"); err != nil || a == nil {
		t.Fatalf("CreateAuthor: %v, %v", a, err)
	}
}

func TestNewStore_WithoutMemDB(t *testing.T) {
	s := NewStore(t, WithoutMemDB())
	if s.UseMemDB {
		t.Fatal("UseMemDB = true with WithoutMemDB()")
	}
	// Warmup was awaited even though memdb is now bypassed.
	if !s.IsMemReady() {
		t.Fatal("warmup was not awaited under WithoutMemDB")
	}
	if a, err := s.CreateAuthor("Synthetic Author"); err != nil || a == nil {
		t.Fatalf("CreateAuthor: %v, %v", a, err)
	}
}

// recordingTB captures Cleanup funcs so the test can run them itself and
// assert the store is closed exactly once.
type recordingTB struct {
	testing.TB
	cleanups []func()
}

func (r *recordingTB) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func TestNewStore_CleanupClosesOnce(t *testing.T) {
	rec := &recordingTB{TB: t}
	s := NewStore(rec)
	if len(rec.cleanups) != 1 {
		t.Fatalf("registered %d cleanups, want 1", len(rec.cleanups))
	}
	rec.cleanups[0]()

	// The store is closed: the Close gate refuses every engine access with an
	// error wrapping pebble.ErrClosed rather than letting it reach a closed
	// engine.
	if _, err := s.GetAuthorByName("x"); !errors.Is(err, pebble.ErrClosed) {
		t.Fatalf("GetAuthorByName after cleanup: err = %v, want pebble.ErrClosed", err)
	}

	// Running the cleanup again must be harmless: the Once guard means the
	// store is closed exactly once.
	rec.cleanups[0]()
}

// A loaded CI agent runs the test body before the async warmup has published.
// The hook models that agent: warmup starts late. A raw constructor then hands
// back a store whose fail-closed lookups refuse ("memdb is not serving"), and
// NewStore must not. Not parallel: the hook is process-global.
func TestNewStore_ServesEvenWhenWarmupStartsLate(t *testing.T) {
	restore := database.SetWarmupStartHookForTest(func() { time.Sleep(300 * time.Millisecond) })
	defer restore()

	// The control: the raw constructor really is in the not-serving window.
	raw, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open raw store: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.BookFilesAtPath("/lib/Gotham/held.mp3"); !errors.Is(err, database.ErrBookFilesAtPathUnavailable) {
		t.Fatalf("raw store BookFilesAtPath err = %v, want ErrBookFilesAtPathUnavailable: the hook no longer reproduces the race", err)
	}

	s := NewStore(t)
	if !s.IsMemReady() {
		t.Fatal("NewStore returned before memdb was serving")
	}
	if _, err := s.BookFilesAtPath("/lib/Gotham/held.mp3"); err != nil {
		t.Fatalf("BookFilesAtPath on a NewStore store: %v", err)
	}
	if _, err := database.BookFileRowsAtPathStrict(s, "/lib/Gotham/held.mp3"); err != nil {
		t.Fatalf("BookFileRowsAtPathStrict on a NewStore store: %v", err)
	}
}
