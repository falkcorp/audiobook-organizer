// file: internal/scanner/book_file_duration_test.go
// version: 1.0.0
// guid: 5e1a9c7d-2b84-4f36-a0d9-8c3f6e2b1a47
// last-edited: 2026-09-25

package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

// A single-file book's row takes the duration and codec ProcessFile already
// read (Book.fileMediaInfo). Until 2026-09-25 the scan computed that value,
// put it on book.Duration, and wrote the row with Duration 0 and no codec (the
// 2026-09-23 "Monster Makers" row), which ABS, summing row durations, shows as
// a "0" book. No second read of the file may happen when the value is in hand.
func TestSingleFileBookRowCarriesTheDurationTheScanAlreadyRead(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	audio := filepath.Join(t.TempDir(), "Monster Makers.m4b")
	require.NoError(t, os.WriteFile(audio, []byte("audio-bytes"), 0o644))
	book, err := store.CreateBook(&database.Book{FilePath: audio, Title: "Monster Makers"})
	require.NoError(t, err)

	t.Cleanup(bookfileaudio.SetProbeForTesting(func(path string) (*mediainfo.MediaInfo, error) {
		t.Errorf("probed %s although the scan already read its media info", path)
		return nil, errors.New("unexpected probe")
	}))

	createSingleFileBookFile(&Book{
		FilePath:      audio,
		fileMediaInfo: &mediainfo.MediaInfo{Duration: 41234, Codec: "AAC"},
	}, logger.New("test"))

	rows, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 41234, rows[0].Duration)
	require.Equal(t, "AAC", rows[0].Codec)
}

// The scan's own value is an ESTIMATE: the row stays 0 rather than record a
// guess as real, and the estimate is not smuggled back in through book.Duration
// (which the scan copied from the same estimate) or a re-probe.
func TestSingleFileBookRowRejectsAnEstimatedScanDuration(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	audio := filepath.Join(t.TempDir(), "Guess.m4b")
	require.NoError(t, os.WriteFile(audio, []byte("audio-bytes"), 0o644))
	est := 5000
	book, err := store.CreateBook(&database.Book{FilePath: audio, Title: "Guess", Duration: &est})
	require.NoError(t, err)

	t.Cleanup(bookfileaudio.SetProbeForTesting(func(path string) (*mediainfo.MediaInfo, error) {
		t.Errorf("probed %s although the scan already read its media info", path)
		return nil, errors.New("unexpected probe")
	}))

	createSingleFileBookFile(&Book{
		FilePath:      audio,
		fileMediaInfo: &mediainfo.MediaInfo{Duration: est, DurationEstimated: true},
	}, logger.New("test"))

	rows, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 0, rows[0].Duration)
}

// A multi-file book's segments have no duration in hand when the rows are built
// (chapter synthesis reads them later), so each row is filled by a header read
// of its own file, never from the book total.
func TestMultiFileBookRowsAreProbedPerSegment(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	seg1 := filepath.Join(dir, "Part 01.mp3")
	seg2 := filepath.Join(dir, "Part 02.mp3")
	require.NoError(t, os.WriteFile(seg1, []byte("one"), 0o644))
	require.NoError(t, os.WriteFile(seg2, []byte("two"), 0o644))
	total := 99999
	book, err := store.CreateBook(&database.Book{FilePath: seg1, Title: "Parts", Duration: &total})
	require.NoError(t, err)

	durations := map[string]int{seg1: 700, seg2: 800}
	var mu sync.Mutex
	var probed []string
	t.Cleanup(bookfileaudio.SetProbeForTesting(func(path string) (*mediainfo.MediaInfo, error) {
		mu.Lock()
		probed = append(probed, path)
		mu.Unlock()
		return &mediainfo.MediaInfo{Duration: durations[path], Codec: "MP3"}, nil
	}))

	createBookFilesForBook(seg1, []string{seg1, seg2}, logger.New("test"), normalizeToDirectory)

	rows, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	got := map[string]int{}
	for _, r := range rows {
		got[r.FilePath] = r.Duration
	}
	require.Equal(t, map[string]int{seg1: 700, seg2: 800}, got)
	require.ElementsMatch(t, []string{seg1, seg2}, probed)
}
