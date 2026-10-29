// file: internal/database/book_version_test.go
// version: 1.2.0
// guid: 7a3d9e1c-5b4f-4a60-b8c5-2e7f0c1b9a48
// last-edited: 2026-10-29

package database

import (
	"errors"
	"testing"
)

func TestBookVersion_CreateAndGet(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	v, err := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusActive, Format: "m4b",
		Source: "deluge", TorrentHash: "abc123",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if v.ID == "" {
		t.Fatal("ID should be auto-assigned")
	}
	if v.Version != 1 {
		t.Errorf("Version = %d, want 1", v.Version)
	}

	got, err := store.GetBookVersion(v.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v / %v", got, err)
	}
	if got.Format != "m4b" {
		t.Errorf("Format = %q", got.Format)
	}
}

func TestBookVersion_SingleActiveInvariant(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusActive, Format: "m4b", Source: "imported",
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Second active for same book should fail.
	if _, err := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusActive, Format: "mp3", Source: "imported",
	}); err == nil {
		t.Error("expected single-active-per-book invariant to reject second active")
	}

	// Alt is fine.
	if _, err := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusAlt, Format: "mp3", Source: "imported",
	}); err != nil {
		t.Errorf("alt status should be allowed: %v", err)
	}

	// Different book's active is fine.
	if _, err := store.CreateBookVersion(&BookVersion{
		BookID: "b2", Status: BookVersionStatusActive, Format: "m4b", Source: "imported",
	}); err != nil {
		t.Errorf("second book's active should be allowed: %v", err)
	}
}

func TestBookVersion_GetActiveForBook(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b1", Status: BookVersionStatusAlt, Format: "mp3", Source: "imported"})
	active, _ := store.CreateBookVersion(&BookVersion{BookID: "b1", Status: BookVersionStatusActive, Format: "m4b", Source: "imported"})
	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b1", Status: BookVersionStatusAlt, Format: "flac", Source: "imported"})

	got, err := store.GetActiveVersionForBook("b1")
	if err != nil || got == nil {
		t.Fatalf("GetActiveVersionForBook: %v, %v", got, err)
	}
	if got.ID != active.ID {
		t.Errorf("active ID = %q, want %q", got.ID, active.ID)
	}
}

func TestBookVersion_GetByTorrentHash(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	v, _ := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusInactivePurged,
		Format: "m4b", Source: "deluge", TorrentHash: "hash-abc",
	})

	got, err := store.GetBookVersionByTorrentHash("hash-abc")
	if err != nil || got == nil {
		t.Fatalf("GetBookVersionByTorrentHash: %v, %v", got, err)
	}
	if got.ID != v.ID {
		t.Errorf("lookup mismatch: got %q, want %q", got.ID, v.ID)
	}

	miss, err := store.GetBookVersionByTorrentHash("not-in-library")
	if err != nil {
		t.Errorf("miss should not error: %v", err)
	}
	if miss != nil {
		t.Errorf("miss should be nil")
	}
}

func TestBookVersion_UpdateStatusTransitions(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	v, _ := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusActive, Format: "m4b", Source: "deluge",
	})

	// Active → alt (demoting a primary). The active-pointer index
	// should clear so GetActiveVersionForBook returns nil.
	v.Status = BookVersionStatusAlt
	if err := store.UpdateBookVersion(v); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := store.GetActiveVersionForBook("b1")
	if got != nil {
		t.Errorf("expected no active version after demoting, got %v", got)
	}

	// After update, Version counter bumped.
	after, _ := store.GetBookVersion(v.ID)
	if after.Version != 2 {
		t.Errorf("Version = %d after update, want 2", after.Version)
	}
}

func TestBookVersion_ListTrashedAndPurged(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b1", Status: BookVersionStatusTrash, Format: "m4b", Source: "imported"})
	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b2", Status: BookVersionStatusTrash, Format: "mp3", Source: "imported"})
	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b3", Status: BookVersionStatusInactivePurged, Format: "flac", Source: "imported"})
	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b4", Status: BookVersionStatusBlockedForRedownload, Format: "m4b", Source: "deluge", TorrentHash: "xyz"})

	trashed, _ := store.ListTrashedBookVersions()
	if len(trashed) != 2 {
		t.Errorf("trashed = %d, want 2", len(trashed))
	}

	purged, _ := store.ListPurgedBookVersions()
	// Includes both inactive_purged and blocked_for_redownload per spec.
	if len(purged) != 2 {
		t.Errorf("purged = %d, want 2 (inactive_purged + blocked_for_redownload)", len(purged))
	}
}

// ListSwappingBookVersions lists exactly the versions a swap left in
// swapping_out / swapping_in, and no trash, active, alt or purge-state version.
func TestBookVersion_ListSwapping(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	want := map[string]bool{}
	for _, st := range []string{
		BookVersionStatusSwappingOut, BookVersionStatusSwappingIn, BookVersionStatusActive,
		BookVersionStatusAlt, BookVersionStatusTrash, BookVersionStatusInactivePurged,
	} {
		v, err := store.CreateBookVersion(&BookVersion{BookID: "b-" + st, Status: st, Format: "m4b", Source: "imported"})
		if err != nil {
			t.Fatal(err)
		}
		want[v.ID] = st == BookVersionStatusSwappingOut || st == BookVersionStatusSwappingIn
	}

	got, err := store.ListSwappingBookVersions()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ListSwappingBookVersions = %d versions, want 2 (swapping_out + swapping_in)", len(got))
	}
	for _, v := range got {
		if !want[v.ID] {
			t.Errorf("listed %s in status %s, which is not a swap in flight", v.ID, v.Status)
		}
	}
}

// The purging claim is enforced under the version's stripe by UpdateBookVersion:
// only a trash (or purging) version can be claimed, and a purging version moves
// only to purging or inactive_purged. This is what closes the restore-versus-
// purge race: whichever write reaches the stripe second is refused.
func TestUpdateBookVersion_PurgingClaim(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	mk := func(status string) *BookVersion {
		v, err := store.CreateBookVersion(&BookVersion{BookID: "b-" + status, Status: status, Format: "m4b", Source: "imported"})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	set := func(v *BookVersion, status string) error {
		c := *v
		c.Status = status
		return store.UpdateBookVersion(&c)
	}

	// Claim: trash -> purging is allowed; alt -> purging is refused.
	trash := mk(BookVersionStatusTrash)
	if err := set(trash, BookVersionStatusPurging); err != nil {
		t.Fatalf("claim of a trashed version: %v", err)
	}
	if err := set(mk(BookVersionStatusAlt), BookVersionStatusPurging); !errors.Is(err, ErrVersionPurgeClaimLost) {
		t.Fatalf("claim of an alt = %v, want ErrVersionPurgeClaimLost", err)
	}

	// A purging version is listed with the trash, so the cleanup retries it.
	listed, _ := store.ListTrashedBookVersions()
	if len(listed) != 1 || listed[0].ID != trash.ID {
		t.Fatalf("ListTrashedBookVersions = %v, want the purging version", listed)
	}

	// Purging -> alt / trash / active are refused; purging -> purging and
	// purging -> inactive_purged are not.
	for _, to := range []string{BookVersionStatusAlt, BookVersionStatusTrash, BookVersionStatusActive, BookVersionStatusSwappingIn} {
		if err := set(trash, to); !errors.Is(err, ErrVersionPurging) {
			t.Errorf("purging -> %s = %v, want ErrVersionPurging", to, err)
		}
	}
	if got, _ := store.GetBookVersion(trash.ID); got.Status != BookVersionStatusPurging {
		t.Fatalf("status = %s after the refused writes, want purging", got.Status)
	}
	if err := set(trash, BookVersionStatusPurging); err != nil {
		t.Errorf("purging -> purging (a retry): %v", err)
	}
	if err := set(trash, BookVersionStatusInactivePurged); err != nil {
		t.Errorf("purging -> inactive_purged: %v", err)
	}
}

// purge_failed is the terminal state of a purge that could not remove a file
// and will not be able to by retrying. It can only be entered from a purge in
// progress, it is listed with the trash (so the trash page shows it and the
// owner can act on it), and it moves only to a retry (purging), a record that
// the version is purged (inactive_purged) or itself: restoring it, or sending it
// back to the trash, is refused, because some of its files may already be gone.
func TestUpdateBookVersion_PurgeFailedState(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	mk := func(id, status string) *BookVersion {
		v, err := store.CreateBookVersion(&BookVersion{BookID: id, Status: status, Format: "m4b", Source: "imported"})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	set := func(v *BookVersion, status string) error {
		c := *v
		c.Status = status
		return store.UpdateBookVersion(&c)
	}

	// Entry: only from purging (or purge_failed). A trashed or alt version never
	// gets there without being claimed first.
	if err := set(mk("b-trash", BookVersionStatusTrash), BookVersionStatusPurgeFailed); !errors.Is(err, ErrVersionPurgeClaimLost) {
		t.Errorf("trash -> purge_failed = %v, want ErrVersionPurgeClaimLost", err)
	}
	if err := set(mk("b-alt", BookVersionStatusAlt), BookVersionStatusPurgeFailed); !errors.Is(err, ErrVersionPurgeClaimLost) {
		t.Errorf("alt -> purge_failed = %v, want ErrVersionPurgeClaimLost", err)
	}

	v := mk("b-stuck", BookVersionStatusTrash)
	if err := set(v, BookVersionStatusPurging); err != nil {
		t.Fatalf("claim: %v", err)
	}
	failed := *v
	failed.Status = BookVersionStatusPurgeFailed
	failedAt := failed.CreatedAt
	failed.PurgeFailedAt = &failedAt
	failed.PurgeFailed = []PurgeFileNote{{Path: "/lib/Gotham/Batman Year One/year-one.m4b", Reason: "operation not permitted"}}
	failed.PurgeKept = []PurgeFileNote{{Path: "/lib/Gotham/Hush/hush.m4b", Reason: "another book names it"}}
	if err := store.UpdateBookVersion(&failed); err != nil {
		t.Fatalf("purging -> purge_failed: %v", err)
	}
	got, err := store.GetBookVersion(v.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Status != BookVersionStatusPurgeFailed || len(got.PurgeFailed) != 1 || got.PurgeFailed[0].Reason != "operation not permitted" ||
		len(got.PurgeKept) != 1 || got.PurgeFailedAt == nil {
		t.Fatalf("stored = %+v, want the failure and the kept file recorded", got)
	}

	// Listed with the trash.
	listed, _ := store.ListTrashedBookVersions()
	found := false
	for _, l := range listed {
		found = found || l.ID == v.ID
	}
	if !found {
		t.Fatalf("ListTrashedBookVersions = %v, want the purge_failed version", listed)
	}

	// Refused: back to alt / trash / active / swapping_in.
	for _, to := range []string{BookVersionStatusAlt, BookVersionStatusTrash, BookVersionStatusActive, BookVersionStatusSwappingIn} {
		if err := set(got, to); !errors.Is(err, ErrVersionPurging) {
			t.Errorf("purge_failed -> %s = %v, want ErrVersionPurging", to, err)
		}
	}
	// Allowed: purge_failed -> purge_failed (a re-record), -> purging (a retry),
	// and purging -> purge_failed again, then -> inactive_purged.
	if err := set(got, BookVersionStatusPurgeFailed); err != nil {
		t.Errorf("purge_failed -> purge_failed: %v", err)
	}
	if err := set(got, BookVersionStatusPurging); err != nil {
		t.Errorf("purge_failed -> purging (a retry): %v", err)
	}
	if err := set(got, BookVersionStatusPurgeFailed); err != nil {
		t.Errorf("purging -> purge_failed: %v", err)
	}
	if err := set(got, BookVersionStatusInactivePurged); err != nil {
		t.Errorf("purge_failed -> inactive_purged: %v", err)
	}
}
