// file: internal/audiobooks/purge_files_bound_test.go
// version: 1.0.0
// guid: 8c4e1a7d-3f26-4b95-a0d8-7e2b5c9f1a43
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// writeFile makes a one-byte file (and its folder) at path.
func writeFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A file the preview said stays is never removed. The preview showed it as
// staying because a live book names it; that book is trashed after the preview
// (so the bound keeps it out of the pass), and the pass purges the other book
// that names the file. The file is the trashed book's now, and it is restorable.
func TestRunPurge_FileOfABookTrashedAfterThePreviewIsNotRemoved(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "x.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	_, err := store.CreateBook(&database.Book{ID: "L", Title: "L", FilePath: x})
	require.NoError(t, err)

	pv := previewOnce(t, svc, nil)
	require.Equal(t, 1, pv.Purgeable, "%+v", pv)
	require.Empty(t, pv.FilesToDelete, "a live book names it")
	require.Equal(t, 1, pv.FilesKept)

	time.Sleep(2 * time.Millisecond)
	_, err = store.ModifyBook("L", func(b *database.Book) error {
		yes := true
		b.MarkedForDeletion = &yes
		return nil
	})
	require.NoError(t, err)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.TrashedAfterPreview, "%+v", res)
	assert.True(t, bookGone(t, store, "T"))
	assert.False(t, bookGone(t, store, "L"), "left alone, still restorable")
	assert.Zero(t, res.FilesDeleted)
	assert.True(t, fileExists(x), "the preview said it stays, and a restorable book names it")
	assert.NotEmpty(t, res.Errors, "the kept file is reported on the purged book's line")
}

// Two trashed books naming one file, both purgeable: the preview lists the file
// once, and the pass removes it once, after the second book is purged. (A rule
// that counted the other trashed row at purge time would keep the file for
// good, or the preview would say it stays while the pass removes it.)
func TestRunPurge_TwoTrashedBooksSharingAFileBothPurgedRemoveItOnce(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "twin.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T1", x)
	softDeleted(t, store, "T2", x)

	pv := previewOnce(t, svc, nil)
	require.Equal(t, 2, pv.Purgeable, "%+v", pv)
	assert.Equal(t, []string{x}, pv.FilesToDelete)
	assert.Equal(t, 1, pv.FilesToDeleteCount)
	assert.Zero(t, pv.FilesKept)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.FilesDeleted, "%+v", res)
	assert.Empty(t, res.Errors)
	assert.False(t, fileExists(x))
}

// One of the two is kept (it holds listening state and has no listed sibling):
// the file is the kept book's, in the preview and in the pass.
func TestRunPurge_FileNamedByAKeptTrashedBookStays(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "shared.m4b")
	writeFile(t, x)
	softDeleted(t, store, "gone", x)
	softDeleted(t, store, "kept", x)
	purgeSeedProgress(t, store, "kept")

	pv := previewOnce(t, svc, nil)
	require.Equal(t, 1, pv.Purgeable, "%+v", pv)
	require.Equal(t, 1, pv.KeptHasProgress)
	assert.Empty(t, pv.FilesToDelete)
	assert.Equal(t, 1, pv.FilesKept)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Zero(t, res.FilesDeleted)
	assert.True(t, fileExists(x), "the kept book still owns it")
	assert.False(t, bookGone(t, store, "kept"))
}

// A trashed book the retention window leaves out still names its file.
func TestRunPurge_FileNamedByABookInsideTheRetentionWindowStays(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "recent.m4b")
	writeFile(t, x)
	old := time.Now().Add(-60 * 24 * time.Hour)
	yes := true
	_, err := store.CreateBook(&database.Book{ID: "old", Title: "old", FilePath: x, MarkedForDeletion: &yes, MarkedForDeletionAt: &old})
	require.NoError(t, err)
	softDeleted(t, store, "recent", x) // trashed an hour ago: inside a 30-day window

	days := 30
	pv := previewOnce(t, svc, &days)
	require.Equal(t, 1, pv.Purgeable, "%+v", pv)
	require.Equal(t, 1, pv.NotYetDue, "%+v", pv)
	assert.Empty(t, pv.FilesToDelete, "the recent book still names it")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, OlderThanDays: &days, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.True(t, fileExists(x))
	assert.False(t, bookGone(t, store, "recent"))
}

// The purge removes only files its preview listed.
func TestRunPurge_FileTheBoundPreviewDidNotListIsKept(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "unlisted.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	_, err := store.CreateBook(&database.Book{ID: "L", Title: "L", FilePath: x})
	require.NoError(t, err)

	pv := previewOnce(t, svc, nil)
	require.Empty(t, pv.FilesToDelete)

	// The live book goes away (hard delete) after the preview: the file is
	// unreferenced now, but the preview never listed it.
	require.NoError(t, store.DeleteBook("L"))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Zero(t, res.FilesDeleted)
	assert.True(t, fileExists(x), "the preview said it stays")
}

// A book the preview showed as kept is not purged by the pass that follows it,
// even when what kept it has changed; a new preview lists it.
func TestRunPurge_BookTheBoundPreviewShowedAsKeptIsNotPurged(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "plain", "")
	softDeleted(t, store, "owner", "")
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "f1", BookID: "owner", FilePath: "/x/owner.m4b", Format: "m4b"}))

	pv := previewOnce(t, svc, nil)
	require.Equal(t, 1, pv.Purgeable, "%+v", pv)
	require.Equal(t, 1, pv.SkippedOwnsFiles, "%+v", pv)

	require.NoError(t, store.DeleteBookFile("f1"))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.NotInPreview, "%+v", res)
	assert.True(t, bookGone(t, store, "plain"))
	assert.False(t, bookGone(t, store, "owner"), "the preview showed it as kept")
	assert.Equal(t, res.Attempted, res.Decided(), "every book is decided: %+v", res)
	assert.Contains(t, res.Summary(), "1 would be purged now but the preview had not listed")

	pv2 := previewOnce(t, svc, nil)
	assert.Equal(t, 1, pv2.Purgeable, "a new preview lists it")
}

// The same bound on the carry path: a book with listening state the preview
// showed as kept (no listed sibling) is not carried and purged when a sibling
// appears afterwards.
func TestRunPurge_CarryPathAlsoHoldsToThePreview(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")

	pv := previewOnce(t, svc, nil)
	require.Equal(t, 1, pv.KeptHasProgress, "%+v", pv)

	groupBook(t, store, "keep", "g1", false, true)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.NotInPreview, "%+v", res)
	assert.False(t, bookGone(t, store, "dup"))
	assert.Zero(t, userPct(t, store, u.ID, "keep"), "nothing was carried")
}

// A bound whose preview is not remembered (a restart, or newer previews pushed
// it out) is refused before anything is read.
func TestRunPurge_BoundWithNoRememberedPreviewIsRefused(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "b", "")
	at := time.Now()

	res, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: &at})
	require.ErrorIs(t, err, ErrPurgePreviewUnknown)
	require.NotNil(t, res)
	assert.True(t, res.StoppedEarly)
	assert.Contains(t, res.StopReason, "run the preview again")
	assert.False(t, bookGone(t, store, "b"))

	// The unattended pass has no preview to find and is not affected.
	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged)
}

func TestRunPurge_OnlyTheLastFewPreviewsAreRemembered(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "b", "")
	first := previewOnce(t, svc, nil)
	for range purgePreviewsKept {
		previewOnce(t, svc, nil)
	}
	_, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: first.TrashedBefore})
	require.ErrorIs(t, err, ErrPurgePreviewUnknown)
	assert.False(t, bookGone(t, store, "b"))
}

// An instant after the pass began is clamped to its start: a book trashed
// while the pass runs is not one any preview could have shown.
func TestRunPurge_FutureTrashedBeforeIsClampedToThePassStart(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shown", "")
	late := time.Now().Add(time.Hour)
	yes := true
	_, err := store.CreateBook(&database.Book{ID: "during", Title: "during", MarkedForDeletion: &yes, MarkedForDeletionAt: &late})
	require.NoError(t, err)

	future := time.Now().Add(2 * time.Hour)
	svc.rememberPreview(&PurgeResult{
		TrashedBefore: &future,
		purgeable:     map[string]struct{}{"shown": {}, "during": {}},
	})
	res, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: &future})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.TrashedAfterPreview, "%+v", res)
	assert.False(t, bookGone(t, store, "during"))
	require.NotNil(t, res.TrashedBefore)
	assert.True(t, res.TrashedBefore.Before(late), "the bound a pass echoes is the clamped one: %v", res.TrashedBefore)
}

// A book restored by older code that left its first trashing's stamp on the
// live row, then trashed again after the preview by a writer that sets only the
// flag, is left out: it carries the new time, not the stale one.
func TestRunPurge_ReTrashedWithAStaleStampAfterThePreviewIsLeftAlone(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	_, err := store.CreateBook(&database.Book{ID: "again", Title: "again"})
	require.NoError(t, err)
	stale := time.Now().Add(-90 * 24 * time.Hour)
	_, err = store.SeedLegacyBookRowForTest("again", func(b *database.Book) error {
		b.MarkedForDeletionAt = &stale
		return nil
	})
	require.NoError(t, err)
	softDeleted(t, store, "shown", "")

	pv := previewOnce(t, svc, nil)
	require.Equal(t, 1, pv.Purgeable, "%+v", pv)

	time.Sleep(2 * time.Millisecond)
	_, err = store.ModifyBook("again", func(b *database.Book) error {
		yes := true
		b.MarkedForDeletion = &yes
		return nil
	})
	require.NoError(t, err)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.TrashedAfterPreview, "%+v", res)
	assert.False(t, bookGone(t, store, "again"), "the preview never counted it")
}

// The retention window leaves out a row nobody stamped when it was written
// recently, instead of taking it whatever its age; the numbers add up.
func TestRunPurge_RetentionDoesNotTakeAnUnstampedRowWrittenRecently(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedLegacy(t, store, "legacy")
	softDeleted(t, store, "old", "")
	// "old" was trashed an hour ago and "legacy" was written just now: a window
	// of one day keeps both of them inside it.
	days := 1
	res, err := svc.RunPurge(context.Background(), PurgeOptions{OlderThanDays: &days, Unattended: true})
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "%+v", res)
	assert.Equal(t, 2, res.NotYetDue, "%+v", res)
	assert.Equal(t, res.Attempted, res.Decided())
	assert.False(t, bookGone(t, store, "legacy"))
	assert.Contains(t, res.Summary(), "2 not yet past the retention window")
}

// The preview's numbers add up to the trash: a book the retention window left
// out is counted, not hidden.
func TestRunPurge_PreviewCountsAddUpWithTheRetentionWindow(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	old := time.Now().Add(-60 * 24 * time.Hour)
	yes := true
	_, err := store.CreateBook(&database.Book{ID: "old", Title: "old", MarkedForDeletion: &yes, MarkedForDeletionAt: &old})
	require.NoError(t, err)
	softDeleted(t, store, "recent", "")

	days := 30
	pv := previewOnce(t, svc, &days)
	assert.Equal(t, 2, pv.Attempted)
	assert.Equal(t, 1, pv.Purgeable)
	assert.Equal(t, 1, pv.NotYetDue)
	assert.Equal(t, pv.Attempted, pv.Decided())
}

// The nightly pass has no preview to hold it to, but the file of a trashed book
// it keeps is still that book's: removeDeferredFiles looks at the trash as the
// pass left it.
func TestPurgeUnattended_FileNamedByAKeptTrashedBookStays(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "night.m4b")
	writeFile(t, x)
	softDeleted(t, store, "gone", x)
	softDeleted(t, store, "kept", x)
	purgeSeedProgress(t, store, "kept")

	res, err := svc.PurgeSoftDeletedUnattended(context.Background(), true, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Zero(t, res.FilesDeleted)
	assert.True(t, fileExists(x), "the kept book still names it")
	assert.False(t, bookGone(t, store, "kept"))
	require.Len(t, res.Errors, 1, "reported on the purged book's line")
	assert.Contains(t, res.Errors[0], "kept")
}

// A book that was in the trash when the preview ran and has only aged into the
// retention window since is one the preview counted as not yet due: the purge
// leaves it alone and counts it the same way. It was not "put in the trash
// after the preview", which is what the screen would otherwise say.
func TestRunPurge_BookThatAgedIntoTheWindowSincePreviewIsStillNotYetDue(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	old := time.Now().Add(-60 * 24 * time.Hour)
	yes := true
	_, err := store.CreateBook(&database.Book{ID: "old", Title: "old", MarkedForDeletion: &yes, MarkedForDeletionAt: &old})
	require.NoError(t, err)
	// Trashed 23 hours ago: inside a one-day window when the preview runs. The
	// purge's clock is two hours later (the test clock: nothing here waits on
	// real time), which puts the book past the window's edge.
	aging := time.Now().Add(-23 * time.Hour)
	_, err = store.CreateBook(&database.Book{ID: "aging", Title: "aging", MarkedForDeletion: &yes, MarkedForDeletionAt: &aging})
	require.NoError(t, err)

	days := 1
	pv := previewOnce(t, svc, &days)
	require.Equal(t, 1, pv.Purgeable, "%+v", pv)
	require.Equal(t, 1, pv.NotYetDue, "%+v", pv)

	svc.purgeClock = func() time.Time { return time.Now().Add(2 * time.Hour) }

	res, err := svc.RunPurge(context.Background(), PurgeOptions{OlderThanDays: &days, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.NotYetDue, "it was already in the trash at the preview: %+v", res)
	assert.Zero(t, res.TrashedAfterPreview, "%+v", res)
	assert.False(t, bookGone(t, store, "aging"))
	assert.Equal(t, res.Attempted, res.Decided(), "%+v", res)
}
