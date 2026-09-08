// file: internal/openlibrary/store_test.go
// version: 1.2.0
// guid: f6a7b8c9-d0e1-2f3a-4b5c-6d7e8f9a0b1c

package openlibrary

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOLStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewOLStore(filepath.Join(dir, "testdb"))
	require.NoError(t, err)
	defer store.Close()

	status, err := store.GetStatus()
	require.NoError(t, err)
	assert.NotNil(t, status)
	assert.Equal(t, int64(0), status.Editions.RecordCount)
}

func writeTSVGz(t *testing.T, dir, filename string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	require.NoError(t, err)
	gz := gzip.NewWriter(f)
	for _, line := range lines {
		_, err := gz.Write([]byte(line + "\n"))
		require.NoError(t, err)
	}
	require.NoError(t, gz.Close())
	require.NoError(t, f.Close())
	return path
}

func TestImportEditions(t *testing.T) {
	dir := t.TempDir()
	store, err := NewOLStore(filepath.Join(dir, "testdb"))
	require.NoError(t, err)
	defer store.Close()

	lines := []string{
		"/type/edition\t/books/OL1M\t5\t2024-01-01\t{\"key\":\"/books/OL1M\",\"title\":\"Test Book\",\"isbn_13\":[\"9780000000001\"],\"isbn_10\":[\"0000000001\"],\"authors\":[{\"key\":\"/authors/OL1A\"}],\"publishers\":[\"Test Publisher\"],\"publish_date\":\"2020\"}",
		"/type/edition\t/books/OL2M\t3\t2024-01-01\t{\"key\":\"/books/OL2M\",\"title\":\"Another Book\",\"isbn_13\":[\"9780000000002\"]}",
	}

	dumpPath := writeTSVGz(t, dir, "editions.txt.gz", lines)

	var lastCount int
	err = store.ImportDump("editions", dumpPath, func(count int) {
		lastCount = count
	})
	require.NoError(t, err)
	assert.Equal(t, 2, lastCount)

	// Verify ISBN-13 lookup
	ed, err := store.LookupByISBN("9780000000001")
	require.NoError(t, err)
	assert.Equal(t, "Test Book", ed.Title)
	assert.Equal(t, []string{"Test Publisher"}, ed.Publishers)

	// Verify ISBN-10 lookup
	ed, err = store.LookupByISBN("0000000001")
	require.NoError(t, err)
	assert.Equal(t, "Test Book", ed.Title)

	// Verify not found
	_, err = store.LookupByISBN("9999999999999")
	assert.Error(t, err)

	// Check status
	status, err := store.GetStatus()
	require.NoError(t, err)
	assert.Equal(t, int64(2), status.Editions.RecordCount)
}

func TestImportAuthors(t *testing.T) {
	dir := t.TempDir()
	store, err := NewOLStore(filepath.Join(dir, "testdb"))
	require.NoError(t, err)
	defer store.Close()

	lines := []string{
		"/type/author\t/authors/OL1A\t2\t2024-01-01\t{\"key\":\"/authors/OL1A\",\"name\":\"Jane Doe\",\"birth_date\":\"1970\"}",
	}

	dumpPath := writeTSVGz(t, dir, "authors.txt.gz", lines)
	err = store.ImportDump("authors", dumpPath, nil)
	require.NoError(t, err)

	author, err := store.LookupAuthor("/authors/OL1A")
	require.NoError(t, err)
	assert.Equal(t, "Jane Doe", author.Name)
	assert.Equal(t, "1970", author.BirthDate)
}

func TestImportWorks(t *testing.T) {
	dir := t.TempDir()
	store, err := NewOLStore(filepath.Join(dir, "testdb"))
	require.NoError(t, err)
	defer store.Close()

	lines := []string{
		"/type/work\t/works/OL1W\t4\t2024-01-01\t{\"key\":\"/works/OL1W\",\"title\":\"A Great Work\",\"authors\":[{\"key\":\"/authors/OL1A\"}]}",
	}

	dumpPath := writeTSVGz(t, dir, "works.txt.gz", lines)
	err = store.ImportDump("works", dumpPath, nil)
	require.NoError(t, err)

	work, err := store.LookupWork("/works/OL1W")
	require.NoError(t, err)
	assert.Equal(t, "A Great Work", work.Title)

	// Test title search
	results, err := store.SearchByTitle("A Great Work")
	require.NoError(t, err)
	assert.NotEmpty(t, results)
	assert.Equal(t, "A Great Work", results[0].Title)
}

func TestSearchByTitleNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewOLStore(filepath.Join(dir, "testdb"))
	require.NoError(t, err)
	defer store.Close()

	results, err := store.SearchByTitle("nonexistent book xyz")
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestImportInvalidDumpType(t *testing.T) {
	dir := t.TempDir()
	store, err := NewOLStore(filepath.Join(dir, "testdb"))
	require.NoError(t, err)
	defer store.Close()

	dumpPath := writeTSVGz(t, dir, "bad.txt.gz", []string{
		"/type/bad\tkey\t1\t2024-01-01\t{}",
	})
	err = store.ImportDump("invalid", dumpPath, nil)
	assert.Error(t, err)
}

func TestOLStore_Optimize(t *testing.T) {
	dir := t.TempDir()
	store, err := NewOLStore(filepath.Join(dir, "testdb"))
	require.NoError(t, err)
	defer store.Close()
	err = store.Optimize(context.Background())
	assert.NoError(t, err)
}
