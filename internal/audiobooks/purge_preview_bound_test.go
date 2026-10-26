// file: internal/audiobooks/purge_preview_bound_test.go
// version: 1.0.1
// guid: 5d0c8e21-93a4-4b7f-8e16-2a7f1c4b9d03
// last-edited: 2026-10-24

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

// A purge is bound to the preview in front of the user: it takes the books the
// preview listed and no book trashed after it (PurgeOptions.TrashedBefore).

func previewOnce(t *testing.T, svc *AudiobookService, days *int) *PurgeResult {
	t.Helper()
	res, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true, OlderThanDays: days})
	require.NoError(t, err)
	require.NotNil(t, res.TrashedBefore, "a preview returns the instant the purge is bound to")
	return res
}

// trashedNow adds a book trashed at this instant, after any earlier preview.
func trashedNow(t *testing.T, store *database.PebbleStore, id, path string) {
	t.Helper()
	time.Sleep(2 * time.Millisecond)
	yes := true
	now := time.Now()
	_, err := store.CreateBook(&database.Book{ID: id, Title: id, FilePath: path, MarkedForDeletion: &yes, MarkedForDeletionAt: &now})
	require.NoError(t, err)
}

func TestRunPurge_InteractivePassWithoutAPreviewIsRefused(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "old", "")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{})
	require.ErrorIs(t, err, ErrPurgePreviewRequired)
	require.NotNil(t, res)
	assert.True(t, res.StoppedEarly)
	assert.Zero(t, res.Purged)
	assert.False(t, bookGone(t, store, "old"), "a refused pass deletes nothing")
}

func TestRunPurge_BookTrashedAfterThePreviewIsNotPurged(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shown", "")

	pv := previewOnce(t, svc, nil)
	require.Equal(t, 1, pv.Purgeable, "%+v", pv)
	require.Zero(t, pv.TrashedAfterPreview)

	trashedNow(t, store, "late", "")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.TrashedAfterPreview, "%+v", res)
	assert.Equal(t, 2, res.Attempted)
	assert.True(t, bookGone(t, store, "shown"))
	assert.False(t, bookGone(t, store, "late"), "the preview never counted it")
	assert.Equal(t, 2, res.Decided(), "a book the bound left out is decided, so progress reaches the total")
	assert.Contains(t, res.Summary(), "1 trashed after the preview")

	// A preview taken now does list it.
	pv2 := previewOnce(t, svc, nil)
	assert.Equal(t, 1, pv2.Purgeable)
	assert.Zero(t, pv2.TrashedAfterPreview)
}

// A row older code trashed with no stamp (every writer stamps now, and the
// store stamps any write that trashes a row, so such rows exist only from before)
// is judged by its last write: trashed after the preview and it is left out,
// trashed before it and it is still shown.
func TestRunPurge_UnstampedTrashFallsBackToTheLastWrite(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedLegacy(t, store, "early-nostamp")

	pv := previewOnce(t, svc, nil)
	require.Equal(t, 1, pv.Purgeable, "%+v", pv)

	trashedLegacy(t, store, "late-nostamp")
	res, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.TrashedAfterPreview)
	assert.True(t, bookGone(t, store, "early-nostamp"))
	assert.False(t, bookGone(t, store, "late-nostamp"))
}

// trashedLegacy adds a book trashed the way older code did: the flag set, no
// stamp (the store stamps every write that trashes a row, so the row is seeded
// through the legacy path).
func trashedLegacy(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	time.Sleep(2 * time.Millisecond)
	_, err := store.CreateBook(&database.Book{ID: id, Title: id})
	require.NoError(t, err)
	_, err = store.SeedLegacyBookRowForTest(id, func(b *database.Book) error {
		yes := true
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, nil
		return nil
	})
	require.NoError(t, err)
	got, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.Nil(t, got.MarkedForDeletionAt, "fixture: the row is unstamped")
}

func TestTrashedAtOrBefore(t *testing.T) {
	bound := time.Now()
	before, after := bound.Add(-time.Minute), bound.Add(time.Minute)
	yes := true
	for _, tc := range []struct {
		name string
		book database.Book
		want bool
	}{
		{"stamp before", database.Book{MarkedForDeletion: &yes, MarkedForDeletionAt: &before}, true},
		{"stamp equal", database.Book{MarkedForDeletion: &yes, MarkedForDeletionAt: &bound}, true},
		{"stamp after", database.Book{MarkedForDeletion: &yes, MarkedForDeletionAt: &after}, false},
		{"no stamp, last write before", database.Book{MarkedForDeletion: &yes, UpdatedAt: &before}, true},
		{"no stamp, last write after", database.Book{MarkedForDeletion: &yes, UpdatedAt: &after}, false},
		{"the stamp wins over a later write", database.Book{MarkedForDeletion: &yes, MarkedForDeletionAt: &before, UpdatedAt: &after}, true},
		{"nothing known is not provably older", database.Book{MarkedForDeletion: &yes}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, trashedAtOrBefore(&tc.book, bound))
		})
	}
}

// The listing is checked before the merge lock; a book restored and trashed
// again after the preview carries a new stamp and is left alone when the pass
// re-reads it under the lock.
func TestPurgeOne_ReTrashedAfterThePreviewIsLeftAloneUnderTheLock(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "b1", "")
	stale, err := store.GetBookByID("b1")
	require.NoError(t, err)
	bound := time.Now().Add(-30 * time.Minute)

	_, err = store.ModifyBook("b1", func(b *database.Book) error {
		now := time.Now()
		b.MarkedForDeletionAt = &now
		return nil
	})
	require.NoError(t, err)

	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	env.trashedBefore = &bound
	out := svc.purgeOne(stale, false, env)
	assert.Equal(t, purgeAfterBound, out.kind)
	assert.False(t, bookGone(t, store, "b1"))

	env.trashedBefore = nil
	out = svc.purgeOne(stale, false, env)
	assert.Equal(t, purgePurged, out.kind, "without the bound the same book purges")
	assert.True(t, bookGone(t, store, "b1"))
}

func TestPurgeOne_CarryPathAlsoHoldsTheBoundUnderTheLock(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	stale, err := store.GetBookByID("dup")
	require.NoError(t, err)
	bound := time.Now().Add(-30 * time.Minute)

	_, err = store.ModifyBook("dup", func(b *database.Book) error {
		now := time.Now()
		b.MarkedForDeletionAt = &now
		return nil
	})
	require.NoError(t, err)

	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	env.trashedBefore = &bound
	out := svc.purgeOne(stale, false, env)
	assert.Equal(t, purgeAfterBound, out.kind)
	assert.False(t, bookGone(t, store, "dup"))
	assert.Zero(t, userPct(t, store, u.ID, "keep"), "nothing was carried")
}

// The preview says what delete_files would do to each file, from the same
// decision the purge makes (planPurgeFile), and the purge then does exactly
// that.
func TestRunPurge_PreviewListsTheFilesAPurgeWouldRemove(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	root := filepath.Join(base, "lib")
	inRoot := filepath.Join(root, "Author", "a.m4b")
	outside := filepath.Join(base, "lib2", "b.m4b")
	referenced := filepath.Join(root, "Author", "c.m4b")
	for _, p := range []string{inRoot, outside, referenced} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	softDeleted(t, store, "a", inRoot)
	softDeleted(t, store, "b", outside)
	softDeleted(t, store, "c", referenced)
	_, err := store.CreateBook(&database.Book{ID: "live", Title: "live", FilePath: referenced})
	require.NoError(t, err)

	pv := previewOnce(t, svc, nil)
	assert.Equal(t, 3, pv.Purgeable, "%+v", pv)
	assert.Equal(t, []string{inRoot}, pv.FilesToDelete)
	assert.Equal(t, 1, pv.FilesToDeleteCount)
	assert.Equal(t, 2, pv.FilesKept, "outside the root and still referenced by a live book stay")
	assert.Zero(t, pv.FilesDeleted)
	for _, p := range []string{inRoot, outside, referenced} {
		_, err := os.Stat(p)
		require.NoError(t, err, "a preview removes nothing: %s", p)
	}
	assert.Contains(t, pv.Summary(), "remove 1 file(s) and leave 2 in place")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.FilesDeleted, "exactly the file the preview listed")
	_, err = os.Stat(inRoot)
	assert.True(t, os.IsNotExist(err), "the listed file is gone")
	for _, p := range []string{outside, referenced} {
		_, err := os.Stat(p)
		assert.NoError(t, err, "%s stays, as the preview said", p)
	}
}

// With a retention window the bound is no later than the window's own cutoff:
// a book that only aged into the window after the preview was not counted.
func TestRunPurge_PreviewBoundRespectsTheRetentionWindow(t *testing.T) {
	svc, _, _ := setupPurgeBoundary(t)
	days := 1
	pv := previewOnce(t, svc, &days)
	assert.True(t, pv.TrashedBefore.Before(time.Now().Add(-23*time.Hour)), "bound %v", pv.TrashedBefore)
	assert.True(t, pv.TrashedBefore.After(time.Now().Add(-25*time.Hour)), "bound %v", pv.TrashedBefore)

	none := previewOnce(t, svc, nil)
	assert.True(t, none.TrashedBefore.After(time.Now().Add(-time.Minute)))
}
