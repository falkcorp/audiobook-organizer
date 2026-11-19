// file: internal/plugins/maintenance/booksig_recovery_audit_locks_test.go
// version: 1.0.0
// guid: 3f9c1d52-7b68-4a0e-9c47-e1d05a2b7f13
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// auditLockPlugin is newAuditPlugin with the store's field-lock read pointed at
// a table: locked[id] lists the user-locked field keys, unreadable[id] makes the
// lock read fail.
func auditLockPlugin(
	current map[string]*database.Book,
	snaps map[string][]database.BookSnapshot,
	order []string,
	locked map[string][]string,
	unreadable map[string]bool,
) (*Plugin, *[]database.Book) {
	p, written := newAuditPlugin(current, snaps, order)
	st := p.deps.(fakeDeps).store.(*database.MockStore)
	st.GetMetadataFieldStatesFunc = func(id string) ([]database.MetadataFieldState, error) {
		if unreadable[id] {
			return nil, fmt.Errorf("field states for %s: store closed", id)
		}
		rows := make([]database.MetadataFieldState, 0, len(locked[id]))
		for _, k := range locked[id] {
			rows = append(rows, database.MetadataFieldState{BookID: id, Field: k, OverrideLocked: true})
		}
		return rows, nil
	}
	return p, written
}

func auditLockFixture(t *testing.T) (map[string]*database.Book, map[string][]database.BookSnapshot, []string) {
	t.Helper()
	snapTS := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	built := snapTS.Add(-time.Hour)
	current := map[string]*database.Book{
		"locked":  {ID: "locked", Title: "The Long Halloween"},
		"open":    {ID: "open", Title: "Year One"},
		"bad":     {ID: "bad", Title: "Hush"},
		"bothgap": {ID: "bothgap", Title: "The Killing Joke", BookSigBuiltAt: new(built)},
	}
	snaps := map[string][]database.BookSnapshot{}
	for id, c := range current {
		snap := database.Book{ID: id, Title: c.Title, Description: new("old description of " + c.Title)}
		if id == "bothgap" {
			snap.BookSigV1 = new("old-signature")
			snap.BookSigBuiltAt = new(built)
		}
		snaps[id] = []database.BookSnapshot{snapshotOf(t, id, snapTS, snap)}
	}
	return current, snaps, []string{"locked", "open", "bad", "bothgap"}
}

// A description the user locked is not restored, even though a snapshot carries
// one and the column is empty: the empty value is the user's. The unlocked book
// beside it is restored, so the hold is per book rather than a global stop.
func TestBookSigRecoveryAudit_LockedDescriptionIsNotRestored(t *testing.T) {
	current, snaps, order := auditLockFixture(t)
	p, written := auditLockPlugin(current, snaps, order,
		map[string][]string{"locked": {database.FieldKeyDescription}}, nil)
	rep := &fakeReporter{}

	_ = p.runBookSigRecoveryAudit(context.Background(), auditParams(t, false), rep)

	ids := map[string]database.Book{}
	for _, b := range *written {
		ids[b.ID] = b
	}
	if _, ok := ids["locked"]; ok {
		t.Errorf("a book whose description is user-locked was written: %+v", ids["locked"])
	}
	if b, ok := ids["open"]; !ok || b.Description == nil || !strings.HasPrefix(*b.Description, "old description") {
		t.Errorf("the unlocked control book was not restored: %+v (written=%v)", b, ok)
	}
	report := rep.logs[len(rep.logs)-1]
	if !strings.Contains(report, "description-locked 1") {
		t.Errorf("report does not count the held description: %s", report)
	}
}

// A lock set that cannot be read writes nothing for that book (fail closed) and
// is counted as a restore error, not skipped silently.
func TestBookSigRecoveryAudit_UnreadableLocksWriteNothing(t *testing.T) {
	current, snaps, order := auditLockFixture(t)
	p, written := auditLockPlugin(current, snaps, order, nil, map[string]bool{"bad": true})
	rep := &fakeReporter{}

	_ = p.runBookSigRecoveryAudit(context.Background(), auditParams(t, false), rep)

	for _, b := range *written {
		if b.ID == "bad" {
			t.Fatalf("a book whose locks could not be read was written: %+v", b)
		}
	}
	report := rep.logs[len(rep.logs)-1]
	if !strings.Contains(report, "restore errors 1") {
		t.Errorf("the unreadable lock read is not counted as a restore error: %s", report)
	}
}

// The signature columns have no user lock, so a locked description holds back
// only the description: a book missing both still gets its signature restored.
func TestBookSigRecoveryAudit_LockedDescriptionDoesNotHoldTheSignature(t *testing.T) {
	current, snaps, order := auditLockFixture(t)
	p, written := auditLockPlugin(current, snaps, order,
		map[string][]string{"bothgap": {database.FieldKeyDescription}}, nil)
	rep := &fakeReporter{}

	_ = p.runBookSigRecoveryAudit(context.Background(), auditParams(t, false), rep)

	var got *database.Book
	for i := range *written {
		if (*written)[i].ID == "bothgap" {
			got = &(*written)[i]
		}
	}
	if got == nil {
		t.Fatalf("the signature was not restored for a book with only its description locked")
	}
	if got.Description != nil {
		t.Errorf("locked description was filled: %q", *got.Description)
	}
	if got.BookSigV1 == nil || *got.BookSigV1 != "old-signature" {
		t.Errorf("signature not restored: %v", got.BookSigV1)
	}
}
