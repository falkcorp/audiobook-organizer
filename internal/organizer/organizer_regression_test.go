// file: internal/organizer/organizer_regression_test.go
// version: 1.0.0
// guid: e4f5a6b7-c8d9-e0f1-a2b3-organizer-reg

package organizer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Regression: OrganizeBookDirectory returns empty pathMap when all files missing
// (Bug: missing source files were silently skipped, but organizeDirectoryBook
// used to ignore the empty pathMap and mark the book as organized anyway.)
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_AllFilesMissing_EmptyPathMap(t *testing.T) {
	rootDir := t.TempDir()

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Ghost Book",
		Format: "mp3",
		Author: &database.Author{Name: "Ghost Author"},
	}

	// All source paths don't exist
	segmentPaths := []string{
		"/nonexistent/ch01.mp3",
		"/nonexistent/ch02.mp3",
		"/nonexistent/ch03.mp3",
	}

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segmentPaths)
	// Should succeed (OrganizeBookDirectory skips missing files)
	// but pathMap should be empty
	require.NoError(t, err)
	assert.NotEmpty(t, targetDir, "target dir is still computed even with no files")
	assert.Empty(t, pathMap, "pathMap must be empty when all source files are missing")
}

func TestOrganizeBookDirectory_PartialFilesMissing(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	// Create only 1 of 3 files
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch02.mp3"), []byte("audio"), 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Partial Book",
		Format: "mp3",
		Author: &database.Author{Name: "Author"},
	}

	segmentPaths := []string{
		filepath.Join(importDir, "ch01.mp3"), // missing
		filepath.Join(importDir, "ch02.mp3"), // exists
		filepath.Join(importDir, "ch03.mp3"), // missing
	}

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segmentPaths)
	require.NoError(t, err)
	assert.NotEmpty(t, targetDir)
	assert.Len(t, pathMap, 1, "only the one existing file should be in pathMap")

	// Verify the copied file exists
	for _, dstPath := range pathMap {
		assert.FileExists(t, dstPath)
	}
}

// ---------------------------------------------------------------------------
// Regression: OrganizeBookDirectory skips dst-already-exists
// (When re-organizing, files may already exist at destination.)
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_DstAlreadyExists(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	srcPath := filepath.Join(importDir, "book.m4b")
	require.NoError(t, os.WriteFile(srcPath, []byte("original"), 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Already There",
		Format: "m4b",
		Author: &database.Author{Name: "Author"},
	}

	// First organize
	targetDir, pathMap, err := org.OrganizeBookDirectory(book, []string{srcPath})
	require.NoError(t, err)
	require.Len(t, pathMap, 1)

	// Second organize of same book — dst already exists
	targetDir2, pathMap2, err := org.OrganizeBookDirectory(book, []string{srcPath})
	require.NoError(t, err)
	assert.Equal(t, targetDir, targetDir2, "target dir should be the same")
	assert.Len(t, pathMap2, 1, "dst-exists should still be included in pathMap")
}

// ---------------------------------------------------------------------------
// Regression: OrganizeBookDirectory with empty segment list
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_EmptySegments(t *testing.T) {
	rootDir := t.TempDir()

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Empty",
		Format: "m4b",
		Author: &database.Author{Name: "Author"},
	}

	_, _, err := org.OrganizeBookDirectory(book, []string{})
	assert.Error(t, err, "empty segment list should error")
}

func TestOrganizeBookDirectory_NilBook(t *testing.T) {
	rootDir := t.TempDir()

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	_, _, err := org.OrganizeBookDirectory(nil, []string{"/some/file.m4b"})
	assert.Error(t, err, "nil book should error")
}

// ---------------------------------------------------------------------------
// New: verify copy strategy preserves file content
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_CopyPreservesContent(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	content1 := []byte("chapter-one-audio-data-here-" + string(make([]byte, 1000)))
	content2 := []byte("chapter-two-audio-data-here-" + string(make([]byte, 2000)))
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch01.mp3"), content1, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch02.mp3"), content2, 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "Content Test",
		Format: "mp3",
		Author: &database.Author{Name: "Author"},
	}

	_, pathMap, err := org.OrganizeBookDirectory(book, []string{
		filepath.Join(importDir, "ch01.mp3"),
		filepath.Join(importDir, "ch02.mp3"),
	})
	require.NoError(t, err)
	require.Len(t, pathMap, 2)

	// Verify each file's content matches
	for srcPath, dstPath := range pathMap {
		srcData, _ := os.ReadFile(srcPath)
		dstData, _ := os.ReadFile(dstPath)
		assert.Equal(t, srcData, dstData,
			"content of %s should match source", filepath.Base(dstPath))
	}
}

// ---------------------------------------------------------------------------
// New: src == dst should be included in pathMap (already in place)
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_SrcEqualsDst(t *testing.T) {
	rootDir := t.TempDir()

	// Create a file already in the target location
	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	book := &database.Book{
		Title:  "SamePlace",
		Format: "m4b",
		Author: &database.Author{Name: "Author"},
	}

	// Pre-create the target directory and file
	targetDir := filepath.Join(rootDir, "Author", "SamePlace")
	require.NoError(t, os.MkdirAll(targetDir, 0755))
	filePath := filepath.Join(targetDir, "book.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("data"), 0644))

	_, pathMap, err := org.OrganizeBookDirectory(book, []string{filePath})
	require.NoError(t, err)
	assert.Len(t, pathMap, 1, "src==dst should still be included in pathMap")
	assert.Equal(t, filePath, pathMap[filePath])
}

// ---------------------------------------------------------------------------
// New: path sanitization — no directory traversal
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_PathTraversalPrevented(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "file.m4b"), []byte("x"), 0644))

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := NewOrganizer(cfg)

	// sanitizeFilename now strips ".." → "__", so this should stay inside rootDir
	book := &database.Book{
		Title:  "../../../etc/passwd",
		Format: "m4b",
		Author: &database.Author{Name: "../../../root"},
	}

	targetDir, _, err := org.OrganizeBookDirectory(book, []string{
		filepath.Join(importDir, "file.m4b"),
	})

	// With our security fix, ".." is replaced with "__" so the path stays inside rootDir
	if err == nil {
		absTarget, _ := filepath.Abs(targetDir)
		absRoot, _ := filepath.Abs(rootDir)
		assert.Contains(t, absTarget, absRoot,
			"target directory must stay inside rootDir even with traversal attempt")
	}
	// Either way, it should NOT create files outside rootDir
}

// Retargeted from the deleted sanitizeFilename onto SanitizePathComponent, the
// package's single sanitizer. The traversal guard has to live on the surviving
// function or it does not live anywhere.
func TestSanitizePathComponent_StripsDotDot(t *testing.T) {
	result := SanitizePathComponent("../../../etc/passwd")
	assert.NotContains(t, result, "..", "dot-dot sequences must be neutralized")

	// Single ".." also stripped
	result2 := SanitizePathComponent("..evil")
	assert.NotContains(t, result2, "..")
}

func TestSanitizePathComponent_PreservesNormalDots(t *testing.T) {
	result := SanitizePathComponent("Dr. Who - Season 1.m4b")
	assert.Contains(t, result, "Dr.")
	assert.Contains(t, result, "1.m4b")
}

func TestEnsureUnderRoot_RejectsEscape(t *testing.T) {
	err := ensureUnderRoot("/tmp/evil/../../../etc/passwd", "/tmp/library")
	assert.Error(t, err, "path escaping root should be rejected")
}

func TestEnsureUnderRoot_AcceptsValid(t *testing.T) {
	err := ensureUnderRoot("/tmp/library/Author/Title/file.m4b", "/tmp/library")
	assert.NoError(t, err, "valid path inside root should be accepted")
}
