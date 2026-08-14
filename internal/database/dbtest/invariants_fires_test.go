// file: internal/database/dbtest/invariants_fires_test.go
// version: 1.0.0
// guid: 8c50f6a2-7b41-4d39-9e28-3a6f1c04b7de
// last-edited: 2026-08-14

package dbtest_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
)

// newTestStore opens a throwaway Pebble store. dbtest_test is an external test
// package, so the database package's own setupPebbleTestDB is out of reach and
// the exported constructor is used directly.
func newTestStore(t *testing.T) (database.Store, func()) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "invariants.pebble"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	store.WaitForWarmup()
	return store, func() { _ = store.Close() }
}

// recordingTB captures what AssertStoreInvariants reports instead of failing
// the real test, so a test can assert that an invariant DID fire.
//
// Only the methods AssertStoreInvariants actually calls are implemented with
// behaviour; the rest satisfy testing.TB. Fatalf must not return, so it panics
// with a sentinel that the caller recovers — a Fatalf reaching here means the
// helper hit a store error, which is a different failure than an invariant
// firing and must not be silently counted as one.
type recordingTB struct {
	testing.TB
	errors []string
	fatal  string
}

type recordingTBFatal struct{}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	panic(recordingTBFatal{})
}

// runInvariants calls the helper against a recording TB and returns what it
// reported.
func runInvariants(t *testing.T, store database.Store) *recordingTB {
	t.Helper()
	rec := &recordingTB{TB: t}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(recordingTBFatal); !ok {
					panic(r)
				}
			}
		}()
		dbtest.AssertStoreInvariants(rec, store)
	}()
	if rec.fatal != "" {
		t.Fatalf("AssertStoreInvariants hit a store error rather than an invariant: %s", rec.fatal)
	}
	return rec
}

// TestInvariantA_FiresOnLivePrimaryMarkedForDeletion is the instrument check
// for invariant (a).
//
// Invariant (a) — "no book is BOTH a live primary version AND
// MarkedForDeletion" — exists to catch a merge that half-applied. Until
// 2026-08-14 it could not fire from this helper at all: the helper enumerated
// via ListBookIDs, which excludes soft-deleted books, so the contradictory row
// was never even visited. The assertion ran, passed, and proved nothing, in all
// ~19 merge/combine/regroup call sites — which are the only places a
// half-applied merge can occur.
//
// A guard that has never been observed going red is not a guard. This test
// constructs the exact contradictory state and asserts the helper reports it.
func TestInvariantA_FiresOnLivePrimaryMarkedForDeletion(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	yes := true

	// A normal live book, so the fixture is not degenerate and a helper that
	// errored unconditionally would be visible.
	_, err := store.CreateBook(&database.Book{
		Title:            "Perfectly Fine Book",
		FilePath:         "/lib/inv/fine",
		IsPrimaryVersion: &yes,
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	// Baseline: a clean store must report nothing. Without this, an assertion
	// that "errors is non-empty" below could be satisfied by an unrelated
	// permanent complaint.
	if rec := runInvariants(t, store); len(rec.errors) != 0 {
		t.Fatalf("clean store reported invariant violations: %v", rec.errors)
	}

	// Now the contradiction: primary version AND marked for deletion. This is
	// what a merge leaves behind if it flags the source row for deletion but
	// fails before demoting it from primary.
	bad, err := store.CreateBook(&database.Book{
		Title:            "Half Merged Book",
		FilePath:         "/lib/inv/halfmerged",
		IsPrimaryVersion: &yes,
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	bad.MarkedForDeletion = &yes
	bad.IsPrimaryVersion = &yes
	if _, err := store.UpdateBook(bad.ID, bad); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}

	// Guard the premise: the row must actually be in the contradictory state,
	// or this test passes for the wrong reason.
	got, err := store.GetBookByID(bad.ID)
	if err != nil || got == nil {
		t.Fatalf("GetBookByID(%s): %v", bad.ID, err)
	}
	if got.MarkedForDeletion == nil || !*got.MarkedForDeletion {
		t.Fatalf("premise failed: book is not MarkedForDeletion")
	}
	if got.IsPrimaryVersion == nil || !*got.IsPrimaryVersion {
		t.Fatalf("premise failed: book is not a primary version — the store demoted it, " +
			"so this fixture no longer represents a half-applied merge")
	}

	rec := runInvariants(t, store)

	var fired bool
	for _, e := range rec.errors {
		if strings.Contains(e, "invariant (a)") && strings.Contains(e, bad.ID) {
			fired = true
		}
	}
	if !fired {
		t.Fatalf("invariant (a) did NOT fire for book %s. Reported: %v\n"+
			"This is the vacuity the 2026-08-14 fix removed: enumerating via ListBookIDs "+
			"alone skips soft-deleted rows, so the contradictory book is never visited and "+
			"the assertion passes without examining it.", bad.ID, rec.errors)
	}
}
