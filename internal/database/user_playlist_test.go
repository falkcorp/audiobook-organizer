// file: internal/database/user_playlist_test.go
// version: 1.0.0
// guid: 8b1e2c4d-6f5a-4a70-b8c5-3d7e0f1b9a59

package database

import (
	"path/filepath"
	"testing"
)

func newPlaylistTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestUserPlaylist_CreateAndGet(t *testing.T) {
	store := newPlaylistTestStore(t)

	pl, err := store.CreateUserPlaylist(&UserPlaylist{
		Name:    "Morning Commute",
		Type:    UserPlaylistTypeStatic,
		BookIDs: []string{"b1", "b2", "b3"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pl.ID == "" {
		t.Fatal("ID should be auto-assigned")
	}
	if pl.Version != 1 {
		t.Errorf("Version = %d, want 1", pl.Version)
	}
	if pl.CreatedAt.IsZero() || pl.UpdatedAt.IsZero() {
		t.Error("timestamps should be populated")
	}

	got, err := store.GetUserPlaylist(pl.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v / %v", got, err)
	}
	if got.Name != "Morning Commute" {
		t.Errorf("Name = %q", got.Name)
	}
	if len(got.BookIDs) != 3 || got.BookIDs[1] != "b2" {
		t.Errorf("BookIDs = %v", got.BookIDs)
	}
}

func TestUserPlaylist_NameIndexUnique(t *testing.T) {
	store := newPlaylistTestStore(t)

	_, err := store.CreateUserPlaylist(&UserPlaylist{Name: "dupe", Type: UserPlaylistTypeStatic})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := store.CreateUserPlaylist(&UserPlaylist{Name: "dupe", Type: UserPlaylistTypeStatic}); err == nil {
		t.Error("expected duplicate-name rejection")
	}
	// Case-insensitive check.
	if _, err := store.CreateUserPlaylist(&UserPlaylist{Name: "DUPE", Type: UserPlaylistTypeStatic}); err == nil {
		t.Error("expected case-insensitive duplicate-name rejection")
	}
}

func TestUserPlaylist_GetByName(t *testing.T) {
	store := newPlaylistTestStore(t)

	pl, _ := store.CreateUserPlaylist(&UserPlaylist{Name: "My Faves", Type: UserPlaylistTypeSmart, Query: "tag:sci-fi"})

	// Case-insensitive lookup.
	got, err := store.GetUserPlaylistByName("my faves")
	if err != nil || got == nil {
		t.Fatalf("GetUserPlaylistByName: %v / %v", got, err)
	}
	if got.ID != pl.ID {
		t.Errorf("ID = %q, want %q", got.ID, pl.ID)
	}

	miss, err := store.GetUserPlaylistByName("does not exist")
	if err != nil {
		t.Errorf("miss should not error: %v", err)
	}
	if miss != nil {
		t.Errorf("miss should be nil")
	}
}

func TestUserPlaylist_GetByITunesPID(t *testing.T) {
	store := newPlaylistTestStore(t)

	pl, _ := store.CreateUserPlaylist(&UserPlaylist{
		Name: "Imported", Type: UserPlaylistTypeStatic, ITunesPersistentID: "ABCDEF0123456789",
	})

	got, err := store.GetUserPlaylistByITunesPID("ABCDEF0123456789")
	if err != nil || got == nil {
		t.Fatalf("GetUserPlaylistByITunesPID: %v / %v", got, err)
	}
	if got.ID != pl.ID {
		t.Errorf("ID = %q, want %q", got.ID, pl.ID)
	}

	miss, _ := store.GetUserPlaylistByITunesPID("NOTINLIBRARY")
	if miss != nil {
		t.Errorf("miss should be nil, got %v", miss)
	}
}

func TestUserPlaylist_ListFilterByType(t *testing.T) {
	store := newPlaylistTestStore(t)

	for _, name := range []string{"s1", "s2", "s3"} {
		_, _ = store.CreateUserPlaylist(&UserPlaylist{Name: name, Type: UserPlaylistTypeStatic})
	}
	for _, name := range []string{"smart-1", "smart-2"} {
		_, _ = store.CreateUserPlaylist(&UserPlaylist{Name: name, Type: UserPlaylistTypeSmart, Query: "*"})
	}

	all, total, err := store.ListUserPlaylists("", 100, 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if total != 5 || len(all) != 5 {
		t.Errorf("list all: got %d/%d, want 5/5", len(all), total)
	}

	static, sTotal, _ := store.ListUserPlaylists(UserPlaylistTypeStatic, 100, 0)
	if sTotal != 3 || len(static) != 3 {
		t.Errorf("list static: got %d/%d, want 3/3", len(static), sTotal)
	}

	smart, smTotal, _ := store.ListUserPlaylists(UserPlaylistTypeSmart, 100, 0)
	if smTotal != 2 || len(smart) != 2 {
		t.Errorf("list smart: got %d/%d, want 2/2", len(smart), smTotal)
	}
}

func TestUserPlaylist_ListPagination(t *testing.T) {
	store := newPlaylistTestStore(t)

	for i := range 5 {
		_, _ = store.CreateUserPlaylist(&UserPlaylist{
			Name: string(rune('a'+i)) + "-list", Type: UserPlaylistTypeStatic,
		})
	}

	page1, total, _ := store.ListUserPlaylists("", 2, 0)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(page1) != 2 {
		t.Errorf("page1 len = %d, want 2", len(page1))
	}

	page2, _, _ := store.ListUserPlaylists("", 2, 2)
	if len(page2) != 2 {
		t.Errorf("page2 len = %d, want 2", len(page2))
	}
	if page1[0].ID == page2[0].ID {
		t.Error("pages should not overlap")
	}
}

func TestUserPlaylist_UpdateBumpsVersionAndReindexes(t *testing.T) {
	store := newPlaylistTestStore(t)

	pl, _ := store.CreateUserPlaylist(&UserPlaylist{Name: "original", Type: UserPlaylistTypeStatic})

	pl.Name = "renamed"
	pl.BookIDs = []string{"b1", "b2"}
	if err := store.UpdateUserPlaylist(pl); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := store.GetUserPlaylist(pl.ID)
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
	if got.Name != "renamed" {
		t.Errorf("Name = %q", got.Name)
	}

	// Old name no longer indexed.
	miss, _ := store.GetUserPlaylistByName("original")
	if miss != nil {
		t.Errorf("old name still indexed: %v", miss)
	}

	// New name indexed.
	byNew, _ := store.GetUserPlaylistByName("renamed")
	if byNew == nil || byNew.ID != pl.ID {
		t.Errorf("new name lookup failed")
	}
}

func TestUserPlaylist_Delete(t *testing.T) {
	store := newPlaylistTestStore(t)

	pl, _ := store.CreateUserPlaylist(&UserPlaylist{
		Name: "to-delete", Type: UserPlaylistTypeStatic, ITunesPersistentID: "PID123",
	})
	if err := store.DeleteUserPlaylist(pl.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, _ := store.GetUserPlaylist(pl.ID)
	if got != nil {
		t.Errorf("playlist still present after delete: %v", got)
	}
	byName, _ := store.GetUserPlaylistByName("to-delete")
	if byName != nil {
		t.Error("name index not cleared")
	}
	byPID, _ := store.GetUserPlaylistByITunesPID("PID123")
	if byPID != nil {
		t.Error("itunes PID index not cleared")
	}

	// Idempotent.
	if err := store.DeleteUserPlaylist(pl.ID); err != nil {
		t.Errorf("second delete should be no-op: %v", err)
	}
}

func TestUserPlaylist_DirtyTracking(t *testing.T) {
	store := newPlaylistTestStore(t)

	clean, _ := store.CreateUserPlaylist(&UserPlaylist{Name: "clean", Type: UserPlaylistTypeStatic})
	dirty1, _ := store.CreateUserPlaylist(&UserPlaylist{Name: "dirty1", Type: UserPlaylistTypeStatic, Dirty: true})
	dirty2, _ := store.CreateUserPlaylist(&UserPlaylist{Name: "dirty2", Type: UserPlaylistTypeSmart, Query: "*", Dirty: true})

	dirties, err := store.ListDirtyUserPlaylists()
	if err != nil {
		t.Fatalf("list dirty: %v", err)
	}
	if len(dirties) != 2 {
		t.Errorf("dirty count = %d, want 2", len(dirties))
	}
	seen := map[string]bool{}
	for _, d := range dirties {
		seen[d.ID] = true
	}
	if !seen[dirty1.ID] || !seen[dirty2.ID] {
		t.Errorf("expected %q and %q in dirty list", dirty1.ID, dirty2.ID)
	}
	if seen[clean.ID] {
		t.Error("clean playlist should not be in dirty list")
	}

	// Clearing dirty via update removes from dirty index.
	dirty1.Dirty = false
	if err := store.UpdateUserPlaylist(dirty1); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := store.ListDirtyUserPlaylists()
	if len(after) != 1 {
		t.Errorf("dirty count after clear = %d, want 1", len(after))
	}
}
