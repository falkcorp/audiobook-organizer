// file: internal/plugins/maintenance/booksig_recovery_audit_test.go
// version: 1.0.0
// guid: 8a1d3f27-6c04-4e59-b2a7-9f5e1c8d0b46
// last-edited: 2026-07-03

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// strptr / timeptr are local helpers for building test Book rows.
func strptr(s string) *string        { return &s }
func timeptr(t time.Time) *time.Time { return &t }

// snapshotOf serializes a Book into a BookSnapshot at ts (raw JSON, like Pebble).
func snapshotOf(t *testing.T, id string, ts time.Time, b database.Book) database.BookSnapshot {
	t.Helper()
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return database.BookSnapshot{BookID: id, Timestamp: ts, Data: data}
}

// newAuditPlugin wires a MockStore over fixed current rows + per-book snapshots.
// Returns the plugin and a pointer to a slice recording every UpdateBook call
// (which must stay empty in dry-run).
func newAuditPlugin(
	current map[string]*database.Book,
	snaps map[string][]database.BookSnapshot,
	order []string,
) (*Plugin, *[]database.Book) {
	written := make([]database.Book, 0)
	store := &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) {
			return order, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return current[id], nil
		},
		GetBookVersionsFunc: func(id string, _ int) ([]database.BookSnapshot, error) {
			return snaps[id], nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			written = append(written, *b)
			return b, nil
		},
	}
	return New(fakeDeps{store: store}), &written
}

func auditParams(t *testing.T, dryRun bool) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(bookSigRecoveryAuditParams{DryRun: dryRun})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return b
}

// TestBookSigRecoveryAudit_Scenarios covers the four brief scenarios plus the
// memdb-false-positive guard, and asserts dry-run writes nothing.
func TestBookSigRecoveryAudit_Scenarios(t *testing.T) {
	built := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	snapTS := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)

	// b1: Description == nil now, but a snapshot carries Description → recoverable.
	// b2: BookSigBuiltAt set, BookSigV1 nil now, snapshot carries BookSigV1 → recoverable.
	// b3: BookSigBuiltAt nil (never built) → NOT a booksig candidate; Description present.
	// b4: Description == nil now, NO snapshot at all → missing but NOT recoverable.
	// b5: memdb false-positive guard — current row is fully intact (Description +
	//     BookSigV1 present). GetBookByID returns the true on-disk (unstripped)
	//     row, so it must NOT be flagged despite what a memdb projection would show.
	current := map[string]*database.Book{
		"b1": {ID: "b1", Title: "Book One", Description: nil},
		"b2": {ID: "b2", Title: "Book Two", Description: strptr("has desc"), BookSigBuiltAt: timeptr(built), BookSigV1: nil},
		"b3": {ID: "b3", Title: "Book Three", Description: strptr("has desc"), BookSigBuiltAt: nil, BookSigV1: nil},
		"b4": {ID: "b4", Title: "Book Four", Description: nil},
		"b5": {ID: "b5", Title: "Book Five", Description: strptr("intact"), BookSigBuiltAt: timeptr(built), BookSigV1: strptr("sig-present")},
	}
	snaps := map[string][]database.BookSnapshot{
		"b1": {snapshotOf(t, "b1", snapTS, database.Book{ID: "b1", Title: "Book One", Description: strptr("old description")})},
		"b2": {snapshotOf(t, "b2", snapTS, database.Book{ID: "b2", Title: "Book Two", BookSigV1: strptr("old-signature"), BookSigBuiltAt: timeptr(built)})},
		// b3, b4, b5: no snapshots.
	}
	order := []string{"b1", "b2", "b3", "b4", "b5"}

	p, written := newAuditPlugin(current, snaps, order)
	rep := &fakeReporter{}

	if err := p.runBookSigRecoveryAudit(context.Background(), auditParams(t, true), rep); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Dry-run must never write.
	if len(*written) != 0 {
		t.Errorf("dry run must write nothing, got %d UpdateBook calls", len(*written))
	}

	// Inspect the final report line (last log emitted).
	if len(rep.logs) == 0 {
		t.Fatal("expected at least one log line")
	}
	report := rep.logs[len(rep.logs)-1]

	// b1 + b4 are Description-missing (2), of which b1 recoverable, b4 not.
	// b2 is the only booksig candidate (built-then-wiped), recoverable.
	// b3 (never built) and b5 (intact) must NOT be flagged at all.
	wantSubstrings := []string{
		"Description missing: 2 (recoverable 1, not-recoverable 1)",
		"BookSigV1 built-then-wiped: 1 (recoverable 1, not-recoverable 0)",
		"audited 5 books",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q\nfull report: %s", want, report)
		}
	}
}

// TestBookSigRecoveryAudit_MemdbNotFalseFlagged proves the check reads the
// Pebble-direct row (GetBookByID) and not a memdb-stripped projection: a book
// whose true row has Description + BookSigV1 present is never flagged.
func TestBookSigRecoveryAudit_MemdbNotFalseFlagged(t *testing.T) {
	built := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	current := map[string]*database.Book{
		"ok": {ID: "ok", Title: "Intact", Description: strptr("present"), BookSigBuiltAt: timeptr(built), BookSigV1: strptr("present")},
	}
	p, written := newAuditPlugin(current, map[string][]database.BookSnapshot{}, []string{"ok"})

	// Wire GetAllBooks to return the SAME id as a memdb-STRIPPED row (Description
	// and BookSigV1 nil). An implementation that (wrongly) sourced the current
	// row from GetAllBooks would see the stripped row and flag "ok" as damaged.
	// The audit must read GetBookByID (Pebble-direct, intact) instead, so "ok"
	// must NOT be flagged. This makes the test discriminate the read source.
	strippedStore := p.deps.Store().(*database.MockStore)
	strippedStore.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		if offset > 0 {
			return nil, nil
		}
		return []database.Book{{ID: "ok", Title: "Intact", Description: nil, BookSigBuiltAt: timeptr(built), BookSigV1: nil}}, nil
	}
	rep := &fakeReporter{}

	if err := p.runBookSigRecoveryAudit(context.Background(), auditParams(t, true), rep); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*written) != 0 {
		t.Errorf("dry run must write nothing, got %d writes", len(*written))
	}
	report := rep.logs[len(rep.logs)-1]
	for _, want := range []string{
		"Description missing: 0 (recoverable 0, not-recoverable 0)",
		"BookSigV1 built-then-wiped: 0 (recoverable 0, not-recoverable 0)",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("intact book was flagged; report: %s", report)
		}
	}
}

// TestBookSigRecoveryAudit_ApplyModeRefused ensures apply mode (dryRun=false) is
// refused rather than silently attempting any write — this task is read-only.
func TestBookSigRecoveryAudit_ApplyModeRefused(t *testing.T) {
	current := map[string]*database.Book{"b1": {ID: "b1", Description: nil}}
	p, written := newAuditPlugin(current, map[string][]database.BookSnapshot{}, []string{"b1"})

	err := p.runBookSigRecoveryAudit(context.Background(), auditParams(t, false), &fakeReporter{})
	if err == nil {
		t.Fatal("expected apply mode (dryRun=false) to be refused")
	}
	if len(*written) != 0 {
		t.Errorf("apply mode must not write, got %d writes", len(*written))
	}
}
