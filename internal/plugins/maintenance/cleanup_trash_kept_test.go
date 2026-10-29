// file: internal/plugins/maintenance/cleanup_trash_kept_test.go
// version: 1.2.2
// guid: 2c8f5a17-6d3b-4e90-b1a4-9e7d0c3f8b52
// last-edited: 2026-10-29

package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// servedFiles are the paths a live book still serves, as the cleanup reports them.
func servedFiles(paths ...string) []database.PurgeFileNote {
	notes := make([]database.PurgeFileNote, len(paths))
	for i, p := range paths {
		notes[i] = database.PurgeFileNote{Path: p, Reason: versions.KeptReason}
	}
	return notes
}

// The trash-cleanup op lists every version it left in the trash because its
// book still serves the files (one WARN each, with book id, version id and
// path count), and its summary line counts them.
func TestRunTrashCleanup_ReportsVersionsTheBookStillServes(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{
		Purged: 2,
		Kept: []versions.KeptVersion{
			{BookID: "hush", VersionID: "ver-hush", Files: servedFiles("/lib/Gotham/hush.m4b")},
			{BookID: "year-one", VersionID: "ver-year-one", Files: servedFiles("/lib/Gotham/y1-01.m4b", "/lib/Gotham/y1-02.m4b", "/lib/Gotham/y1-03.m4b")},
		},
	}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	require.Equal(t, []string{
		"Trash cleanup: kept version ver-hush of book hush in the trash: 1 path(s) kept, book still serves this file",
		"Trash cleanup: kept version ver-year-one of book year-one in the trash: 3 path(s) kept, book still serves this file",
		"Trash cleanup: purged 2 versions, kept 2 versions whose files the book still serves; kept-file sweep: removed 0 files, 0 notes dropped (already gone), 0 still owned, 0 kept on error",
	}, rep.logs)
}

// The same versions are in the persisted result, so the operation page can link
// to them: they are counted apart from Purged and from the purged-with-files-kept set.
func TestRunTrashCleanup_ServedVersionsAreInTheResult(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{
		Purged: 2,
		Kept:   []versions.KeptVersion{{BookID: "hush", VersionID: "ver-hush", Files: servedFiles("/lib/Gotham/hush.m4b")}},
	}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	got, ok := rep.result.(TrashCleanupResult)
	require.True(t, ok, "result = %#v", rep.result)
	require.Equal(t, 1, got.ServedKept)
	require.Equal(t, []string{"ver-hush"}, got.ServedKeptVersionIDs)
	require.Equal(t, []string{"hush"}, got.ServedKeptBookIDs)
	require.Equal(t, []string{"/lib/Gotham/hush.m4b"}, got.ServedKeptFilePaths)
	require.Equal(t, 0, got.KeptFiles)
	require.Equal(t, 2, got.Purged)
}

func TestRunTrashCleanup_NothingKept(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{Purged: 1}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	require.Equal(t, []string{"Trash cleanup: purged 1 versions, kept 0 versions whose files the book still serves; kept-file sweep: removed 0 files, 0 notes dropped (already gone), 0 still owned, 0 kept on error"}, rep.logs)
}

// The kept-file sweep is in the persisted result and the run's journal: what it
// removed, what is still owned, and what it could not decide (one WARN naming
// the versions and files, which a later run retries).
func TestRunTrashCleanup_ReportsTheKeptFileSweep(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{
		KeptSweep: versions.KeptSweepResult{
			Versions: 3, Removed: 1, Gone: 1, StillKept: 2, KeptOnError: 1,
			RemovedFiles: []versions.KeptVersion{{VersionID: "ver-year-one", BookID: "year-one", Files: servedFiles("/lib/Gotham/y1-01.m4b")}},
			ErrorFiles:   []versions.KeptVersion{{VersionID: "ver-hush", BookID: "hush", Files: servedFiles("/lib/Gotham/hush.m4b")}},
		},
	}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	got, ok := rep.result.(TrashCleanupResult)
	require.True(t, ok, "result = %#v", rep.result)
	require.Equal(t, 1, got.KeptSweepRemoved)
	require.Equal(t, 1, got.KeptSweepGone)
	require.Equal(t, 2, got.KeptSweepStillKept)
	require.Equal(t, 1, got.KeptSweepKeptOnError)
	require.Equal(t, []string{"/lib/Gotham/y1-01.m4b"}, got.KeptSweepRemovedFilePaths)
	require.Equal(t, []string{"ver-hush"}, got.KeptSweepKeptOnErrorVersions)
	require.Equal(t, []string{"/lib/Gotham/hush.m4b"}, got.KeptSweepKeptOnErrorPaths)
	var warns int
	for i, l := range rep.levels {
		if l == slog.LevelWarn && strings.Contains(rep.logs[i], "kept-file sweep left 1 file(s)") {
			warns++
			require.Contains(t, rep.logs[i], "ver-hush")
			require.Contains(t, rep.logs[i], "/lib/Gotham/hush.m4b")
		}
	}
	require.Equal(t, 1, warns, "logs = %v", rep.logs)
	require.Contains(t, rep.logs[len(rep.logs)-1], "kept-file sweep: removed 1 files, 1 notes dropped (already gone), 2 still owned, 1 kept on error")
}

// A sweep that could not even list the purged versions fails the night.
func TestRunTrashCleanup_KeptSweepListErrorFailsTheRun(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{KeptSweepErr: errors.New("list down")}})
	rep := &levelReporter{}

	err := p.runTrashCleanup(context.Background(), nil, rep)

	require.Error(t, err)
	require.ErrorContains(t, err, "list down")
}

func stuckSwapBook() versions.SwapIncompleteBook {
	return versions.SwapIncompleteBook{
		BookID: "long-halloween", Title: "The Long Halloween", BookPath: "/lib/Gotham/The Long Halloween",
		Versions: []versions.SwapIncompleteVersion{
			{ID: "ver-out", Status: database.BookVersionStatusSwappingOut},
			{ID: "ver-in", Status: database.BookVersionStatusSwappingIn},
			{ID: "ver-other", Status: database.BookVersionStatusAlt},
		},
		Resume: versions.SwapResumeFinish,
	}
}

// A book whose version swap did not finish is listed by the nightly run: the
// result carries the swapping versions (not the book's other versions), the
// book and its path, and the log has one Warn line; the run does not fail, and
// nothing is resumed by it.
func TestRunTrashCleanup_ListsBooksWhoseSwapDidNotFinish(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{
		Purged:         1,
		SwapIncomplete: []versions.SwapIncompleteBook{stuckSwapBook()},
	}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	got, ok := rep.result.(TrashCleanupResult)
	require.True(t, ok, "result = %#v", rep.result)
	require.Equal(t, 2, got.SwapIncomplete)
	require.Equal(t, []string{"ver-out", "ver-in"}, got.SwapIncompleteVersionIDs)
	require.Equal(t, []string{"long-halloween"}, got.SwapIncompleteBookIDs)
	require.Equal(t, []string{"/lib/Gotham/The Long Halloween"}, got.SwapIncompleteFilePaths)
	require.Len(t, got.SwapIncompleteBooks, 1)
	require.Equal(t, versions.SwapResumeFinish, got.SwapIncompleteBooks[0].Resume)

	var warns []string
	for i, l := range rep.levels {
		if l == slog.LevelWarn && strings.Contains(rep.logs[i], "swap that did not finish") {
			warns = append(warns, rep.logs[i])
		}
	}
	require.Len(t, warns, 1, "logs = %v", rep.logs)
	require.Contains(t, warns[0], "long-halloween")
	require.Contains(t, rep.logs[len(rep.logs)-1], "1 book(s) have a version swap that did not finish")
}

// The books are not listed when no swap is stuck.
func TestRunTrashCleanup_NoUnfinishedSwapsListsNone(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{Purged: 1}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	got := rep.result.(TrashCleanupResult)
	require.Zero(t, got.SwapIncomplete)
	require.Empty(t, got.SwapIncompleteBookIDs)
	for _, l := range rep.logs {
		require.NotContains(t, l, "swap that did not finish")
	}
}

// Not being able to list the unfinished swaps fails the night: a run that
// could not look must not read as one with nothing to report.
func TestRunTrashCleanup_UnlistableUnfinishedSwapsFailTheOp(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{
		Purged:            1,
		SwapIncompleteErr: errors.New("injected: list failed"),
	}})
	rep := &levelReporter{}

	err := p.runTrashCleanup(context.Background(), nil, rep)

	require.Error(t, err)
	require.Contains(t, err.Error(), "injected: list failed")
	require.Contains(t, err.Error(), "version swap did not finish")
	// No purge failed, and the log does not claim a count of failures it has no
	// purge to name ("0 failed" beside an error read as a contradiction).
	require.Len(t, rep.logs, 1)
	require.Contains(t, rep.logs[0], "could not be listed")
	require.NotContains(t, rep.logs[0], "0 failed")
}
