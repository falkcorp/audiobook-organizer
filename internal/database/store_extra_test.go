// file: internal/database/store_extra_test.go
// version: 2.2.0
// guid: 68b2b2f9-2b8f-4f7f-9d8f-26e6306a3c8e
// last-edited: 2026-07-06

// NOTE(fable5 T022): SQLiteStore type assertions replaced with PebbleStore;
// TestSQLiteExtendedFeatures renamed to TestPebbleExtendedFeatures.

package database

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreAdditionalCoverageSQLite(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	if err := RunMigrations(store); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	exerciseStoreCommon(t, store)
}

func TestStoreAdditionalCoveragePebble(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	exerciseStoreCommon(t, store)
	exercisePebbleAdvanced(t, store.(*PebbleStore))
}

func TestPebbleExtendedFeatures(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	sqliteStore := store.(*PebbleStore)

	// ---- User Management ----
	user, err := sqliteStore.CreateUser("testuser", "test@example.com", "bcrypt", "hashvalue", []string{"user", "admin"}, "active")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if user.Username != "testuser" || user.Email != "test@example.com" {
		t.Fatalf("CreateUser returned wrong data: %+v", user)
	}

	fetched, err := sqliteStore.GetUserByID(user.ID)
	if err != nil || fetched == nil || fetched.Username != "testuser" {
		t.Fatalf("GetUserByID failed: err=%v user=%+v", err, fetched)
	}
	fetched, err = sqliteStore.GetUserByUsername("testuser")
	if err != nil || fetched == nil || fetched.Email != "test@example.com" {
		t.Fatalf("GetUserByUsername failed: err=%v user=%+v", err, fetched)
	}
	fetched, err = sqliteStore.GetUserByEmail("test@example.com")
	if err != nil || fetched == nil || fetched.ID != user.ID {
		t.Fatalf("GetUserByEmail failed: err=%v user=%+v", err, fetched)
	}

	// Missing user returns nil, nil
	missing, err := sqliteStore.GetUserByID("nonexistent")
	if err != nil || missing != nil {
		t.Fatalf("GetUserByID(missing) should return nil,nil: err=%v user=%+v", err, missing)
	}

	fetched.Status = "disabled"
	if err := sqliteStore.UpdateUser(fetched); err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	updated, _ := sqliteStore.GetUserByID(fetched.ID)
	if updated.Status != "disabled" {
		t.Fatalf("UpdateUser didn't persist: status=%s", updated.Status)
	}

	// ---- Sessions ----
	sess, err := sqliteStore.CreateSession(user.ID, "127.0.0.1", "TestAgent/1.0", time.Hour)
	if err != nil || sess == nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if sess.UserID != user.ID || sess.IP != "127.0.0.1" {
		t.Fatalf("CreateSession wrong data: %+v", sess)
	}

	gotSess, err := sqliteStore.GetSession(sess.ID)
	if err != nil || gotSess == nil || gotSess.Revoked {
		t.Fatalf("GetSession failed: err=%v sess=%+v", err, gotSess)
	}

	missingSess, err := sqliteStore.GetSession("nonexistent")
	if err != nil || missingSess != nil {
		t.Fatalf("GetSession(missing) should return nil,nil")
	}

	sessions, err := sqliteStore.ListUserSessions(user.ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("ListUserSessions: expected 1, got %d", len(sessions))
	}

	if err := sqliteStore.RevokeSession(sess.ID); err != nil {
		t.Fatalf("RevokeSession failed: %v", err)
	}
	revoked, _ := sqliteStore.GetSession(sess.ID)
	if !revoked.Revoked {
		t.Fatal("RevokeSession didn't set revoked flag")
	}

	// ---- Per-User Preferences ----
	if err := sqliteStore.SetUserPreferenceForUser(user.ID, "theme", "dark"); err != nil {
		t.Fatalf("SetUserPreferenceForUser failed: %v", err)
	}
	pref, err := sqliteStore.GetUserPreferenceForUser(user.ID, "theme")
	if err != nil || pref == nil || pref.Value != "dark" {
		t.Fatalf("GetUserPreferenceForUser failed: err=%v pref=%+v", err, pref)
	}
	missingPref, err := sqliteStore.GetUserPreferenceForUser(user.ID, "nonexistent")
	if err != nil || missingPref != nil {
		t.Fatalf("GetUserPreferenceForUser(missing) should return nil,nil")
	}

	if err := sqliteStore.SetUserPreferenceForUser(user.ID, "lang", "en"); err != nil {
		t.Fatalf("SetUserPreferenceForUser(lang) failed: %v", err)
	}
	allPrefs, err := sqliteStore.GetAllPreferencesForUser(user.ID)
	if err != nil || len(allPrefs) != 2 {
		t.Fatalf("GetAllPreferencesForUser: expected 2, got %d", len(allPrefs))
	}

	// ---- Book Segments ----
	seg, err := sqliteStore.CreateBookSegment(1, &BookSegment{FilePath: "/tmp/ch1.mp3", Format: "mp3", SizeBytes: 1024, DurationSec: 300, Active: true})
	if err != nil || seg == nil {
		t.Fatalf("CreateBookSegment failed: %v", err)
	}
	seg2, err := sqliteStore.CreateBookSegment(1, &BookSegment{FilePath: "/tmp/ch2.mp3", Format: "mp3", Active: true})
	if err != nil {
		t.Fatalf("CreateBookSegment(2) failed: %v", err)
	}

	segs, err := sqliteStore.ListBookSegments(1)
	if err != nil || len(segs) != 2 {
		t.Fatalf("ListBookSegments: expected 2, got %d", len(segs))
	}

	emptySegs, err := sqliteStore.ListBookSegments(999)
	if err != nil {
		t.Fatalf("ListBookSegments(empty) failed: %v", err)
	}
	if len(emptySegs) != 0 {
		t.Fatalf("ListBookSegments(empty): expected 0, got %d", len(emptySegs))
	}

	merged := &BookSegment{FilePath: "/tmp/merged.m4b", Format: "m4b", SizeBytes: 2048, DurationSec: 600, Active: true}
	if err := sqliteStore.MergeBookSegments(1, merged, []string{seg.ID, seg2.ID}); err != nil {
		t.Fatalf("MergeBookSegments failed: %v", err)
	}
	segsAfter, _ := sqliteStore.ListBookSegments(1)
	activeCount := 0
	for _, s := range segsAfter {
		if s.Active {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("MergeBookSegments: expected 1 active segment, got %d", activeCount)
	}

	// ---- Playback ----
	if err := sqliteStore.AddPlaybackEvent(&PlaybackEvent{UserID: "u1", BookID: 1, SegmentID: "s1", PositionSec: 120, EventType: "progress", PlaySpeed: 1.0}); err != nil {
		t.Fatalf("AddPlaybackEvent failed: %v", err)
	}
	events, err := sqliteStore.ListPlaybackEvents("u1", 1, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListPlaybackEvents: expected 1, got %d (err=%v)", len(events), err)
	}
	emptyEvents, err := sqliteStore.ListPlaybackEvents("u1", 999, 10)
	if err != nil || len(emptyEvents) != 0 {
		t.Fatalf("ListPlaybackEvents(empty): expected 0, got %d", len(emptyEvents))
	}

	if err := sqliteStore.UpdatePlaybackProgress(&PlaybackProgress{UserID: "u1", BookID: 1, SegmentID: "s1", PositionSec: 120, Percent: 0.5}); err != nil {
		t.Fatalf("UpdatePlaybackProgress failed: %v", err)
	}
	progress, err := sqliteStore.GetPlaybackProgress("u1", 1)
	if err != nil || progress == nil || progress.PositionSec != 120 {
		t.Fatalf("GetPlaybackProgress failed: err=%v progress=%+v", err, progress)
	}
	missingProgress, err := sqliteStore.GetPlaybackProgress("u1", 999)
	if err != nil || missingProgress != nil {
		t.Fatalf("GetPlaybackProgress(missing) should return nil,nil")
	}

	// Upsert progress
	if err := sqliteStore.UpdatePlaybackProgress(&PlaybackProgress{UserID: "u1", BookID: 1, SegmentID: "s1", PositionSec: 240, Percent: 0.8}); err != nil {
		t.Fatalf("UpdatePlaybackProgress(upsert) failed: %v", err)
	}
	updated2, _ := sqliteStore.GetPlaybackProgress("u1", 1)
	if updated2.PositionSec != 240 {
		t.Fatalf("UpdatePlaybackProgress upsert didn't update: got %d", updated2.PositionSec)
	}

	// ---- Stats ----
	if err := sqliteStore.IncrementBookPlayStats(1, 300); err != nil {
		t.Fatalf("IncrementBookPlayStats failed: %v", err)
	}
	bookStats, err := sqliteStore.GetBookStats(1)
	if err != nil || bookStats == nil || bookStats.PlayCount != 1 || bookStats.ListenSeconds != 300 {
		t.Fatalf("GetBookStats failed: err=%v stats=%+v", err, bookStats)
	}
	// Increment again
	sqliteStore.IncrementBookPlayStats(1, 100)
	bookStats, _ = sqliteStore.GetBookStats(1)
	if bookStats.PlayCount != 2 || bookStats.ListenSeconds != 400 {
		t.Fatalf("IncrementBookPlayStats(2nd): expected 2/400, got %d/%d", bookStats.PlayCount, bookStats.ListenSeconds)
	}
	missingStats, err := sqliteStore.GetBookStats(999)
	if err != nil || missingStats != nil {
		t.Fatalf("GetBookStats(missing) should return nil,nil")
	}

	if err := sqliteStore.IncrementUserListenStats("u1", 300); err != nil {
		t.Fatalf("IncrementUserListenStats failed: %v", err)
	}
	userStats, err := sqliteStore.GetUserStats("u1")
	if err != nil || userStats == nil || userStats.ListenSeconds != 300 {
		t.Fatalf("GetUserStats failed: err=%v stats=%+v", err, userStats)
	}
	sqliteStore.IncrementUserListenStats("u1", 100)
	userStats, _ = sqliteStore.GetUserStats("u1")
	if userStats.ListenSeconds != 400 {
		t.Fatalf("IncrementUserListenStats(2nd): expected 400, got %d", userStats.ListenSeconds)
	}
	missingUserStats, err := sqliteStore.GetUserStats("missing")
	if err != nil || missingUserStats != nil {
		t.Fatalf("GetUserStats(missing) should return nil,nil")
	}
}

func exerciseStoreCommon(t *testing.T, store Store) {
	t.Helper()

	author, err := store.CreateAuthor("Coverage Author")
	if err != nil {
		t.Fatalf("CreateAuthor failed: %v", err)
	}
	if _, err := store.GetAuthorByID(author.ID); err != nil {
		t.Fatalf("GetAuthorByID failed: %v", err)
	}
	if _, err := store.GetAuthorByName(author.Name); err != nil {
		t.Fatalf("GetAuthorByName failed: %v", err)
	}
	if _, err := store.GetAllAuthors(); err != nil {
		t.Fatalf("GetAllAuthors failed: %v", err)
	}

	series, err := store.CreateSeries("Coverage Series", &author.ID)
	if err != nil {
		t.Fatalf("CreateSeries failed: %v", err)
	}
	if _, err := store.GetSeriesByID(series.ID); err != nil {
		t.Fatalf("GetSeriesByID failed: %v", err)
	}
	if _, err := store.GetSeriesByName(series.Name, &author.ID); err != nil {
		t.Fatalf("GetSeriesByName failed: %v", err)
	}
	if _, err := store.GetAllSeries(); err != nil {
		t.Fatalf("GetAllSeries failed: %v", err)
	}

	work, err := store.CreateWork(&Work{Title: "Coverage Work"})
	if err != nil {
		t.Fatalf("CreateWork failed: %v", err)
	}
	if _, err := store.GetAllWorks(); err != nil {
		t.Fatalf("GetAllWorks failed: %v", err)
	}
	if _, err := store.GetWorkByID(work.ID); err != nil {
		t.Fatalf("GetWorkByID failed: %v", err)
	}
	if _, err := store.UpdateWork(work.ID, &Work{Title: "Coverage Work Updated"}); err != nil {
		t.Fatalf("UpdateWork failed: %v", err)
	}

	fileHash := "hash-1"
	origHash := "orig-1"
	orgHash := fileHash
	bookPath := filepath.Join(t.TempDir(), "book.mp3")
	book, err := store.CreateBook(&Book{
		Title:             "Coverage Book",
		AuthorID:          &author.ID,
		SeriesID:          &series.ID,
		WorkID:            &work.ID,
		FilePath:          bookPath,
		FileHash:          &fileHash,
		OriginalFileHash:  &origHash,
		OrganizedFileHash: &orgHash,
	})
	if err != nil {
		t.Fatalf("CreateBook failed: %v", err)
	}
	if _, err := store.GetBooksByWorkID(work.ID); err != nil {
		t.Fatalf("GetBooksByWorkID failed: %v", err)
	}
	if _, err := store.GetBookByFilePath(book.FilePath); err != nil {
		t.Fatalf("GetBookByFilePath failed: %v", err)
	}
	if _, err := store.GetBookByFileHash(fileHash); err != nil {
		t.Fatalf("GetBookByFileHash failed: %v", err)
	}
	if _, err := store.GetBookByOriginalHash(origHash); err != nil {
		t.Fatalf("GetBookByOriginalHash failed: %v", err)
	}
	if _, err := store.GetBookByOrganizedHash(orgHash); err != nil {
		t.Fatalf("GetBookByOrganizedHash failed: %v", err)
	}

	book2Path := filepath.Join(t.TempDir(), "book2.mp3")
	if _, err := store.CreateBook(&Book{
		Title:             "Coverage Book 2",
		FilePath:          book2Path,
		FileHash:          &fileHash,
		OrganizedFileHash: &orgHash,
	}); err != nil {
		t.Fatalf("CreateBook (dup) failed: %v", err)
	}
	if _, err := store.GetDuplicateBooks(); err != nil {
		t.Fatalf("GetDuplicateBooks failed: %v", err)
	}

	marked := true
	ts := time.Now().Add(-48 * time.Hour)
	book.MarkedForDeletion = &marked
	book.MarkedForDeletionAt = &ts
	if _, err := store.UpdateBook(book.ID, book); err != nil {
		t.Fatalf("UpdateBook (soft delete) failed: %v", err)
	}
	if books, err := store.ListSoftDeletedBooks(10, 0, nil); err != nil || len(books) == 0 {
		t.Fatalf("ListSoftDeletedBooks failed: %v", err)
	}
	if count, err := store.CountPrimaryBooks(); err != nil || count < 1 {
		t.Fatalf("CountPrimaryBooks failed: %v", err)
	}
	if _, err := store.GetBooksBySeriesIDCore(series.ID); err != nil {
		t.Fatalf("GetBooksBySeriesIDCore failed: %v", err)
	}
	if _, err := store.GetBooksByAuthorIDCore(author.ID); err != nil {
		t.Fatalf("GetBooksByAuthorIDCore failed: %v", err)
	}
	if _, err := store.SearchBooks("Coverage", 10, 0); err != nil {
		t.Fatalf("SearchBooks failed: %v", err)
	}

	importPath, err := store.CreateImportPath("/tmp/coverage", "Coverage Import")
	if err != nil {
		t.Fatalf("CreateImportPath failed: %v", err)
	}
	if _, err := store.GetImportPathByID(importPath.ID); err != nil {
		t.Fatalf("GetImportPathByID failed: %v", err)
	}
	if _, err := store.GetImportPathByPath(importPath.Path); err != nil {
		t.Fatalf("GetImportPathByPath failed: %v", err)
	}
	importPath.Enabled = false
	importPath.BookCount = 2
	if err := store.UpdateImportPath(importPath.ID, importPath); err != nil {
		t.Fatalf("UpdateImportPath failed: %v", err)
	}
	if _, err := store.GetAllImportPaths(); err != nil {
		t.Fatalf("GetAllImportPaths failed: %v", err)
	}
	if err := store.DeleteImportPath(importPath.ID); err != nil {
		t.Fatalf("DeleteImportPath failed: %v", err)
	}

	folderPath := "/tmp/coverage"
	op, err := store.CreateOperation("op-coverage", "scan", &folderPath)
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}
	if err := store.UpdateOperationStatus(op.ID, "running", 0, 10, "start"); err != nil {
		t.Fatalf("UpdateOperationStatus failed: %v", err)
	}
	if err := store.UpdateOperationStatus(op.ID, "completed", 10, 10, "done"); err != nil {
		t.Fatalf("UpdateOperationStatus completed failed: %v", err)
	}
	if err := store.UpdateOperationError(op.ID, "err"); err != nil {
		t.Fatalf("UpdateOperationError failed: %v", err)
	}
	if _, err := store.GetOperationByID(op.ID); err != nil {
		t.Fatalf("GetOperationByID failed: %v", err)
	}
	if _, err := store.GetRecentOperations(5); err != nil {
		t.Fatalf("GetRecentOperations failed: %v", err)
	}
	detail := "detail"
	if err := store.AddOperationLog(op.ID, "info", "message", &detail); err != nil {
		t.Fatalf("AddOperationLog failed: %v", err)
	}
	if _, err := store.GetOperationLogs(op.ID); err != nil {
		t.Fatalf("GetOperationLogs failed: %v", err)
	}

	state := &MetadataFieldState{
		BookID:         book.ID,
		Field:          "title",
		OverrideLocked: true,
		UpdatedAt:      time.Now(),
	}
	if err := store.UpsertMetadataFieldState(state); err != nil {
		t.Fatalf("UpsertMetadataFieldState failed: %v", err)
	}
	if _, err := store.GetMetadataFieldStates(book.ID); err != nil {
		t.Fatalf("GetMetadataFieldStates failed: %v", err)
	}
	if err := store.DeleteMetadataFieldState(book.ID, "title"); err != nil {
		t.Fatalf("DeleteMetadataFieldState failed: %v", err)
	}

	if err := store.SetUserPreference("theme", "dark"); err != nil {
		t.Fatalf("SetUserPreference failed: %v", err)
	}
	if _, err := store.GetUserPreference("theme"); err != nil {
		t.Fatalf("GetUserPreference failed: %v", err)
	}
	if _, err := store.GetAllUserPreferences(); err != nil {
		t.Fatalf("GetAllUserPreferences failed: %v", err)
	}

	playlist, err := store.CreatePlaylist("Coverage Playlist", &series.ID, "/tmp/list.m3u")
	if err != nil {
		t.Fatalf("CreatePlaylist failed: %v", err)
	}
	if _, err := store.GetPlaylistByID(playlist.ID); err != nil {
		t.Fatalf("GetPlaylistByID failed: %v", err)
	}
	if _, err := store.GetPlaylistBySeriesID(series.ID); err != nil {
		t.Fatalf("GetPlaylistBySeriesID failed: %v", err)
	}
	if err := store.AddPlaylistItem(playlist.ID, 1, 1); err != nil {
		t.Fatalf("AddPlaylistItem failed: %v", err)
	}
	if _, err := store.GetPlaylistItems(playlist.ID); err != nil {
		t.Fatalf("GetPlaylistItems failed: %v", err)
	}

	if blocked, err := store.IsHashBlocked("abc"); err != nil || blocked {
		t.Fatalf("IsHashBlocked failed: %v", err)
	}
	if err := store.AddBlockedHash("abc", "reason"); err != nil {
		t.Fatalf("AddBlockedHash failed: %v", err)
	}
	if _, err := store.GetBlockedHashByHash("abc"); err != nil {
		t.Fatalf("GetBlockedHashByHash failed: %v", err)
	}
	if _, err := store.GetAllBlockedHashes(); err != nil {
		t.Fatalf("GetAllBlockedHashes failed: %v", err)
	}
	if err := store.RemoveBlockedHash("abc"); err != nil {
		t.Fatalf("RemoveBlockedHash failed: %v", err)
	}

	if err := store.DeleteBook("missing-book"); err != nil {
		t.Fatalf("expected DeleteBook to ignore missing book: %v", err)
	}

	if err := store.DeleteWork(work.ID); err != nil {
		t.Fatalf("DeleteWork failed: %v", err)
	}
	if _, err := store.UpdateWork("missing-work", &Work{Title: "Missing"}); err == nil {
		t.Fatal("expected UpdateWork to fail for missing work")
	}
	if err := store.DeleteWork("missing-work"); err != nil {
		t.Fatalf("expected DeleteWork to ignore missing work: %v", err)
	}
}

func exercisePebbleAdvanced(t *testing.T, store *PebbleStore) {
	t.Helper()

	user, err := store.CreateUser("User", "user@example.com", "algo", "hash", []string{"admin"}, "active")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if _, err := store.GetUserByID(user.ID); err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if _, err := store.GetUserByUsername(user.Username); err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}
	if _, err := store.GetUserByEmail(user.Email); err != nil {
		t.Fatalf("GetUserByEmail failed: %v", err)
	}
	user.Status = "disabled"
	if err := store.UpdateUser(user); err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}

	sess, err := store.CreateSession(user.ID, "127.0.0.1", "agent", time.Minute)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if _, err := store.GetSession(sess.ID); err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if err := store.RevokeSession(sess.ID); err != nil {
		t.Fatalf("RevokeSession failed: %v", err)
	}
	if _, err := store.ListUserSessions(user.ID); err != nil {
		t.Fatalf("ListUserSessions failed: %v", err)
	}

	if err := store.SetUserPreferenceForUser(user.ID, "theme", "dark"); err != nil {
		t.Fatalf("SetUserPreferenceForUser failed: %v", err)
	}
	if _, err := store.GetUserPreferenceForUser(user.ID, "theme"); err != nil {
		t.Fatalf("GetUserPreferenceForUser failed: %v", err)
	}
	if _, err := store.GetAllPreferencesForUser(user.ID); err != nil {
		t.Fatalf("GetAllPreferencesForUser failed: %v", err)
	}

	segment, err := store.CreateBookSegment(1, &BookSegment{FilePath: "/tmp/segment.mp3", DurationSec: 60})
	if err != nil {
		t.Fatalf("CreateBookSegment failed: %v", err)
	}
	if _, err := store.ListBookSegments(1); err != nil {
		t.Fatalf("ListBookSegments failed: %v", err)
	}
	if err := store.MergeBookSegments(1, &BookSegment{FilePath: "/tmp/segment2.mp3", DurationSec: 40}, []string{segment.ID}); err != nil {
		t.Fatalf("MergeBookSegments failed: %v", err)
	}

	if err := store.AddPlaybackEvent(&PlaybackEvent{UserID: user.ID, BookID: 1}); err != nil {
		t.Fatalf("AddPlaybackEvent failed: %v", err)
	}
	if _, err := store.ListPlaybackEvents(user.ID, 1, 10); err != nil {
		t.Fatalf("ListPlaybackEvents failed: %v", err)
	}
	if err := store.UpdatePlaybackProgress(&PlaybackProgress{UserID: user.ID, BookID: 1, PositionSec: 10}); err != nil {
		t.Fatalf("UpdatePlaybackProgress failed: %v", err)
	}
	if _, err := store.GetPlaybackProgress(user.ID, 1); err != nil {
		t.Fatalf("GetPlaybackProgress failed: %v", err)
	}

	if err := store.IncrementBookPlayStats(1, 120); err != nil {
		t.Fatalf("IncrementBookPlayStats failed: %v", err)
	}
	if _, err := store.GetBookStats(1); err != nil {
		t.Fatalf("GetBookStats failed: %v", err)
	}
	if err := store.IncrementUserListenStats(user.ID, 30); err != nil {
		t.Fatalf("IncrementUserListenStats failed: %v", err)
	}
	if _, err := store.GetUserStats(user.ID); err != nil {
		t.Fatalf("GetUserStats failed: %v", err)
	}
}

// TestCreateWorkWithAltTitles tests creating a work with alternative titles
func TestCreateWorkWithAltTitles(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	author, err := store.CreateAuthor("Test Author")
	if err != nil {
		t.Fatalf("Failed to create author: %v", err)
	}

	series, err := store.CreateSeries("Test Series", &author.ID)
	if err != nil {
		t.Fatalf("Failed to create series: %v", err)
	}

	// Create work with alternative titles
	work := &Work{
		Title:     "Primary Title",
		AuthorID:  &author.ID,
		SeriesID:  &series.ID,
		AltTitles: []string{"Alt Title 1", "Alt Title 2", "Alt Title 3"},
	}

	created, err := store.CreateWork(work)
	if err != nil {
		t.Fatalf("Failed to create work with alt titles: %v", err)
	}

	if created.ID == "" {
		t.Error("Expected non-empty work ID")
	}

	// Retrieve and verify
	retrieved, err := store.GetWorkByID(created.ID)
	if err != nil {
		t.Fatalf("Failed to get work: %v", err)
	}

	if len(retrieved.AltTitles) != 3 {
		t.Errorf("Expected 3 alt titles, got %d", len(retrieved.AltTitles))
	}

	// Verify alt titles match
	for i, title := range work.AltTitles {
		if i >= len(retrieved.AltTitles) || retrieved.AltTitles[i] != title {
			t.Errorf("Alt title mismatch at index %d: expected %s, got %s", i, title, retrieved.AltTitles[i])
		}
	}
}

// TestUpdateWorkWithAltTitles tests updating a work's alternative titles
func TestUpdateWorkWithAltTitles(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create initial work
	work := &Work{
		Title:     "Original Title",
		AltTitles: []string{"Original Alt"},
	}
	created, err := store.CreateWork(work)
	if err != nil {
		t.Fatalf("Failed to create work: %v", err)
	}

	// Update with new alt titles
	updated := &Work{
		Title:     "Updated Title",
		AltTitles: []string{"New Alt 1", "New Alt 2"},
	}
	result, err := store.UpdateWork(created.ID, updated)
	if err != nil {
		t.Fatalf("Failed to update work: %v", err)
	}

	if len(result.AltTitles) != 2 {
		t.Errorf("Expected 2 alt titles after update, got %d", len(result.AltTitles))
	}
}

// TestMaskSecretEdgeCases tests the MaskSecret function with various inputs
func TestMaskSecretEdgeCases(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "****"},
		{"a", "****"},
		{"abc", "****"},
		{"1234567", "****"},
		{"12345678", "123****5678"},
		{"verylongsecret123456", "ver****3456"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := MaskSecret(tt.input)
			if result != tt.expected {
				t.Errorf("MaskSecret(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestDecryptValueErrors tests error handling in DecryptValue
func TestDecryptValueErrors(t *testing.T) {
	tempDir := t.TempDir()
	if err := InitEncryption(tempDir); err != nil {
		t.Fatalf("InitEncryption failed: %v", err)
	}

	// Test with invalid base64
	_, err := DecryptValue("not-valid-base64!!!")
	if err == nil {
		t.Error("Expected error for invalid base64")
	}

	// Test with too short ciphertext
	_, err = DecryptValue("YWJj") // "abc" in base64, but too short
	if err == nil {
		t.Error("Expected error for too short ciphertext")
	}
}
