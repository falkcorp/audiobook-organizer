// file: internal/server/duplicates_ops_reroute_test.go
// version: 1.1.0
// guid: 4a9c1f27-8b3e-4d05-9a61-2f7c0d3e6b58
// last-edited: 2026-07-18

// F6 regression test: the dedup.book-merge op (POST /audiobooks/merge) was
// rerouted from the legacy dedup.MergeBooks hard-delete path to
// merge.Service.MergeBooks via applyBookMergeReroute. The legacy path hard-
// deleted losers with store.DeleteBook, which does NOT tombstone external-ID
// (ext_id:*) mappings and does NOT enqueue ITL removals — orphaning the losers'
// PID/ASIN lookups and stranding their iTunes tracks. This test drives the
// actual reroute helper on a real PebbleStore and asserts the loser is
// SOFT-deleted (not hard-deleted), its external ID is reassigned to the winner,
// and the winner inherits the loser's iTunes stats first-win. It fails against
// the old hard-delete code (loser gone, external ID left dangling).
package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
)

func t10IntPtr(v int) *int { return &v }

func TestApplyBookMergeReroute_SoftDeletesAndReassignsExternalIDs(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	keepID := ulid.Make().String()
	loserID := ulid.Make().String()

	// Winner (M4B) with NO iTunes rating; loser (mp3) carries a rating so we can
	// prove the first-win copy runs.
	if _, err := store.CreateBook(&database.Book{
		ID: keepID, Title: "Keep", Format: "m4b", FilePath: "/tmp/" + keepID + ".m4b",
	}); err != nil {
		t.Fatalf("CreateBook keep: %v", err)
	}
	if _, err := store.CreateBook(&database.Book{
		ID: loserID, Title: "Loser", Format: "mp3", FilePath: "/tmp/" + loserID + ".mp3",
		ITunesRating: t10IntPtr(80), ITunesPlayCount: t10IntPtr(5),
	}); err != nil {
		t.Fatalf("CreateBook loser: %v", err)
	}

	// Give the loser an iTunes external-ID mapping. Under the legacy hard-delete
	// path this row would be left dangling, resolving to a deleted book.
	const loserPID = "PID-LOSER-T10"
	if err := store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: loserPID, BookID: loserID, Provenance: "itunes",
	}); err != nil {
		t.Fatalf("CreateExternalIDMapping: %v", err)
	}

	ms := merge.NewService(store)
	if err := applyBookMergeReroute(context.Background(), store, ms, keepID, []string{loserID}); err != nil {
		t.Fatalf("applyBookMergeReroute: %v", err)
	}

	// (1) Loser is SOFT-deleted, not hard-deleted: the row still exists and is
	// flagged MarkedForDeletion. Under legacy hard-delete GetBookByID returns nil.
	loser, err := store.GetBookByID(loserID)
	if err != nil {
		t.Fatalf("GetBookByID loser: %v", err)
	}
	if loser == nil {
		t.Fatal("loser was HARD-deleted (nil) — reroute must soft-delete so it stays recoverable")
	}
	if loser.MarkedForDeletion == nil || !*loser.MarkedForDeletion {
		t.Fatal("loser not marked for deletion — expected soft-delete")
	}

	// (2) External ID reassigned to the winner (not dangling on the loser).
	resolved, err := store.GetBookByExternalID("itunes", loserPID)
	if err != nil {
		t.Fatalf("GetBookByExternalID: %v", err)
	}
	if resolved != keepID {
		t.Fatalf("external ID %s resolves to %q, want winner %q (legacy path left it dangling on the deleted loser)", loserPID, resolved, keepID)
	}

	// (3) First-win iTunes copy: winner inherited the loser's rating.
	keep, err := store.GetBookByID(keepID)
	if err != nil {
		t.Fatalf("GetBookByID keep: %v", err)
	}
	if keep == nil {
		t.Fatal("keep book missing after merge")
	}
	if keep.ITunesRating == nil || *keep.ITunesRating != 80 {
		t.Fatalf("keep iTunes rating = %v, want 80 (first-win copy from loser)", keep.ITunesRating)
	}
}

// TestApplyBookMergeReroute_KeepIDInMergeIDs guards the version-group integrity
// hole: the handler binds keep_id/merge_ids without validation, so the keep book
// can appear in mergeIDs. If it were passed through to merge.Service.MergeBooks
// the version-group loop would write the keep book twice and demote it to
// non-primary, leaving the group with NO primary and the keep book neither
// primary nor soft-deleted. The reroute must exclude keepID from the loser set.
func TestApplyBookMergeReroute_KeepIDInMergeIDs(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	keepID := ulid.Make().String()
	loserID := ulid.Make().String()
	if _, err := store.CreateBook(&database.Book{
		ID: keepID, Title: "Keep", Format: "m4b", FilePath: "/tmp/" + keepID + ".m4b",
	}); err != nil {
		t.Fatalf("CreateBook keep: %v", err)
	}
	if _, err := store.CreateBook(&database.Book{
		ID: loserID, Title: "Loser", Format: "mp3", FilePath: "/tmp/" + loserID + ".mp3",
	}); err != nil {
		t.Fatalf("CreateBook loser: %v", err)
	}

	ms := merge.NewService(store)
	// keepID deliberately included in mergeIDs (and duplicated).
	if err := applyBookMergeReroute(context.Background(), store, ms, keepID, []string{keepID, loserID, loserID}); err != nil {
		t.Fatalf("applyBookMergeReroute: %v", err)
	}

	// Keep book survives as the primary version, NOT soft-deleted.
	keep, err := store.GetBookByID(keepID)
	if err != nil {
		t.Fatalf("GetBookByID keep: %v", err)
	}
	if keep == nil {
		t.Fatal("keep book was deleted — keepID-in-mergeIDs must not remove the winner")
	}
	if keep.MarkedForDeletion != nil && *keep.MarkedForDeletion {
		t.Fatal("keep book was soft-deleted — keepID must be excluded from the loser set")
	}
	if keep.IsPrimaryVersion == nil || !*keep.IsPrimaryVersion {
		t.Fatalf("keep book is not the primary version (%v) — version group left with no primary", keep.IsPrimaryVersion)
	}

	// Loser is soft-deleted as expected.
	loser, err := store.GetBookByID(loserID)
	if err != nil {
		t.Fatalf("GetBookByID loser: %v", err)
	}
	if loser == nil || loser.MarkedForDeletion == nil || !*loser.MarkedForDeletion {
		t.Fatalf("loser not soft-deleted: %+v", loser)
	}
}
