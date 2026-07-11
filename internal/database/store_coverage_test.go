// file: internal/database/store_coverage_test.go
// version: 2.3.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef0123456789
// last-edited: 2026-07-11

// NOTE(fable5 T022): setupCoverageDB ported to PebbleStore; SQLiteStore
// type assertions updated. Tests for SQLite-only methods (CountTableRows,
// WipeTable) removed.

package database

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupCoverageDB creates a migrated PebbleStore for coverage tests.
// NOTE(fable5 T022): Previously created a SQLiteStore; now uses PebbleStore.
func setupCoverageDB(t *testing.T) Store {
	t.Helper()
	store, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, RunMigrations(store))
	t.Cleanup(func() { store.Close() })
	return store
}

// helper to create a book and return its ID.
func createTestBook(t *testing.T, store Store, title, filePath string, authorID *int, seriesID *int) string {
	t.Helper()
	book := &Book{
		Title:    title,
		AuthorID: authorID,
		SeriesID: seriesID,
		FilePath: filePath,
	}
	created, err := store.CreateBook(book)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	return created.ID
}

// --- Author CRUD ---

func TestCoverage_AuthorDeleteAndUpdate(t *testing.T) {
	store := setupCoverageDB(t)

	author, err := store.CreateAuthor("Original Name")
	require.NoError(t, err)

	// Update name
	err = store.UpdateAuthorName(author.ID, "Updated Name")
	require.NoError(t, err)

	fetched, err := store.GetAuthorByID(author.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", fetched.Name)

	// Delete author
	err = store.DeleteAuthor(author.ID)
	require.NoError(t, err)

	fetched, err = store.GetAuthorByID(author.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

// --- Author Aliases ---

func TestCoverage_AuthorAliases(t *testing.T) {
	store := setupCoverageDB(t)

	author, err := store.CreateAuthor("Brandon Sanderson")
	require.NoError(t, err)

	// Create alias
	alias, err := store.CreateAuthorAlias(author.ID, "B. Sanderson", "abbreviation")
	require.NoError(t, err)
	assert.Equal(t, "B. Sanderson", alias.AliasName)
	assert.Equal(t, "abbreviation", alias.AliasType)

	// Get aliases for author
	aliases, err := store.GetAuthorAliases(author.ID)
	require.NoError(t, err)
	assert.Len(t, aliases, 1)

	// Get all aliases
	allAliases, err := store.GetAllAuthorAliases()
	require.NoError(t, err)
	assert.Len(t, allAliases, 1)

	// Find author by alias
	found, err := store.FindAuthorByAlias("B. Sanderson")
	require.NoError(t, err)
	assert.Equal(t, author.ID, found.ID)

	// Case insensitive
	found, err = store.FindAuthorByAlias("b. sanderson")
	require.NoError(t, err)
	assert.Equal(t, author.ID, found.ID)

	// Delete alias
	err = store.DeleteAuthorAlias(alias.ID)
	require.NoError(t, err)

	aliases, err = store.GetAuthorAliases(author.ID)
	require.NoError(t, err)
	assert.Len(t, aliases, 0)
}

// --- Series CRUD ---

func TestCoverage_SeriesDeleteAndUpdate(t *testing.T) {
	store := setupCoverageDB(t)

	series, err := store.CreateSeries("Mistborn", nil)
	require.NoError(t, err)

	// Update name
	err = store.UpdateSeriesName(series.ID, "Stormlight Archive")
	require.NoError(t, err)

	fetched, err := store.GetSeriesByID(series.ID)
	require.NoError(t, err)
	assert.Equal(t, "Stormlight Archive", fetched.Name)

	// Delete series
	err = store.DeleteSeries(series.ID)
	require.NoError(t, err)

	fetched, err = store.GetSeriesByID(series.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

// --- Series/Author Book/File Counts ---

func TestCoverage_SeriesBookAndFileCounts(t *testing.T) {
	store := setupCoverageDB(t)

	author, err := store.CreateAuthor("Test Author")
	require.NoError(t, err)
	series, err := store.CreateSeries("Test Series", &author.ID)
	require.NoError(t, err)

	bookID := createTestBook(t, store, "Book 1", "/tmp/book1.m4b", &author.ID, &series.ID)
	_ = createTestBook(t, store, "Book 2", "/tmp/book2.m4b", &author.ID, &series.ID)

	// Series book counts
	counts, err := store.GetAllSeriesBookCounts()
	require.NoError(t, err)
	assert.Equal(t, 2, counts[series.ID])

	// Series file counts (no book_files, each book counts as 1)
	fileCounts, err := store.GetAllSeriesFileCounts()
	require.NoError(t, err)
	assert.Equal(t, 2, fileCounts[series.ID])

	// Add a book file to test file-count logic
	err = store.CreateBookFile(&BookFile{
		BookID:   bookID,
		FilePath: "/tmp/book1_part1.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)

	fileCounts, err = store.GetAllSeriesFileCounts()
	require.NoError(t, err)
	// book1 has 1 file, book2 has 0 files (counts as 1) = 2
	assert.GreaterOrEqual(t, fileCounts[series.ID], 2)
}

// --- Book-Author Relationships ---

func TestCoverage_BookAuthors(t *testing.T) {
	store := setupCoverageDB(t)

	author1, err := store.CreateAuthor("Author One")
	require.NoError(t, err)
	author2, err := store.CreateAuthor("Author Two")
	require.NoError(t, err)

	bookID := createTestBook(t, store, "Collab Book", "/tmp/collab.m4b", &author1.ID, nil)

	// Set book authors
	err = store.SetBookAuthors(bookID, []BookAuthor{
		{BookID: bookID, AuthorID: author1.ID, Role: "author", Position: 0},
		{BookID: bookID, AuthorID: author2.ID, Role: "co-author", Position: 1},
	})
	require.NoError(t, err)

	// Get book authors
	authors, err := store.GetBookAuthors(bookID)
	require.NoError(t, err)
	assert.Len(t, authors, 2)
	assert.Equal(t, "author", authors[0].Role)
	assert.Equal(t, "co-author", authors[1].Role)

	// GetBooksByAuthorIDWithRoleCore
	books, err := store.GetBooksByAuthorIDWithRoleCore(author2.ID)
	require.NoError(t, err)
	assert.Len(t, books, 1)

	// Author book counts
	bookCounts, err := store.GetAllAuthorBookCounts()
	require.NoError(t, err)
	assert.Equal(t, 1, bookCounts[author1.ID])
	assert.Equal(t, 1, bookCounts[author2.ID])

	// Author file counts
	fileCounts, err := store.GetAllAuthorFileCounts()
	require.NoError(t, err)
	assert.Equal(t, 1, fileCounts[author1.ID])
}

// --- Count Functions ---

func TestCoverage_CountFunctions(t *testing.T) {
	store := setupCoverageDB(t)

	author, err := store.CreateAuthor("Counter Author")
	require.NoError(t, err)
	series, err := store.CreateSeries("Counter Series", nil)
	require.NoError(t, err)

	_ = createTestBook(t, store, "Count Book", "/tmp/count.m4b", &author.ID, &series.ID)

	// CountPrimaryBooks already covered, test CountFiles, CountAuthors, CountSeries
	fileCount, err := store.CountFiles()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, fileCount, 1) // 1 book with no files counts as 1 file

	authorCount, err := store.CountAuthors()
	require.NoError(t, err)
	assert.Equal(t, 1, authorCount)

	seriesCount, err := store.CountSeries()
	require.NoError(t, err)
	assert.Equal(t, 1, seriesCount)
}

// --- Book Location Counts/Sizes ---

func TestCoverage_BookCountsByLocation(t *testing.T) {
	store := setupCoverageDB(t)

	_ = createTestBook(t, store, "Library Book", "/library/book1.m4b", nil, nil)
	_ = createTestBook(t, store, "Import Book", "/import/book2.m4b", nil, nil)

	// With root dir
	lib, imp, err := store.GetBookCountsByLocation("/library")
	require.NoError(t, err)
	assert.Equal(t, 1, lib)
	assert.Equal(t, 1, imp)

	// Without root dir (all are imports)
	lib, imp, err = store.GetBookCountsByLocation("")
	require.NoError(t, err)
	assert.Equal(t, 0, lib)
	assert.Equal(t, 2, imp)
}

func TestCoverage_BookSizesByLocation(t *testing.T) {
	store := setupCoverageDB(t)

	size := int64(1024)
	book := &Book{Title: "Sized Book", FilePath: "/library/sized.m4b", FileSize: &size}
	created, err := store.CreateBook(book)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)

	libSize, impSize, err := store.GetBookSizesByLocation("/library")
	require.NoError(t, err)
	assert.Equal(t, int64(1024), libSize)
	assert.Equal(t, int64(0), impSize)

	// Empty root dir
	libSize, impSize, err = store.GetBookSizesByLocation("")
	require.NoError(t, err)
	assert.Equal(t, int64(0), libSize)
	assert.Equal(t, int64(1024), impSize)
}

// --- Dashboard Stats ---

func TestCoverage_DashboardStats(t *testing.T) {
	store := setupCoverageDB(t)

	author, err := store.CreateAuthor("Stats Author")
	require.NoError(t, err)
	_ = createTestBook(t, store, "Stats Book", "/tmp/stats.m4b", &author.ID, nil)

	stats, err := store.GetDashboardStats()
	require.NoError(t, err)
	assert.NotNil(t, stats)
	assert.Equal(t, 1, stats.TotalBooks)
	assert.Equal(t, 1, stats.TotalAuthors)
}

// --- Book Tombstones ---

func TestCoverage_BookTombstones(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "Doomed Book", "/tmp/doomed.m4b", nil, nil)
	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)

	// Create tombstone
	err = store.CreateBookTombstone(book)
	require.NoError(t, err)

	// Get tombstone
	tombstone, err := store.GetBookTombstone(bookID)
	require.NoError(t, err)
	assert.Equal(t, "Doomed Book", tombstone.Title)

	// List tombstones
	tombstones, err := store.ListBookTombstones(10)
	require.NoError(t, err)
	assert.Len(t, tombstones, 1)

	// Delete tombstone
	err = store.DeleteBookTombstone(bookID)
	require.NoError(t, err)

	tombstone, err = store.GetBookTombstone(bookID)
	require.NoError(t, err)
	assert.Nil(t, tombstone)
}

// --- KV Operations ---

func TestCoverage_KVOperations(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	// SetRaw / ScanPrefix
	err := s.SetRaw("prefix:key1", []byte("value1"))
	require.NoError(t, err)
	err = s.SetRaw("prefix:key2", []byte("value2"))
	require.NoError(t, err)
	err = s.SetRaw("other:key3", []byte("value3"))
	require.NoError(t, err)

	pairs, err := s.ScanPrefix("prefix:")
	require.NoError(t, err)
	assert.Len(t, pairs, 2)

	// DeleteRaw
	err = s.DeleteRaw("prefix:key1")
	require.NoError(t, err)

	pairs, err = s.ScanPrefix("prefix:")
	require.NoError(t, err)
	assert.Len(t, pairs, 1)
}

// --- Operation Summary Logs ---

func TestCoverage_OperationSummaryLogs(t *testing.T) {
	store := setupCoverageDB(t)

	now := time.Now()
	completedAt := now.Add(time.Minute)
	result := `{"ok": true}`

	log := &OperationSummaryLog{
		ID:          "op-summary-1",
		Type:        "organize",
		Status:      "completed",
		Progress:    100.0,
		Result:      &result,
		CreatedAt:   now,
		CompletedAt: &completedAt,
	}

	err := store.SaveOperationSummaryLog(log)
	require.NoError(t, err)

	// Get
	fetched, err := store.GetOperationSummaryLog("op-summary-1")
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "completed", fetched.Status)

	// Not found
	fetched, err = store.GetOperationSummaryLog("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, fetched)

	// List
	logs, err := store.ListOperationSummaryLogs(10, 0)
	require.NoError(t, err)
	assert.Len(t, logs, 1)
}

// --- Operation Results ---

func TestCoverage_OperationResults(t *testing.T) {
	store := setupCoverageDB(t)

	// Create an operation first
	op, err := store.CreateOperation("op-result-1", "organize", nil)
	require.NoError(t, err)

	// Create result
	err = store.CreateOperationResult(&OperationResult{
		OperationID: op.ID,
		BookID:      "book-123",
		ResultJSON:  `{"status": "moved"}`,
		Status:      "success",
	})
	require.NoError(t, err)

	// Get results
	results, err := store.GetOperationResults(op.ID)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "book-123", results[0].BookID)

	// Complete the operation
	err = store.UpdateOperationStatus(op.ID, "completed", 1, 1, "done")
	require.NoError(t, err)

	// Get recent completed
	completed, err := store.GetRecentCompletedOperations(10)
	require.NoError(t, err)
	assert.Len(t, completed, 1)
}

// --- Operation State Persistence ---

func TestCoverage_OperationStatePersistence(t *testing.T) {
	store := setupCoverageDB(t)

	opID := "resumable-op-1"

	// Save state
	err := store.SaveOperationState(opID, []byte(`{"step": 5}`))
	require.NoError(t, err)

	// Get state
	data, err := store.GetOperationState(opID)
	require.NoError(t, err)
	assert.Equal(t, `{"step": 5}`, string(data))

	// Save params
	err = store.SaveOperationParams(opID, []byte(`{"folder": "/tmp"}`))
	require.NoError(t, err)

	// Get params
	params, err := store.GetOperationParams(opID)
	require.NoError(t, err)
	assert.Equal(t, `{"folder": "/tmp"}`, string(params))

	// Not found
	data, err = store.GetOperationState("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, data)

	params, err = store.GetOperationParams("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, params)

	// Delete
	err = store.DeleteOperationState(opID)
	require.NoError(t, err)
	data, err = store.GetOperationState(opID)
	require.NoError(t, err)
	assert.Nil(t, data)
}

// --- ListOperations ---

func TestCoverage_ListOperations(t *testing.T) {
	store := setupCoverageDB(t)

	_, err := store.CreateOperation("list-op-1", "scan", nil)
	require.NoError(t, err)
	_, err = store.CreateOperation("list-op-2", "organize", nil)
	require.NoError(t, err)

	ops, total, err := store.ListOperations(10, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, ops, 2)

	// Pagination
	ops, total, err = store.ListOperations(1, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, ops, 1)
}

// --- DeleteOperationsByStatus ---

func TestCoverage_DeleteOperationsByStatus(t *testing.T) {
	store := setupCoverageDB(t)

	_, err := store.CreateOperation("del-op-1", "scan", nil)
	require.NoError(t, err)
	err = store.UpdateOperationStatus("del-op-1", "completed", 1, 1, "done")
	require.NoError(t, err)

	_, err = store.CreateOperation("del-op-2", "scan", nil)
	require.NoError(t, err)

	// Delete completed
	n, err := store.DeleteOperationsByStatus([]string{"completed"})
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// Empty statuses
	n, err = store.DeleteOperationsByStatus([]string{})
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// --- UpdateOperationResultData ---

func TestCoverage_UpdateOperationResultData(t *testing.T) {
	store := setupCoverageDB(t)

	op, err := store.CreateOperation("update-rd-1", "scan", nil)
	require.NoError(t, err)

	err = store.UpdateOperationResultData(op.ID, `{"count": 42}`)
	require.NoError(t, err)

	fetched, err := store.GetOperationByID(op.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.ResultData)
	assert.Equal(t, `{"count": 42}`, *fetched.ResultData)
}

// --- GetInterruptedOperations ---

func TestCoverage_GetInterruptedOperations(t *testing.T) {
	store := setupCoverageDB(t)

	_, err := store.CreateOperation("int-op-1", "scan", nil)
	require.NoError(t, err)
	err = store.UpdateOperationStatus("int-op-1", "running", 0, 10, "processing")
	require.NoError(t, err)

	_, err = store.CreateOperation("int-op-2", "organize", nil)
	require.NoError(t, err)
	err = store.UpdateOperationStatus("int-op-2", "completed", 10, 10, "done")
	require.NoError(t, err)

	ops, err := store.GetInterruptedOperations()
	require.NoError(t, err)
	assert.Len(t, ops, 1)
	assert.Equal(t, "int-op-1", ops[0].ID)
}

// --- Operation Changes ---

func TestCoverage_OperationChanges(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "Changed Book", "/tmp/changed.m4b", nil, nil)
	op, err := store.CreateOperation("change-op-1", "organize", nil)
	require.NoError(t, err)

	// Create change
	change := &OperationChange{
		OperationID: op.ID,
		BookID:      bookID,
		ChangeType:  "file_move",
		FieldName:   "file_path",
		OldValue:    "/old/path.m4b",
		NewValue:    "/new/path.m4b",
	}
	err = store.CreateOperationChange(change)
	require.NoError(t, err)
	assert.NotEmpty(t, change.ID) // ULID generated

	// Get by operation
	changes, err := store.GetOperationChanges(op.ID)
	require.NoError(t, err)
	assert.Len(t, changes, 1)
	assert.Equal(t, "file_move", changes[0].ChangeType)

	// Get by book
	bookChanges, err := store.GetBookChanges(bookID)
	require.NoError(t, err)
	assert.Len(t, bookChanges, 1)

	// Revert
	err = store.RevertOperationChanges(op.ID)
	require.NoError(t, err)

	changes, err = store.GetOperationChanges(op.ID)
	require.NoError(t, err)
	assert.NotNil(t, changes[0].RevertedAt)
}

// --- System Activity Logs ---

func TestCoverage_SystemActivityLogs(t *testing.T) {
	store := setupCoverageDB(t)

	err := store.AddSystemActivityLog("scanner", "info", "Scan started")
	require.NoError(t, err)
	err = store.AddSystemActivityLog("organizer", "warn", "File missing")
	require.NoError(t, err)

	// Get all
	logs, err := store.GetSystemActivityLogs("", 10)
	require.NoError(t, err)
	assert.Len(t, logs, 2)

	// Filter by source
	logs, err = store.GetSystemActivityLogs("scanner", 10)
	require.NoError(t, err)
	assert.Len(t, logs, 1)
}

// --- Pruning ---

func TestCoverage_PruneOperationLogs(t *testing.T) {
	store := setupCoverageDB(t)

	op, err := store.CreateOperation("prune-op-1", "scan", nil)
	require.NoError(t, err)
	err = store.AddOperationLog(op.ID, "info", "test log", nil)
	require.NoError(t, err)

	// Prune with past time (should delete nothing — exercises the code path)
	n, err := store.PruneOperationLogs(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// Prune with far-future time — deletes all
	// SQLite stores created_at as text via CURRENT_TIMESTAMP; pass a far-future time string
	n, err = store.PruneOperationLogs(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 1)
}

func TestCoverage_PruneOperationChanges(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "Prune Book", "/tmp/prune.m4b", nil, nil)
	op, err := store.CreateOperation("prune-change-op", "organize", nil)
	require.NoError(t, err)

	err = store.CreateOperationChange(&OperationChange{
		OperationID: op.ID,
		BookID:      bookID,
		ChangeType:  "metadata_update",
		FieldName:   "title",
		OldValue:    "Old",
		NewValue:    "New",
	})
	require.NoError(t, err)

	n, err := store.PruneOperationChanges(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 1)
}

func TestCoverage_PruneSystemActivityLogs(t *testing.T) {
	store := setupCoverageDB(t)

	err := store.AddSystemActivityLog("test", "info", "prunable")
	require.NoError(t, err)

	n, err := store.PruneSystemActivityLogs(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 1)
}

// --- Author Tombstones ---

func TestCoverage_AuthorTombstoneStubs(t *testing.T) {
	store := setupCoverageDB(t)

	// PebbleStore fully supports author tombstones.
	err := store.CreateAuthorTombstone(1, 2)
	assert.NoError(t, err)

	// GetAuthorTombstone returns the canonical ID.
	id, err := store.GetAuthorTombstone(1)
	assert.NoError(t, err)
	assert.Equal(t, 2, id)

	// ResolveTombstoneChains: no chain (2 is not a tombstone), so 0 chains resolved.
	n, err := store.ResolveTombstoneChains()
	assert.NoError(t, err)
	assert.Equal(t, 0, n)

	// Non-existent tombstone returns 0.
	id2, err := store.GetAuthorTombstone(999)
	assert.NoError(t, err)
	assert.Equal(t, 0, id2)
}

// --- Book Version Stubs ---

func TestCoverage_BookVersionStubs(t *testing.T) {
	store := setupCoverageDB(t)

	versions, err := store.GetBookSnapshots("some-id", 10)
	assert.NoError(t, err)
	assert.Nil(t, versions)

	_, err = store.GetBookAtVersion("some-id", time.Now())
	assert.Error(t, err)

	_, err = store.RevertBookToVersion("some-id", time.Now())
	assert.Error(t, err)

	n, err := store.PruneBookSnapshots("some-id", 5)
	assert.NoError(t, err)
	assert.Equal(t, 0, n)
}

// --- Optimize ---

func TestCoverage_Optimize(t *testing.T) {
	store := setupCoverageDB(t)

	err := store.Optimize()
	assert.NoError(t, err)
}

// --- CountUsers / DeleteExpiredSessions ---

func TestCoverage_CountUsersAndExpiredSessions(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	count, err := s.CountUsers()
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Create user + session
	user, err := s.CreateUser("testuser", "test@test.com", "bcrypt", "hash", []string{"user"}, "active")
	require.NoError(t, err)

	count, err = s.CountUsers()
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Create expired session
	_, err = s.CreateSession(user.ID, "127.0.0.1", "TestAgent", -time.Hour)
	require.NoError(t, err)

	n, err := s.DeleteExpiredSessions(time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// NOTE(fable5 T022): TestCoverage_WipeTableAndCountRows removed; WipeTable
// and CountTableRows were SQLite-only methods.

// --- Book Tags ---

func TestCoverage_BookTags(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "Tagged Book", "/tmp/tagged.m4b", nil, nil)

	// AddBookTag
	err := store.AddBookTag(bookID, "Sci-Fi")
	require.NoError(t, err)
	err = store.AddBookTag(bookID, "LitRPG")
	require.NoError(t, err)

	// Empty tag should error
	err = store.AddBookTag(bookID, "")
	assert.Error(t, err)

	// GetBookTags
	tags, err := store.GetBookTags(bookID)
	require.NoError(t, err)
	assert.Len(t, tags, 2)

	// SetBookTags (replace)
	err = store.SetBookTags(bookID, []string{"Fantasy", "Epic"})
	require.NoError(t, err)

	tags, err = store.GetBookTags(bookID)
	require.NoError(t, err)
	assert.Len(t, tags, 2)
	assert.Contains(t, tags, "fantasy") // lowercased
	assert.Contains(t, tags, "epic")

	// ListAllTags
	allTags, err := store.ListAllTags()
	require.NoError(t, err)
	assert.Len(t, allTags, 2)

	// GetBooksByTag
	bookIDs, err := store.GetBooksByTag("fantasy")
	require.NoError(t, err)
	assert.Len(t, bookIDs, 1)
	assert.Equal(t, bookID, bookIDs[0])

	// RemoveBookTag
	err = store.RemoveBookTag(bookID, "Fantasy")
	require.NoError(t, err)

	// Empty tag remove should error
	err = store.RemoveBookTag(bookID, "")
	assert.Error(t, err)

	tags, err = store.GetBookTags(bookID)
	require.NoError(t, err)
	assert.Len(t, tags, 1)
}

// TestCoverage_BookTagsColonCollision regression-tests a bug where a
// colon-containing tag (e.g. a system tag like "metadata:source:audible")
// and a bare tag that is its prefix (e.g. "metadata") coexisted in the tag
// index. ListAllTags/GetBooksByTag used to truncate or byte-prefix-collide
// on the colon, collapsing counts and returning zero (or garbage) results
// for the namespaced tag. See internal/database/pebble_store_tags.go.
func TestCoverage_BookTagsColonCollision(t *testing.T) {
	store := setupCoverageDB(t)

	bareBook := createTestBook(t, store, "Bare Tag Book", "/tmp/bare.m4b", nil, nil)
	nsBook := createTestBook(t, store, "Namespaced Tag Book", "/tmp/ns.m4b", nil, nil)
	bothBook := createTestBook(t, store, "Both Tags Book", "/tmp/both.m4b", nil, nil)

	require.NoError(t, store.AddBookTagWithSource(bareBook, "metadata", "system"))
	require.NoError(t, store.AddBookTagWithSource(nsBook, "metadata:source:audible", "system"))
	require.NoError(t, store.AddBookTagWithSource(bothBook, "metadata", "system"))
	require.NoError(t, store.AddBookTagWithSource(bothBook, "metadata:source:audible", "system"))

	allTags, err := store.ListAllTags()
	require.NoError(t, err)
	counts := make(map[string]int)
	for _, tc := range allTags {
		counts[tc.Tag] = tc.Count
	}
	assert.Equal(t, 2, counts["metadata"], "bare tag count must not include namespaced-tag rows")
	assert.Equal(t, 2, counts["metadata:source:audible"], "namespaced tag must be preserved whole, not truncated")

	bareResults, err := store.GetBooksByTag("metadata")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{bareBook, bothBook}, bareResults, "bare-tag lookup must not match the namespaced tag's rows")

	nsResults, err := store.GetBooksByTag("metadata:source:audible")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{nsBook, bothBook}, nsResults, "namespaced-tag lookup must resolve to real book IDs, not zero results")
}

// TestCoverage_AuthorSeriesTagsColonCollision is the author/series
// counterpart of TestCoverage_BookTagsColonCollision, covering the shared
// pebbleListAllTags/pebbleEntitiesByTag helpers.
func TestCoverage_AuthorSeriesTagsColonCollision(t *testing.T) {
	store := setupCoverageDB(t)

	bareAuthor, err := store.CreateAuthor("Bare Tag Author")
	require.NoError(t, err)
	nsAuthor, err := store.CreateAuthor("Namespaced Tag Author")
	require.NoError(t, err)

	require.NoError(t, store.AddAuthorTagWithSource(bareAuthor.ID, "metadata", "system"))
	require.NoError(t, store.AddAuthorTagWithSource(nsAuthor.ID, "metadata:source:audible", "system"))

	allAuthorTags, err := store.ListAllAuthorTags()
	require.NoError(t, err)
	authorCounts := make(map[string]int)
	for _, tc := range allAuthorTags {
		authorCounts[tc.Tag] = tc.Count
	}
	assert.Equal(t, 1, authorCounts["metadata"])
	assert.Equal(t, 1, authorCounts["metadata:source:audible"])

	bareAuthorIDs, err := store.GetAuthorsByTag("metadata")
	require.NoError(t, err)
	assert.Equal(t, []int{bareAuthor.ID}, bareAuthorIDs, "bare-tag lookup must not pull in the namespaced tag's author, and must not return a garbage ID")

	nsAuthorIDs, err := store.GetAuthorsByTag("metadata:source:audible")
	require.NoError(t, err)
	assert.Equal(t, []int{nsAuthor.ID}, nsAuthorIDs)

	bareSeries, err := store.CreateSeries("Bare Tag Series", nil)
	require.NoError(t, err)
	nsSeries, err := store.CreateSeries("Namespaced Tag Series", nil)
	require.NoError(t, err)

	require.NoError(t, store.AddSeriesTagWithSource(bareSeries.ID, "dedup", "system"))
	require.NoError(t, store.AddSeriesTagWithSource(nsSeries.ID, "dedup:merge-survivor:auto-hash", "system"))

	bareSeriesIDs, err := store.GetSeriesByTag("dedup")
	require.NoError(t, err)
	assert.Equal(t, []int{bareSeries.ID}, bareSeriesIDs)

	nsSeriesIDs, err := store.GetSeriesByTag("dedup:merge-survivor:auto-hash")
	require.NoError(t, err)
	assert.Equal(t, []int{nsSeries.ID}, nsSeriesIDs)
}

// --- User Tags (on book_tags via BookUserTag interface) ---

func TestCoverage_BookUserTags(t *testing.T) {
	store := setupCoverageDB(t)
	bookID := createTestBook(t, store, "User Tag Book", "/tmp/usertag.m4b", nil, nil)

	err := store.AddBookUserTag(bookID, "favorite")
	require.NoError(t, err)

	tags, err := store.GetBookUserTags(bookID)
	require.NoError(t, err)
	assert.Contains(t, tags, "favorite")

	err = store.SetBookUserTags(bookID, []string{"top10", "recent"})
	require.NoError(t, err)

	tags, err = store.GetBookUserTags(bookID)
	require.NoError(t, err)
	assert.Len(t, tags, 2)

	err = store.RemoveBookUserTag(bookID, "top10")
	require.NoError(t, err)

	tags, err = store.GetBookUserTags(bookID)
	require.NoError(t, err)
	assert.Len(t, tags, 1)
}

// --- Scan Cache ---

func TestCoverage_ScanCache(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "Scan Book", "/tmp/scan.m4b", nil, nil)

	// Update scan cache
	err := store.UpdateScanCache(bookID, 1234567890, 999)
	require.NoError(t, err)

	// Get scan cache map
	cacheMap, err := store.GetScanCacheMap()
	require.NoError(t, err)
	entry, ok := cacheMap["/tmp/scan.m4b"]
	assert.True(t, ok)
	assert.Equal(t, int64(1234567890), entry.Mtime)
	assert.Equal(t, int64(999), entry.Size)

	// Mark needs rescan
	err = store.MarkNeedsRescan(bookID)
	require.NoError(t, err)

	// Get dirty folders
	dirs, err := store.GetDirtyBookFolders()
	require.NoError(t, err)
	assert.Contains(t, dirs, "/tmp")
}

// --- Path History ---

func TestCoverage_PathHistory(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "Moved Book", "/tmp/old.m4b", nil, nil)

	err := store.RecordPathChange(&BookPathChange{
		BookID:     bookID,
		OldPath:    "/tmp/old.m4b",
		NewPath:    "/library/new.m4b",
		ChangeType: "organize",
	})
	require.NoError(t, err)

	history, err := store.GetBookPathHistory(bookID)
	require.NoError(t, err)
	// CreateBook records an implicit import entry; find the "organize" one.
	var organizeEntry *BookPathChange
	for i, h := range history {
		if h.ChangeType == "organize" {
			organizeEntry = &history[i]
			break
		}
	}
	require.NotNil(t, organizeEntry, "expected an organize entry in %v", history)
	assert.Equal(t, "/tmp/old.m4b", organizeEntry.OldPath)
	assert.Equal(t, "/library/new.m4b", organizeEntry.NewPath)
}

// --- Library Fingerprints ---

func TestCoverage_LibraryFingerprints(t *testing.T) {
	store := setupCoverageDB(t)

	modTime := time.Now().Truncate(time.Second)
	err := store.SaveLibraryFingerprint("/path/to/library.xml", 1024, modTime, 12345)
	require.NoError(t, err)

	rec, err := store.GetLibraryFingerprint("/path/to/library.xml")
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, int64(1024), rec.Size)
	assert.Equal(t, uint32(12345), rec.CRC32)

	// Not found
	rec, err = store.GetLibraryFingerprint("/nonexistent")
	require.NoError(t, err)
	assert.Nil(t, rec)
}

// --- Deferred iTunes Updates ---

func TestCoverage_DeferredITunesUpdates(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "iTunes Book", "/tmp/itunes.m4b", nil, nil)

	err := store.CreateDeferredITunesUpdate(bookID, "PID123", "/old/path.m4b", "/new/path.m4b", "path_change")
	require.NoError(t, err)

	// Get pending
	pending, err := store.GetPendingDeferredITunesUpdates()
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "PID123", pending[0].PersistentID)

	// Get by book ID
	updates, err := store.GetDeferredITunesUpdatesByBookID(bookID)
	require.NoError(t, err)
	assert.Len(t, updates, 1)

	// Mark applied
	err = store.MarkDeferredITunesUpdateApplied(pending[0].ID)
	require.NoError(t, err)

	// Should be empty now
	pending, err = store.GetPendingDeferredITunesUpdates()
	require.NoError(t, err)
	assert.Len(t, pending, 0)
}

// --- iTunes Sync ---

func TestCoverage_ITunesSyncStatus(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "Sync Book", "/tmp/sync.m4b", nil, nil)

	// Mark synced
	n, err := store.MarkITunesSynced([]string{bookID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// Empty list
	n, err = store.MarkITunesSynced([]string{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// Get dirty books (should be empty since we just marked synced)
	dirty, err := store.GetITunesDirtyBooks()
	require.NoError(t, err)
	assert.Len(t, dirty, 0)
}

// --- GetBookByITunesPersistentID ---

func TestCoverage_GetBookByITunesPersistentID(t *testing.T) {
	store := setupCoverageDB(t)

	pid := "ITUNES_PID_123"
	book := &Book{
		Title:              "iTunes PID Book",
		FilePath:           "/tmp/itunespid.m4b",
		ITunesPersistentID: &pid,
	}
	created, err := store.CreateBook(book)
	require.NoError(t, err)

	found, err := store.GetBookByITunesPersistentID(pid)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, created.ID, found.ID)

	// Not found
	found, err = store.GetBookByITunesPersistentID("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, found)
}

// --- GetBooksByTitleInDir ---

func TestCoverage_GetBooksByTitleInDir(t *testing.T) {
	store := setupCoverageDB(t)

	_ = createTestBook(t, store, "Same Title", "/shared/dir/book1.m4b", nil, nil)
	_ = createTestBook(t, store, "Same Title", "/shared/dir/book2.m4b", nil, nil)
	_ = createTestBook(t, store, "Different", "/shared/dir/book3.m4b", nil, nil)

	books, err := store.GetBooksByTitleInDir("same title", "/shared/dir")
	require.NoError(t, err)
	assert.Len(t, books, 2)
}

// --- External ID: MarkExternalIDRemoved, SetExternalIDProvenance, GetRemovedExternalIDs ---

func TestCoverage_ExternalIDExtended(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "ExtID Book", "/tmp/extid.m4b", nil, nil)

	// Create mapping
	err := store.CreateExternalIDMapping(&ExternalIDMapping{
		Source:     "itunes",
		ExternalID: "PID456",
		BookID:     bookID,
		FilePath:   "/tmp/extid.m4b",
	})
	require.NoError(t, err)

	// Set provenance
	err = store.SetExternalIDProvenance("itunes", "PID456", "xml_import")
	require.NoError(t, err)

	// Mark removed
	err = store.MarkExternalIDRemoved("itunes", "PID456")
	require.NoError(t, err)

	// Get removed
	removed, err := store.GetRemovedExternalIDs("itunes")
	require.NoError(t, err)
	assert.Len(t, removed, 1)
	assert.Equal(t, "PID456", removed[0].ExternalID)
	assert.Equal(t, "xml_import", removed[0].Provenance)
	assert.NotNil(t, removed[0].RemovedAt)
}

// --- Book Segments (PebbleStore supports segments — no table-drop behavior) ---

func TestCoverage_BookSegmentOperationsGraceful(t *testing.T) {
	store := setupCoverageDB(t)

	// PebbleStore supports book segments natively (unlike SQLite post-migration 43).
	seg, err := store.CreateBookSegment(1, &BookSegment{
		FilePath: "/tmp/seg_part1.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)
	require.NotNil(t, seg)

	_, err = store.GetBookSegmentByID("nonexistent")
	assert.Error(t, err) // PebbleStore: not-found returns an error

	err = store.MoveSegmentsToBook([]string{}, 1) // empty list is no-op
	assert.NoError(t, err)
}

// --- BookFile extended: GetBookFileByID, MoveBookFilesToBook, BatchUpsertBookFiles ---

func TestCoverage_BookFileExtended(t *testing.T) {
	store := setupCoverageDB(t)

	bookID := createTestBook(t, store, "File Book", "/tmp/filebook.m4b", nil, nil)

	// Create file
	err := store.CreateBookFile(&BookFile{
		BookID:   bookID,
		FilePath: "/tmp/filebook_part1.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)

	// Get files
	files, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, files, 1)

	// GetBookFileByID
	fetched, err := store.GetBookFileByID(bookID, files[0].ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "/tmp/filebook_part1.m4b", fetched.FilePath)

	// Move to another book
	bookID2 := createTestBook(t, store, "Dest File Book", "/tmp/destfile.m4b", nil, nil)
	err = store.MoveBookFilesToBook([]string{files[0].ID}, bookID, bookID2)
	require.NoError(t, err)

	// Files should now be under bookID2
	files2, err := store.GetBookFiles(bookID2)
	require.NoError(t, err)
	assert.Len(t, files2, 1)

	// BatchUpsertBookFiles
	err = store.BatchUpsertBookFiles([]*BookFile{
		{BookID: bookID2, FilePath: "/tmp/batch1.m4b", Format: "m4b"},
		{BookID: bookID2, FilePath: "/tmp/batch2.m4b", Format: "m4b"},
	})
	require.NoError(t, err)

	files2, err = store.GetBookFiles(bookID2)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(files2), 3)
}

// --- GetFolderDuplicates ---

func TestCoverage_GetFolderDuplicates(t *testing.T) {
	store := setupCoverageDB(t)

	// Create books in same folder with same title
	_ = createTestBook(t, store, "Duplicate", "/same/folder/dup1.m4b", nil, nil)
	_ = createTestBook(t, store, "Duplicate", "/same/folder/dup2.m4b", nil, nil)

	dupes, err := store.GetFolderDuplicatesCore()
	require.NoError(t, err)
	// May or may not find dupes depending on implementation (normalized title matching),
	// but the function should not error
	_ = dupes
}

// --- GetDuplicateBooksByMetadata ---

func TestCoverage_GetDuplicateBooksByMetadata(t *testing.T) {
	store := setupCoverageDB(t)

	author, err := store.CreateAuthor("Dupe Author")
	require.NoError(t, err)
	dur := 3600
	_ = createBookWithDuration(t, store, "Almost Same Title", "/tmp/dupe1.m4b", &author.ID, &dur)
	_ = createBookWithDuration(t, store, "Almost Same Title", "/tmp/dupe2.m4b", &author.ID, &dur)

	dupes, err := store.GetDuplicateBooksByMetadataCore(0.8)
	require.NoError(t, err)
	_ = dupes // just testing it doesn't error
}

func createBookWithDuration(t *testing.T, store Store, title, filePath string, authorID *int, duration *int) string {
	t.Helper()
	book := &Book{
		Title:    title,
		AuthorID: authorID,
		FilePath: filePath,
		Duration: duration,
	}
	created, err := store.CreateBook(book)
	require.NoError(t, err)
	return created.ID
}

// --- Activity Store: WipeAllActivity ---

func TestCoverage_WipeAllActivity(t *testing.T) {
	dir := t.TempDir()
	as, err := NewNutsActivityStore(dir)
	require.NoError(t, err)
	defer as.Close()

	// Record some activity
	_, err = as.Record(ActivityEntry{
		Source:  "test_source",
		Type:    "test_action",
		Summary: "test summary",
		Level:   "info",
		Tier:    "system",
	})
	require.NoError(t, err)

	// Wipe
	_, err = as.WipeAllActivity()
	require.NoError(t, err)

	// Verify empty
	events, _, err := as.Query(ActivityFilter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, events, 0)
}

// --- AI Scan Store: 0% coverage functions ---

func TestCoverage_AIScanStoreExtended(t *testing.T) {
	dir := t.TempDir()
	as, err := NewAIScanStore(filepath.Join(dir, "aiscan.db"))
	require.NoError(t, err)
	defer as.Close()

	// Create a scan
	models := map[string]string{"phase1": "gpt-4"}
	scan, err := as.CreateScan("full", models, 10)
	require.NoError(t, err)

	// UpdateScanOperationID
	err = as.UpdateScanOperationID(scan.ID, "batch-op-123")
	require.NoError(t, err)

	fetched, err := as.GetScan(scan.ID)
	require.NoError(t, err)
	assert.Equal(t, "batch-op-123", fetched.OperationID)

	// Create phase
	_, err = as.CreatePhase(scan.ID, "groups_scan", "gpt-4")
	require.NoError(t, err)

	// SavePhaseData
	input := json.RawMessage(`{"books": [1]}`)
	output := json.RawMessage(`{"groups": []}`)
	suggestions := json.RawMessage(`[]`)
	err = as.SavePhaseData(scan.ID, "groups_scan", input, output, suggestions)
	require.NoError(t, err)

	phaseFetched, err := as.GetPhase(scan.ID, "groups_scan")
	require.NoError(t, err)
	assert.NotNil(t, phaseFetched.InputData)

	// Create result and mark applied
	err = as.SaveScanResult(&ScanResult{
		ScanID:    scan.ID,
		Agreement: "agreed",
		Suggestion: ScanSuggestion{
			Action:        "merge",
			CanonicalName: "Test Author",
			Reason:        "same author",
			Confidence:    "high",
			Source:        "groups_scan",
		},
	})
	require.NoError(t, err)

	results, err := as.GetScanResults(scan.ID)
	require.NoError(t, err)
	require.Len(t, results, 1)

	err = as.MarkResultApplied(scan.ID, results[0].ID)
	require.NoError(t, err)

	// GetAllAppliedResults
	applied, err := as.GetAllAppliedResults()
	require.NoError(t, err)
	assert.Len(t, applied, 1)
}
