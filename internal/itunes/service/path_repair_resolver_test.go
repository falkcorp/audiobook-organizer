// file: internal/itunes/service/path_repair_resolver_test.go
// version: 1.1.0
// guid: 8aef0d23-1c84-4f3d-9b41-2d70eaf1c7c0

package itunesservice

import (
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fileSet is a tiny fake filesystem for resolver tests — paths in the
// set are considered to exist on disk.
type fileSet map[string]bool

func (fs fileSet) exists(p string) bool { return fs[p] }

// ---------------------------------------------------------------------------
// resolveTierA — happy path: PID → book → file-level path exists on disk
// ---------------------------------------------------------------------------

func TestResolveTierA_FileLevelPID(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookFiles("book-1").
		Return([]database.BookFile{
			{ID: "f1", FilePath: "/disk/old.m4b", ITunesPersistentID: "PID_OTHER"},
			{ID: "f2", FilePath: "/disk/new.m4b", ITunesPersistentID: "PID_FOO"},
		}, nil).Once()

	fs := fileSet{"/disk/new.m4b": true}
	got, ok := resolveTierA(m, "PID_FOO", "book-1", fs.exists)
	assert.True(t, ok)
	assert.Equal(t, "/disk/new.m4b", got)
}

// ---------------------------------------------------------------------------
// resolveTierA — falls back to book.FilePath when no file-level PID match
// ---------------------------------------------------------------------------

func TestResolveTierA_FallbackToBookFilePath(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookFiles("book-2").
		Return([]database.BookFile{
			{ID: "f1", FilePath: "/disk/seg1.mp3", ITunesPersistentID: ""},
		}, nil).Once()
	m.EXPECT().GetBookByID("book-2").
		Return(&database.Book{ID: "book-2", FilePath: "/disk/book2-folder/book.m4b"}, nil).Once()

	fs := fileSet{"/disk/book2-folder/book.m4b": true}
	got, ok := resolveTierA(m, "PID_BAR", "book-2", fs.exists)
	assert.True(t, ok)
	assert.Equal(t, "/disk/book2-folder/book.m4b", got)
}

// ---------------------------------------------------------------------------
// resolveTierA — DB has the PID but the path doesn't exist on disk → unresolved
// ---------------------------------------------------------------------------

func TestResolveTierA_DBPathAlsoMissing(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookFiles("book-3").
		Return([]database.BookFile{
			{ID: "f1", FilePath: "/disk/also-gone.m4b", ITunesPersistentID: "PID_BAZ"},
		}, nil).Once()
	m.EXPECT().GetBookByID("book-3").
		Return(&database.Book{ID: "book-3", FilePath: "/disk/also-gone-book.m4b"}, nil).Once()

	fs := fileSet{} // nothing exists
	got, ok := resolveTierA(m, "PID_BAZ", "book-3", fs.exists)
	assert.False(t, ok)
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// resolveTierA — PID has no DB mapping → unresolved (cheap path)
// ---------------------------------------------------------------------------

func TestResolveTierA_EmptyBookID(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	got, ok := resolveTierA(m, "PID_UNKNOWN", "", func(string) bool { return true })
	assert.False(t, ok)
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// lookupBookID — store error treated as empty (graceful degradation)
// ---------------------------------------------------------------------------

func TestLookupBookID_StoreErrorReturnsEmpty(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookByExternalID("itunes", mock.Anything).
		Return("", assert.AnError).Once()

	got := lookupBookID(m, "PID_X")
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// lookupBookID — successful mapping returns the book ID
// ---------------------------------------------------------------------------

func TestLookupBookID_HappyPath(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookByExternalID("itunes", "PID_HAPPY").
		Return("book-happy", nil).Once()

	got := lookupBookID(m, "PID_HAPPY")
	assert.Equal(t, "book-happy", got)
}

// fakeTagScanner is a deterministic tag scanner for tier B tests.
type fakeTagScanner struct {
	index map[string][]string
	all   []string
}

func (f *fakeTagScanner) bookIDToPaths(bookID string) []string { return f.index[bookID] }
func (f *fakeTagScanner) allPaths() []string                   { return f.all }

// ---------------------------------------------------------------------------
// fsTagScanner — production scanner walks a tmpdir and indexes via the
// injected bookIDExtractor, then resolves on demand.
// ---------------------------------------------------------------------------

func TestFSTagScanner_IndexesAudioFiles(t *testing.T) {
	root := t.TempDir()
	// Create three audio files and one ignored .txt sidecar.
	mustWrite(t, filepath.Join(root, "author/title-A.m4b"), "audio")
	mustWrite(t, filepath.Join(root, "author/title-B.mp3"), "audio")
	mustWrite(t, filepath.Join(root, "author/title-C.m4b"), "audio")
	mustWrite(t, filepath.Join(root, "author/title-A.txt"), "ignored")

	extractor := func(p string) (string, error) {
		switch filepath.Base(p) {
		case "title-A.m4b":
			return "book-1", nil
		case "title-B.mp3":
			return "book-2", nil
		case "title-C.m4b":
			return "book-1", nil // shares bookID — multi-segment
		}
		return "", nil
	}

	scan := newFSTagScanner(root, pathutil.AppDirs{}, extractor)
	one := scan.bookIDToPaths("book-1")
	assert.Len(t, one, 2)
	two := scan.bookIDToPaths("book-2")
	assert.Len(t, two, 1)
	none := scan.bookIDToPaths("book-9999")
	assert.Empty(t, none)
}

// mustWrite is a tiny helper for fixture trees.
func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

// ---------------------------------------------------------------------------
// fsTagScanner — parallel extraction matches sequential, progress fires
// ---------------------------------------------------------------------------

func TestFSTagScanner_ParallelMatchesSequential(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		mustWrite(t, filepath.Join(root, fmt.Sprintf("a%02d.m4b", i)), "x")
	}
	extractor := func(p string) (string, error) {
		base := filepath.Base(p)
		// Group into 5 books so the index has multi-path entries too.
		return "book-" + string(base[1]), nil
	}

	seq := newFSTagScanner(root, pathutil.AppDirs{}, extractor).withWorkers(1)
	seqAll := append([]string(nil), seq.allPaths()...)
	seqOne := append([]string(nil), seq.bookIDToPaths("book-0")...)

	var progressCalls atomic.Int32
	par := newFSTagScanner(root, pathutil.AppDirs{}, extractor).
		withWorkers(8).
		withProgress(func(done, total int) { progressCalls.Add(1) }, 10)
	parAll := append([]string(nil), par.allPaths()...)
	parOne := append([]string(nil), par.bookIDToPaths("book-0")...)

	assert.ElementsMatch(t, seqAll, parAll, "every audio file is found regardless of worker count")
	assert.ElementsMatch(t, seqOne, parOne, "bookID index is identical")
	assert.Greater(t, int(progressCalls.Load()), 0, "progress callback fires during scan")
}

// ---------------------------------------------------------------------------
// resolveTierC — fuzzy candidates ranked by score, threshold-gated
// ---------------------------------------------------------------------------

func TestResolveTierC_RanksByScore(t *testing.T) {
	candidates := []string{
		"/disk/dune.m4b",
		"/disk/dune-messiah.m4b",
		"/disk/totally-different.m4b",
	}
	info := trackInfo{Title: "Dune", OldBasename: "dune.mp3"}

	got := resolveTierC(candidates, info, 50, 3)
	require.Len(t, got, 2, "totally-different should score below 50 and be excluded")
	assert.Equal(t, "/disk/dune.m4b", got[0].Path, "exact basename match wins")
	assert.GreaterOrEqual(t, got[0].Score, got[1].Score, "results sorted desc by score")
}

// ---------------------------------------------------------------------------
// resolveTierC — threshold filters out weak matches
// ---------------------------------------------------------------------------

func TestResolveTierC_ThresholdRespected(t *testing.T) {
	candidates := []string{"/disk/zzzzz.m4b"}
	info := trackInfo{Title: "The Hobbit", OldBasename: "hobbit.m4b"}

	got := resolveTierC(candidates, info, 85, 5)
	assert.Empty(t, got, "weak match filtered by high threshold")
}

// ---------------------------------------------------------------------------
// resolveTierC — topN cap respected
// ---------------------------------------------------------------------------

func TestResolveTierC_TopNCap(t *testing.T) {
	candidates := []string{
		"/disk/foo-1.m4b",
		"/disk/foo-2.m4b",
		"/disk/foo-3.m4b",
		"/disk/foo-4.m4b",
		"/disk/foo-5.m4b",
	}
	info := trackInfo{Title: "Foo", OldBasename: "foo.m4b"}
	got := resolveTierC(candidates, info, 0, 2)
	assert.Len(t, got, 2)
}

// ---------------------------------------------------------------------------
// resolveTierC — empty inputs return empty
// ---------------------------------------------------------------------------

func TestResolveTierC_EmptyInputs(t *testing.T) {
	assert.Empty(t, resolveTierC(nil, trackInfo{Title: "x"}, 50, 3))
	assert.Empty(t, resolveTierC([]string{"/x.m4b"}, trackInfo{}, 50, 3))
}

// ---------------------------------------------------------------------------
// resolveTierB — single on-disk file matches the bookID → resolved
// ---------------------------------------------------------------------------

func TestResolveTierB_UniqueMatch(t *testing.T) {
	scan := &fakeTagScanner{index: map[string][]string{
		"book-x": {"/disk/relocated.m4b"},
	}}
	fs := fileSet{"/disk/relocated.m4b": true}

	got, ok := resolveTierB(scan, "book-x", fs.exists)
	assert.True(t, ok)
	assert.Equal(t, "/disk/relocated.m4b", got)
}

// ---------------------------------------------------------------------------
// resolveTierB — multi-segment book → ambiguous, defer to tier C
// ---------------------------------------------------------------------------

func TestResolveTierB_AmbiguousMultiSegment(t *testing.T) {
	scan := &fakeTagScanner{index: map[string][]string{
		"book-m": {"/disk/seg1.mp3", "/disk/seg2.mp3"},
	}}
	got, ok := resolveTierB(scan, "book-m", func(string) bool { return true })
	assert.False(t, ok)
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// resolveTierB — bookID has no on-disk match → unresolved
// ---------------------------------------------------------------------------

func TestResolveTierB_NoOnDiskMatch(t *testing.T) {
	scan := &fakeTagScanner{index: map[string][]string{}}
	got, ok := resolveTierB(scan, "book-y", func(string) bool { return true })
	assert.False(t, ok)
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// resolveTierB — empty bookID short-circuits (no scan)
// ---------------------------------------------------------------------------

func TestResolveTierB_EmptyBookID(t *testing.T) {
	scan := &fakeTagScanner{index: map[string][]string{}}
	got, ok := resolveTierB(scan, "", func(string) bool { return true })
	assert.False(t, ok)
	assert.Empty(t, got)
}
