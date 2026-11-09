// file: internal/books/swap_slot_test.go
// version: 1.0.0
// guid: 7c3e1a95-4b8d-4f26-9e07-d2a6b58f1c43
// last-edited: 2026-11-09

package books

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// liveStager is the restore stager Attach installs, over h's stopped store,
// with a restart that only counts.
func (h *offlineABO) liveStager(restarts *int) restoreStager {
	h.t.Helper()
	build, err := app.BinaryID(app.Options{Version: telemetry.Version()})
	require.NoError(h.t, err)
	return restoreStager{stateDir: h.state, dbPath: h.path, buildID: build, restart: func(string) { *restarts++ }}
}

// stagingLeft lists the restore staging directories beside h's store.
func (h *offlineABO) stagingLeft() []string {
	h.t.Helper()
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(h.path), restorejournal.AsideDirName))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestSwapSlotExclusive (06 §6.10; replaces TestRestoreAndResetBothStaged,
// 05-backup §5B.3.4), ABO's half: the one swap slot holds one intent, whoever
// staged it. A live restore staged by books' stager (POST /backup/restore)
// refuses `storage reset` with restorejournal.ErrSwapPending, and the
// in-place `storage restore-offline` supersedes it as its spec says
// (rejected in its journal, never two intents); a reset staged by `storage reset` refuses
// the live restore before it extracts anything; and of several live
// restores racing for the slot exactly one is staged, the others refused
// with ErrSwapPending and their extractions removed. A refused stager never
// changes the slot.
func TestSwapSlotExclusive(t *testing.T) {
	ctx := context.Background()
	archive := func(h *offlineABO) *backup.BackupInfo {
		info, err := backup.CreateBackup(h.path, "pebble", backup.BackupConfig{BackupDir: backup.ResolveDir("", h.path), MaxBackups: 5})
		require.NoError(t, err)
		return info
	}

	t.Run("live restore first", func(t *testing.T) {
		h := newOfflineABO(t)
		info := archive(h)
		restarts := 0
		staged, err := h.liveStager(&restarts).StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred")
		require.NoError(t, err)
		require.Equal(t, staged.ID, h.slot().Pending.ID)

		out, err := h.run("storage reset", "--kind", "store", "--confirm", "RESET")
		require.ErrorIs(t, err, restorejournal.ErrSwapPending, out)
		require.Equal(t, staged.ID, h.slot().Pending.ID, "a refused stager changed the slot")
		assert.Equal(t, restorejournal.KindArchive, h.slot().Pending.Kind)
		assert.Equal(t, []string{staged.ID}, h.stagingLeft())

		// The in-place offline restore is the one stager that takes a
		// pending (never an in-flight) intent's place: it rejects it as
		// superseded and runs its own swap now (05-storage §5S.3.12 "In
		// place").
		out, err = h.run("storage restore-offline", "--archive", info.Path, "--accept-loss")
		require.NoError(t, err, out)
		assert.Contains(t, out, staged.ID)
		var steps []string
		for _, r := range journalOf(t, h.state, staged.ID) {
			steps = append(steps, r.Step)
		}
		assert.Equal(t, "rejected", steps[len(steps)-1], "the superseded live restore's journal: %v", steps)
		require.False(t, h.slot().Busy())
	})

	t.Run("reset first", func(t *testing.T) {
		h := newOfflineABO(t)
		info := archive(h)
		out, err := h.run("storage reset", "--kind", "store", "--confirm", "RESET")
		require.NoError(t, err, out)
		reset := h.slot().Pending
		require.NotNil(t, reset)
		require.Equal(t, restorejournal.KindReset, reset.Kind)

		restarts := 0
		_, err = h.liveStager(&restarts).StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred")
		require.ErrorIs(t, err, restorejournal.ErrSwapPending)
		require.Equal(t, reset.ID, h.slot().Pending.ID, "a refused stager changed the slot")
		assert.Empty(t, h.stagingLeft(), "the refused restore extracted the archive")
		assert.Zero(t, restarts)
	})

	t.Run("racing live restores", func(t *testing.T) {
		h := newOfflineABO(t)
		info := archive(h)
		const n = 6
		restarts := 0
		st := h.liveStager(&restarts)
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			won  []string
			lost int
			errs []error
		)
		for range n {
			wg.Go(func() {
				got, err := st.StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred")
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					won = append(won, got.ID)
				case errors.Is(err, restorejournal.ErrSwapPending):
					lost++
				default:
					errs = append(errs, err)
				}
			})
		}
		wg.Wait()
		require.Empty(t, errs)
		require.Len(t, won, 1, "stagers that won the slot")
		require.Equal(t, n-1, lost)
		require.Equal(t, won[0], h.slot().Pending.ID)
		assert.Equal(t, won, h.stagingLeft(), "a losing stager left its extraction")
		entries, err := os.ReadDir(restorejournal.Dir(h.state))
		require.NoError(t, err)
		var journals []string
		for _, e := range entries {
			if e.IsDir() {
				journals = append(journals, e.Name())
			}
		}
		assert.Equal(t, won, journals, "a losing stager left its journal directory")
	})
}
