// file: internal/maintenance/jobs/backfill_sync_ids_test.go
// version: 1.0.0
// guid: a20f7354-5d7e-49aa-b3b5-94758d52bfc9
// last-edited: 2026-07-30

// Package jobs_test — coverage for the backfill-sync-ids maintenance job
// (TASK-04). Every test drives a real PebbleStore because the sync_item /
// sync_file keyspaces are *PebbleStore-only capability interfaces
// (database.AsSyncIdentityStore / AsSyncFileStore), not part of database.Store.
package jobs_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
)

// seedSyncLibrary creates n books, each with filesPerBook BookFiles, and
// returns the book IDs in creation order.
func seedSyncLibrary(t *testing.T, store *database.PebbleStore, n, filesPerBook int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		book, err := store.CreateBook(&database.Book{
			Title:    fmt.Sprintf("Sync Backfill Book %d", i),
			FilePath: fmt.Sprintf("/library/sync-%d", i),
		})
		require.NoError(t, err)
		for f := 0; f < filesPerBook; f++ {
			require.NoError(t, store.CreateBookFile(&database.BookFile{
				BookID:   book.ID,
				FilePath: fmt.Sprintf("/library/sync-%d/track-%02d.mp3", i, f),
			}))
		}
		ids = append(ids, book.ID)
	}
	return ids
}

// collectSyncIDs snapshots every book's syncID and every file's syncFileID so
// two runs can be compared byte-for-byte.
func collectSyncIDs(t *testing.T, store *database.PebbleStore, bookIDs []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, id := range bookIDs {
		syncID, ok, err := store.GetSyncIDForBook(id)
		require.NoError(t, err)
		if ok {
			out["book:"+id] = syncID
		}
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		for _, f := range files {
			sfID, ok, err := store.GetSyncFileID(id, f.ID)
			require.NoError(t, err)
			if ok {
				out["file:"+id+":"+f.ID] = sfID
			}
		}
	}
	return out
}

func newSyncPebbleStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestBackfillSyncIDsJob_Registered(t *testing.T) {
	assertJobRegistered(t, "backfill-sync-ids")
	j, err := maintenance.Get("backfill-sync-ids")
	require.NoError(t, err)
	require.NotEmpty(t, j.Name())
	require.NotEmpty(t, j.Description())
	require.NotEmpty(t, j.Category())
	require.NotNil(t, j.DefaultParams())
	// Idempotent re-run from book 0 IS the resume story — no checkpoint index.
	require.False(t, j.CanResume())
}

// Test 1 — fresh library: every book gets a syncID, every file a syncFileID.
func TestBackfillSyncIDsJob_FreshLibrary(t *testing.T) {
	store := newSyncPebbleStore(t)
	bookIDs := seedSyncLibrary(t, store, 20, 3)

	j, err := maintenance.Get("backfill-sync-ids")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))

	for _, id := range bookIDs {
		syncID, ok, err := store.GetSyncIDForBook(id)
		require.NoError(t, err)
		require.True(t, ok, "book %s has no syncID", id)
		require.Len(t, syncID, 36, "syncID must be a 36-char UUID (§1.7.1)")

		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, files, 3)
		for _, f := range files {
			_, ok, err := store.GetSyncFileID(id, f.ID)
			require.NoError(t, err)
			require.True(t, ok, "file %s of book %s has no syncFileID", f.ID, id)
		}
	}
}

// Test 2 — idempotent re-run: the full ID set must be byte-identical.
func TestBackfillSyncIDsJob_IdempotentRerun(t *testing.T) {
	store := newSyncPebbleStore(t)
	bookIDs := seedSyncLibrary(t, store, 12, 2)

	j, err := maintenance.Get("backfill-sync-ids")
	require.NoError(t, err)

	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	first := collectSyncIDs(t, store, bookIDs)
	require.Len(t, first, 12+12*2)

	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	second := collectSyncIDs(t, store, bookIDs)

	require.Equal(t, first, second, "re-run must not re-mint any ID")
}

// Test 3 — partial pre-existing state (mint-on-first-encounter already hit some
// books): those IDs survive untouched and the rest get minted.
func TestBackfillSyncIDsJob_PreservesPreExistingIDs(t *testing.T) {
	store := newSyncPebbleStore(t)
	bookIDs := seedSyncLibrary(t, store, 10, 2)

	pre := map[string]string{}
	for i, id := range bookIDs {
		if i%2 != 0 {
			continue
		}
		syncID, err := store.MintOrGetSyncID(id)
		require.NoError(t, err)
		pre[id] = syncID
	}

	j, err := maintenance.Get("backfill-sync-ids")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))

	for id, want := range pre {
		got, ok, err := store.GetSyncIDForBook(id)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, want, got, "pre-existing syncID for %s was re-minted", id)
	}
	for _, id := range bookIDs {
		_, ok, err := store.GetSyncIDForBook(id)
		require.NoError(t, err)
		require.True(t, ok, "book %s still has no syncID after backfill", id)
	}
}

// Test 4 — dryRun mints nothing at all.
func TestBackfillSyncIDsJob_DryRunMintsNothing(t *testing.T) {
	store := newSyncPebbleStore(t)
	bookIDs := seedSyncLibrary(t, store, 8, 2)

	j, err := maintenance.Get("backfill-sync-ids")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, true))

	for _, id := range bookIDs {
		_, ok, err := store.GetSyncIDForBook(id)
		require.NoError(t, err)
		require.False(t, ok, "dry run minted a syncID for %s", id)

		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		for _, f := range files {
			_, ok, err := store.GetSyncFileID(id, f.ID)
			require.NoError(t, err)
			require.False(t, ok, "dry run minted a syncFileID for %s", f.ID)
		}
	}
}

// Test 5 — concurrency sanity under -race: exactly one syncID per book, and
// ListSyncFilesForBook sees exactly one entry per file.
func TestBackfillSyncIDsJob_ConcurrentRaceSanity(t *testing.T) {
	store := newSyncPebbleStore(t)
	bookIDs := seedSyncLibrary(t, store, 50, 2)

	j, err := maintenance.Get("backfill-sync-ids")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))

	for _, id := range bookIDs {
		_, ok, err := store.GetSyncIDForBook(id)
		require.NoError(t, err)
		require.True(t, ok)

		syncFiles, err := store.ListSyncFilesForBook(id)
		require.NoError(t, err)
		require.Len(t, syncFiles, 2, "book %s must have exactly one sync_file per file", id)
	}
}

// Test 6 — the worker pool is bounded but NOT sequential. Guards against a
// future edit silently reverting to RunItemsOptions' Concurrency: 0 default,
// which is the exact anti-pattern behind this repo's 3-hour single-core stall.
func TestBackfillSyncIDsConcurrencyIsNotSequential(t *testing.T) {
	require.Greater(t, jobs.BackfillConcurrency(), 1,
		"backfill concurrency must be > 1 (RunItemsOptions.Concurrency 0/1 == sequential)")
}
