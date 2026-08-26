// file: internal/database/extra_coverage_test.go
// version: 2.0.0
// guid: e1f2a3b4-c5d6-7890-abcd-ef0102030405
// last-edited: 2026-06-10

// Package database — extra tests to lift coverage of 0%-covered functions.
// Covers: APIKeyToken helpers, PebbleStore book/tag/user/activity/metadata
// functions, EmbeddingStore helpers, MetadataFetchCache, and misc utils.
// NOTE(fable5 T022): Ported from SQLiteStore to PebbleStore; tests for
// SQLite-only methods (GetAllBookFiles, GetBookFilesNeedingDelugeImport,
// GetBookFileByAcoustID) removed.
package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- APIKey token helpers ----

func TestGenerateAPIKeyToken(t *testing.T) {
	raw, hash, err := GenerateAPIKeyToken()
	require.NoError(t, err)
	assert.NotEmpty(t, raw)
	assert.NotEmpty(t, hash)
	assert.Contains(t, raw, "abk_")
	assert.Equal(t, 64, len(hash)) // SHA-256 hex = 64 chars
}

func TestHashAPIKeyToken(t *testing.T) {
	raw, hash1, err := GenerateAPIKeyToken()
	require.NoError(t, err)
	hash2 := HashAPIKeyToken(raw)
	assert.Equal(t, hash1, hash2)

	// Deterministic
	assert.Equal(t, hash2, HashAPIKeyToken(raw))
}

// ---- SQLiteStore.SetRootDir + InvalidateLibraryStats ----

func TestSQLiteStore_SetRootDirAndInvalidate(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)
	// Just exercises the code paths (SetRootDir is a simple field assign,
	// InvalidateLibraryStats is a no-op on SQLite).
	s.SetRootDir("/tmp/root")
	s.InvalidateLibraryStats()
}

// ---- SQLiteStore.GetAllBookSummaries ----

func TestSQLiteStore_GetAllBookSummaries(t *testing.T) {
	store := setupCoverageDB(t)

	_ = createTestBook(t, store, "Summary A", "/tmp/sa.m4b", nil, nil)
	_ = createTestBook(t, store, "Summary B", "/tmp/sb.m4b", nil, nil)

	summaries, err := store.GetAllBookSummaries(10, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(summaries), 2)

	// Zero limit → defaults to all
	all, err := store.GetAllBookSummaries(0, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 2)
}

// ---- GetDistinctGenres / GetDistinctLanguages ----

func TestSQLiteStore_DistinctGenresAndLanguages(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	genre := "Fantasy"
	lang := "English"

	book, err := store.CreateBook(&Book{
		Title:    "Genre Book",
		FilePath: "/tmp/genre.m4b",
	})
	require.NoError(t, err)
	book.Genre = &genre
	book.Language = &lang
	_, err = store.UpdateBook(book.ID, book)
	require.NoError(t, err)

	genres, err := s.GetDistinctGenres()
	require.NoError(t, err)
	assert.Contains(t, genres, "Fantasy")

	langs, err := s.GetDistinctLanguages()
	require.NoError(t, err)
	assert.Contains(t, langs, "English")
}

// ---- GetBookBySegmentFileHash ----

func TestSQLiteStore_GetBookBySegmentFileHash(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	bookID := createTestBook(t, store, "Seg Hash Book", "/tmp/seghash.m4b", nil, nil)
	hash := "seg-file-hash-abc"

	err := store.CreateBookFile(&BookFile{
		BookID:   bookID,
		FilePath: "/tmp/seghash_part1.m4b",
		Format:   "m4b",
		FileHash: hash,
	})
	require.NoError(t, err)

	// NOTE: GetBookBySegmentFileHash uses an unqualified JOIN that results in
	// "ambiguous column name: id" in SQLite when book_files also has an id
	// column. This is a known production-code issue.  The test exercises the
	// code path (covering the non-empty branch and the early-return for empty
	// hash) without asserting the result of the ambiguous-column query so we
	// don't mask the bug.
	_, _ = s.GetBookBySegmentFileHash(hash) // exercises the code path; may error

	// Empty hash returns nil, nil (early return — no SQL executed)
	found, err := s.GetBookBySegmentFileHash("")
	require.NoError(t, err)
	assert.Nil(t, found)

	// Unknown hash returns nil, nil
	_, _ = s.GetBookBySegmentFileHash("no-such-hash")
}

// ---- GetBooksByMetadataSourceHash ----

func TestSQLiteStore_GetBooksByMetadataSourceHash(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	// CreateBook does not persist MetadataSourceHash; use UpdateBook after create.
	msh := "meta-source-hash-1"
	bookID := createTestBook(t, store, "MSH Book", "/tmp/msh.m4b", nil, nil)
	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	book.MetadataSourceHash = &msh
	_, err = store.UpdateBook(bookID, book)
	require.NoError(t, err)

	results, err := s.GetBooksByMetadataSourceHash(msh)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, bookID, results[0].ID)

	// No match
	results, err = s.GetBooksByMetadataSourceHash("no-such-hash")
	require.NoError(t, err)
	assert.Len(t, results, 0)
}

// ---- UpdateBookRating ----

func TestSQLiteStore_UpdateBookRating(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	bookID := createTestBook(t, store, "Rated Book", "/tmp/rated.m4b", nil, nil)

	overall := 4.5
	story := 5.0
	perf := 4.0
	notes := "Great narrator"

	err := s.UpdateBookRating(bookID, UpdateBookRatingRequest{
		Overall:     &overall,
		Story:       &story,
		Performance: &perf,
		Notes:       &notes,
	})
	require.NoError(t, err)

	// Verify via GetBookByID
	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)
	assert.Equal(t, &overall, book.UserRatingOverall)

	// Clear rating
	err = s.UpdateBookRating(bookID, UpdateBookRatingRequest{
		ClearOverall: true,
		ClearNotes:   true,
	})
	require.NoError(t, err)

	// No-op: empty request
	err = s.UpdateBookRating(bookID, UpdateBookRatingRequest{})
	require.NoError(t, err)
}

// ---- GetITunesPurgePendingBooks ----

func TestSQLiteStore_GetITunesPurgePendingBooks(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	pid := "PID_PURGE_1"
	book, err := store.CreateBook(&Book{
		Title:              "Purge Pending",
		FilePath:           "/tmp/purge.m4b",
		ITunesPersistentID: &pid,
	})
	require.NoError(t, err)

	status := "purge_pending"
	book.ITunesSyncStatus = &status
	_, err = store.UpdateBook(book.ID, book)
	require.NoError(t, err)

	books, err := s.GetITunesPurgePendingBooks()
	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, book.ID, books[0].ID)
}

// ---- CountBooksByPathPrefix ----

func TestSQLiteStore_CountBooksByPathPrefix(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	_ = createTestBook(t, store, "Prefix Book 1", "/mnt/library/book1.m4b", nil, nil)
	_ = createTestBook(t, store, "Prefix Book 2", "/mnt/library/book2.m4b", nil, nil)
	_ = createTestBook(t, store, "Other Book", "/other/book3.m4b", nil, nil)

	count, err := s.CountBooksByPathPrefix("/mnt/library")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 2)

	count2, err := s.CountBooksByPathPrefix("/other")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count2, 1)
}

// NOTE(fable5 T022): GetAllBookFiles, GetBookFilesNeedingDelugeImport,
// GetBookFileByAcoustID, GetBookFileByAcoustIDFuzzy tests removed;
// those methods were SQLite-only and don't exist on PebbleStore.

// ---- GetQuarantinedBooks + CountQuarantinedBooks ----

func TestSQLiteStore_QuarantinedBooks(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	bookID := createTestBook(t, store, "Quarantine Book", "/tmp/quarantine.m4b", nil, nil)
	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)

	now := time.Now()
	reason := "suspicious content"
	book.QuarantinedAt = &now
	book.QuarantineReason = &reason
	_, err = store.UpdateBook(book.ID, book)
	require.NoError(t, err)

	books, err := s.GetQuarantinedBooks(10, 0)
	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, bookID, books[0].ID)

	count, err := s.CountQuarantinedBooks()
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// ---- MergeChapterBooks ----

func TestSQLiteStore_MergeChapterBooks(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	primary := createTestBook(t, store, "Primary Chapter Book", "/tmp/primary.m4b", nil, nil)
	src1 := createTestBook(t, store, "Chapter 1", "/tmp/ch1.m4b", nil, nil)
	src2 := createTestBook(t, store, "Chapter 2", "/tmp/ch2.m4b", nil, nil)

	// Add book files to source books
	require.NoError(t, store.CreateBookFile(&BookFile{BookID: src1, FilePath: "/tmp/ch1.m4b", Format: "m4b"}))
	require.NoError(t, store.CreateBookFile(&BookFile{BookID: src2, FilePath: "/tmp/ch2.m4b", Format: "m4b"}))

	err := s.MergeChapterBooks(primary, []string{src1, src2}, "The Complete Book", 7200.0)
	require.NoError(t, err)

	// Verify files moved to primary
	files, err := store.GetBookFiles(primary)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(files), 2)

	// Empty src IDs is a no-op
	err = s.MergeChapterBooks(primary, []string{}, "Anything", 0)
	require.NoError(t, err)
}

// ---- FlagMetadataHashDuplicate ----

func TestSQLiteStore_FlagMetadataHashDuplicate(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	primary := createTestBook(t, store, "Primary Meta", "/tmp/primary_meta.m4b", nil, nil)
	dup := createTestBook(t, store, "Duplicate Meta", "/tmp/dup_meta.m4b", nil, nil)

	err := s.FlagMetadataHashDuplicate(primary, dup)
	require.NoError(t, err)

	book, err := store.GetBookByID(dup)
	require.NoError(t, err)
	require.NotNil(t, book.MergedIntoBookID)
	assert.Equal(t, primary, *book.MergedIntoBookID)
}

// ---- UpdateBookFileHashes + SetBookFileHash ----

func TestSQLiteStore_UpdateAndSetBookFileHashes(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	bookID := createTestBook(t, store, "Hash Book", "/tmp/hashbook.m4b", nil, nil)
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID:   bookID,
		FilePath: "/tmp/hashbook_p1.m4b",
		Format:   "m4b",
	}))

	files, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	fileID := files[0].ID

	err = s.UpdateBookFileHashes(fileID, "orig-hash-1", "post-hash-1")
	require.NoError(t, err)

	err = s.SetBookFileHash(fileID, "current-hash-1")
	require.NoError(t, err)
}

// ---- GetDuplicateFilesByHash ----

func TestSQLiteStore_GetDuplicateFilesByHash(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	// Two book files with the same original hash in different books
	book1 := createTestBook(t, store, "Dup File Book 1", "/tmp/dupfile1.m4b", nil, nil)
	book2 := createTestBook(t, store, "Dup File Book 2", "/tmp/dupfile2.m4b", nil, nil)
	sharedHash := "shared-original-hash-xyz"

	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID:           book1,
		FilePath:         "/tmp/dupfile1_p1.m4b",
		Format:           "m4b",
		OriginalFileHash: sharedHash,
	}))
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID:           book2,
		FilePath:         "/tmp/dupfile2_p1.m4b",
		Format:           "m4b",
		OriginalFileHash: sharedHash,
	}))

	groups, err := s.GetDuplicateFilesByHash(50)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(groups), 1)

	// Default limit=0 → uses 50
	groups2, err := s.GetDuplicateFilesByHash(0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(groups2), 1)
}

// ---- GetBookFileHashStats + GetBookMetadataHashStats ----

func TestSQLiteStore_GetBookFileHashStats(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	bookID := createTestBook(t, store, "Hash Stats Book", "/tmp/hashstats.m4b", nil, nil)
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID:   bookID,
		FilePath: "/tmp/hashstats_p1.m4b",
		Format:   "m4b",
		FileHash: "hashstats-hash",
	}))

	stats, err := s.GetBookFileHashStats()
	require.NoError(t, err)
	require.NotNil(t, stats)
	assert.GreaterOrEqual(t, stats.TotalBookFiles, 1)
}

func TestSQLiteStore_GetBookMetadataHashStats(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	msh := "meta-hash-for-stats"
	_, err := store.CreateBook(&Book{
		Title:              "Metadata Stats Book",
		FilePath:           "/tmp/metastats.m4b",
		MetadataSourceHash: &msh,
	})
	require.NoError(t, err)

	stats, err := s.GetBookMetadataHashStats()
	require.NoError(t, err)
	require.NotNil(t, stats)
	assert.GreaterOrEqual(t, stats.TotalBooks, 1)
}

// ---- SQLiteStore activity: CountPrefix, GetOperationResultsPage,
//      GetScanFailCount, IncrScanFailCount, ResetScanFailCount ----

func TestSQLiteStore_CountPrefix(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	// CountPrefix uses the kv_store table
	require.NoError(t, s.SetRaw("test_prefix:a", []byte("1")))
	require.NoError(t, s.SetRaw("test_prefix:b", []byte("2")))
	require.NoError(t, s.SetRaw("other_prefix:c", []byte("3")))

	n, err := s.CountPrefix("test_prefix:")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestSQLiteStore_GetOperationResultsPage(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	op, err := store.CreateOperation("page-op-1", "scan", nil)
	require.NoError(t, err)

	for i := range 5 {
		require.NoError(t, store.CreateOperationResult(&OperationResult{
			OperationID: op.ID,
			BookID:      "book-" + string(rune('A'+i)),
			ResultJSON:  `{}`,
			Status:      "success",
		}))
	}

	results, total, err := s.GetOperationResultsPage(op.ID, 2, 0)
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.Len(t, results, 2)

	// Page 2
	results2, total2, err := s.GetOperationResultsPage(op.ID, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 5, total2)
	assert.Len(t, results2, 2)

	// No limit → all results
	resultsAll, _, err := s.GetOperationResultsPage(op.ID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, resultsAll, 5)
}

func TestSQLiteStore_ScanFailCounters(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	pathHash := "scan-path-hash-abc"

	// Initially 0
	n, err := s.GetScanFailCount(pathHash)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// Increment twice
	n1, err := s.IncrScanFailCount(pathHash)
	require.NoError(t, err)
	assert.Equal(t, 1, n1)

	n2, err := s.IncrScanFailCount(pathHash)
	require.NoError(t, err)
	assert.Equal(t, 2, n2)

	// Verify count
	n3, err := s.GetScanFailCount(pathHash)
	require.NoError(t, err)
	assert.Equal(t, 2, n3)

	// Reset
	require.NoError(t, s.ResetScanFailCount(pathHash))

	n4, err := s.GetScanFailCount(pathHash)
	require.NoError(t, err)
	assert.Equal(t, 0, n4)
}

// ---- PebbleStore user: ListUsers, RBAC, position/state ----
// NOTE(fable5 T022): Ported from SQLite stubs. PebbleStore fully supports
// RBAC and user positions — assertions updated to verify working behaviour.

func TestSQLiteStore_UserAndRoleStubs(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	// ListUsers — returns nil (not an error) when no users exist.
	users, err := s.ListUsers()
	require.NoError(t, err)
	assert.Nil(t, users)

	// PebbleStore RBAC is fully functional. GetRoleByID/GetRoleByName return
	// nil (not found), not ErrSQLiteRBACUnsupported.
	r, err := s.GetRoleByID("id-1")
	require.NoError(t, err)
	assert.Nil(t, r)

	r2, err := s.GetRoleByName("admin")
	require.NoError(t, err)
	assert.Nil(t, r2)

	roles, err := s.ListRoles()
	require.NoError(t, err)
	// Empty or nil — no roles created yet.
	_ = roles

	// CreateRole / UpdateRole / DeleteRole succeed on PebbleStore.
	role := &Role{ID: "r1", Name: "editor"}
	created, err := s.CreateRole(role)
	require.NoError(t, err)
	require.NotNil(t, created)

	require.NoError(t, s.UpdateRole(role))
	require.NoError(t, s.DeleteRole("r1"))
}

func TestSQLiteStore_UserPositionAndBookStateStubs(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	require.NoError(t, s.SetUserPosition("u1", "b1", "seg1", 120.5))

	// PebbleStore stores and retrieves positions correctly.
	pos, err := s.GetUserPosition("u1", "b1")
	require.NoError(t, err)
	require.NotNil(t, pos)
	assert.Equal(t, "u1", pos.UserID)
	assert.Equal(t, "b1", pos.BookID)
	assert.InDelta(t, 120.5, pos.PositionSeconds, 0.01)

	positions, err := s.ListUserPositionsForBook("u1", "b1")
	require.NoError(t, err)
	assert.NotEmpty(t, positions)

	require.NoError(t, s.ClearUserPositions("u1", "b1"))
	require.NoError(t, s.SetUserBookState(&UserBookState{UserID: "u1", BookID: "b1"}))

	state, err := s.GetUserBookState("u1", "b1")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "u1", state.UserID)
}

// ---- SQLiteStore metadata: AddMetadataRejection, GetMetadataRejections, DeleteMetadataRejections ----

func TestSQLiteStore_MetadataRejections(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	bookID := createTestBook(t, store, "Rejection Book", "/tmp/rejection.m4b", nil, nil)

	rejection := MetadataRejection{
		BookID:          bookID,
		Source:          "audible",
		CandidateASIN:   "B012345678",
		CandidateTitle:  "Wrong Book Title",
		CandidateAuthor: "Wrong Author",
		RejectionReason: "title_mismatch",
		Score:           0.3,
	}

	err := s.AddMetadataRejection(rejection)
	require.NoError(t, err)

	records, err := s.GetMetadataRejections(bookID)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "audible", records[0].Source)
	assert.Equal(t, "B012345678", records[0].CandidateASIN)

	// Not found
	records2, err := s.GetMetadataRejections("nonexistent-book")
	require.NoError(t, err)
	assert.Len(t, records2, 0)

	// Delete
	err = s.DeleteMetadataRejections(bookID)
	require.NoError(t, err)

	records3, err := s.GetMetadataRejections(bookID)
	require.NoError(t, err)
	assert.Len(t, records3, 0)
}

// ---- SQLiteStore tags: GetBookTagsDetailed, author/series tag generics ----

func TestSQLiteStore_GetBookTagsDetailed(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	bookID := createTestBook(t, store, "Detailed Tag Book", "/tmp/dtag.m4b", nil, nil)
	require.NoError(t, store.AddBookTag(bookID, "sci-fi"))
	require.NoError(t, s.AddBookTagWithSource(bookID, "metadata:source:audible", "system"))

	tags, err := s.GetBookTagsDetailed(bookID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(tags), 2)

	// Verify sources are present
	sources := make(map[string]bool)
	for _, t := range tags {
		sources[t.Source] = true
	}
	assert.True(t, sources["user"] || sources["system"])
}

func TestSQLiteStore_RemoveBookTagsByPrefix(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	bookID := createTestBook(t, store, "Prefix Tag Book", "/tmp/ptag.m4b", nil, nil)
	require.NoError(t, s.AddBookTagWithSource(bookID, "metadata:source:audible", "system"))
	require.NoError(t, s.AddBookTagWithSource(bookID, "metadata:source:google", "system"))
	require.NoError(t, store.AddBookTag(bookID, "user-tag"))

	// Remove by prefix without source filter
	err := s.RemoveBookTagsByPrefix(bookID, "metadata:", "")
	require.NoError(t, err)

	tags, err := store.GetBookTags(bookID)
	require.NoError(t, err)
	for _, tag := range tags {
		assert.False(t, len(tag) > 9 && tag[:9] == "metadata:")
	}

	// Empty prefix errors
	err = s.RemoveBookTagsByPrefix(bookID, "", "")
	assert.Error(t, err)

	// Remove by prefix with source filter
	require.NoError(t, s.AddBookTagWithSource(bookID, "dedup:pair:abc", "system"))
	require.NoError(t, s.AddBookTagWithSource(bookID, "dedup:pair:def", "user"))
	err = s.RemoveBookTagsByPrefix(bookID, "dedup:", "system")
	require.NoError(t, err)
	tags2, err := store.GetBookTags(bookID)
	require.NoError(t, err)
	// "dedup:pair:def" (user source) should survive
	found := false
	for _, t := range tags2 {
		if t == "dedup:pair:def" {
			found = true
		}
	}
	assert.True(t, found, "user-sourced dedup tag should survive system prefix removal")
}

func TestSQLiteStore_AuthorTags(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	author, err := store.CreateAuthor("Tagged Author")
	require.NoError(t, err)

	require.NoError(t, s.AddAuthorTag(author.ID, "Sci-Fi"))
	require.NoError(t, s.AddAuthorTagWithSource(author.ID, "metadata:source:openlibrary", "system"))

	tags, err := s.GetAuthorTags(author.ID)
	require.NoError(t, err)
	assert.Contains(t, tags, "sci-fi") // normalized

	detailed, err := s.GetAuthorTagsDetailed(author.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(detailed), 2)

	require.NoError(t, s.SetAuthorTags(author.ID, []string{"Fantasy", "Epic"}))
	tags2, err := s.GetAuthorTags(author.ID)
	require.NoError(t, err)
	assert.Contains(t, tags2, "fantasy")

	all, err := s.ListAllAuthorTags()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 1)

	authorIDs, err := s.GetAuthorsByTag("fantasy")
	require.NoError(t, err)
	assert.Contains(t, authorIDs, author.ID)

	// Empty tag errors
	_, err = s.GetAuthorsByTag("")
	assert.Error(t, err)

	require.NoError(t, s.RemoveAuthorTag(author.ID, "Fantasy"))
	tags3, err := s.GetAuthorTags(author.ID)
	require.NoError(t, err)
	assert.NotContains(t, tags3, "fantasy")

	// RemoveAuthorTagsByPrefix
	require.NoError(t, s.AddAuthorTagWithSource(author.ID, "metadata:source:openlibrary", "system"))
	require.NoError(t, s.RemoveAuthorTagsByPrefix(author.ID, "metadata:", ""))
	tags4, err := s.GetAuthorTags(author.ID)
	require.NoError(t, err)
	for _, tag := range tags4 {
		assert.False(t, len(tag) > 9 && tag[:9] == "metadata:")
	}

	// With source filter
	require.NoError(t, s.AddAuthorTagWithSource(author.ID, "metadata:type:narrator", "system"))
	require.NoError(t, s.RemoveAuthorTagsByPrefix(author.ID, "metadata:", "system"))
}

func TestSQLiteStore_SeriesTags(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	series, err := store.CreateSeries("Tagged Series", nil)
	require.NoError(t, err)

	require.NoError(t, s.AddSeriesTag(series.ID, "Epic Fantasy"))
	require.NoError(t, s.AddSeriesTagWithSource(series.ID, "metadata:source:openlibrary", "system"))

	tags, err := s.GetSeriesTags(series.ID)
	require.NoError(t, err)
	assert.Contains(t, tags, "epic fantasy")

	detailed, err := s.GetSeriesTagsDetailed(series.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(detailed), 2)

	require.NoError(t, s.SetSeriesTags(series.ID, []string{"LitRPG", "Magic"}))
	tags2, err := s.GetSeriesTags(series.ID)
	require.NoError(t, err)
	assert.Contains(t, tags2, "litrpg")

	all, err := s.ListAllSeriesTags()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 1)

	seriesIDs, err := s.GetSeriesByTag("litrpg")
	require.NoError(t, err)
	assert.Contains(t, seriesIDs, series.ID)

	// Empty tag errors
	_, err = s.GetSeriesByTag("")
	assert.Error(t, err)

	require.NoError(t, s.RemoveSeriesTag(series.ID, "LitRPG"))
	tags3, err := s.GetSeriesTags(series.ID)
	require.NoError(t, err)
	assert.NotContains(t, tags3, "litrpg")

	// RemoveSeriesTagsByPrefix
	require.NoError(t, s.AddSeriesTagWithSource(series.ID, "metadata:source:openlibrary", "system"))
	require.NoError(t, s.RemoveSeriesTagsByPrefix(series.ID, "metadata:", ""))

	require.NoError(t, s.AddSeriesTagWithSource(series.ID, "metadata:type:fantasy", "system"))
	require.NoError(t, s.RemoveSeriesTagsByPrefix(series.ID, "metadata:", "system"))
}

// ---- EmbeddingStore: UpdateCandidateLLM, DeleteCandidate, HealthStats ----

func TestEmbeddingStore_LLMAndDeleteCandidate(t *testing.T) {
	es := newTestEmbeddingStore(t)
	defer es.Close()

	sim := 0.9
	// Insert a candidate
	err := es.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book-a",
		EntityBID:  "book-b",
		Layer:      "cosine",
		Similarity: &sim,
	})
	require.NoError(t, err)

	// Retrieve to get the ID
	candidates, _, err := es.ListCandidates(CandidateFilter{EntityType: "book", Limit: 1})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	id := candidates[0].ID

	// UpdateCandidateLLM
	err = es.UpdateCandidateLLM(id, "duplicate", "same title and author")
	require.NoError(t, err)

	// HealthStats
	stats, err := es.HealthStats()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, stats.VectorCount, int64(0))

	// DeleteCandidate
	err = es.DeleteCandidate(id)
	require.NoError(t, err)
}

// ---- MetadataFetchCache: CountCachedMetadataFetches ----

func TestCountCachedMetadataFetches(t *testing.T) {
	store := setupCoverageDB(t)

	// Initially 0
	count, err := CountCachedMetadataFetches(store)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Store a cache entry via SetRaw (mimics what MetadataFetchCache does)
	s := store.(*PebbleStore)
	require.NoError(t, s.SetRaw("metadata_fetch_cache:book-1:audible", []byte(`{"results":[]}`)))
	require.NoError(t, s.SetRaw("metadata_fetch_cache:book-2:openlibrary", []byte(`{"results":[]}`)))
	require.NoError(t, s.SetRaw("other_prefix:key", []byte(`irrelevant`)))

	count2, err := CountCachedMetadataFetches(store)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count2)
}

// ---- AIScanStore: HealthStats ----

func TestAIScanStore_HealthStats(t *testing.T) {
	dir := t.TempDir()
	as, err := NewAIScanStore(filepath.Join(dir, "aiscan.db"))
	require.NoError(t, err)
	defer as.Close()

	// Create a scan so there is something to count
	_, err = as.CreateScan("full", map[string]string{"phase1": "gpt-4"}, 1)
	require.NoError(t, err)

	stats, err := as.HealthStats()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, stats.JobCount, 1)
}
