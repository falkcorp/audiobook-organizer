// file: internal/database/pebble_store_test.go
// version: 1.7.0
// guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-07-06

package database

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// setupPebbleTestDB creates a temporary PebbleDB database for testing
// Returns the store and a cleanup function
func setupPebbleTestDB(t *testing.T) (Store, func()) {
	// Create temporary database directory with unique name
	tmpdir := "/tmp/test_pebble_" + ulid.Make().String()

	// Create the store
	store, err := NewPebbleStore(tmpdir)
	if err != nil {
		t.Fatalf("Failed to create test Pebble database: %v", err)
	}
	// Wait for the async memdb warmup to settle BEFORE any test writes, so
	// write-throughs land in the published memdb and GetAll* reads are
	// deterministic. Without this the warmup race drops some write-throughs and
	// surfaces as order-dependent "expected N, got N-1" flakes under -race
	// (e.g. TestPebbleGetAllAuthors).
	store.WaitForWarmup()

	// Cleanup function removes the database directory
	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpdir)
	}

	return store, cleanup
}

// TestNewPebbleStore tests Pebble store creation
func TestNewPebbleStore(t *testing.T) {
	// Arrange-Act
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Assert
	if store == nil {
		t.Fatal("Expected non-nil store")
	}
}

// TestPebbleCreateAndGetBook tests basic book CRUD operations
func TestPebbleCreateAndGetBook(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create test author
	createdAuthor, err := store.CreateAuthor("Test Author")
	if err != nil {
		t.Fatalf("Failed to create author: %v", err)
	}

	// Create test book
	book := &Book{
		Title:    "Test Book",
		AuthorID: &createdAuthor.ID,
		FilePath: "/test/path/book.mp3",
	}

	// Act
	createdBook, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("Failed to create book: %v", err)
	}

	// Assert
	if createdBook.ID == "" {
		t.Error("Expected non-empty book ID (ULID)")
	}

	// Retrieve the book by ID
	retrievedBook, err := store.GetBookByID(createdBook.ID)
	if err != nil {
		t.Fatalf("Failed to get book by ID: %v", err)
	}

	if retrievedBook.Title != "Test Book" {
		t.Errorf("Expected title 'Test Book', got '%s'", retrievedBook.Title)
	}

	if retrievedBook.AuthorID == nil || *retrievedBook.AuthorID != *book.AuthorID {
		t.Error("Author ID mismatch")
	}

	// Test GetBookByFilePath
	bookByPath, err := store.GetBookByFilePath("/test/path/book.mp3")
	if err != nil {
		t.Fatalf("Failed to get book by file path: %v", err)
	}

	if bookByPath.ID != createdBook.ID {
		t.Error("Expected same book when retrieved by file path")
	}
}

// TestPebbleUpdateBook tests book update operations
func TestPebbleUpdateBook(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create test book
	book := &Book{
		Title:    "Original Title",
		FilePath: "/test/path/book.mp3",
	}
	createdBook, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("Failed to create book: %v", err)
	}

	// Act - Update the book
	createdBook.Title = "Updated Title"
	narrator := "Test Narrator"
	createdBook.Narrator = &narrator

	updatedBook, err := store.UpdateBook(createdBook.ID, createdBook)
	if err != nil {
		t.Fatalf("Failed to update book: %v", err)
	}

	// Assert
	if updatedBook.Title != "Updated Title" {
		t.Errorf("Expected title 'Updated Title', got '%s'", updatedBook.Title)
	}

	if updatedBook.Narrator == nil || *updatedBook.Narrator != "Test Narrator" {
		t.Error("Narrator not updated correctly")
	}
}

// TestPebbleDeleteBook tests book deletion
func TestPebbleDeleteBook(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create test book
	book := &Book{
		Title:    "Book to Delete",
		FilePath: "/test/path/book.mp3",
	}
	createdBook, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("Failed to create book: %v", err)
	}

	// Act - Delete the book
	err = store.DeleteBook(createdBook.ID)
	if err != nil {
		t.Fatalf("Failed to delete book: %v", err)
	}

	// Assert - Verify deletion (GetBookByID returns nil book, not error)
	deletedBook, err := store.GetBookByID(createdBook.ID)
	if err != nil {
		t.Fatalf("Unexpected error when getting deleted book: %v", err)
	}
	if deletedBook != nil {
		t.Error("Expected book to be nil after deletion")
	}
}

// TestPebbleDeleteBook_RemovesEmbedding locks in the fix for the orphaned-
// embedding leak: DeleteBook must also delete the book's embedding row
// (emb:v:book:<id>). Before this fix, deleting a book (e.g. the "loser" side
// of a dedup merge) left its embedding behind forever at whatever
// model/dimension it was last embedded with, since dedup.embed-scan only
// ever iterates GetAllBooks and a deleted book never appears there again.
func TestPebbleDeleteBook_RemovesEmbedding(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	pebbleStore, ok := store.(*PebbleStore)
	if !ok {
		t.Fatalf("expected *PebbleStore, got %T", store)
	}

	book := &Book{
		Title:    "Book With Embedding",
		FilePath: "/test/path/embedded-book.mp3",
	}
	createdBook, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("Failed to create book: %v", err)
	}

	embStore := NewEmbeddingStore(pebbleStore.DB())
	if err := embStore.Upsert(Embedding{
		EntityType: "book",
		EntityID:   createdBook.ID,
		Vector:     []float32{0.1, 0.2, 0.3},
		Model:      "text-embedding-3-large",
	}); err != nil {
		t.Fatalf("Failed to upsert embedding: %v", err)
	}

	// Sanity check: embedding is present before delete.
	before, err := embStore.Get("book", createdBook.ID)
	if err != nil {
		t.Fatalf("Failed to get embedding before delete: %v", err)
	}
	if before == nil {
		t.Fatal("Expected embedding to exist before DeleteBook")
	}

	// Act
	if err := store.DeleteBook(createdBook.ID); err != nil {
		t.Fatalf("Failed to delete book: %v", err)
	}

	// Assert - book row is gone
	deletedBook, err := store.GetBookByID(createdBook.ID)
	if err != nil {
		t.Fatalf("Unexpected error when getting deleted book: %v", err)
	}
	if deletedBook != nil {
		t.Error("Expected book to be nil after deletion")
	}

	// Assert - embedding row is gone too (the orphan-leak fix)
	after, err := embStore.Get("book", createdBook.ID)
	if err != nil {
		t.Fatalf("Unexpected error when getting embedding after delete: %v", err)
	}
	if after != nil {
		t.Error("Expected embedding to be deleted along with the book, but it still exists")
	}
}

// TestPebbleGetAllBooks tests book listing with pagination
func TestPebbleGetAllBooks(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create multiple books
	for i := 0; i < 5; i++ {
		book := &Book{
			Title:    "Book " + string(rune('A'+i)),
			FilePath: "/test/path/book" + string(rune('A'+i)) + ".mp3",
		}
		_, err := store.CreateBook(book)
		if err != nil {
			t.Fatalf("Failed to create book %d: %v", i, err)
		}
	}

	// Act - List books with pagination
	books, err := store.GetAllBooks(10, 0)
	if err != nil {
		t.Fatalf("Failed to list books: %v", err)
	}

	// Assert
	if len(books) != 5 {
		t.Errorf("Expected 5 books, got %d", len(books))
	}
}

// TestPebbleGetBooksBySeriesIDCore tests filtering books by series
func TestPebbleGetBooksBySeriesIDCore(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create author
	author, err := store.CreateAuthor("Series Author")
	if err != nil {
		t.Fatalf("Failed to create author: %v", err)
	}

	// Create series
	series, err := store.CreateSeries("Test Series", &author.ID)
	if err != nil {
		t.Fatalf("Failed to create series: %v", err)
	}

	// Create books in series
	for i := 0; i < 3; i++ {
		seq := i + 1
		book := &Book{
			Title:          "Series Book " + string(rune('A'+i)),
			FilePath:       "/test/series/book" + string(rune('A'+i)) + ".mp3",
			SeriesID:       &series.ID,
			SeriesSequence: &seq,
		}
		_, err := store.CreateBook(book)
		if err != nil {
			t.Fatalf("Failed to create book %d: %v", i, err)
		}
	}

	// Act - Get books by series
	seriesBooks, err := store.GetBooksBySeriesIDCore(series.ID)
	if err != nil {
		t.Fatalf("Failed to get books by series: %v", err)
	}

	// Assert
	if len(seriesBooks) != 3 {
		t.Errorf("Expected 3 books in series, got %d", len(seriesBooks))
	}

	// Verify books are in series
	for _, book := range seriesBooks {
		if book.SeriesID == nil || *book.SeriesID != series.ID {
			t.Error("Book not properly associated with series")
		}
	}
}

// TestPebbleGetBooksByAuthorIDCore tests filtering books by author
func TestPebbleGetBooksByAuthorIDCore(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create author
	author, err := store.CreateAuthor("Author with Books")
	if err != nil {
		t.Fatalf("Failed to create author: %v", err)
	}

	// Create books by author
	for i := 0; i < 3; i++ {
		book := &Book{
			Title:    "Author Book " + string(rune('A'+i)),
			FilePath: "/test/author/book" + string(rune('A'+i)) + ".mp3",
			AuthorID: &author.ID,
		}
		_, err := store.CreateBook(book)
		if err != nil {
			t.Fatalf("Failed to create book %d: %v", i, err)
		}
	}

	// Act - Get books by author
	authorBooks, err := store.GetBooksByAuthorIDCore(author.ID)
	if err != nil {
		t.Fatalf("Failed to get books by author: %v", err)
	}

	// Assert
	if len(authorBooks) != 3 {
		t.Errorf("Expected 3 books by author, got %d", len(authorBooks))
	}

	// Verify books are by author
	for _, book := range authorBooks {
		if book.AuthorID == nil || *book.AuthorID != author.ID {
			t.Error("Book not properly associated with author")
		}
	}
}

// TestPebbleVersionManagement tests book version grouping
func TestPebbleVersionManagement(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create two books
	book1 := &Book{
		Title:    "Book Version 1",
		FilePath: "/test/path/book_v1.mp3",
	}
	createdBook1, err := store.CreateBook(book1)
	if err != nil {
		t.Fatalf("Failed to create book 1: %v", err)
	}

	book2 := &Book{
		Title:    "Book Version 2",
		FilePath: "/test/path/book_v2.mp3",
	}
	createdBook2, err := store.CreateBook(book2)
	if err != nil {
		t.Fatalf("Failed to create book 2: %v", err)
	}

	// Act - Link books as versions
	groupID := ulid.Make().String()
	createdBook1.VersionGroupID = &groupID
	isPrimary := true
	createdBook1.IsPrimaryVersion = &isPrimary

	_, err = store.UpdateBook(createdBook1.ID, createdBook1)
	if err != nil {
		t.Fatalf("Failed to update book 1 with version group: %v", err)
	}

	createdBook2.VersionGroupID = &groupID
	isPrimaryFalse := false
	createdBook2.IsPrimaryVersion = &isPrimaryFalse

	_, err = store.UpdateBook(createdBook2.ID, createdBook2)
	if err != nil {
		t.Fatalf("Failed to update book 2 with version group: %v", err)
	}

	// Assert - Get books by version group
	versions, err := store.GetBooksByVersionGroup(groupID)
	if err != nil {
		t.Fatalf("Failed to get books by version group: %v", err)
	}

	if len(versions) != 2 {
		t.Errorf("Expected 2 versions, got %d", len(versions))
	}

	// Verify primary version
	foundPrimary := false
	for _, v := range versions {
		if v.IsPrimaryVersion != nil && *v.IsPrimaryVersion {
			foundPrimary = true
			if v.ID != createdBook1.ID {
				t.Error("Wrong book marked as primary")
			}
		}
	}
	if !foundPrimary {
		t.Error("No primary version found")
	}
}

// TestPebbleCreateAndGetAuthor tests author CRUD operations
func TestPebbleCreateAndGetAuthor(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Act
	createdAuthor, err := store.CreateAuthor("J.R.R. Tolkien")
	if err != nil {
		t.Fatalf("Failed to create author: %v", err)
	}

	// Assert
	if createdAuthor.ID == 0 {
		t.Error("Expected non-zero author ID")
	}

	// Get author by ID
	retrievedAuthor, err := store.GetAuthorByID(createdAuthor.ID)
	if err != nil {
		t.Fatalf("Failed to get author: %v", err)
	}

	if retrievedAuthor.Name != "J.R.R. Tolkien" {
		t.Errorf("Expected name 'J.R.R. Tolkien', got '%s'", retrievedAuthor.Name)
	}

	// Get author by name
	authorByName, err := store.GetAuthorByName("J.R.R. Tolkien")
	if err != nil {
		t.Fatalf("Failed to get author by name: %v", err)
	}

	if authorByName.ID != createdAuthor.ID {
		t.Error("Expected same author ID when retrieved by name")
	}
}

// TestPebbleGetAllAuthors tests listing all authors
func TestPebbleGetAllAuthors(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create multiple authors
	authorNames := []string{"Author A", "Author B", "Author C"}
	for _, name := range authorNames {
		_, err := store.CreateAuthor(name)
		if err != nil {
			t.Fatalf("Failed to create author '%s': %v", name, err)
		}
	}

	// Act
	authors, err := store.GetAllAuthors()
	if err != nil {
		t.Fatalf("Failed to get all authors: %v", err)
	}

	// Assert
	if len(authors) != 3 {
		t.Errorf("Expected 3 authors, got %d", len(authors))
	}
}

// TestPebbleCreateAndGetSeries tests series CRUD operations
func TestPebbleCreateAndGetSeries(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create author
	author, err := store.CreateAuthor("Series Author")
	if err != nil {
		t.Fatalf("Failed to create author: %v", err)
	}

	// Act
	createdSeries, err := store.CreateSeries("Test Series", &author.ID)
	if err != nil {
		t.Fatalf("Failed to create series: %v", err)
	}

	// Assert
	if createdSeries.ID == 0 {
		t.Error("Expected non-zero series ID")
	}

	// Get series by ID
	retrievedSeries, err := store.GetSeriesByID(createdSeries.ID)
	if err != nil {
		t.Fatalf("Failed to get series: %v", err)
	}

	if retrievedSeries.Name != "Test Series" {
		t.Errorf("Expected series name 'Test Series', got '%s'", retrievedSeries.Name)
	}

	// Get series by name
	seriesByName, err := store.GetSeriesByName("Test Series", &author.ID)
	if err != nil {
		t.Fatalf("Failed to get series by name: %v", err)
	}

	if seriesByName.ID != createdSeries.ID {
		t.Error("Expected same series ID when retrieved by name")
	}
}

// TestPebbleGetAllSeries tests listing all series
func TestPebbleGetAllSeries(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create author
	author, err := store.CreateAuthor("Prolific Author")
	if err != nil {
		t.Fatalf("Failed to create author: %v", err)
	}

	// Create multiple series
	seriesNames := []string{"Series A", "Series B", "Series C"}
	for _, name := range seriesNames {
		_, err := store.CreateSeries(name, &author.ID)
		if err != nil {
			t.Fatalf("Failed to create series '%s': %v", name, err)
		}
	}

	// Act
	series, err := store.GetAllSeries()
	if err != nil {
		t.Fatalf("Failed to get all series: %v", err)
	}

	// Assert
	if len(series) != 3 {
		t.Errorf("Expected 3 series, got %d", len(series))
	}
}

// TestPebbleSearchBooks tests book search functionality
func TestPebbleSearchBooks(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create test books with searchable titles
	testBooks := []string{
		"The Hobbit",
		"The Lord of the Rings",
		"The Silmarillion",
		"Harry Potter",
	}

	for i, title := range testBooks {
		book := &Book{
			Title:    title,
			FilePath: "/test/search/book" + string(rune('0'+i)) + ".mp3",
		}
		_, err := store.CreateBook(book)
		if err != nil {
			t.Fatalf("Failed to create book '%s': %v", title, err)
		}
	}

	// Act - Search for "The"
	results, err := store.SearchBooks("The", 10, 0)
	if err != nil {
		t.Fatalf("Failed to search books: %v", err)
	}

	// Assert
	if len(results) < 3 {
		t.Errorf("Expected at least 3 results for 'The', got %d", len(results))
	}
}

// TestSearchBooksUnlimited verifies that limit=0 returns all matching results
// (regression for PERF-4: count < 0 was always false, producing zero results).
func TestSearchBooksUnlimited(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		_, err := store.CreateBook(&Book{
			Title:    "Unlimited Test Book " + string(rune('A'+i)),
			FilePath: "/test/unlimited/book" + string(rune('A'+i)) + ".mp3",
		})
		if err != nil {
			t.Fatalf("create book %d: %v", i, err)
		}
	}

	// limit=0 should return all 5 matches, not zero
	results, err := store.SearchBooks("Unlimited Test", 0, 0)
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(results) < 5 {
		t.Errorf("limit=0 should return all matches; got %d, want ≥5", len(results))
	}
}

// TestPebbleCountPrimaryBooks tests book counting
func TestPebbleCountPrimaryBooks(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	initialCount, err := store.CountPrimaryBooks()
	if err != nil {
		t.Fatalf("Failed to count books: %v", err)
	}

	// Create books
	for i := 0; i < 5; i++ {
		book := &Book{
			Title:    "Count Book " + string(rune('A'+i)),
			FilePath: "/test/count/book" + string(rune('A'+i)) + ".mp3",
		}
		_, err := store.CreateBook(book)
		if err != nil {
			t.Fatalf("Failed to create book %d: %v", i, err)
		}
	}

	// Act
	newCount, err := store.CountPrimaryBooks()
	if err != nil {
		t.Fatalf("Failed to count books after creation: %v", err)
	}

	// Assert
	if newCount != initialCount+5 {
		t.Errorf("Expected count to increase by 5, got %d -> %d", initialCount, newCount)
	}
}

// TestPebbleImportPaths tests import path management
func TestPebbleImportPaths(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Act - Create import path
	folder, err := store.CreateImportPath("/media/audiobooks", "Main Library")
	if err != nil {
		t.Fatalf("Failed to create import path: %v", err)
	}

	// Assert
	if folder.ID == 0 {
		t.Error("Expected non-zero import path ID")
	}

	// Get import path by ID
	retrievedFolder, err := store.GetImportPathByID(folder.ID)
	if err != nil {
		t.Fatalf("Failed to get import path: %v", err)
	}

	if retrievedFolder.Path != "/media/audiobooks" {
		t.Errorf("Expected path '/media/audiobooks', got '%s'", retrievedFolder.Path)
	}

	// Get import path by path
	folderByPath, err := store.GetImportPathByPath("/media/audiobooks")
	if err != nil {
		t.Fatalf("Failed to get import path by path: %v", err)
	}

	if folderByPath.ID != folder.ID {
		t.Error("Expected same folder ID when retrieved by path")
	}

	// List all import paths
	folders, err := store.GetAllImportPaths()
	if err != nil {
		t.Fatalf("Failed to get all import paths: %v", err)
	}

	if len(folders) != 1 {
		t.Errorf("Expected 1 import path, got %d", len(folders))
	}
}

func TestPebbleFormatVersion(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ps := store.(*PebbleStore)
	version := ps.db.FormatMajorVersion()
	if version != pebble.FormatNewest {
		t.Errorf("Expected FormatNewest (%s), got %s", pebble.FormatNewest, version)
	}
}

func TestPebbleFormatVersion_UpgradesExisting(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pebble-upgrade")

	// Create a DB at minimum supported format
	db, err := pebble.Open(dbPath, &pebble.Options{
		FormatMajorVersion: pebble.FormatFlushableIngest,
	})
	if err != nil {
		t.Fatalf("Failed to create old-format DB: %v", err)
	}
	oldVersion := db.FormatMajorVersion()
	if err := db.Set([]byte("test:key"), []byte("value"), pebble.Sync); err != nil {
		t.Fatalf("Failed to write test key: %v", err)
	}
	db.Close()

	// Reopen via NewPebbleStore — should upgrade to FormatNewest
	store, err := NewPebbleStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to reopen with NewPebbleStore: %v", err)
	}
	defer store.Close()

	newVersion := store.db.FormatMajorVersion()
	if newVersion == oldVersion {
		t.Errorf("Format version was not upgraded: still %s", newVersion)
	}
	if newVersion != pebble.FormatNewest {
		t.Errorf("Expected FormatNewest (%s), got %s", pebble.FormatNewest, newVersion)
	}

	// Verify data survived the upgrade
	val, closer, err := store.db.Get([]byte("test:key"))
	if err != nil {
		t.Fatalf("Data lost after format upgrade: %v", err)
	}
	defer closer.Close()
	if string(val) != "value" {
		t.Errorf("Expected 'value', got %q", string(val))
	}
}

func TestPebbleMigrateImportPathKeys(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pebble-migrate")
	db, err := pebble.Open(dbPath, &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest,
	})
	if err != nil {
		t.Fatalf("Failed to open Pebble: %v", err)
	}
	defer db.Close()

	legacyFolder := []byte(`{"id":1,"path":"/legacy","name":"Legacy","enabled":true,"book_count":0}`)
	if err := db.Set([]byte("library:1"), legacyFolder, pebble.Sync); err != nil {
		t.Fatalf("Failed to write legacy key: %v", err)
	}
	if err := db.Set([]byte("library:path:/legacy"), []byte("1"), pebble.Sync); err != nil {
		t.Fatalf("Failed to write legacy index key: %v", err)
	}
	if err := db.Set([]byte("counter:library"), []byte("2"), pebble.Sync); err != nil {
		t.Fatalf("Failed to write legacy counter: %v", err)
	}

	store := &PebbleStore{db: db}
	if err := store.migrateImportPathKeys(); err != nil {
		t.Fatalf("migrateImportPathKeys failed: %v", err)
	}

	if _, closer, err := db.Get([]byte("import_path:1")); err != nil {
		t.Fatalf("expected migrated import_path key: %v", err)
	} else {
		closer.Close()
	}
	if _, closer, err := db.Get([]byte("import_path:path:/legacy")); err != nil {
		t.Fatalf("expected migrated import_path index: %v", err)
	} else {
		closer.Close()
	}

	if _, _, err := db.Get([]byte("library:1")); err != pebble.ErrNotFound {
		t.Fatalf("expected legacy key removed, got %v", err)
	}
	if _, _, err := db.Get([]byte("library:path:/legacy")); err != pebble.ErrNotFound {
		t.Fatalf("expected legacy index removed, got %v", err)
	}

	if val, closer, err := db.Get([]byte("counter:import_path")); err != nil {
		t.Fatalf("expected migrated counter: %v", err)
	} else {
		if string(val) != "2" {
			t.Fatalf("unexpected counter value: %s", string(val))
		}
		closer.Close()
	}
	if _, _, err := db.Get([]byte("counter:library")); err != pebble.ErrNotFound {
		t.Fatalf("expected legacy counter removed, got %v", err)
	}

	if err := store.migrateImportPathKeys(); err != nil {
		t.Fatalf("idempotent migration failed: %v", err)
	}
}

// TestPebbleOperations tests operation tracking
func TestPebbleOperations(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	operationID := ulid.Make().String()
	folderPath := "/media/audiobooks"

	// Act - Create operation
	op, err := store.CreateOperation(operationID, "scan", &folderPath)
	if err != nil {
		t.Fatalf("Failed to create operation: %v", err)
	}

	// Assert
	if op.ID != operationID {
		t.Errorf("Expected operation ID '%s', got '%s'", operationID, op.ID)
	}

	// Get operation by ID
	retrievedOp, err := store.GetOperationByID(operationID)
	if err != nil {
		t.Fatalf("Failed to get operation: %v", err)
	}

	if retrievedOp.Type != "scan" {
		t.Errorf("Expected operation type 'scan', got '%s'", retrievedOp.Type)
	}

	// Update operation status
	err = store.UpdateOperationStatus(operationID, "completed", 100, 100, "Scan completed")
	if err != nil {
		t.Fatalf("Failed to update operation status: %v", err)
	}

	// Verify update
	updatedOp, err := store.GetOperationByID(operationID)
	if err != nil {
		t.Fatalf("Failed to get updated operation: %v", err)
	}

	if updatedOp.Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", updatedOp.Status)
	}
}

// TestPebbleUserPreferences tests user preference storage
func TestPebbleUserPreferences(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Act - Set preference
	err := store.SetUserPreference("theme", "dark")
	if err != nil {
		t.Fatalf("Failed to set user preference: %v", err)
	}

	// Get preference
	pref, err := store.GetUserPreference("theme")
	if err != nil {
		t.Fatalf("Failed to get user preference: %v", err)
	}

	// Assert
	if pref.Value == nil || *pref.Value != "dark" {
		if pref.Value == nil {
			t.Error("Expected preference value to be set")
		} else {
			t.Errorf("Expected preference value 'dark', got '%s'", *pref.Value)
		}
	}

	// Get all preferences
	prefs, err := store.GetAllUserPreferences()
	if err != nil {
		t.Fatalf("Failed to get all preferences: %v", err)
	}

	if len(prefs) != 1 {
		t.Errorf("Expected 1 preference, got %d", len(prefs))
	}
}

func TestPebbleUpdateBookCreatesVersion(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	book := &Book{Title: "Original Title", FilePath: "/test/book.m4b"}
	created, err := store.CreateBook(book)
	require.NoError(t, err)

	created.Title = "Updated Title"
	updated, err := store.UpdateBook(created.ID, created)
	require.NoError(t, err)
	require.Equal(t, "Updated Title", updated.Title)

	versions, err := store.GetBookSnapshots(created.ID, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)

	var oldBook Book
	err = json.Unmarshal(versions[0].Data, &oldBook)
	require.NoError(t, err)
	require.Equal(t, "Original Title", oldBook.Title)
}

func TestPebbleGetBookAtVersion(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	book := &Book{Title: "V1", FilePath: "/test/v1.m4b"}
	created, err := store.CreateBook(book)
	require.NoError(t, err)

	created.Title = "V2"
	store.UpdateBook(created.ID, created)
	time.Sleep(time.Millisecond)
	created.Title = "V3"
	store.UpdateBook(created.ID, created)

	versions, err := store.GetBookSnapshots(created.ID, 10)
	require.NoError(t, err)
	require.Len(t, versions, 2)

	// Newest first, so versions[1] is V1
	oldBook, err := store.GetBookAtVersion(created.ID, versions[1].Timestamp)
	require.NoError(t, err)
	require.Equal(t, "V1", oldBook.Title)
}

func TestPebbleRevertBookToVersion(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	book := &Book{Title: "Original", FilePath: "/test/orig.m4b"}
	created, err := store.CreateBook(book)
	require.NoError(t, err)

	created.Title = "Modified"
	store.UpdateBook(created.ID, created)

	versions, err := store.GetBookSnapshots(created.ID, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)

	reverted, err := store.RevertBookToVersion(created.ID, versions[0].Timestamp)
	require.NoError(t, err)
	require.Equal(t, "Original", reverted.Title)

	current, err := store.GetBookByID(created.ID)
	require.NoError(t, err)
	require.Equal(t, "Original", current.Title)

	// Revert creates a new version (snapshot of "Modified")
	versions2, err := store.GetBookSnapshots(created.ID, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(versions2), 2)
}

func TestPebblePruneBookVersions(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	book := &Book{Title: "V1", FilePath: "/test/prune.m4b"}
	created, err := store.CreateBook(book)
	require.NoError(t, err)

	for i := 2; i <= 6; i++ {
		created.Title = fmt.Sprintf("V%d", i)
		store.UpdateBook(created.ID, created)
		time.Sleep(time.Millisecond)
	}

	versions, _ := store.GetBookSnapshots(created.ID, 100)
	require.Len(t, versions, 5)

	pruned, err := store.PruneBookSnapshots(created.ID, 2)
	require.NoError(t, err)
	require.Equal(t, 3, pruned)

	remaining, _ := store.GetBookSnapshots(created.ID, 100)
	require.Len(t, remaining, 2)
}

func TestPebbleAuthorAliases(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create an author
	author, err := store.CreateAuthor("Stephen King")
	require.NoError(t, err)

	// Create aliases
	alias1, err := store.CreateAuthorAlias(author.ID, "Richard Bachman", "pen_name")
	require.NoError(t, err)
	require.Equal(t, "Richard Bachman", alias1.AliasName)
	require.Equal(t, "pen_name", alias1.AliasType)
	require.Equal(t, author.ID, alias1.AuthorID)

	_, err = store.CreateAuthorAlias(author.ID, "John Swithen", "pen_name")
	require.NoError(t, err)

	// Get aliases by author
	aliases, err := store.GetAuthorAliases(author.ID)
	require.NoError(t, err)
	require.Len(t, aliases, 2)
	// Sorted by name
	require.Equal(t, "John Swithen", aliases[0].AliasName)
	require.Equal(t, "Richard Bachman", aliases[1].AliasName)

	// Get all aliases
	all, err := store.GetAllAuthorAliases()
	require.NoError(t, err)
	require.Len(t, all, 2)

	// Find author by alias (case-insensitive)
	found, err := store.FindAuthorByAlias("richard bachman")
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Equal(t, author.ID, found.ID)

	// Not found
	notFound, err := store.FindAuthorByAlias("nonexistent")
	require.NoError(t, err)
	require.Nil(t, notFound)

	// Duplicate alias
	_, err = store.CreateAuthorAlias(author.ID, "Richard Bachman", "pen_name")
	require.Error(t, err)

	// Delete alias
	err = store.DeleteAuthorAlias(alias1.ID)
	require.NoError(t, err)

	aliases, err = store.GetAuthorAliases(author.ID)
	require.NoError(t, err)
	require.Len(t, aliases, 1)
	require.Equal(t, "John Swithen", aliases[0].AliasName)

	// Lookup deleted alias should return nil
	found, err = store.FindAuthorByAlias("Richard Bachman")
	require.NoError(t, err)
	require.Nil(t, found)

	// Default alias type
	alias3, err := store.CreateAuthorAlias(author.ID, "The Master", "")
	require.NoError(t, err)
	require.Equal(t, "alias", alias3.AliasType)
}

func TestPebbleAuthorDeleteCascadesAliases(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	author, err := store.CreateAuthor("Mark Twain")
	require.NoError(t, err)

	_, err = store.CreateAuthorAlias(author.ID, "Samuel Clemens", "real_name")
	require.NoError(t, err)

	// Delete author should cascade delete aliases
	err = store.DeleteAuthor(author.ID)
	require.NoError(t, err)

	aliases, err := store.GetAuthorAliases(author.ID)
	require.NoError(t, err)
	require.Empty(t, aliases)

	// Alias lookup should return nil
	found, err := store.FindAuthorByAlias("Samuel Clemens")
	require.NoError(t, err)
	require.Nil(t, found)
}

func TestPebbleTombstones(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create two authors
	authorA, err := store.CreateAuthor("J.R.R. Tolkien")
	require.NoError(t, err)
	authorB, err := store.CreateAuthor("JRR Tolkien")
	require.NoError(t, err)

	// Delete authorB and create tombstone pointing to authorA
	err = store.DeleteAuthor(authorB.ID)
	require.NoError(t, err)
	err = store.CreateAuthorTombstone(authorB.ID, authorA.ID)
	require.NoError(t, err)

	// GetAuthorByID for the tombstoned ID should follow redirect
	result, err := store.GetAuthorByID(authorB.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, authorA.ID, result.ID)
	require.Equal(t, "J.R.R. Tolkien", result.Name)

	// GetAuthorTombstone should return the canonical ID
	canonicalID, err := store.GetAuthorTombstone(authorB.ID)
	require.NoError(t, err)
	require.Equal(t, authorA.ID, canonicalID)

	// Non-existent tombstone should return 0
	missingID, err := store.GetAuthorTombstone(9999)
	require.NoError(t, err)
	require.Equal(t, 0, missingID)
}

func TestPebbleTombstoneChainResolution(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create three authors: A, B, C
	authorA, err := store.CreateAuthor("Author A")
	require.NoError(t, err)
	authorB, err := store.CreateAuthor("Author B")
	require.NoError(t, err)
	authorC, err := store.CreateAuthor("Author C")
	require.NoError(t, err)

	// Create chain: A → B → C
	err = store.DeleteAuthor(authorA.ID)
	require.NoError(t, err)
	err = store.CreateAuthorTombstone(authorA.ID, authorB.ID)
	require.NoError(t, err)

	err = store.DeleteAuthor(authorB.ID)
	require.NoError(t, err)
	err = store.CreateAuthorTombstone(authorB.ID, authorC.ID)
	require.NoError(t, err)

	// Before resolution, A → B (which chains to C via GetAuthorByID)
	tombA, err := store.GetAuthorTombstone(authorA.ID)
	require.NoError(t, err)
	require.Equal(t, authorB.ID, tombA, "Before resolution, A should point to B")

	// Resolve chains
	updated, err := store.ResolveTombstoneChains()
	require.NoError(t, err)
	require.Equal(t, 1, updated, "Should have updated 1 tombstone (A→C)")

	// After resolution, A → C directly
	tombA, err = store.GetAuthorTombstone(authorA.ID)
	require.NoError(t, err)
	require.Equal(t, authorC.ID, tombA, "After resolution, A should point directly to C")

	// B → C should remain unchanged
	tombB, err := store.GetAuthorTombstone(authorB.ID)
	require.NoError(t, err)
	require.Equal(t, authorC.ID, tombB)

	// GetAuthorByID for A should resolve to C
	result, err := store.GetAuthorByID(authorA.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, authorC.ID, result.ID)
	require.Equal(t, "Author C", result.Name)
}

// TestGetAllSeriesBookCounts_Pebble tests the Pebble-based series book counts
func TestGetAllSeriesBookCounts_Pebble(t *testing.T) {
	// Arrange
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	pstore := store.(*PebbleStore)

	// Create test data: 3 series with different book counts
	// Series 1: 5 books
	// Series 2: 3 books
	// Series 3: 2 books
	author, err := store.CreateAuthor("Test Author")
	require.NoError(t, err)

	series1, err := store.CreateSeries("Series 1", &author.ID)
	require.NoError(t, err)
	series2, err := store.CreateSeries("Series 2", &author.ID)
	require.NoError(t, err)
	series3, err := store.CreateSeries("Series 3", &author.ID)
	require.NoError(t, err)

	// Create books for series 1 (5 books)
	for i := 1; i <= 5; i++ {
		isPrimary := i <= 3 // Only first 3 are primary
		book := &Book{
			Title:           fmt.Sprintf("Series 1 Book %d", i),
			AuthorID:        &author.ID,
			SeriesID:        &series1.ID,
			FilePath:        fmt.Sprintf("/test/path/s1b%d.mp3", i),
			IsPrimaryVersion: &isPrimary,
		}
		_, err := store.CreateBook(book)
		require.NoError(t, err)
	}

	// Create books for series 2 (3 books, all primary)
	for i := 1; i <= 3; i++ {
		isPrimary := true
		book := &Book{
			Title:           fmt.Sprintf("Series 2 Book %d", i),
			AuthorID:        &author.ID,
			SeriesID:        &series2.ID,
			FilePath:        fmt.Sprintf("/test/path/s2b%d.mp3", i),
			IsPrimaryVersion: &isPrimary,
		}
		_, err := store.CreateBook(book)
		require.NoError(t, err)
	}

	// Create books for series 3 (2 books, all primary)
	for i := 1; i <= 2; i++ {
		isPrimary := true
		book := &Book{
			Title:           fmt.Sprintf("Series 3 Book %d", i),
			AuthorID:        &author.ID,
			SeriesID:        &series3.ID,
			FilePath:        fmt.Sprintf("/test/path/s3b%d.mp3", i),
			IsPrimaryVersion: &isPrimary,
		}
		_, err := store.CreateBook(book)
		require.NoError(t, err)
	}

	// Act
	counts, err := pstore.GetAllSeriesBookCounts_Pebble()

	// Assert
	require.NoError(t, err)
	require.NotNil(t, counts)

	// Should count only primary versions
	require.Equal(t, 3, counts[series1.ID], "Series 1 should have 3 primary books")
	require.Equal(t, 3, counts[series2.ID], "Series 2 should have 3 primary books")
	require.Equal(t, 2, counts[series3.ID], "Series 3 should have 2 primary books")
}

// BenchmarkGetAllSeriesBookCounts_Pebble benchmarks the Pebble implementation
func BenchmarkGetAllSeriesBookCounts_Pebble(b *testing.B) {
	// Setup: Create a database with many books
	store, cleanup := setupPebbleTestDB(&testing.T{})
	defer cleanup()
	pstore := store.(*PebbleStore)

	author, _ := store.CreateAuthor("Test Author")

	// Create 20 series with 50 books each (1000 total books for reasonable benchmark)
	for seriesIdx := 0; seriesIdx < 20; seriesIdx++ {
		series, _ := store.CreateSeries(fmt.Sprintf("Series %d", seriesIdx), &author.ID)

		for bookIdx := 0; bookIdx < 50; bookIdx++ {
			isPrimary := true
			book := &Book{
				Title:            fmt.Sprintf("Book %d", bookIdx),
				AuthorID:         &author.ID,
				SeriesID:         &series.ID,
				FilePath:         fmt.Sprintf("/test/s%db%d.mp3", seriesIdx, bookIdx),
				IsPrimaryVersion: &isPrimary,
			}
			store.CreateBook(book)
		}
	}

	// Benchmark
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pstore.GetAllSeriesBookCounts_Pebble()
	}
}

