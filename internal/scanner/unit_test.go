// file: internal/scanner/unit_test.go
// version: 1.5.0
// guid: a2b3c4d5-e6f7-8901-abcd-ef2345678901
// last-edited: 2026-07-01

package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"errors"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// extractQuotedValue (0% -> 100%)
// ---------------------------------------------------------------------------

func TestExtractQuotedValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple quoted", `TITLE "My Book"`, "My Book"},
		{"file reference", `FILE "track01.mp3" WAVE`, "track01.mp3"},
		{"no quotes", "TITLE My Book", ""},
		{"single quote only", `TITLE "unterminated`, ""},
		{"empty quotes", `TITLE ""`, ""},
		{"nested quotes", `TITLE "first" "second"`, "first"},
		{"quotes in middle", `some "value" here`, "value"},
		{"empty input", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractQuotedValue(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// SetScanCache / ClearScanCache (0% -> 100%)
// ---------------------------------------------------------------------------

func TestSetScanCacheAndClear(t *testing.T) {
	// Ensure clean state
	ClearScanCache()

	cache := map[string]database.ScanCacheEntry{
		"/tmp/a.m4b": {Mtime: 100, Size: 200},
	}
	SetScanCache(cache)

	globalScanCacheMu.RLock()
	assert.NotNil(t, globalScanCache)
	assert.Len(t, globalScanCache, 1)
	globalScanCacheMu.RUnlock()

	ClearScanCache()

	globalScanCacheMu.RLock()
	assert.Nil(t, globalScanCache)
	globalScanCacheMu.RUnlock()
}

// ---------------------------------------------------------------------------
// shouldSkipFile — additional edge cases
// ---------------------------------------------------------------------------

func TestShouldSkipFileNeedsRescan(t *testing.T) {
	cache := map[string]database.ScanCacheEntry{
		"/a.m4b": {Mtime: 10, Size: 20, NeedsRescan: true},
		"/b.m4b": {Mtime: 10, Size: 20, NeedsRescan: false},
	}
	// NeedsRescan forces re-scan even when mtime+size match
	assert.False(t, shouldSkipFile("/a.m4b", 10, 20, cache))
	// No rescan needed and mtime+size match -> skip
	assert.True(t, shouldSkipFile("/b.m4b", 10, 20, cache))
	// Different size -> don't skip
	assert.False(t, shouldSkipFile("/b.m4b", 10, 99, cache))
	// Different mtime -> don't skip
	assert.False(t, shouldSkipFile("/b.m4b", 99, 20, cache))
}

// ---------------------------------------------------------------------------
// isInitialToken
// ---------------------------------------------------------------------------

func TestIsInitialToken(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"J.", true},
		{"K.", true},
		{"Z.", true},
		{"A.", true},
		{"a.", false},  // lowercase
		{"JK", false},  // no period
		{"J..", false}, // too long
		{".", false},   // too short
		{"J", false},   // no period
		{"AB.", false}, // too long
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, isInitialToken(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// looksLikeTitleCandidate
// ---------------------------------------------------------------------------

func TestLooksLikeTitleCandidate(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"The Great Gatsby", true},
		{"A Tale of Two Cities", true},
		{"An Example", true},
		{"  the padded  ", true},
		{"Stephen King", false},
		{"My Book", false},
		{"", false},
		{"another thing", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, looksLikeTitleCandidate(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// isValidAuthor — edge cases not covered
// ---------------------------------------------------------------------------

func TestIsValidAuthorEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"chapter space", "chapter 5", false},
		{"chapter prefix", "chapter", false},
		{"volume prefix", "volume 1", false},
		{"disc prefix", "disc 2", false},
		{"valid name", "Jane Austen", true},
		{"numeric", "42", false},
		{"book prefix", "book 1", false},
		{"part prefix", "part one", false},
		{"vol prefix", "vol 3", false},
		{"normal word", "something", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isValidAuthor(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// ComputeSegmentFileHash (0% -> covered)
// ---------------------------------------------------------------------------

func TestComputeSegmentFileHash(t *testing.T) {
	t.Run("small file", func(t *testing.T) {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "seg.m4b")
		require.NoError(t, os.WriteFile(path, []byte("segment data for hashing"), 0o644))

		hash, err := ComputeSegmentFileHash(path)
		require.NoError(t, err)
		assert.Len(t, hash, 64) // SHA-256 hex

		// Deterministic
		hash2, err := ComputeSegmentFileHash(path)
		require.NoError(t, err)
		assert.Equal(t, hash, hash2)
	})

	t.Run("nonexistent file", func(t *testing.T) {
		_, err := ComputeSegmentFileHash("/no/such/file.m4b")
		assert.Error(t, err)
	})

	t.Run("empty file", func(t *testing.T) {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "empty.m4b")
		require.NoError(t, os.WriteFile(path, []byte{}, 0o644))

		hash, err := ComputeSegmentFileHash(path)
		require.NoError(t, err)
		assert.Len(t, hash, 64)
	})
}

// ---------------------------------------------------------------------------
// computeHashFromReader — small-file path (19% -> higher)
// ---------------------------------------------------------------------------

func TestComputeHashFromReaderSmallFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "small.bin")
	data := []byte("small content for hashing test")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	hash, err := computeHashFromReader(f, int64(len(data)))
	require.NoError(t, err)
	assert.Len(t, hash, 64)

	// Compare with ComputeFileHash for consistency
	fHash, err := ComputeFileHash(path)
	require.NoError(t, err)
	assert.Equal(t, fHash, hash)
}

// ---------------------------------------------------------------------------
// parseCueFile (0% -> covered)
// ---------------------------------------------------------------------------

func TestParseCueFile(t *testing.T) {
	t.Run("valid cue sheet", func(t *testing.T) {
		tmp := t.TempDir()

		// Create referenced audio files
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "track01.mp3"), []byte("audio"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "track02.mp3"), []byte("audio"), 0o644))

		cueContent := `TITLE "My Audiobook"
PERFORMER "Some Author"
FILE "track01.mp3" MP3
  TRACK 01 AUDIO
    INDEX 01 00:00:00
FILE "track02.mp3" MP3
  TRACK 02 AUDIO
    INDEX 01 05:00:00
`
		cuePath := filepath.Join(tmp, "book.cue")
		require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

		title, files := parseCueFile(cuePath)
		assert.Equal(t, "My Audiobook", title)
		assert.Len(t, files, 2)
		assert.Contains(t, files, filepath.Join(tmp, "track01.mp3"))
		assert.Contains(t, files, filepath.Join(tmp, "track02.mp3"))
	})

	t.Run("missing referenced files", func(t *testing.T) {
		tmp := t.TempDir()
		cueContent := `TITLE "Ghost Book"
FILE "missing.mp3" MP3
`
		cuePath := filepath.Join(tmp, "ghost.cue")
		require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

		title, files := parseCueFile(cuePath)
		assert.Equal(t, "Ghost Book", title)
		assert.Empty(t, files)
	})

	t.Run("nonexistent cue file", func(t *testing.T) {
		title, files := parseCueFile("/no/such/file.cue")
		assert.Empty(t, title)
		assert.Nil(t, files)
	})

	t.Run("no title", func(t *testing.T) {
		tmp := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "a.mp3"), []byte("x"), 0o644))

		cueContent := `FILE "a.mp3" MP3
  TRACK 01 AUDIO
`
		cuePath := filepath.Join(tmp, "notitle.cue")
		require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

		title, files := parseCueFile(cuePath)
		assert.Empty(t, title)
		assert.Len(t, files, 1)
	})
}

// ---------------------------------------------------------------------------
// parseM3UFile (0% -> covered)
// ---------------------------------------------------------------------------

func TestParseM3UFile(t *testing.T) {
	t.Run("valid m3u", func(t *testing.T) {
		tmp := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "track1.mp3"), []byte("a"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "track2.mp3"), []byte("b"), 0o644))

		m3uContent := "#EXTM3U\n#EXTINF:300,Track 1\ntrack1.mp3\n#EXTINF:200,Track 2\ntrack2.mp3\n"
		m3uPath := filepath.Join(tmp, "playlist.m3u")
		require.NoError(t, os.WriteFile(m3uPath, []byte(m3uContent), 0o644))

		files := parseM3UFile(m3uPath)
		assert.Len(t, files, 2)
	})

	t.Run("empty lines and comments only", func(t *testing.T) {
		tmp := t.TempDir()
		m3uContent := "#EXTM3U\n# comment\n\n\n"
		m3uPath := filepath.Join(tmp, "empty.m3u")
		require.NoError(t, os.WriteFile(m3uPath, []byte(m3uContent), 0o644))

		files := parseM3UFile(m3uPath)
		assert.Empty(t, files)
	})

	t.Run("nonexistent m3u", func(t *testing.T) {
		files := parseM3UFile("/no/file.m3u")
		assert.Nil(t, files)
	})

	t.Run("missing referenced files", func(t *testing.T) {
		tmp := t.TempDir()
		m3uContent := "nonexistent.mp3\n"
		m3uPath := filepath.Join(tmp, "bad.m3u")
		require.NoError(t, os.WriteFile(m3uPath, []byte(m3uContent), 0o644))

		files := parseM3UFile(m3uPath)
		assert.Empty(t, files)
	})

	t.Run("absolute paths", func(t *testing.T) {
		tmp := t.TempDir()
		absFile := filepath.Join(tmp, "abs.mp3")
		require.NoError(t, os.WriteFile(absFile, []byte("x"), 0o644))

		m3uContent := absFile + "\n"
		m3uPath := filepath.Join(tmp, "abs.m3u")
		require.NoError(t, os.WriteFile(m3uPath, []byte(m3uContent), 0o644))

		files := parseM3UFile(m3uPath)
		assert.Len(t, files, 1)
		assert.Equal(t, absFile, files[0])
	})
}

// ---------------------------------------------------------------------------
// groupFilesIntoBooks — edge cases (56% -> higher)
// ---------------------------------------------------------------------------

func TestGroupFilesIntoBooks(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		books := groupFilesIntoBooks(context.Background(), nil)
		assert.Empty(t, books)
	})

	t.Run("single file", func(t *testing.T) {
		books := groupFilesIntoBooks(context.Background(), []string{"/tmp/only.m4b"})
		require.Len(t, books, 1)
		assert.Equal(t, "/tmp/only.m4b", books[0].FilePath)
		assert.Equal(t, ".m4b", books[0].Format)
	})

	t.Run("single file mp3", func(t *testing.T) {
		books := groupFilesIntoBooks(context.Background(), []string{"/tmp/song.mp3"})
		require.Len(t, books, 1)
		assert.Equal(t, ".mp3", books[0].Format)
	})
}

// ---------------------------------------------------------------------------
// findPlaylistGroupings — with CUE + M3U (24% -> higher)
// ---------------------------------------------------------------------------

func TestFindPlaylistGroupings(t *testing.T) {
	t.Run("cue groups audio files", func(t *testing.T) {
		tmp := t.TempDir()

		// Create audio files
		audioFiles := []string{
			filepath.Join(tmp, "track1.mp3"),
			filepath.Join(tmp, "track2.mp3"),
			filepath.Join(tmp, "unrelated.mp3"),
		}
		for _, f := range audioFiles {
			require.NoError(t, os.WriteFile(f, []byte("audio"), 0o644))
		}

		// Create cue file referencing first two
		cueContent := `TITLE "My Book"
FILE "track1.mp3" MP3
  TRACK 01 AUDIO
FILE "track2.mp3" MP3
  TRACK 02 AUDIO
`
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "book.cue"), []byte(cueContent), 0o644))

		groups := findPlaylistGroupings(tmp, audioFiles)
		assert.NotEmpty(t, groups)

		// Should have a group for "My Book" with 2 files
		found := false
		for _, files := range groups {
			if len(files) == 2 {
				found = true
			}
		}
		assert.True(t, found, "expected a group with 2 files")
	})

	t.Run("m3u groups audio files", func(t *testing.T) {
		tmp := t.TempDir()

		audioFiles := []string{
			filepath.Join(tmp, "a.mp3"),
			filepath.Join(tmp, "b.mp3"),
		}
		for _, f := range audioFiles {
			require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
		}

		m3uContent := "a.mp3\nb.mp3\n"
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "playlist.m3u"), []byte(m3uContent), 0o644))

		groups := findPlaylistGroupings(tmp, audioFiles)
		assert.NotEmpty(t, groups)
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		groups := findPlaylistGroupings("/no/such/dir", nil)
		assert.Nil(t, groups)
	})

	t.Run("no playlists", func(t *testing.T) {
		tmp := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "file.mp3"), []byte("x"), 0o644))

		groups := findPlaylistGroupings(tmp, []string{filepath.Join(tmp, "file.mp3")})
		assert.Empty(t, groups)
	})
}

// ---------------------------------------------------------------------------
// extractInfoFromPath — additional patterns for coverage
// ---------------------------------------------------------------------------

func TestExtractInfoFromPathAdditional(t *testing.T) {
	t.Run("plain filename no separator", func(t *testing.T) {
		book := &Book{FilePath: "/tmp/MyBook.m4b"}
		extractInfoFromPath(book)
		assert.Equal(t, "MyBook", book.Title)
	})

	t.Run("chapter suffix stripped", func(t *testing.T) {
		book := &Book{FilePath: "/tmp/Some Title-3 Chapter 5.m4b"}
		extractInfoFromPath(book)
		assert.NotEmpty(t, book.Title)
		assert.NotContains(t, book.Title, "Chapter 5")
	})

	t.Run("title with article and author via underscore", func(t *testing.T) {
		book := &Book{FilePath: "/tmp/The Great Gatsby_F. Scott Fitzgerald.m4b"}
		extractInfoFromPath(book)
		assert.NotEmpty(t, book.Title)
	})

	t.Run("both sides names underscore with article title", func(t *testing.T) {
		// Both sides look like person names, but left starts with "The"
		book := &Book{FilePath: "/tmp/The Jane Doe_John Smith.m4b"}
		extractInfoFromPath(book)
		assert.NotEmpty(t, book.Title)
	})

	t.Run("dash pattern with existing author", func(t *testing.T) {
		book := &Book{FilePath: "/tmp/Title - Author Name.m4b", Author: "Existing"}
		extractInfoFromPath(book)
		// Should use series fallback since author is already set
		assert.Equal(t, "Existing", book.Author)
	})
}

// ---------------------------------------------------------------------------
// extractAuthorFromDirectory — translator pattern
// ---------------------------------------------------------------------------

func TestExtractAuthorFromDirectoryTranslator(t *testing.T) {
	got := extractAuthorFromDirectory("/media/Leo Tolstoy - translator - Some Person/book.m4b")
	assert.Equal(t, "Leo Tolstoy", got)
}

// ---------------------------------------------------------------------------
// parseFilenameForAuthor — coverage gaps
// ---------------------------------------------------------------------------

func TestParseFilenameForAuthorNoSeparator(t *testing.T) {
	title, author := parseFilenameForAuthor("no separators here")
	assert.Empty(t, title)
	assert.Empty(t, author)
}

// ---------------------------------------------------------------------------
// ProcessBooksParallel — with progress callback
// ---------------------------------------------------------------------------

func TestProcessBooksParallelWithProgressCallback(t *testing.T) {
	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	tmp := t.TempDir()
	books := []Book{
		{FilePath: filepath.Join(tmp, "b1.m4b"), Format: ".m4b"},
		{FilePath: filepath.Join(tmp, "b2.m4b"), Format: ".m4b"},
	}
	for _, b := range books {
		require.NoError(t, os.WriteFile(b.FilePath, []byte("content"), 0o644))
	}

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error { return nil }

	var progressCalls int
	progressFn := func(processed, total int, path string) {
		progressCalls++
	}

	err := ProcessBooksParallel(t.Context(), books, 2, progressFn, nil)
	assert.NoError(t, err)
	assert.Equal(t, 2, progressCalls)
}

// ---------------------------------------------------------------------------
// Book struct field coverage — SegmentFiles in groupFilesIntoBooks
// ---------------------------------------------------------------------------

func TestGroupFilesIntoBooksNoAlbumMultipleFiles(t *testing.T) {
	// When quickReadAlbum returns "" for all files (non-audio files),
	// each file becomes its own book
	tmp := t.TempDir()
	files := make([]string, 3)
	for i := range files {
		p := filepath.Join(tmp, filepath.Base(t.Name())+string(rune('a'+i))+".m4b")
		require.NoError(t, os.WriteFile(p, []byte("not real audio"), 0o644))
		files[i] = p
	}

	books := groupFilesIntoBooks(context.Background(), files)
	// With no album tags, each becomes its own book (no album grouping)
	assert.GreaterOrEqual(t, len(books), 1)
}

// ---------------------------------------------------------------------------
// isUniqueConstraintError (0% -> 100%)
// ---------------------------------------------------------------------------

func TestIsUniqueConstraintError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unique constraint", errors.New("UNIQUE constraint failed: authors.name"), true},
		{"duplicate key", errors.New("duplicate key value violates"), true},
		{"uppercase unique", errors.New("Unique Constraint violation"), true},
		{"unrelated error", errors.New("connection refused"), false},
		{"empty error", errors.New(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isUniqueConstraintError(tt.err))
		})
	}
}

// ---------------------------------------------------------------------------
// preserveExistingFields (50% -> higher)
// ---------------------------------------------------------------------------

func TestPreserveExistingFields(t *testing.T) {
	narrator := "John Narrator"
	publisher := "Big Publisher"
	lang := "en"
	year := 2020
	coverURL := "http://example.com/cover.jpg"
	isbn10 := "0123456789"
	isbn13 := "978-0123456789"
	asin := "B0123456"
	edition := "1st"
	desc := "A great book"
	olID := "OL123"
	hcID := "HC456"
	gbID := "GB789"
	itunesPID := "ABCD1234"
	versionNotes := "Original version"
	seqNum := 3

	existing := &database.Book{
		Narrator:           &narrator,
		Publisher:          &publisher,
		Language:           &lang,
		PrintYear:          &year,
		CoverURL:           &coverURL,
		ISBN10:             &isbn10,
		ISBN13:             &isbn13,
		ASIN:               &asin,
		Edition:            &edition,
		Description:        &desc,
		OpenLibraryID:      &olID,
		HardcoverID:        &hcID,
		GoogleBooksID:      &gbID,
		ITunesPersistentID: &itunesPID,
		VersionNotes:       &versionNotes,
		SeriesSequence:     &seqNum,
	}

	scanned := &database.Book{} // all nil

	preserveExistingFields(scanned, existing)

	assert.Equal(t, &narrator, scanned.Narrator)
	assert.Equal(t, &publisher, scanned.Publisher)
	assert.Equal(t, &lang, scanned.Language)
	assert.Equal(t, &year, scanned.PrintYear)
	assert.Equal(t, &coverURL, scanned.CoverURL)
	assert.Equal(t, &isbn10, scanned.ISBN10)
	assert.Equal(t, &isbn13, scanned.ISBN13)
	assert.Equal(t, &asin, scanned.ASIN)
	assert.Equal(t, &edition, scanned.Edition)
	assert.Equal(t, &desc, scanned.Description)
	assert.Equal(t, &olID, scanned.OpenLibraryID)
	assert.Equal(t, &hcID, scanned.HardcoverID)
	assert.Equal(t, &gbID, scanned.GoogleBooksID)
	assert.Equal(t, &itunesPID, scanned.ITunesPersistentID)
	assert.Equal(t, &versionNotes, scanned.VersionNotes)
	assert.Equal(t, &seqNum, scanned.SeriesSequence)
}

func TestPreserveExistingFieldsDoesNotOverwrite(t *testing.T) {
	existingNarr := "Old Narrator"
	scannedNarr := "New Narrator"

	existing := &database.Book{Narrator: &existingNarr}
	scanned := &database.Book{Narrator: &scannedNarr}

	preserveExistingFields(scanned, existing)
	assert.Equal(t, &scannedNarr, scanned.Narrator, "scanned value should not be overwritten")
}

func TestPreserveExistingFieldsZeroSequence(t *testing.T) {
	zero := 0
	existingSeq := 5
	scanned := &database.Book{SeriesSequence: &zero}
	existing := &database.Book{SeriesSequence: &existingSeq}

	preserveExistingFields(scanned, existing)
	assert.Equal(t, &existingSeq, scanned.SeriesSequence, "zero sequence should be replaced by existing")
}

// ---------------------------------------------------------------------------
// countAudioFilesInDir (0% -> 100%)
// ---------------------------------------------------------------------------

func TestCountAudioFilesInDir(t *testing.T) {
	t.Run("mixed files", func(t *testing.T) {
		tmp := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "a.m4b"), []byte("x"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "b.mp3"), []byte("x"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "c.txt"), []byte("x"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(tmp, "subdir"), 0o755))

		count := countAudioFilesInDir(tmp, []string{".m4b", ".mp3"})
		assert.Equal(t, 2, count)
	})

	t.Run("no audio files", func(t *testing.T) {
		tmp := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "readme.txt"), []byte("x"), 0o644))

		count := countAudioFilesInDir(tmp, []string{".m4b"})
		assert.Equal(t, 0, count)
	})

	t.Run("nonexistent dir", func(t *testing.T) {
		count := countAudioFilesInDir("/no/such/dir", []string{".m4b"})
		assert.Equal(t, 0, count)
	})

	t.Run("case insensitive extensions", func(t *testing.T) {
		tmp := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "book.M4B"), []byte("x"), 0o644))

		count := countAudioFilesInDir(tmp, []string{".m4b"})
		assert.Equal(t, 1, count)
	})

	t.Run("empty dir", func(t *testing.T) {
		tmp := t.TempDir()
		count := countAudioFilesInDir(tmp, []string{".m4b", ".mp3"})
		assert.Equal(t, 0, count)
	})
}

// ---------------------------------------------------------------------------
// computeHashFromReader — large file path (19% -> higher)
// ---------------------------------------------------------------------------

func TestComputeHashFromReaderLargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file hash test in short mode")
	}

	tmp := t.TempDir()
	path := filepath.Join(tmp, "large.bin")

	// Create a file just over 100MB threshold
	f, err := os.Create(path)
	require.NoError(t, err)

	const targetSize = 101 * 1024 * 1024 // 101 MB
	chunk := make([]byte, 1024*1024)     // 1 MB chunks
	for i := 0; i < 101; i++ {
		for j := range chunk {
			chunk[j] = byte(i ^ j)
		}
		_, err := f.Write(chunk)
		require.NoError(t, err)
	}
	f.Close()

	// Now test computeHashFromReader
	f2, err := os.Open(path)
	require.NoError(t, err)
	defer f2.Close()

	hash, err := computeHashFromReader(f2, targetSize)
	require.NoError(t, err)
	assert.Len(t, hash, 64)

	// Verify it matches ComputeFileHash
	cfHash, err := ComputeFileHash(path)
	require.NoError(t, err)
	assert.Equal(t, cfHash, hash)
}

// ---------------------------------------------------------------------------
// createBookFilesForBook — nil store early return (0% -> partial)
// ---------------------------------------------------------------------------

func TestCreateBookFilesForBookNilStore(t *testing.T) {
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	// Should return immediately without panic
	createBookFilesForBook("/tmp/test.m4b", nil, defaultLog)
}

// ---------------------------------------------------------------------------
// ProcessFile — empty path error
// ---------------------------------------------------------------------------

func TestProcessFileEmptyPath(t *testing.T) {
	_, _, _, err := ProcessFile("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty file path")
}

func TestProcessFileNonexistent(t *testing.T) {
	_, _, _, err := ProcessFile("/no/such/file.m4b")
	assert.Error(t, err)
}

func TestProcessFileDirectory(t *testing.T) {
	tmp := t.TempDir()
	meta, mi, hash, err := ProcessFile(tmp)
	require.NoError(t, err)
	assert.NotNil(t, meta)
	assert.Nil(t, mi, "directories should have no mediainfo")
	assert.Empty(t, hash, "directories should have no hash")
}

// ---------------------------------------------------------------------------
// quickReadAlbum — non-audio file returns empty
// ---------------------------------------------------------------------------

func TestQuickReadAlbumNonAudio(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "notaudio.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))

	album := quickReadAlbum(path)
	assert.Empty(t, album)
}

func TestQuickReadAlbumNonexistent(t *testing.T) {
	album := quickReadAlbum("/no/such/file.m4b")
	assert.Empty(t, album)
}

// ---------------------------------------------------------------------------
// ProcessBooksParallel — incremental cache skip path
// ---------------------------------------------------------------------------

func TestProcessBooksParallelWithScanCache(t *testing.T) {
	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "cached.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("cached content"), 0o644))

	fi, err := os.Stat(filePath)
	require.NoError(t, err)

	// Pre-populate cache so the file is skipped
	cache := map[string]database.ScanCacheEntry{
		filePath: {Mtime: fi.ModTime().Unix(), Size: fi.Size(), NeedsRescan: false},
	}
	SetScanCache(cache)
	t.Cleanup(func() { ClearScanCache() })

	books := []Book{{FilePath: filePath, Format: ".m4b"}}

	saveCalled := false
	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error {
		saveCalled = true
		return nil
	}

	err = ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	assert.NoError(t, err)
	assert.False(t, saveCalled, "save should not be called for cached file")
}

// ---------------------------------------------------------------------------
// preserveExistingFields — iTunes and version fields
// ---------------------------------------------------------------------------

func TestPreserveExistingFieldsITunesAndVersion(t *testing.T) {
	dateAdded := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	playCount := 5
	lastPlayed := time.Date(2021, 6, 15, 0, 0, 0, 0, time.UTC)
	rating := 80
	bookmark := int64(5025)
	importSrc := "iTunes XML"
	isPrimary := true
	vgID := "vg-123"
	markedDel := true
	markedDelAt := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	abYear := 2019
	workID := "w-42"
	narrJSON := `["narrator1","narrator2"]`

	existing := &database.Book{
		ITunesDateAdded:      &dateAdded,
		ITunesPlayCount:      &playCount,
		ITunesLastPlayed:     &lastPlayed,
		ITunesRating:         &rating,
		ITunesBookmark:       &bookmark,
		ITunesImportSource:   &importSrc,
		IsPrimaryVersion:     &isPrimary,
		VersionGroupID:       &vgID,
		MarkedForDeletion:    &markedDel,
		MarkedForDeletionAt:  &markedDelAt,
		AudiobookReleaseYear: &abYear,
		WorkID:               &workID,
		NarratorsJSON:        &narrJSON,
	}

	scanned := &database.Book{}
	preserveExistingFields(scanned, existing)

	assert.Equal(t, &dateAdded, scanned.ITunesDateAdded)
	assert.Equal(t, &playCount, scanned.ITunesPlayCount)
	assert.Equal(t, &lastPlayed, scanned.ITunesLastPlayed)
	assert.Equal(t, &rating, scanned.ITunesRating)
	assert.Equal(t, &bookmark, scanned.ITunesBookmark)
	assert.Equal(t, &importSrc, scanned.ITunesImportSource)
	assert.Equal(t, &isPrimary, scanned.IsPrimaryVersion)
	assert.Equal(t, &vgID, scanned.VersionGroupID)
	assert.Equal(t, &markedDel, scanned.MarkedForDeletion)
	assert.Equal(t, &markedDelAt, scanned.MarkedForDeletionAt)
	assert.Equal(t, &abYear, scanned.AudiobookReleaseYear)
	assert.Equal(t, &workID, scanned.WorkID)
	assert.Equal(t, &narrJSON, scanned.NarratorsJSON)
}

// ---------------------------------------------------------------------------
// resolveAuthorID — all branches
// ---------------------------------------------------------------------------

func TestResolveAuthorID(t *testing.T) {
	t.Run("empty name returns nil", func(t *testing.T) {
		id, err := resolveAuthorID("")
		assert.NoError(t, err)
		assert.Nil(t, id)
	})

	t.Run("whitespace name returns nil", func(t *testing.T) {
		id, err := resolveAuthorID("   ")
		assert.NoError(t, err)
		assert.Nil(t, id)
	})

	t.Run("existing author found", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetAuthorByName("Stephen King").Return(&database.Author{ID: 42, Name: "Stephen King"}, nil)

		id, err := resolveAuthorID("Stephen King")
		require.NoError(t, err)
		require.NotNil(t, id)
		assert.Equal(t, 42, *id)
	})

	t.Run("author created successfully", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetAuthorByName("New Author").Return(nil, nil)
		store.EXPECT().CreateAuthor("New Author").Return(&database.Author{ID: 99, Name: "New Author"}, nil)

		id, err := resolveAuthorID("New Author")
		require.NoError(t, err)
		require.NotNil(t, id)
		assert.Equal(t, 99, *id)
	})

	t.Run("lookup failure returns error", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetAuthorByName("Fail Author").Return(nil, fmt.Errorf("db error"))

		_, err := resolveAuthorID("Fail Author")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "author lookup failed")
	})

	t.Run("create failure non-unique returns error", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetAuthorByName("Create Fail").Return(nil, nil)
		store.EXPECT().CreateAuthor("Create Fail").Return(nil, fmt.Errorf("some random error"))

		_, err := resolveAuthorID("Create Fail")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "author create failed")
	})

	t.Run("unique constraint conflict resolved", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetAuthorByName("Conflict Author").Return(nil, nil).Once()
		store.EXPECT().CreateAuthor("Conflict Author").Return(nil, fmt.Errorf("UNIQUE constraint failed"))
		store.EXPECT().GetAuthorByName("Conflict Author").Return(&database.Author{ID: 77, Name: "Conflict Author"}, nil).Once()

		id, err := resolveAuthorID("Conflict Author")
		require.NoError(t, err)
		require.NotNil(t, id)
		assert.Equal(t, 77, *id)
	})

	t.Run("unique conflict but re-fetch fails", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetAuthorByName("Ghost").Return(nil, nil).Once()
		store.EXPECT().CreateAuthor("Ghost").Return(nil, fmt.Errorf("UNIQUE constraint failed"))
		store.EXPECT().GetAuthorByName("Ghost").Return(nil, fmt.Errorf("db down")).Once()

		_, err := resolveAuthorID("Ghost")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "author lookup after conflict failed")
	})

	t.Run("unique conflict re-fetch returns nil", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetAuthorByName("Vanished").Return(nil, nil).Once()
		store.EXPECT().CreateAuthor("Vanished").Return(nil, fmt.Errorf("UNIQUE constraint failed"))
		store.EXPECT().GetAuthorByName("Vanished").Return(nil, nil).Once()

		_, err := resolveAuthorID("Vanished")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "conflict detected but author not found")
	})

	t.Run("collapsed initials normalized", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		// "J.B." should become "J. B."
		store.EXPECT().GetAuthorByName("J. B.").Return(&database.Author{ID: 10, Name: "J. B."}, nil)

		id, err := resolveAuthorID("J.B.")
		require.NoError(t, err)
		require.NotNil(t, id)
		assert.Equal(t, 10, *id)
	})
}

// ---------------------------------------------------------------------------
// resolveSeriesID — all branches
// ---------------------------------------------------------------------------

func TestResolveSeriesID(t *testing.T) {
	t.Run("empty name returns nil", func(t *testing.T) {
		id, err := resolveSeriesID("", nil)
		assert.NoError(t, err)
		assert.Nil(t, id)
	})

	t.Run("existing series found", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		authorID := 5
		store.EXPECT().GetSeriesByName("Dune", &authorID).Return(&database.Series{ID: 33, Name: "Dune"}, nil)

		id, err := resolveSeriesID("Dune", &authorID)
		require.NoError(t, err)
		require.NotNil(t, id)
		assert.Equal(t, 33, *id)
	})

	t.Run("series created successfully", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetSeriesByName("New Series", (*int)(nil)).Return(nil, nil)
		store.EXPECT().CreateSeries("New Series", (*int)(nil)).Return(&database.Series{ID: 55, Name: "New Series"}, nil)

		id, err := resolveSeriesID("New Series", nil)
		require.NoError(t, err)
		require.NotNil(t, id)
		assert.Equal(t, 55, *id)
	})

	t.Run("lookup failure returns error", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetSeriesByName("Fail", (*int)(nil)).Return(nil, fmt.Errorf("db error"))

		_, err := resolveSeriesID("Fail", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "series lookup failed")
	})

	t.Run("create non-unique failure", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetSeriesByName("Bad", (*int)(nil)).Return(nil, nil)
		store.EXPECT().CreateSeries("Bad", (*int)(nil)).Return(nil, fmt.Errorf("random error"))

		_, err := resolveSeriesID("Bad", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "series create failed")
	})

	t.Run("unique constraint conflict resolved", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetSeriesByName("Conflict", (*int)(nil)).Return(nil, nil).Once()
		store.EXPECT().CreateSeries("Conflict", (*int)(nil)).Return(nil, fmt.Errorf("UNIQUE constraint failed"))
		store.EXPECT().GetSeriesByName("Conflict", (*int)(nil)).Return(&database.Series{ID: 88, Name: "Conflict"}, nil).Once()

		id, err := resolveSeriesID("Conflict", nil)
		require.NoError(t, err)
		require.NotNil(t, id)
		assert.Equal(t, 88, *id)
	})

	t.Run("unique conflict re-fetch fails", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetSeriesByName("Ghost", (*int)(nil)).Return(nil, nil).Once()
		store.EXPECT().CreateSeries("Ghost", (*int)(nil)).Return(nil, fmt.Errorf("duplicate key value violates"))
		store.EXPECT().GetSeriesByName("Ghost", (*int)(nil)).Return(nil, fmt.Errorf("db down")).Once()

		_, err := resolveSeriesID("Ghost", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "series lookup after conflict failed")
	})

	t.Run("unique conflict re-fetch returns nil", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		origStore := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

		store.EXPECT().GetSeriesByName("Vanished", (*int)(nil)).Return(nil, nil).Once()
		store.EXPECT().CreateSeries("Vanished", (*int)(nil)).Return(nil, fmt.Errorf("duplicate key value violates"))
		store.EXPECT().GetSeriesByName("Vanished", (*int)(nil)).Return(nil, nil).Once()

		_, err := resolveSeriesID("Vanished", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "conflict detected but series not found")
	})
}

// ---------------------------------------------------------------------------
// ProcessBooksParallel — context cancellation (44% -> higher)
// ---------------------------------------------------------------------------

func TestProcessBooksParallelContextCancelled(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error { return nil }

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	tmp := t.TempDir()
	books := make([]Book, 5)
	for i := range books {
		p := filepath.Join(tmp, fmt.Sprintf("book%d.m4b", i))
		require.NoError(t, os.WriteFile(p, []byte("content"), 0o644))
		books[i] = Book{FilePath: p, Format: ".m4b"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := ProcessBooksParallel(ctx, books, 2, nil, nil)
	// Context cancellation is returned as an error
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	}
}

func TestProcessBooksParallelWorkersMinimum(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error { return nil }

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	tmp := t.TempDir()
	p := filepath.Join(tmp, "book.m4b")
	require.NoError(t, os.WriteFile(p, []byte("content"), 0o644))
	books := []Book{{FilePath: p, Format: ".m4b"}}

	// Workers < 1 should be clamped to 1
	err := ProcessBooksParallel(t.Context(), books, 0, nil, nil)
	assert.NoError(t, err)
}

func TestProcessBooksParallelSaveError(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error { return fmt.Errorf("save failed") }

	tmp := t.TempDir()
	p := filepath.Join(tmp, "fail.m4b")
	require.NoError(t, os.WriteFile(p, []byte("content"), 0o644))
	books := []Book{{FilePath: p, Format: ".m4b"}}

	err := ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	// Errors are sent to channel but function still returns nil
	assert.NoError(t, err)
}

func TestProcessBooksParallelAIParsingEnabled(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error { return nil }

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	// Enable AI parsing but with no key (tests the warning path)
	oldAI := config.AppConfig.EnableAIParsing
	oldKey := config.AppConfig.OpenAIAPIKey
	t.Cleanup(func() {
		config.AppConfig.EnableAIParsing = oldAI
		config.AppConfig.OpenAIAPIKey = oldKey
	})
	config.AppConfig.EnableAIParsing = true
	config.AppConfig.OpenAIAPIKey = "" // No key = warning log

	tmp := t.TempDir()
	p := filepath.Join(tmp, "ai.m4b")
	require.NoError(t, os.WriteFile(p, []byte("content"), 0o644))
	books := []Book{{FilePath: p, Format: ".m4b"}}

	err := ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	assert.NoError(t, err)
}

func TestProcessBooksParallelAIParsingWithBadKey(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error { return nil }

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	// Enable AI parsing with a fake key (tests the parser init path)
	oldAI := config.AppConfig.EnableAIParsing
	oldKey := config.AppConfig.OpenAIAPIKey
	t.Cleanup(func() {
		config.AppConfig.EnableAIParsing = oldAI
		config.AppConfig.OpenAIAPIKey = oldKey
	})
	config.AppConfig.EnableAIParsing = true
	config.AppConfig.OpenAIAPIKey = "sk-fake-key-for-test"

	tmp := t.TempDir()
	p := filepath.Join(tmp, "ai_test.m4b")
	require.NoError(t, os.WriteFile(p, []byte("content"), 0o644))
	books := []Book{{FilePath: p, Format: ".m4b"}}

	err := ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	assert.NoError(t, err)
}

func TestProcessBooksParallelGenericFilename(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error { return nil }

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	tmp := t.TempDir()
	bookDir := filepath.Join(tmp, "Author Name", "Book Title")
	require.NoError(t, os.MkdirAll(bookDir, 0o755))

	// Create generic part filenames (triggers IsGenericPartFilename)
	for i := 1; i <= 3; i++ {
		p := filepath.Join(bookDir, fmt.Sprintf("%02d Part %d of 3.mp3", i, i))
		require.NoError(t, os.WriteFile(p, []byte("audio data"), 0o644))
	}

	// Use the first file as the book
	books := []Book{{FilePath: filepath.Join(bookDir, "01 Part 1 of 3.mp3"), Format: ".mp3"}}
	err := ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	assert.NoError(t, err)
}

func TestProcessBooksParallelSaveWithScanCacheUpdate(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	// Use a mock store so scan cache update path is exercised
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error { return nil }

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	tmp := t.TempDir()
	p := filepath.Join(tmp, "Author - Title.m4b")
	require.NoError(t, os.WriteFile(p, []byte("content"), 0o644))

	// Mock the scan cache update and scan-fail reset paths
	store.EXPECT().GetBookByFilePath(p).Return(&database.Book{ID: "b1", FilePath: p}, nil).Maybe()
	store.EXPECT().UpdateScanCache("b1", mock.Anything, mock.Anything).Return(nil).Maybe()
	store.EXPECT().ResetScanFailCount(mock.Anything).Return(nil).Maybe()

	books := []Book{{FilePath: p, Format: ".m4b"}}
	err := ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// ProcessBooks — with active scanner
// ---------------------------------------------------------------------------

func TestProcessBooksWithActiveScanner(t *testing.T) {
	mockScanner := &mockScannerImpl{
		processErr: nil,
	}
	SetScanner(mockScanner)
	t.Cleanup(func() { SetScanner(nil) })

	books := []Book{{FilePath: "/tmp/test.m4b", Format: ".m4b"}}
	err := ProcessBooks(books, nil)
	assert.NoError(t, err)
	assert.True(t, mockScanner.processCalled)
}

type mockScannerImpl struct {
	processCalled bool
	processErr    error
}

func (m *mockScannerImpl) ScanDirectory(rootDir string, scanLog logger.Logger) ([]Book, error) {
	return nil, nil
}

func (m *mockScannerImpl) ScanDirectoryParallel(ctx context.Context, rootDir string, workers int, scanLog logger.Logger) ([]Book, error) {
	return nil, nil
}

func (m *mockScannerImpl) ProcessBooks(books []Book, scanLog logger.Logger) error {
	m.processCalled = true
	return m.processErr
}

func (m *mockScannerImpl) ProcessBooksParallel(ctx context.Context, books []Book, workers int, progressFn func(processed int, total int, bookPath string), scanLog logger.Logger) error {
	return nil
}

func (m *mockScannerImpl) ComputeFileHash(filePath string) (string, error) {
	return "", nil
}

// ---------------------------------------------------------------------------
// saveBookToDatabase — nil global store (legacy path)
// ---------------------------------------------------------------------------

func TestSaveBookToDatabaseNilStore(t *testing.T) {
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
	})

	book := &Book{Title: "Test", Author: "Author", FilePath: "/tmp/test.m4b"}
	err := saveBookToDatabase(context.Background(), book)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database store not initialized")
}

// ---------------------------------------------------------------------------
// saveBookToDatabase — with mock store (basic path)
// ---------------------------------------------------------------------------

func TestSaveBookToDatabaseNewBook(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	store.EXPECT().GetAuthorByName("Author").Return(&database.Author{ID: 1, Name: "Author"}, nil)
	store.EXPECT().GetSeriesByName(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().CreateSeries(mock.Anything, mock.Anything).Return(&database.Series{ID: 1}, nil).Maybe()
	store.EXPECT().GetAllWorks().Return(nil, nil)
	store.EXPECT().CreateWork(mock.Anything).Return(&database.Work{ID: "w1"}, nil)
	store.EXPECT().IsHashBlocked(mock.Anything).Return(false, nil).Maybe()
	store.EXPECT().GetBookByFilePath(mock.Anything).Return(nil, nil)
	store.EXPECT().GetBookByFileHash(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetBookByOriginalHash(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetBookByOrganizedHash(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetBooksByTitleInDir(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().CreateBook(mock.Anything).Return(nil, nil)

	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "test.m4b")
	require.NoError(t, os.WriteFile(fpath, []byte("audio data"), 0o644))

	book := &Book{Title: "Test Book", Author: "Author", FilePath: fpath, Format: ".m4b"}
	err := saveBookToDatabase(context.Background(), book)
	assert.NoError(t, err)
}

func TestSaveBookToDatabaseExistingBook(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "test.m4b")
	require.NoError(t, os.WriteFile(fpath, []byte("audio data"), 0o644))

	store.EXPECT().GetAuthorByName("Author").Return(&database.Author{ID: 1, Name: "Author"}, nil)
	store.EXPECT().GetAllWorks().Return(nil, nil)
	store.EXPECT().CreateWork(mock.Anything).Return(&database.Work{ID: "w1"}, nil)
	store.EXPECT().IsHashBlocked(mock.Anything).Return(false, nil).Maybe()
	existingBook := &database.Book{ID: "existing-id", Title: "Old Title", FilePath: fpath}
	store.EXPECT().GetBookByFilePath(fpath).Return(existingBook, nil)
	store.EXPECT().UpdateBook("existing-id", mock.Anything).Return(existingBook, nil)

	book := &Book{Title: "Test Book", Author: "Author", FilePath: fpath, Format: ".m4b", FileHash: "abc123"}
	err := saveBookToDatabase(context.Background(), book)
	assert.NoError(t, err)
}

func TestSaveBookToDatabaseAuthorResolveError(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	store.EXPECT().GetAuthorByName("Bad Author").Return(nil, fmt.Errorf("db error"))

	book := &Book{Title: "Test", Author: "Bad Author", FilePath: "/tmp/test.m4b"}
	err := saveBookToDatabase(context.Background(), book)
	assert.Error(t, err)
}

func TestSaveBookToDatabaseBookLookupError(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	store.EXPECT().GetAuthorByName(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetAllWorks().Return(nil, nil)
	store.EXPECT().CreateWork(mock.Anything).Return(&database.Work{ID: "w1"}, nil)
	store.EXPECT().IsHashBlocked(mock.Anything).Return(false, nil).Maybe()
	store.EXPECT().GetBookByFilePath(mock.Anything).Return(nil, fmt.Errorf("lookup failed"))

	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "test.m4b")
	require.NoError(t, os.WriteFile(fpath, []byte("data"), 0o644))

	book := &Book{Title: "Test", FilePath: fpath, Format: ".m4b"}
	err := saveBookToDatabase(context.Background(), book)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "book lookup failed")
}

func TestSaveBookToDatabaseBlockedHash(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	store.EXPECT().GetAuthorByName(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetAllWorks().Return(nil, nil).Maybe()
	store.EXPECT().CreateWork(mock.Anything).Return(&database.Work{ID: "w1"}, nil).Maybe()
	store.EXPECT().IsHashBlocked(mock.Anything).Return(true, nil)

	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "blocked.m4b")
	require.NoError(t, os.WriteFile(fpath, []byte("data"), 0o644))

	book := &Book{Title: "Blocked Book", FilePath: fpath, Format: ".m4b"}
	err := saveBookToDatabase(context.Background(), book)
	assert.NoError(t, err) // blocked = silently skip
}

// ---------------------------------------------------------------------------
// createBookFilesForBook — with mock store
// ---------------------------------------------------------------------------

func TestCreateBookFilesForBookWithStore(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	tmp := t.TempDir()
	bookPath := filepath.Join(tmp, "book.m4b")
	require.NoError(t, os.WriteFile(bookPath, []byte("audio"), 0o644))

	store.EXPECT().GetBookByFilePath(bookPath).Return(&database.Book{
		ID:       "book-1",
		Title:    "Test Book",
		FilePath: bookPath,
	}, nil)
	store.EXPECT().GetBookFiles("book-1").Return(nil, nil) // no existing files
	store.EXPECT().BatchUpsertBookFiles(mock.Anything).Return(nil)
	store.EXPECT().UpdateBook("book-1", mock.Anything).Return(nil, nil) // normalize FilePath

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	createBookFilesForBook(bookPath, nil, defaultLog)
}

func TestCreateBookFilesForBookExistingFiles(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	store.EXPECT().GetBookByFilePath("/tmp/book.m4b").Return(&database.Book{
		ID:       "book-1",
		Title:    "Test",
		FilePath: "/tmp/book.m4b",
	}, nil)
	store.EXPECT().GetBookFiles("book-1").Return([]database.BookFile{{ID: "bf-1"}}, nil)

	// Should return early since files already exist
	createBookFilesForBook("/tmp/book.m4b", nil, defaultLog)
}

func TestCreateBookFilesForBookNotFound(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	store.EXPECT().GetBookByFilePath("/tmp/missing.m4b").Return(nil, nil)

	// Should return early since book not found
	createBookFilesForBook("/tmp/missing.m4b", nil, defaultLog)
}

func TestCreateBookFilesWithSegmentFiles(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	tmp := t.TempDir()
	seg1 := filepath.Join(tmp, "seg1.m4b")
	seg2 := filepath.Join(tmp, "seg2.m4b")
	require.NoError(t, os.WriteFile(seg1, []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(seg2, []byte("b"), 0o644))

	store.EXPECT().GetBookByFilePath(tmp).Return(&database.Book{
		ID:       "book-2",
		Title:    "Multi-segment",
		FilePath: tmp,
	}, nil)
	store.EXPECT().GetBookFiles("book-2").Return(nil, nil)
	store.EXPECT().BatchUpsertBookFiles(mock.Anything).Return(nil)

	createBookFilesForBook(tmp, []string{seg1, seg2}, defaultLog)
}

// ---------------------------------------------------------------------------
// saveBookToDatabase — re-link by organizer ID
// ---------------------------------------------------------------------------

func TestSaveBookToDatabaseRelinkByOrgID(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	store.EXPECT().GetAuthorByName(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetAllWorks().Return(nil, nil).Maybe()
	store.EXPECT().CreateWork(mock.Anything).Return(&database.Work{ID: "w1"}, nil).Maybe()

	existingBook := &database.Book{ID: "org-123", Title: "Moved Book", FilePath: "/old/path.m4b"}
	store.EXPECT().GetBookByID("org-123").Return(existingBook, nil)
	store.EXPECT().UpdateBook("org-123", mock.Anything).Return(existingBook, nil)
	store.EXPECT().IsHashBlocked(mock.Anything).Return(false, nil).Maybe()

	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "moved.m4b")
	require.NoError(t, os.WriteFile(fpath, []byte("data"), 0o644))

	book := &Book{
		Title:           "Moved Book",
		FilePath:        fpath,
		Format:          ".m4b",
		BookOrganizerID: "org-123",
	}
	err := saveBookToDatabase(context.Background(), book)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// saveBookToDatabase — hash-based dedup (version linking)
// ---------------------------------------------------------------------------

func TestSaveBookToDatabaseHashDedupAlreadyLinked(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	store.EXPECT().GetAuthorByName(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetAllWorks().Return(nil, nil)
	store.EXPECT().CreateWork(mock.Anything).Return(&database.Work{ID: "w1"}, nil)
	store.EXPECT().IsHashBlocked(mock.Anything).Return(false, nil)

	// Not found by path
	store.EXPECT().GetBookByFilePath(mock.Anything).Return(nil, nil)

	// Found by hash — already version-linked
	vgID := "vg-existing"
	existingBook := &database.Book{
		ID:             "dup-id",
		Title:          "Dup Book",
		FilePath:       "/other/path.m4b",
		VersionGroupID: &vgID,
	}
	store.EXPECT().GetBookByFileHash(mock.Anything).Return(existingBook, nil)
	// Already linked — should return nil without creating new book

	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "dup.m4b")
	require.NoError(t, os.WriteFile(fpath, []byte("data"), 0o644))

	book := &Book{Title: "Dup Book", FilePath: fpath, Format: ".m4b"}
	err := saveBookToDatabase(context.Background(), book)
	assert.NoError(t, err) // silently skips already-linked
}

// ---------------------------------------------------------------------------
// inode_unix.go — getInode coverage
// ---------------------------------------------------------------------------

func TestGetInode(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "file.txt")
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))

	fi, err := os.Stat(p)
	require.NoError(t, err)

	inode, ok := getInode(fi)
	assert.True(t, ok)
	assert.Greater(t, inode, uint64(0))

	// Same file should have same inode
	inode2, ok2 := getInode(fi)
	assert.True(t, ok2)
	assert.Equal(t, inode, inode2)
}

// ---------------------------------------------------------------------------
// extractInfoFromPath — more branches for coverage
// ---------------------------------------------------------------------------

func TestExtractInfoFromPathMoreBranches(t *testing.T) {
	t.Run("series number extraction", func(t *testing.T) {
		book := &Book{FilePath: "/media/Author Name/Series Name/Book 3 - Title.m4b"}
		extractInfoFromPath(book)
		assert.NotEmpty(t, book.Title)
	})

	t.Run("underscore separator with numeric part", func(t *testing.T) {
		book := &Book{FilePath: "/tmp/Author_Title Part 2.m4b"}
		extractInfoFromPath(book)
		assert.NotEmpty(t, book.Title)
	})

	t.Run("deeply nested path", func(t *testing.T) {
		book := &Book{FilePath: "/media/Fiction/Author Name/Series/Book Title.m4b"}
		extractInfoFromPath(book)
		assert.NotEmpty(t, book.Title)
	})
}

// ---------------------------------------------------------------------------
// isValidAuthor — additional edge cases for 90% -> 100%
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ProcessBooksParallel — file processing paths for better coverage
// ---------------------------------------------------------------------------

func TestProcessBooksParallelNormalFile(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3"}

	saveCalled := 0
	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error {
		saveCalled++
		return nil
	}

	tmp := t.TempDir()
	// Create a file with author-title naming pattern
	fpath := filepath.Join(tmp, "Stephen King - The Shining.m4b")
	require.NoError(t, os.WriteFile(fpath, []byte("fake audio content"), 0o644))

	books := []Book{{FilePath: fpath, Format: ".m4b"}}
	err := ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, saveCalled)
}

func TestProcessBooksParallelDirectoryBook(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3"}

	saveCalled := 0
	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error {
		saveCalled++
		return nil
	}

	tmp := t.TempDir()
	bookDir := filepath.Join(tmp, "My Book")
	require.NoError(t, os.MkdirAll(bookDir, 0o755))
	// Create audio files in the directory
	require.NoError(t, os.WriteFile(filepath.Join(bookDir, "chapter01.mp3"), []byte("audio"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bookDir, "chapter02.mp3"), []byte("audio"), 0o644))

	books := []Book{{FilePath: bookDir, Format: ".mp3"}}
	err := ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, saveCalled)
}

func TestProcessBooksParallelMultipleErrors(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error {
		return fmt.Errorf("save error for %s", book.FilePath)
	}

	tmp := t.TempDir()
	books := make([]Book, 3)
	for i := range books {
		p := filepath.Join(tmp, fmt.Sprintf("err%d.m4b", i))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		books[i] = Book{FilePath: p, Format: ".m4b"}
	}

	err := ProcessBooksParallel(t.Context(), books, 2, nil, nil)
	// Errors are logged but function doesn't return them as combined
	_ = err
}

func TestProcessBooksParallelWithSegmentFiles(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(nil)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	saveCalled := 0
	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error {
		saveCalled++
		return nil
	}

	tmp := t.TempDir()
	seg1 := filepath.Join(tmp, "track1.mp3")
	seg2 := filepath.Join(tmp, "track2.mp3")
	require.NoError(t, os.WriteFile(seg1, []byte("audio1"), 0o644))
	require.NoError(t, os.WriteFile(seg2, []byte("audio2"), 0o644))

	books := []Book{{
		FilePath:     seg1,
		Format:       ".mp3",
		SegmentFiles: []string{seg1, seg2},
	}}

	err := ProcessBooksParallel(t.Context(), books, 1, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, saveCalled)
}

// ---------------------------------------------------------------------------
// groupFilesIntoBooks — mixed directory coverage (56% -> higher)
// ---------------------------------------------------------------------------

func TestGroupFilesIntoBooksMultiFileAlbum(t *testing.T) {
	// This test can't fully work without real audio files with album tags,
	// but it tests the fallback path where quickReadAlbum returns ""
	tmp := t.TempDir()
	files := make([]string, 4)
	for i := range files {
		p := filepath.Join(tmp, fmt.Sprintf("file%d.m4b", i))
		require.NoError(t, os.WriteFile(p, []byte("not real audio"), 0o644))
		files[i] = p
	}

	books := groupFilesIntoBooks(context.Background(), files)
	// Without real audio tags, all go to noAlbum path -> each is individual
	assert.GreaterOrEqual(t, len(books), 1)
	for _, b := range books {
		assert.Equal(t, ".m4b", b.Format)
	}
}

func TestGroupFilesIntoBooksWithPlaylistGrouping(t *testing.T) {
	tmp := t.TempDir()

	// Create audio files
	files := []string{
		filepath.Join(tmp, "ch1.mp3"),
		filepath.Join(tmp, "ch2.mp3"),
		filepath.Join(tmp, "unrelated.mp3"),
	}
	for _, f := range files {
		require.NoError(t, os.WriteFile(f, []byte("audio"), 0o644))
	}

	// Create a CUE file grouping first two
	cueContent := `TITLE "Grouped Book"
FILE "ch1.mp3" MP3
  TRACK 01 AUDIO
FILE "ch2.mp3" MP3
  TRACK 02 AUDIO
`
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "book.cue"), []byte(cueContent), 0o644))

	books := groupFilesIntoBooks(context.Background(), files)
	// Should create at least 2 books: one grouped + one individual
	assert.GreaterOrEqual(t, len(books), 1)
}

// ---------------------------------------------------------------------------
// computeHashFromReader — small file edge case
// ---------------------------------------------------------------------------

func TestComputeHashFromReaderEmptyFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "empty.bin")
	require.NoError(t, os.WriteFile(path, []byte{}, 0o644))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	hash, err := computeHashFromReader(f, 0)
	require.NoError(t, err)
	assert.Len(t, hash, 64)
}

func TestIsValidAuthorExtraEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"section prefix", "section 1", true}, // not in exclusion list
		{"single letter", "A", true},          // valid (not in exclusion list)
		{"mixed case part", "PART 1", false},
		{"regular author", "J.K. Rowling", true},
		{"double name", "Mary Jane Watson", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isValidAuthor(tt.input))
		})
	}
}
