// file: internal/audiobooks/purge_run_test.go
// version: 1.0.0
// guid: 8d2e6a41-5f07-4b93-a1c8-3e9b7d04f5a2
// last-edited: 2026-10-22

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// holdByCombine writes an applied combine journal that absorbed the soft-deleted
// book id, stamped with the row's current MarkedForDeletionAt: the state in
// which an undo of the combine would restore it.
func holdByCombine(t *testing.T, store *database.PebbleStore, id string) *merge.CombineJournal {
	t.Helper()
	row, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.MarkedForDeletionAt)
	stamp := *row.MarkedForDeletionAt
	j := merge.NewCombineJournal("survivor")
	j.Status = merge.CombineJournalApplied
	j.StepJournaled = true
	j.Absorbed = []merge.CombineAbsorbed{{BookID: id, MarkedForDeletionAt: &stamp}}
	require.NoError(t, merge.WriteCombineJournal(store, j))
	return j
}

func ownFile(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: id, FilePath: "/x/" + id + ".m4b", Format: "m4b"}))
}

// The shell of a combine whose undo can still run is kept and counted as such;
// an ordinary trashed book beside it is purged. Nothing else about which books
// are purgeable changes.
func TestRunPurge_UndoHeldShellIsKeptAndCounted(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "plain", "")
	softDeleted(t, store, "shell", "")
	j := holdByCombine(t, store, "shell")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.KeptUndoPossible)
	assert.Equal(t, []string{"shell"}, res.KeptUndoPossibleIDs)
	assert.Empty(t, res.Errors)
	assert.True(t, bookGone(t, store, "plain"))
	assert.False(t, bookGone(t, store, "shell"), "the held shell must still be in the trash")

	// Once the combine is undone the journal no longer holds it.
	j.Status = merge.CombineJournalUndone
	require.NoError(t, merge.WriteCombineJournal(store, j))
	res, err = svc.RunPurge(context.Background(), PurgeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.True(t, bookGone(t, store, "shell"))
}

// A shell restored and re-trashed later carries another stamp: UndoCombine
// would not restore it, so it is not held.
func TestRunPurge_ShellWithStaleStampIsNotHeld(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	holdByCombine(t, store, "shell")
	row, err := store.GetBookByID("shell")
	require.NoError(t, err)
	later := row.MarkedForDeletionAt.Add(time.Hour)
	row.MarkedForDeletionAt = &later
	_, err = store.UpdateBook("shell", row)
	require.NoError(t, err)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Zero(t, res.KeptUndoPossible)
}

// A shell that also owns book_file rows is counted once, as owning files (the
// existing rule is checked first and is unchanged).
func TestRunPurge_OwnsFilesIsCountedBeforeUndoHold(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "both", "")
	ownFile(t, store, "both")
	holdByCombine(t, store, "both")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.SkippedOwnsFiles)
	assert.Zero(t, res.KeptUndoPossible)
	assert.Zero(t, res.Purged)
}

// The preview and the purge that follows it must agree on every count, and the
// preview must change nothing.
func TestRunPurge_PreviewAgreesWithRunAndChangesNothing(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "plain1", "")
	softDeleted(t, store, "plain2", "")
	softDeleted(t, store, "owns", "")
	ownFile(t, store, "owns")
	softDeleted(t, store, "shell", "")
	holdByCombine(t, store, "shell")
	softDeleted(t, store, "listening", "")
	purgeSeedProgress(t, store, "listening")

	prev, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true})
	require.NoError(t, err)
	require.True(t, prev.DryRun)
	assert.Equal(t, 5, prev.Attempted)
	assert.Equal(t, 2, prev.Purgeable)
	assert.Zero(t, prev.Purged, "a preview purges nothing")
	for _, id := range []string{"plain1", "plain2", "owns", "shell", "listening"} {
		assert.False(t, bookGone(t, store, id), "preview deleted %s", id)
	}
	b := svc.LastPurgeBreakdown()
	require.NotNil(t, b)
	assert.True(t, b.FromPreview)
	assert.Equal(t, 5, b.Total)
	assert.Equal(t, 2, b.Purgeable)

	run, err := svc.RunPurge(context.Background(), PurgeOptions{})
	require.NoError(t, err)
	assert.Equal(t, prev.Purgeable, run.Purged)
	assert.Equal(t, prev.SkippedOwnsFiles, run.SkippedOwnsFiles)
	assert.Equal(t, prev.KeptUndoPossible, run.KeptUndoPossible)
	assert.Equal(t, prev.KeptHasProgress, run.KeptHasProgress)
	assert.Equal(t, 1, run.SkippedOwnsFiles)
	assert.Equal(t, 1, run.KeptUndoPossible)
	assert.Equal(t, 1, run.KeptHasProgress)

	b = svc.LastPurgeBreakdown()
	require.NotNil(t, b)
	assert.False(t, b.FromPreview)
	assert.Equal(t, 3, b.Total, "what remains in the trash")
	assert.Equal(t, 1, b.OwnsFiles)
	assert.Equal(t, 1, b.UndoPossible)
	assert.Equal(t, 1, b.HasListeningState)
	assert.Zero(t, b.Purgeable)
}

// Progress runs inside the worker pool; with -race this fails if it or the
// result accumulation is not goroutine-safe. The final call reports every book.
func TestRunPurge_ProgressIsCalledConcurrentlySafely(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	const n = purgeProgressEvery*2 + 50
	for i := 0; i < n; i++ {
		softDeleted(t, store, fmt.Sprintf("b%04d", i), "")
	}

	var calls atomic.Int64
	var mu sync.Mutex
	var last [2]int
	res, err := svc.RunPurge(context.Background(), PurgeOptions{Progress: func(done, total int) {
		calls.Add(1)
		mu.Lock()
		last = [2]int{done, total}
		mu.Unlock()
	}})
	require.NoError(t, err)
	assert.Equal(t, n, res.Purged, "%+v", res)
	assert.GreaterOrEqual(t, calls.Load(), int64(3), "two interval reports and the final one")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, [2]int{n, n}, last)
}

// A cancelled pass says it stopped early and does not overwrite the remembered
// breakdown with figures that cover only part of the trash.
func TestRunPurge_CancelledPassIsNotRemembered(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "a", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := svc.RunPurge(ctx, PurgeOptions{DryRun: true})
	require.NoError(t, err)
	require.NotEmpty(t, res.Errors)
	assert.Nil(t, svc.LastPurgeBreakdown())
}

// "Purge now" on a held shell is refused with ErrPurgeUndoPossible and the
// row stays.
func TestPurgeNow_RefusesUndoHeldShell(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	holdByCombine(t, store, "shell")
	row, err := store.GetBookByID("shell")
	require.NoError(t, err)

	_, err = svc.purgeNow(row)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPurgeUndoPossible), "%v", err)
	assert.False(t, bookGone(t, store, "shell"))
}

// Discarding progress must not be a way around the hold: nothing is cleared
// and the shell stays.
func TestDiscardProgressAndPurge_RefusesUndoHeldShell(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	rec := &fakeRecorder{}
	svc.auditOverride = rec
	softDeleted(t, store, "shell", "")
	u := purgeSeedProgress(t, store, "shell")
	holdByCombine(t, store, "shell")

	_, err := svc.DiscardProgressAndPurge(context.Background(), "shell", "owner")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPurgeUndoPossible), "%v", err)
	assert.False(t, bookGone(t, store, "shell"))
	assert.Equal(t, 40, userPct(t, store, u.ID, "shell"), "no state was cleared")
	assert.Empty(t, rec.entries)
}
