// file: internal/database/dbtest/newstore_test.go
// version: 1.1.0
// guid: c4a9d1e6-72b3-4f58-8e0a-1b6d3f9a2c57
// last-edited: 2026-10-11

package dbtest

import (
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
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
