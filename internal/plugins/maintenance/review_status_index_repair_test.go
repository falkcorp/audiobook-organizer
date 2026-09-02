// file: internal/plugins/maintenance/review_status_index_repair_test.go
// version: 1.0.0
// guid: 4a7d1e93-6c2b-4f58-9e0a-b3c8d5f17a29
// last-edited: 2026-09-02

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newReviewIndexFixture seeds a store with three pending review items and then
// damages the status index the way the unlocked SetReviewItemDecision could:
// one item gains a second index row under a status it does not have, and one
// item loses the row under the status it does have. The third item is left
// healthy so the repair has something it must NOT touch.
func newReviewIndexFixture(t *testing.T) (*Plugin, *database.PebbleStore) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	var ids []string
	for i := range 3 {
		item, err := store.UpsertReviewItem(database.ReviewItem{
			Kind:      "regroup.multidisc",
			DedupKey:  "k-" + string(rune('a'+i)),
			FolderRef: "/books/" + string(rune('a'+i)),
			Summary:   "s",
			Payload:   `{}`,
		})
		require.NoError(t, err)
		require.Equal(t, database.ReviewStatusPending, item.Status, "upsert defaults a new item to pending")
		ids = append(ids, item.ID)
	}

	db := store.DB()
	// Doubled: a stray row under approved for an item that is still pending.
	require.NoError(t, db.Set([]byte("review_item:status:approved:"+ids[0]), nil, pebble.Sync))
	// Unindexed: the pending row for a live pending item is gone.
	require.NoError(t, db.Delete([]byte("review_item:status:pending:"+ids[1]), pebble.Sync))

	return New(fakeDeps{store: store}), store
}

func runReviewIndexRepair(t *testing.T, p *Plugin, params string) reviewStatusIndexRepairResult {
	t.Helper()
	res, err := p.repairReviewStatusIndex(context.Background(), json.RawMessage(params))
	require.NoError(t, err)
	return res
}

func TestReviewStatusIndexRepairDryRunCountsAndWritesNothing(t *testing.T) {
	p, store := newReviewIndexFixture(t)

	res := runReviewIndexRepair(t, p, `{}`)
	assert.True(t, res.DryRun)
	assert.Equal(t, 3, res.ItemsScanned)
	assert.Equal(t, 3, res.IndexEntriesScanned, "two healthy pending rows + one stray approved row")
	assert.Equal(t, 1, res.StaleIndexEntriesFound)
	assert.Equal(t, 1, res.MissingIndexEntriesFound)
	assert.Zero(t, res.StaleIndexEntriesRemoved, "a dry run must report zero written")
	assert.Zero(t, res.MissingIndexEntriesAdded, "a dry run must report zero written")

	// The store is untouched. CountReviewItems is the instrument: it reads the
	// index with no record reads (it is what the queue badge shows), so it is
	// the path that actually reports a doubled row — ListReviewItems re-checks
	// each record and masks it.
	approved, err := store.CountReviewItems(database.ReviewStatusApproved)
	require.NoError(t, err)
	assert.Equal(t, 1, approved, "dry run must leave the stray approved row in place")
	pending, err := store.CountReviewItems(database.ReviewStatusPending)
	require.NoError(t, err)
	assert.Equal(t, 2, pending, "dry run must leave the unindexed pending item unindexed")
}

func TestReviewStatusIndexRepairApplyFixesAndIsIdempotent(t *testing.T) {
	p, store := newReviewIndexFixture(t)

	res := runReviewIndexRepair(t, p, `{"apply": true}`)
	assert.False(t, res.DryRun)
	assert.Equal(t, 1, res.StaleIndexEntriesFound)
	assert.Equal(t, 1, res.StaleIndexEntriesRemoved)
	assert.Equal(t, 1, res.MissingIndexEntriesFound)
	assert.Equal(t, 1, res.MissingIndexEntriesAdded)

	// The index now agrees with the records: three pending, zero approved —
	// on both the index-only count and the record-checked list.
	approved, err := store.CountReviewItems(database.ReviewStatusApproved)
	require.NoError(t, err)
	assert.Equal(t, 0, approved)
	pending, err := store.CountReviewItems(database.ReviewStatusPending)
	require.NoError(t, err)
	assert.Equal(t, 3, pending)
	items, total, err := store.ListReviewItems(database.ReviewFilter{Status: database.ReviewStatusPending, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, items, 3)

	// A second apply finds nothing and writes nothing.
	again := runReviewIndexRepair(t, p, `{"apply": true}`)
	assert.Zero(t, again.StaleIndexEntriesFound)
	assert.Zero(t, again.MissingIndexEntriesFound)
	assert.Zero(t, again.StaleIndexEntriesRemoved)
	assert.Zero(t, again.MissingIndexEntriesAdded)
}

func TestReviewStatusIndexRepairReportsUnsupportedStore(t *testing.T) {
	p := New(fakeDeps{store: nil})
	_, err := p.repairReviewStatusIndex(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support")
}

func TestReviewStatusIndexRepairRunLogsSummary(t *testing.T) {
	p, _ := newReviewIndexFixture(t)
	rep := &fakeReporter{}
	require.NoError(t, p.runReviewStatusIndexRepair(context.Background(), json.RawMessage(`{}`), rep))
	require.NotEmpty(t, rep.logs)
	assert.Contains(t, rep.logs[0], "stale found=1 removed=0, missing found=1 added=0 (dry_run=true)")
	assert.Contains(t, rep.logs[1], "NOT fixed")
}
