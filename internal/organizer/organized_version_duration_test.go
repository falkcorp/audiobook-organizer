// file: internal/organizer/organized_version_duration_test.go
// version: 1.0.0
// guid: 0d6c3b7e-94a2-4f1e-8c5d-7b2a1e9f4c36
// last-edited: 2026-09-25

package organizer

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

// On 2026-09-23 library.scan created "Monster Makers" with a book_file row of
// Duration 0, and CreateOrganizedVersion copied that row as-is (`newBF := bf`)
// into the organized book. ABS shows a book's duration as the sum of its rows,
// so the organized book read "0" in the app. The organized copy's rows must be
// filled from the file each row now points at: the NEW path.
func TestCreateOrganizedVersion_ZeroDurationSourceRowsAreFilledFromTheNewPath(t *testing.T) {
	store := newLandingTestStore(t)
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}

	srcDir := t.TempDir()
	src1 := filepath.Join(srcDir, "ch01.mp3")
	src2 := filepath.Join(srcDir, "ch02.mp3")
	require.NoError(t, os.WriteFile(src1, []byte("ch01"), 0o644))
	require.NoError(t, os.WriteFile(src2, []byte("ch02"), 0o644))

	targetDir := filepath.Join(rootDir, "Author", "Monster Makers")
	require.NoError(t, os.MkdirAll(targetDir, 0o775))
	dst1 := filepath.Join(targetDir, "Monster Makers - 01.mp3")
	dst2 := filepath.Join(targetDir, "Monster Makers - 02.mp3")
	require.NoError(t, os.WriteFile(dst1, []byte("ch01"), 0o644))
	require.NoError(t, os.WriteFile(dst2, []byte("ch02"), 0o644))

	// The book's total must NOT be used for either row: it is a two-file book.
	total := 9999
	book, err := store.CreateBook(&database.Book{Title: "Monster Makers", FilePath: srcDir, Duration: &total})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: src1, TrackNumber: 1}))
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: src2, TrackNumber: 2}))

	durations := map[string]int{dst1: 1200, dst2: 1500}
	var mu sync.Mutex
	var probed []string
	t.Cleanup(bookfileaudio.SetProbeForTesting(func(path string) (*mediainfo.MediaInfo, error) {
		mu.Lock()
		probed = append(probed, path)
		mu.Unlock()
		d, ok := durations[path]
		if !ok {
			return nil, errors.New("probed a path that is not the organized copy's")
		}
		return &mediainfo.MediaInfo{Duration: d, Codec: "MP3"}, nil
	}))

	landing := &Landing{
		Path:    targetDir,
		Files:   map[string]string{src1: dst1, src2: dst2},
		Created: []string{dst1, dst2},
	}
	created, err := svcCreateOrganized(t, store, book, landing)
	require.NoError(t, err)

	rows, err := store.GetBookFiles(created.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	got := map[string]int{}
	for _, r := range rows {
		got[r.FilePath] = r.Duration
		require.Equal(t, "MP3", r.Codec, "an empty codec is filled from the same probe")
	}
	require.Equal(t, map[string]int{dst1: 1200, dst2: 1500}, got,
		"each organized row's duration comes from ITS new file, not 0 and not the book total")
	require.ElementsMatch(t, []string{dst1, dst2}, probed, "the probe reads the NEW paths only")
}

// A single-file book whose only row has Duration 0: the book's own duration IS
// the file's, so it is used without reading the file.
func TestCreateOrganizedVersion_SingleFileZeroDurationRowTakesBookDuration(t *testing.T) {
	store := newLandingTestStore(t)
	rootDir := t.TempDir()
	config.AppConfig = config.Config{RootDir: rootDir}

	src := filepath.Join(t.TempDir(), "book.m4b")
	require.NoError(t, os.WriteFile(src, []byte("book"), 0o644))
	dst := filepath.Join(rootDir, "Author", "Title", "Title.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o775))
	require.NoError(t, os.WriteFile(dst, []byte("book"), 0o644))

	dur := 36000
	book, err := store.CreateBook(&database.Book{Title: "Title", FilePath: src, Duration: &dur})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: src, TrackNumber: 1}))

	t.Cleanup(bookfileaudio.SetProbeForTesting(func(path string) (*mediainfo.MediaInfo, error) {
		t.Errorf("probed %s although the single-file book's duration was in hand", path)
		return nil, errors.New("unexpected probe")
	}))

	landing := &Landing{Path: dst, Files: map[string]string{src: dst}, Created: []string{dst}}
	created, err := svcCreateOrganized(t, store, book, landing)
	require.NoError(t, err)

	rows, err := store.GetBookFiles(created.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, dst, rows[0].FilePath)
	require.Equal(t, 36000, rows[0].Duration)
}
