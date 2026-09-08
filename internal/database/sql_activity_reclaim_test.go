// file: internal/database/sql_activity_reclaim_test.go
// version: 1.1.0
// guid: c8f31d92-4a67-4b05-8e13-2d90a5c76f48
// last-edited: 2026-09-08

package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reclaim deletes production data out of a store that is still being
// written to, so the tests that matter are the ones proving what it must NOT
// delete:
//
//   - nothing at all while reads are still served from Pebble, because until
//     the cutover the Pebble copy IS the activity log; and
//   - nothing newer than the cutoff once it is deleting, because Record
//     dual-writes forever and a delete with no lower bound would race it.
//
// A test that only checked "does it delete" would pass against a plain
// WipeAllActivity, which is the implementation this one exists to rule out.

// seedActivity writes one row into a store at a chosen age.
func seedActivity(t *testing.T, s ActivityStorer, tier string, age time.Duration, summary string) {
	t.Helper()
	_, err := s.Record(ActivityEntry{
		Timestamp: time.Now().UTC().Add(-age),
		Tier:      tier,
		Type:      "test_event",
		Level:     "info",
		Source:    "reclaim_test",
		Summary:   summary,
	})
	require.NoError(t, err)
}

// countAll returns every row a store holds, summed across tiers via the exact
// counter. Query's total cannot be used here: it is a pagination probe on both
// backends and reports single digits for a large keyspace.
func countAll(t *testing.T, s ActivityCounter) int {
	t.Helper()
	total := 0
	for _, tier := range actTiers {
		n, err := s.CountActivity(context.Background(), tier, nil)
		require.NoError(t, err)
		total += n
	}
	return total
}

// newReclaimFixture builds a migrating store over real Pebble and SQLite
// backends. Both start empty; the caller seeds what each case needs.
func newReclaimFixture(t *testing.T, readSecondary bool) (*MigratingActivityStore, *PebbleActivityStore, *SQLActivityStore) {
	t.Helper()
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	return NewMigratingActivityStore(pebbleStore, sqlStore, readSecondary), pebbleStore, sqlStore
}

func TestReclaimMigratedActivity_RefusesWhileReadsAreStillOnPebble(t *testing.T) {
	// THE fail-closed test. Before the cutover, Pebble is what the activity UI
	// reads. Deleting from it would destroy live data rather than reclaim dead
	// data, so the op must refuse however old the rows are.
	mig, pebbleStore, sqlStore := newReclaimFixture(t, false /* readSecondary */)
	for range 5 {
		seedActivity(t, pebbleStore, "change", 30*24*time.Hour, "ancient row")
	}
	seedActivity(t, sqlStore, "change", 30*24*time.Hour, "copied row")

	res, err := ReclaimMigratedActivity(context.Background(), mig, time.Hour, false /* apply */, nil)
	require.NoError(t, err, "a refusal is a result, not an error")

	assert.True(t, res.Refused, "must refuse while read_secondary is false")
	assert.Contains(t, res.RefusedReason, "still served from Pebble")
	assert.Zero(t, res.RowsDeleted)
	assert.Equal(t, 5, countAll(t, pebbleStore),
		"not one row may be deleted before the cutover")

	// And the census is still reported — that is what makes a blocked run useful.
	assert.Equal(t, 5, res.PrimaryRows)
	assert.Equal(t, 1, res.SecondaryRows)
	assert.False(t, res.ReadSecondary)
}

func TestReclaimMigratedActivity_DeletesOnlyRowsBehindTheCutoff(t *testing.T) {
	// THE anti-race test. Record fans out to BOTH backends forever — there is no
	// post-cutover stop — so the Pebble store is live, not frozen. A reclaim that
	// wiped the prefix would take rows written seconds ago. Only rows behind the
	// cutoff may go.
	mig, pebbleStore, sqlStore := newReclaimFixture(t, true /* readSecondary */)

	seedActivity(t, pebbleStore, "change", 10*24*time.Hour, "old A")
	seedActivity(t, pebbleStore, "change", 10*24*time.Hour, "old B")
	seedActivity(t, pebbleStore, "debug", 10*24*time.Hour, "old C")
	// Inside the retention window — must survive.
	seedActivity(t, pebbleStore, "change", time.Minute, "fresh, mid-dual-write")

	// Secondary must not be behind the primary or the parity guard trips.
	for range 6 {
		seedActivity(t, sqlStore, "change", 10*24*time.Hour, "copied")
	}

	res, err := ReclaimMigratedActivity(context.Background(), mig, 48*time.Hour, false /* apply */, nil)
	require.NoError(t, err)
	require.False(t, res.Refused, "reason: %s", res.RefusedReason)

	assert.Equal(t, 3, res.RowsDeleted, "the three rows behind the cutoff")
	assert.Equal(t, 1, countAll(t, pebbleStore),
		"the row inside the retention window must survive — that is the dual-write race guard")

	// SQLite is untouched: this reclaims the redundant copy, not the real one.
	assert.Equal(t, 6, countAll(t, sqlStore))
}

func TestReclaimMigratedActivity_DryRunReportsWithoutDeleting(t *testing.T) {
	mig, pebbleStore, sqlStore := newReclaimFixture(t, true)
	for range 4 {
		seedActivity(t, pebbleStore, "change", 10*24*time.Hour, "old")
	}
	seedActivity(t, pebbleStore, "change", time.Minute, "fresh")
	for range 5 {
		seedActivity(t, sqlStore, "change", 10*24*time.Hour, "copied")
	}

	res, err := ReclaimMigratedActivity(context.Background(), mig, 48*time.Hour, true /* dryRun */, nil)
	require.NoError(t, err)
	require.False(t, res.Refused, "reason: %s", res.RefusedReason)

	assert.True(t, res.DryRun)
	assert.Zero(t, res.RowsDeleted)
	assert.Equal(t, 4, res.EligibleRows,
		"a dry run must report exactly what an applied run would delete")
	assert.Equal(t, 5, countAll(t, pebbleStore), "dry run must not delete anything")
}

func TestReclaimMigratedActivity_RefusesWhenSecondaryIsBehindPrimary(t *testing.T) {
	// Defence in depth. The cutover flip already required verified parity, so a
	// secondary that has since fallen behind means an assumption underneath this
	// has broken. Stop rather than delete on top of it.
	mig, pebbleStore, sqlStore := newReclaimFixture(t, true)
	for range 10 {
		seedActivity(t, pebbleStore, "change", 10*24*time.Hour, "old")
	}
	seedActivity(t, sqlStore, "change", 10*24*time.Hour, "only one copied")

	res, err := ReclaimMigratedActivity(context.Background(), mig, 48*time.Hour, false, nil)
	require.NoError(t, err)

	assert.True(t, res.Refused)
	assert.Contains(t, res.RefusedReason, "fewer rows")
	assert.Zero(t, res.RowsDeleted)
	assert.Equal(t, 10, countAll(t, pebbleStore))
}

func TestReclaimMigratedActivity_RetainFloorOverridesAnUnsafeRequest(t *testing.T) {
	// retain=0 asks to delete everything up to this instant, which is precisely
	// the dual-write race. The floor silently raises it instead of honouring it.
	mig, pebbleStore, sqlStore := newReclaimFixture(t, true)
	seedActivity(t, pebbleStore, "change", 10*24*time.Hour, "old")
	seedActivity(t, pebbleStore, "change", time.Minute, "written moments ago")
	for range 2 {
		seedActivity(t, sqlStore, "change", 10*24*time.Hour, "copied")
	}

	res, err := ReclaimMigratedActivity(context.Background(), mig, 0 /* retain */, false, nil)
	require.NoError(t, err)
	require.False(t, res.Refused, "reason: %s", res.RefusedReason)

	assert.Equal(t, 1, res.RowsDeleted)
	assert.Equal(t, 1, countAll(t, pebbleStore),
		"retain=0 must not be honoured literally; the floor keeps recent history")
	assert.WithinDuration(t, time.Now().UTC().Add(-activityReclaimMinRetain), res.Cutoff, time.Minute)
}

func TestReclaimMigratedActivity_NonWrapperStoreIsARefusalNotAnError(t *testing.T) {
	// A Pebble-only deployment has no duplicate copy. That is an answer the op
	// should report, not a failure it should raise.
	//
	// The refusal must also NAME the store it got. "Pebble-only escape hatch"
	// and "someone wired a decorator in front of the migration wrapper and this
	// op has silently refused ever since" produce the identical outcome, and the
	// concrete type is the only thing that separates them in a log.
	for _, tc := range []struct {
		name  string
		store ActivityStorer
		want  string
	}{
		{"nil store", nil, "<nil>"},
		{"pebble-only", newTestPebbleActivityStore(t), "PebbleActivityStore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ReclaimMigratedActivity(context.Background(), tc.store, time.Hour, false, nil)
			require.NoError(t, err)
			assert.True(t, res.Refused)
			assert.Contains(t, res.RefusedReason, "no duplicate copy")
			assert.Contains(t, res.RefusedReason, tc.want,
				"the refusal must name the concrete store type it was handed")
		})
	}
}

func TestReclaimMigratedActivity_ReportsProgressForBothPhases(t *testing.T) {
	// Each phase is one blocking call per tier with no callback of its own, so
	// between-tier is the only liveness signal the op has. The census needs one
	// as much as the prune does: it walks the primary keyspace twice per tier,
	// and on a refused run it is the entire run. If either stops reporting, a
	// long reclaim goes silent and looks wedged.
	mig, pebbleStore, sqlStore := newReclaimFixture(t, true)
	seedActivity(t, pebbleStore, "change", 10*24*time.Hour, "old")
	seedActivity(t, sqlStore, "change", 10*24*time.Hour, "copied")

	byPhase := map[ActivityReclaimPhase][]string{}
	_, err := ReclaimMigratedActivity(context.Background(), mig, 48*time.Hour, false,
		func(phase ActivityReclaimPhase, tier string, _, total int, _ ActivityReclaimTier) {
			byPhase[phase] = append(byPhase[phase], tier)
			assert.Equal(t, len(actTiers), total)
		})
	require.NoError(t, err)
	assert.Equal(t, actTiers, byPhase[ReclaimPhaseCensus], "every tier must report a census, in order")
	assert.Equal(t, actTiers, byPhase[ReclaimPhasePrune], "every tier must report a prune, in order")
}

func TestReclaimMigratedActivity_CensusReportsEvenWhenTheRunIsRefused(t *testing.T) {
	// The refusal path is the one production takes today, so it is the path that
	// has to stay observable: no prune callbacks, but a full census.
	mig, pebbleStore, _ := newReclaimFixture(t, false /* reads still on Pebble */)
	seedActivity(t, pebbleStore, "change", 10*24*time.Hour, "old")

	byPhase := map[ActivityReclaimPhase][]string{}
	res, err := ReclaimMigratedActivity(context.Background(), mig, 48*time.Hour, false,
		func(phase ActivityReclaimPhase, tier string, _, _ int, _ ActivityReclaimTier) {
			byPhase[phase] = append(byPhase[phase], tier)
		})
	require.NoError(t, err)
	require.True(t, res.Refused)
	assert.Equal(t, actTiers, byPhase[ReclaimPhaseCensus],
		"a refused run still walks the whole keyspace; it must say so as it goes")
	assert.Empty(t, byPhase[ReclaimPhasePrune], "a refused run must not prune")
}
