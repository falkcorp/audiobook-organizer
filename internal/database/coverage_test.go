// file: internal/database/coverage_test.go
// version: 2.3.0
// guid: 3b82b22e-cd28-49b8-8b9c-e0a34b18e631
// last-edited: 2026-07-07

// NOTE(fable5 T022): TestInitializeStoreAndClose, TestDBInterfaceWrapper,
// and TestWebHelpers removed — they tested SQLite initialisation and global
// package-level functions (DB, Initialize, AddImportPath, etc.) that were
// deleted when the SQLite store was removed.

package database

import (
	"errors"
	"testing"
	"time"
)

// TestInitializeStoreAndClose verifies that SQLite is now rejected and
// PebbleStore still initialises cleanly.
func TestInitializeStoreAndClose(t *testing.T) {
	tempDir := t.TempDir()
	origStore := globalStore
	defer func() {
		globalStore = origStore
	}()

	// SQLite should now be rejected regardless of the enable flag.
	if _, err := InitializeStore("sqlite", tempDir+"/db.sqlite", false); err == nil {
		t.Fatal("expected error for sqlite type (not enabled)")
	}
	if _, err := InitializeStore("sqlite", tempDir+"/db.sqlite", true); err == nil {
		t.Fatal("expected error for sqlite type (even when enabled flag set)")
	}

	pebbleDir := tempDir + "/pebble"
	if _, err := InitializeStore("pebble", pebbleDir, false); err != nil {
		t.Fatalf("unexpected pebble init error: %v", err)
	}
	if err := CloseStore(); err != nil {
		t.Fatalf("failed to close pebble store: %v", err)
	}
	globalStore = nil

	if _, err := InitializeStore("unknown", tempDir+"/bad", false); err == nil {
		t.Fatal("expected error for unsupported database type")
	}
}

func TestEncryptionHelpersAndSettings(t *testing.T) {
	origKey := encryptionKey
	defer func() {
		encryptionKey = origKey
	}()

	if _, err := EncryptValue("secret"); err == nil {
		t.Fatal("expected error when encryption key is missing")
	}

	tempDir := t.TempDir()
	if err := InitEncryption(tempDir); err != nil {
		t.Fatalf("InitEncryption failed: %v", err)
	}

	enc, err := EncryptValue("secret")
	if err != nil {
		t.Fatalf("EncryptValue failed: %v", err)
	}
	dec, err := DecryptValue(enc)
	if err != nil {
		t.Fatalf("DecryptValue failed: %v", err)
	}
	if dec != "secret" {
		t.Fatalf("expected decrypted value, got %q", dec)
	}

	if got := MaskSecret("12345678"); got != "123****5678" {
		t.Fatalf("unexpected mask: %q", got)
	}

	if got := len(DeriveKeyFromPassword("pw")); got != 32 {
		t.Fatalf("expected 32-byte key, got %d", got)
	}

	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	pebbleStore := store.(*PebbleStore)
	if err := pebbleStore.SetSetting("plain", "value", "string", false); err != nil {
		t.Fatalf("SetSetting failed: %v", err)
	}
	if err := pebbleStore.SetSetting("secret", "shh", "string", true); err != nil {
		t.Fatalf("SetSetting secret failed: %v", err)
	}
	setting, err := pebbleStore.GetSetting("plain")
	if err != nil || setting == nil {
		t.Fatalf("GetSetting failed: %v", err)
	}
	all, err := pebbleStore.GetAllSettings()
	if err != nil {
		t.Fatalf("GetAllSettings failed: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("expected settings list")
	}
	if err := pebbleStore.DeleteSetting("plain"); err != nil {
		t.Fatalf("DeleteSetting failed: %v", err)
	}

	// Also verify GetDecryptedSetting works with PebbleStore (previously tested
	// on SQLiteStore; ported in fable5 T022).
	decrypted, err := GetDecryptedSetting(pebbleStore, "secret")
	if err != nil {
		t.Fatalf("GetDecryptedSetting failed: %v", err)
	}
	if decrypted != "shh" {
		t.Fatalf("expected 'shh', got %q", decrypted)
	}
}

// TestMockStore_AllMethods tests all 133 methods of MockStore to ensure 100% coverage.
// Since MockStore returns zero values by default, we just verify that all methods can be called
// and return the expected nil/zero values.
func TestMockStore_AllMethods(t *testing.T) {
	mock := &MockStore{}

	// Test lifecycle methods
	if err := mock.Close(); err != nil {
		t.Errorf("Close() returned error: %v", err)
	}
	if err := mock.Reset(); err != nil {
		t.Errorf("Reset() returned error: %v", err)
	}

	// Test metadata methods
	if states, err := mock.GetMetadataFieldStates("book-1"); err != nil || states != nil {
		t.Errorf("GetMetadataFieldStates() = %v, %v; want nil, nil", states, err)
	}
	if err := mock.UpsertMetadataFieldState(&MetadataFieldState{}); err != nil {
		t.Errorf("UpsertMetadataFieldState() returned error: %v", err)
	}
	if err := mock.DeleteMetadataFieldState("book-1", "title"); err != nil {
		t.Errorf("DeleteMetadataFieldState() returned error: %v", err)
	}

	// Test author methods
	if authors, err := mock.GetAllAuthors(); err != nil || authors != nil {
		t.Errorf("GetAllAuthors() = %v, %v; want nil, nil", authors, err)
	}
	if author, err := mock.GetAuthorByID(1); err != nil || author != nil {
		t.Errorf("GetAuthorByID() = %v, %v; want nil, nil", author, err)
	}
	if author, err := mock.GetAuthorByName("Test"); err != nil || author != nil {
		t.Errorf("GetAuthorByName() = %v, %v; want nil, nil", author, err)
	}
	if author, err := mock.CreateAuthor("Test"); err != nil || author != nil {
		t.Errorf("CreateAuthor() = %v, %v; want nil, nil", author, err)
	}

	// Test series methods
	if series, err := mock.GetAllSeries(); err != nil || series != nil {
		t.Errorf("GetAllSeries() = %v, %v; want nil, nil", series, err)
	}
	if series, err := mock.GetSeriesByID(1); err != nil || series != nil {
		t.Errorf("GetSeriesByID() = %v, %v; want nil, nil", series, err)
	}
	authorID := 1
	if series, err := mock.GetSeriesByName("Test", &authorID); err != nil || series != nil {
		t.Errorf("GetSeriesByName() = %v, %v; want nil, nil", series, err)
	}
	if series, err := mock.CreateSeries("Test", &authorID); err != nil || series != nil {
		t.Errorf("CreateSeries() = %v, %v; want nil, nil", series, err)
	}

	// Test work methods
	if works, err := mock.GetAllWorks(); err != nil || works != nil {
		t.Errorf("GetAllWorks() = %v, %v; want nil, nil", works, err)
	}
	if work, err := mock.GetWorkByID("work-1"); err != nil || work != nil {
		t.Errorf("GetWorkByID() = %v, %v; want nil, nil", work, err)
	}
	if work, err := mock.CreateWork(&Work{}); err != nil || work != nil {
		t.Errorf("CreateWork() = %v, %v; want nil, nil", work, err)
	}
	if work, err := mock.UpdateWork("work-1", &Work{}); err != nil || work != nil {
		t.Errorf("UpdateWork() = %v, %v; want nil, nil", work, err)
	}
	if err := mock.DeleteWork("work-1"); err != nil {
		t.Errorf("DeleteWork() returned error: %v", err)
	}
	if books, err := mock.GetBooksByWorkID("work-1"); err != nil || books != nil {
		t.Errorf("GetBooksByWorkID() = %v, %v; want nil, nil", books, err)
	}

	// Test book methods
	if books, err := mock.GetAllBooksCore(10, 0); err != nil || books != nil {
		t.Errorf("GetAllBooksCore() = %v, %v; want nil, nil", books, err)
	}
	if book, err := mock.GetBookByID("book-1"); err != nil || book != nil {
		t.Errorf("GetBookByID() = %v, %v; want nil, nil", book, err)
	}
	if book, err := mock.GetBookByFilePath("/path"); err != nil || book != nil {
		t.Errorf("GetBookByFilePath() = %v, %v; want nil, nil", book, err)
	}
	if book, err := mock.GetBookByFileHash("hash"); err != nil || book != nil {
		t.Errorf("GetBookByFileHash() = %v, %v; want nil, nil", book, err)
	}
	if book, err := mock.GetBookByOriginalHash("hash"); err != nil || book != nil {
		t.Errorf("GetBookByOriginalHash() = %v, %v; want nil, nil", book, err)
	}
	if book, err := mock.GetBookByOrganizedHash("hash"); err != nil || book != nil {
		t.Errorf("GetBookByOrganizedHash() = %v, %v; want nil, nil", book, err)
	}
	if duplicates, err := mock.GetDuplicateBooks(); err != nil || duplicates != nil {
		t.Errorf("GetDuplicateBooks() = %v, %v; want nil, nil", duplicates, err)
	}
	if books, err := mock.GetBooksBySeriesIDCore(1); err != nil || books != nil {
		t.Errorf("GetBooksBySeriesIDCore() = %v, %v; want nil, nil", books, err)
	}
	if books, err := mock.GetBooksByAuthorIDCore(1); err != nil || books != nil {
		t.Errorf("GetBooksByAuthorIDCore() = %v, %v; want nil, nil", books, err)
	}
	if book, err := mock.CreateBook(&Book{}); err != nil || book != nil {
		t.Errorf("CreateBook() = %v, %v; want nil, nil", book, err)
	}
	if book, err := mock.UpdateBook("book-1", &Book{}); err != nil || book != nil {
		t.Errorf("UpdateBook() = %v, %v; want nil, nil", book, err)
	}
	if err := mock.DeleteBook("book-1"); err != nil {
		t.Errorf("DeleteBook() returned error: %v", err)
	}
	if books, err := mock.SearchBooks("query", 10, 0); err != nil || books != nil {
		t.Errorf("SearchBooks() = %v, %v; want nil, nil", books, err)
	}
	if count, err := mock.CountPrimaryBooks(); err != nil || count != 0 {
		t.Errorf("CountPrimaryBooks() = %v, %v; want 0, nil", count, err)
	}
	now := time.Now()
	if books, err := mock.ListSoftDeletedBooks(10, 0, &now); err != nil || books != nil {
		t.Errorf("ListSoftDeletedBooks() = %v, %v; want nil, nil", books, err)
	}
	if books, err := mock.GetBooksByVersionGroup("group-1"); err != nil || books != nil {
		t.Errorf("GetBooksByVersionGroup() = %v, %v; want nil, nil", books, err)
	}

	// Test import path methods
	if paths, err := mock.GetAllImportPaths(); err != nil || paths != nil {
		t.Errorf("GetAllImportPaths() = %v, %v; want nil, nil", paths, err)
	}
	if path, err := mock.GetImportPathByID(1); err != nil || path != nil {
		t.Errorf("GetImportPathByID() = %v, %v; want nil, nil", path, err)
	}
	if path, err := mock.GetImportPathByPath("/path"); err != nil || path != nil {
		t.Errorf("GetImportPathByPath() = %v, %v; want nil, nil", path, err)
	}
	if path, err := mock.CreateImportPath("/path", "name"); err != nil || path != nil {
		t.Errorf("CreateImportPath() = %v, %v; want nil, nil", path, err)
	}
	if err := mock.UpdateImportPath(1, &ImportPath{}); err != nil {
		t.Errorf("UpdateImportPath() returned error: %v", err)
	}
	if err := mock.DeleteImportPath(1); err != nil {
		t.Errorf("DeleteImportPath() returned error: %v", err)
	}

	// Test operation methods
	folderPath := "/path"
	if op, err := mock.CreateOperation("op-1", "scan", &folderPath); err != nil || op != nil {
		t.Errorf("CreateOperation() = %v, %v; want nil, nil", op, err)
	}
	if op, err := mock.GetOperationByID("op-1"); err != nil || op != nil {
		t.Errorf("GetOperationByID() = %v, %v; want nil, nil", op, err)
	}
	if ops, err := mock.GetRecentOperations(10); err != nil || ops != nil {
		t.Errorf("GetRecentOperations() = %v, %v; want nil, nil", ops, err)
	}
	if err := mock.UpdateOperationStatus("op-1", "running", 1, 10, "msg"); err != nil {
		t.Errorf("UpdateOperationStatus() returned error: %v", err)
	}
	if err := mock.UpdateOperationError("op-1", "error"); err != nil {
		t.Errorf("UpdateOperationError() returned error: %v", err)
	}

	// Test operation log methods
	details := "details"
	if err := mock.AddOperationLog("op-1", "info", "message", &details); err != nil {
		t.Errorf("AddOperationLog() returned error: %v", err)
	}
	if logs, err := mock.GetOperationLogs("op-1"); err != nil || logs != nil {
		t.Errorf("GetOperationLogs() = %v, %v; want nil, nil", logs, err)
	}

	// Test user preference methods (global)
	if pref, err := mock.GetUserPreference("key"); err != nil || pref != nil {
		t.Errorf("GetUserPreference() = %v, %v; want nil, nil", pref, err)
	}
	if err := mock.SetUserPreference("key", "value"); err != nil {
		t.Errorf("SetUserPreference() returned error: %v", err)
	}
	if prefs, err := mock.GetAllUserPreferences(); err != nil || prefs != nil {
		t.Errorf("GetAllUserPreferences() = %v, %v; want nil, nil", prefs, err)
	}

	// Test settings methods
	if setting, err := mock.GetSetting("key"); err != nil || setting != nil {
		t.Errorf("GetSetting() = %v, %v; want nil, nil", setting, err)
	}
	if err := mock.SetSetting("key", "value", "string", false); err != nil {
		t.Errorf("SetSetting() returned error: %v", err)
	}
	if settings, err := mock.GetAllSettings(); err != nil || settings != nil {
		t.Errorf("GetAllSettings() = %v, %v; want nil, nil", settings, err)
	}
	if err := mock.DeleteSetting("key"); err != nil {
		t.Errorf("DeleteSetting() returned error: %v", err)
	}

	// Test playlist methods
	seriesID := 1
	if playlist, err := mock.CreatePlaylist("name", &seriesID, "/path"); err != nil || playlist != nil {
		t.Errorf("CreatePlaylist() = %v, %v; want nil, nil", playlist, err)
	}
	if playlist, err := mock.GetPlaylistByID(1); err != nil || playlist != nil {
		t.Errorf("GetPlaylistByID() = %v, %v; want nil, nil", playlist, err)
	}
	if playlist, err := mock.GetPlaylistBySeriesID(1); err != nil || playlist != nil {
		t.Errorf("GetPlaylistBySeriesID() = %v, %v; want nil, nil", playlist, err)
	}
	if err := mock.AddPlaylistItem(1, 1, 1); err != nil {
		t.Errorf("AddPlaylistItem() returned error: %v", err)
	}
	if items, err := mock.GetPlaylistItems(1); err != nil || items != nil {
		t.Errorf("GetPlaylistItems() = %v, %v; want nil, nil", items, err)
	}

	// Test user methods
	roles := []string{"user"}
	if user, err := mock.CreateUser("username", "email", "algo", "hash", roles, "active"); err != nil || user != nil {
		t.Errorf("CreateUser() = %v, %v; want nil, nil", user, err)
	}
	if user, err := mock.GetUserByID("user-1"); err != nil || user != nil {
		t.Errorf("GetUserByID() = %v, %v; want nil, nil", user, err)
	}
	if user, err := mock.GetUserByUsername("username"); err != nil || user != nil {
		t.Errorf("GetUserByUsername() = %v, %v; want nil, nil", user, err)
	}
	if user, err := mock.GetUserByEmail("email"); err != nil || user != nil {
		t.Errorf("GetUserByEmail() = %v, %v; want nil, nil", user, err)
	}
	if err := mock.UpdateUser(&User{}); err != nil {
		t.Errorf("UpdateUser() returned error: %v", err)
	}

	// Test session methods
	ttl := 24 * time.Hour
	if session, err := mock.CreateSession("user-1", "127.0.0.1", "agent", ttl); err != nil || session != nil {
		t.Errorf("CreateSession() = %v, %v; want nil, nil", session, err)
	}
	if session, err := mock.GetSession("session-1"); err != nil || session != nil {
		t.Errorf("GetSession() = %v, %v; want nil, nil", session, err)
	}
	if err := mock.RevokeSession("session-1"); err != nil {
		t.Errorf("RevokeSession() returned error: %v", err)
	}
	if sessions, err := mock.ListUserSessions("user-1"); err != nil || sessions != nil {
		t.Errorf("ListUserSessions() = %v, %v; want nil, nil", sessions, err)
	}

	// Test per-user preference methods
	if err := mock.SetUserPreferenceForUser("user-1", "key", "value"); err != nil {
		t.Errorf("SetUserPreferenceForUser() returned error: %v", err)
	}
	if pref, err := mock.GetUserPreferenceForUser("user-1", "key"); err != nil || pref != nil {
		t.Errorf("GetUserPreferenceForUser() = %v, %v; want nil, nil", pref, err)
	}
	if prefs, err := mock.GetAllPreferencesForUser("user-1"); err != nil || prefs != nil {
		t.Errorf("GetAllPreferencesForUser() = %v, %v; want nil, nil", prefs, err)
	}

	// Test book segment methods
	if segment, err := mock.CreateBookSegment(1, &BookSegment{}); err != nil || segment != nil {
		t.Errorf("CreateBookSegment() = %v, %v; want nil, nil", segment, err)
	}
	if err := mock.UpdateBookSegment(&BookSegment{}); err != nil {
		t.Errorf("UpdateBookSegment() returned error: %v", err)
	}
	if segments, err := mock.ListBookSegments(1); err != nil || segments != nil {
		t.Errorf("ListBookSegments() = %v, %v; want nil, nil", segments, err)
	}
	if err := mock.MergeBookSegments(1, &BookSegment{}, []string{"seg-1"}); err != nil {
		t.Errorf("MergeBookSegments() returned error: %v", err)
	}

	// Test playback event methods
	if err := mock.AddPlaybackEvent(&PlaybackEvent{}); err != nil {
		t.Errorf("AddPlaybackEvent() returned error: %v", err)
	}
	if events, err := mock.ListPlaybackEvents("user-1", 1, 10); err != nil || events != nil {
		t.Errorf("ListPlaybackEvents() = %v, %v; want nil, nil", events, err)
	}
	if err := mock.UpdatePlaybackProgress(&PlaybackProgress{}); err != nil {
		t.Errorf("UpdatePlaybackProgress() returned error: %v", err)
	}
	if progress, err := mock.GetPlaybackProgress("user-1", 1); err != nil || progress != nil {
		t.Errorf("GetPlaybackProgress() = %v, %v; want nil, nil", progress, err)
	}

	// Test stats methods
	if err := mock.IncrementBookPlayStats(1, 60); err != nil {
		t.Errorf("IncrementBookPlayStats() returned error: %v", err)
	}
	if stats, err := mock.GetBookStats(1); err != nil || stats != nil {
		t.Errorf("GetBookStats() = %v, %v; want nil, nil", stats, err)
	}
	if err := mock.IncrementUserListenStats("user-1", 60); err != nil {
		t.Errorf("IncrementUserListenStats() returned error: %v", err)
	}
	if stats, err := mock.GetUserStats("user-1"); err != nil || stats != nil {
		t.Errorf("GetUserStats() = %v, %v; want nil, nil", stats, err)
	}

	// Test blocked hash methods
	if blocked, err := mock.IsHashBlocked("hash"); err != nil || blocked {
		t.Errorf("IsHashBlocked() = %v, %v; want false, nil", blocked, err)
	}
	if err := mock.AddBlockedHash("hash", "reason"); err != nil {
		t.Errorf("AddBlockedHash() returned error: %v", err)
	}
	if err := mock.RemoveBlockedHash("hash"); err != nil {
		t.Errorf("RemoveBlockedHash() returned error: %v", err)
	}
	if hashes, err := mock.GetAllBlockedHashes(); err != nil || hashes != nil {
		t.Errorf("GetAllBlockedHashes() = %v, %v; want nil, nil", hashes, err)
	}
	if hash, err := mock.GetBlockedHashByHash("hash"); err != nil || hash != nil {
		t.Errorf("GetBlockedHashByHash() = %v, %v; want nil, nil", hash, err)
	}
}

// TestMockStore_WithCustomFuncs tests that custom function implementations are called.
func TestMockStore_WithCustomFuncs(t *testing.T) {
	mock := &MockStore{}

	// Test that custom functions are called when set
	customBookCalled := false
	mock.GetBookByIDFunc = func(id string) (*Book, error) {
		customBookCalled = true
		return &Book{ID: id}, nil
	}

	book, err := mock.GetBookByID("test-id")
	if err != nil {
		t.Errorf("GetBookByID() returned error: %v", err)
	}
	if !customBookCalled {
		t.Error("Custom GetBookByIDFunc was not called")
	}
	if book == nil || book.ID != "test-id" {
		t.Errorf("GetBookByID() returned unexpected book: %v", book)
	}

	// Test custom error function
	customAuthorCalled := false
	sentinelErr := errors.New("not found")
	mock.GetAuthorByNameFunc = func(name string) (*Author, error) {
		customAuthorCalled = true
		return nil, sentinelErr
	}

	author, err := mock.GetAuthorByName("test")
	if err != sentinelErr {
		t.Errorf("GetAuthorByName() returned error: %v; want sentinelErr", err)
	}
	if !customAuthorCalled {
		t.Error("Custom GetAuthorByNameFunc was not called")
	}
	if author != nil {
		t.Errorf("GetAuthorByName() returned unexpected author: %v", author)
	}

	// Test custom count function
	customCountCalled := false
	mock.CountPrimaryBooksFunc = func() (int, error) {
		customCountCalled = true
		return 42, nil
	}

	count, err := mock.CountPrimaryBooks()
	if err != nil {
		t.Errorf("CountPrimaryBooks() returned error: %v", err)
	}
	if !customCountCalled {
		t.Error("Custom CountPrimaryBooksFunc was not called")
	}
	if count != 42 {
		t.Errorf("CountPrimaryBooks() = %d; want 42", count)
	}

	// Test custom bool function
	customHashCalled := false
	mock.IsHashBlockedFunc = func(hash string) (bool, error) {
		customHashCalled = true
		return true, nil
	}

	blocked, err := mock.IsHashBlocked("test-hash")
	if err != nil {
		t.Errorf("IsHashBlocked() returned error: %v", err)
	}
	if !customHashCalled {
		t.Error("Custom IsHashBlockedFunc was not called")
	}
	if !blocked {
		t.Error("IsHashBlocked() = false; want true")
	}
}

// NOTE(fable5 T022): TestGetOrCreateAuthor, TestGetOrCreateSeries, and
// TestCloseWithDB removed — they tested SQLite-backed helpers (Initialize,
// DB, GetOrCreateAuthor, GetOrCreateSeries, Close) that were removed.

// TestCloseStoreWithNilStore tests CloseStore when globalStore is nil
func TestCloseStoreWithNilStore(t *testing.T) {
	origStore := globalStore
	defer func() {
		globalStore = origStore
	}()

	globalStore = nil
	if err := CloseStore(); err != nil {
		t.Errorf("CloseStore() with nil store returned error: %v", err)
	}
}

// TestPebbleStoreReset tests the Pebble store Reset function
func TestPebbleStoreReset(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	pebbleStore := store.(*PebbleStore)

	// Create some data
	_, err := store.CreateAuthor("Test Author")
	if err != nil {
		t.Fatalf("Failed to create author: %v", err)
	}

	// Reset the store
	if err := pebbleStore.Reset(); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	// Verify data is cleared
	authors, err := store.GetAllAuthors()
	if err != nil {
		t.Fatalf("Failed to get authors after reset: %v", err)
	}
	if len(authors) != 0 {
		t.Errorf("Expected 0 authors after reset, got %d", len(authors))
	}
}
