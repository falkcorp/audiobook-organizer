// file: internal/plugins/maintenance/duration_backfill_outcomes_test.go
// version: 1.0.1
// guid: de2bb3ee-9054-4ce7-ba91-b3f361468064
// last-edited: 2026-11-04

package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// stubFFprobe puts a stub "ffprobe" script on PATH (and only there) and
// disables mediainfo, so a header probe does exactly what the script does. An
// empty script leaves PATH with no ffprobe at all. No audio is read.
func stubFFprobe(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\n"+script+"\n"), 0o700))
		t.Setenv("PATH", dir+":/bin:/usr/bin")
	} else {
		t.Setenv("PATH", dir)
	}
	t.Setenv("ABK_DISABLE_MEDIAINFO", "1")
}

// outcomesBook creates a one-segment book whose file exists on disk (a junk
// .mp3: nothing here decodes it) and has no stored duration.
func outcomesBook(t *testing.T, s *database.PebbleStore, title string, present bool) (database.Book, database.BookFile) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, title+".mp3")
	if present {
		require.NoError(t, os.WriteFile(path, make([]byte, 480_000), 0o600))
	}
	book, err := s.CreateBook(&database.Book{Title: title, FilePath: dir})
	require.NoError(t, err)
	f := database.BookFile{BookID: book.ID, FilePath: path, FileSize: 480_000, TrackNumber: 1}
	require.NoError(t, s.CreateBookFile(&f))
	return *book, f
}

func onlySegment(t *testing.T, s *database.PebbleStore, bookID string) database.BookFile {
	t.Helper()
	files, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	return files[0]
}

// A present file that ffprobe cannot read is recorded on its row, one attempt
// per run, reaching Unreadable at the ceiling; the book's own total is still
// withheld.
func TestDurationReextract_RecordsProbeFailuresPerRow(t *testing.T) {
	stubFFprobe(t, "exit 1")
	s := newRepairPebble(t)
	book, _ := outcomesBook(t, s, "Year One", true)
	p := New(fakeDeps{store: s})

	for i := 1; i <= database.DeepScanMaxAttempts; i++ {
		require.NoError(t, p.runDurationBackfill(context.Background(), applyReextractParams(t), &fakeReporter{}))
		seg := onlySegment(t, s, book.ID)
		require.Nil(t, seg.Duration, "run %d", i)
		require.Equal(t, i, seg.Scan.DurationOK.Attempts, "run %d", i)
		require.NotEmpty(t, seg.Scan.DurationOK.LastError)
		require.False(t, seg.Scan.DurationOK.Done)
	}
	seg := onlySegment(t, s, book.ID)
	require.Equal(t, database.DurationUnreadable, seg.DurationState())
	got, _ := s.GetBookByID(book.ID)
	require.True(t, got.Duration == nil || *got.Duration == 0, "an unreadable segment must not produce a book total")
}

// A dry run writes nothing, failures included.
func TestDurationReextract_DryRunRecordsNoProbeFailure(t *testing.T) {
	stubFFprobe(t, "exit 1")
	s := newRepairPebble(t)
	book, _ := outcomesBook(t, s, "The Killing Joke", true)
	p := New(fakeDeps{store: s})
	require.NoError(t, p.runDurationBackfill(context.Background(), mustReextractParams(t, true, 0), &fakeReporter{}))
	seg := onlySegment(t, s, book.ID)
	require.Equal(t, database.StageStatus{}, seg.Scan.DurationOK)
	require.Equal(t, database.DurationUnprobed, seg.DurationState())
}

// A missing ffprobe says nothing about the file, and neither does a file that
// is not there: no attempt either way.
func TestDurationReextract_UncountedOutcomesRecordNothing(t *testing.T) {
	t.Run("ffprobe not installed", func(t *testing.T) {
		stubFFprobe(t, "")
		s := newRepairPebble(t)
		book, _ := outcomesBook(t, s, "Year One", true)
		require.NoError(t, New(fakeDeps{store: s}).runDurationBackfill(context.Background(), applyReextractParams(t), &fakeReporter{}))
		seg := onlySegment(t, s, book.ID)
		require.Equal(t, database.StageStatus{}, seg.Scan.DurationOK)
		require.Equal(t, database.DurationUnprobed, seg.DurationState())
	})
	t.Run("file absent", func(t *testing.T) {
		stubFFprobe(t, "exit 1")
		s := newRepairPebble(t)
		book, _ := outcomesBook(t, s, "Batman Hush", false)
		require.NoError(t, New(fakeDeps{store: s}).runDurationBackfill(context.Background(), applyReextractParams(t), &fakeReporter{}))
		seg := onlySegment(t, s, book.ID)
		require.Equal(t, database.StageStatus{}, seg.Scan.DurationOK)
		require.Equal(t, database.DurationUnprobed, seg.DurationState())
	})
}

// A book with one measurable segment and one unreadable one: the measured
// segment is Done, the unreadable one carries its attempt, in the same run.
func TestDurationReextract_MixedBookRecordsBothOutcomes(t *testing.T) {
	stubFFprobe(t, "exit 1")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book, err := s.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: dir})
	require.NoError(t, err)
	bad := filepath.Join(dir, "02.mp3")
	require.NoError(t, os.WriteFile(bad, make([]byte, 480_000), 0o600))
	good := database.BookFile{BookID: book.ID, FilePath: filepath.Join(dir, "01.mp3"), FileSize: 2_000_000,
		TrackNumber: 1, AcoustIDFingerprintDurationSec: 100}
	unreadable := database.BookFile{BookID: book.ID, FilePath: bad, FileSize: 480_000, TrackNumber: 2}
	require.NoError(t, s.CreateBookFile(&good))
	require.NoError(t, s.CreateBookFile(&unreadable))

	require.NoError(t, New(fakeDeps{store: s}).runDurationBackfill(context.Background(), applyReextractParams(t), &fakeReporter{}))

	files, err := s.GetBookFiles(book.ID)
	require.NoError(t, err)
	byPath := map[string]database.BookFile{}
	for _, f := range files {
		byPath[f.FilePath] = f
	}
	g := byPath[good.FilePath]
	require.Equal(t, 100, durtest.Sec(g.Duration))
	require.Equal(t, database.StageStatus{Done: true}, g.Scan.DurationOK)
	u := byPath[bad]
	require.Nil(t, u.Duration)
	require.Equal(t, 1, u.Scan.DurationOK.Attempts)
	require.Equal(t, database.DurationProbeFailed, u.DurationState())
}

// A book whose every segment fails to probe must record them with ONE aggregate
// recompute, not one per row (the 2026-09-19 O(n^2) incident). Real PebbleStore:
// the per-write recompute lives inside it, so a MockStore could never go red.
func TestDurationReextract_ManyFailedSegmentsRecomputeAggregatesOnce(t *testing.T) {
	stubFFprobe(t, "exit 1")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book, err := s.CreateBook(&database.Book{Title: "Batman: Hush", FilePath: dir})
	require.NoError(t, err)
	const n = 25
	for i := 0; i < n; i++ {
		path := filepath.Join(dir, fmt.Sprintf("part%03d.mp3", i))
		require.NoError(t, os.WriteFile(path, make([]byte, 480_000), 0o600))
		require.NoError(t, s.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: path, FileSize: 480_000, TrackNumber: i + 1}))
	}

	logs := aggtest.Capture(t) // after setup: only the op's recomputes count
	require.NoError(t, New(fakeDeps{store: s}).runDurationBackfill(context.Background(), applyReextractParams(t), &fakeReporter{}))

	require.Equal(t, 1, aggtest.CountInvocations(logs(), book.ID),
		"RecomputeBookAggregates must run once for a %d-segment book of failures", n)
	files, err := s.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, n)
	for _, f := range files {
		require.Nil(t, f.Duration)
		require.Equal(t, 1, f.Scan.DurationOK.Attempts, f.FilePath)
	}
}
