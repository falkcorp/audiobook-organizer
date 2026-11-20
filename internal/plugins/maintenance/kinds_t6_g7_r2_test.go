// file: internal/plugins/maintenance/kinds_t6_g7_r2_test.go
// version: 1.0.0
// guid: c7d04b0b-4cbb-43f7-91a3-3d91c9912788
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
)

// A native shell refuses a run whose owner left out a dep its kind reads, naming
// the dep, before it reads or writes anything. One row per kind that checks one
// of the optional deps (the version-group repair and the rewrite and denumber
// kinds have tests of their own).
func TestT6G7Kinds_NativeNeedsItsDeps(t *testing.T) {
	for _, tc := range []struct {
		kind  tasks.Kind
		dep   string
		strip func(*t6g7Deps)
	}{
		{titleRepairKind, "AgreedChapterTitle", func(d *t6g7Deps) { d.AgreedChapterTitle = nil }},
		{reportsPruneKind, "RootDir", func(d *t6g7Deps) { d.RootDir = nil }},
		{userMergeKind, "Store", func(d *t6g7Deps) { d.Store = nil }},
		{tagBackfillKind, "Store", func(d *t6g7Deps) { d.Store = nil }},
		{repointUnrecordedRenamesKind, "Store", func(d *t6g7Deps) { d.Store = nil }},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			p := newT6G7Probe(t)
			deps := p.deps()
			tc.strip(&deps)
			def := t6g7ByKind(t, (&Plugin{}).t6g7Kinds(deps, nil), tc.kind)
			_, err := runT6G7Kind(t, def, tasks.ModePreview, nil, "")
			require.ErrorContains(t, err, "t6g7Deps."+tc.dep+" is not set")
			require.Empty(t, p.rec.snapshot())
		})
	}
}

// The series-phantom core stops writing when the lease it holds lapses (HEAD's
// controller can lose it; the native kernel fences the writer instead), and a
// live core with no interlock refuses before a write.
func TestSeriesPhantomCore_StopsWritingWhenTheLeaseLapses(t *testing.T) {
	newEnv := func(t *testing.T) (*t6g7Probe, seriesPhantomRepairEnv) {
		p := newT6G7Probe(t)
		seedSeriesPhantomRig(p, 7)
		return p, seriesPhantomRepairEnv{
			t6g7Env:               t6g7HeadEnv(&fakeReporter{}, true),
			Store:                 p.store.(*t6g7TestStore),
			Writer:                p.rec,
			InvalidateSeriesCache: func() {},
		}
	}

	p, env := newEnv(t)
	released := false
	env.StandDown = func(context.Context, string) (t6g7Hold, error) {
		return t6g7Hold{Lost: func() bool { return true }, Release: func() { released = true }}, nil
	}
	res, err := seriesPhantomRepairCore(context.Background(), seriesPhantomModeNull, false, seriesPhantomRepairParams{}, env)
	require.Error(t, err)
	require.ErrorIs(t, err, errSeriesPhantomStandDownLost)
	require.Zero(t, res.Nulled+res.Repointed, "a book was counted written after the lease lapsed")
	require.NotEmpty(t, res.Aborted)
	require.Empty(t, p.rec.snapshot(), "a book was written after the lease lapsed")
	require.True(t, released, "the lapsed hold was not released")

	p, env = newEnv(t)
	_, err = seriesPhantomRepairCore(context.Background(), seriesPhantomModeNull, false, seriesPhantomRepairParams{}, env)
	require.ErrorContains(t, err, "no scan stand-down available")
	require.Empty(t, p.rec.snapshot())
}

// The folder-audio repair checks the lease twice: before it starts a book, and
// per row inside the batch of a book it has started (a lease that lapses
// between two rows leaves the rest of that book's rows alone).
func TestRepointFolderAudioCore_StopsAMidBatchBookWhenTheLeaseLapses(t *testing.T) {
	p := newT6G7Probe(t)
	seedFolderAudioRig(t, p, 1)
	env := rfCoreEnv{
		t6g7Env: t6g7HeadEnv(&fakeReporter{}, true),
		Reads:   p.store, Writer: p.rec, Queue: p.store, RootDir: p.root,
	}
	var checks atomic.Int64
	released := false
	env.StandDown = func(context.Context, string) (t6g7Hold, error) {
		// The first check, the worker's own before it starts the book, finds the
		// lease; every later one (the batch's) finds it lapsed.
		return t6g7Hold{Lost: func() bool { return checks.Add(1) > 1 }, Release: func() { released = true }}, nil
	}
	rep, err := repointMissingToFolderAudioCore(context.Background(), rfParams{}, env)
	require.ErrorContains(t, err, "scan stand-down lease lapsed")
	// The batch checks after each row, so the first row lands and the book stops
	// there: the book is reported failed (stopped), never applied.
	require.Equal(t, map[string]int{rfOutcomeFailed: 1}, rep.ByOutcome)
	require.Len(t, p.rec.snapshot(), 1, "the book's batch was attempted once")
	require.True(t, released)
}

// series-denumber's membership read checks the store's capability before it looks
// at the ids, as HEAD's database.SeriesMembershipAllVersions does: a store that
// could never answer fails closed even when nothing is requested.
func TestSeriesDenumberMembership_ChecksTheCapabilityBeforeTheEmptyShortcut(t *testing.T) {
	_, err := seriesDenumberMembership(seriesDenumberHead{}, nil)
	require.ErrorContains(t, err, "cannot load bulk series membership")

	m, err := seriesDenumberMembership(seriesDenumberHead{OpsStore: &database.MockStore{}}, nil)
	require.NoError(t, err)
	require.Empty(t, m)
}

// HEAD's tag-backfill pool keeps its raw NumCPU()*4 workers (HEAD's RunItems has
// no cap); the native pool is the same count up to tasks.MaxFixed. Asserted
// apart, so each holds on any core count.
func TestTagBackfill_HeadPoolKeepsTheRawWorkerCount(t *testing.T) {
	require.Equal(t, runtime.NumCPU()*4, tagBackfillHeadWorkers(), "HEAD's pool size")
	require.Equal(t, min(runtime.NumCPU()*4, tasks.MaxFixed), tagBackfillParallelism().Workers(), "the native pool is capped at the kernel's maximum")

	// The core hands the HEAD pool the raw count and the native pool the capped
	// parallelism.
	var rig t6g7WriteCase
	for _, c := range t6g7WriteCases() {
		if c.kind == tagBackfillKind {
			rig = c
		}
	}
	p := newT6G7Probe(t)
	rig.setup(t, p)
	env := tagBackfillEnv{t6g7Env: t6g7HeadEnv(&fakeReporter{}, true), Store: p.store, Writer: p.rec}
	var gotHead int
	var gotPar tasks.Parallelism
	items := env.Items
	env.Items = func(ctx context.Context, par tasks.Parallelism, n int, o t6g7ItemsOptions, fn func(context.Context, int) error) error {
		gotHead, gotPar = o.HeadWorkers, par
		return items(ctx, par, n, o, fn)
	}
	require.NoError(t, tagBackfillCore(context.Background(), false, tagBackfillParams{}, env))
	require.Equal(t, runtime.NumCPU()*4, gotHead)
	require.Equal(t, tagBackfillParallelism().String(), gotPar.String())
}

// HEAD's pool runs the worker count the loop names for HEAD, not the
// parallelism's own, when they differ: 70 workers (above tasks.MaxFixed) all run
// at once. Every item waits until 70 are in flight, so a pool capped at 64 never
// gets there and the test fails by its timeout instead of by timing luck.
func TestT6G7HeadItems_HonoursHeadWorkersAboveTheKernelCap(t *testing.T) {
	const want = 70
	require.Greater(t, want, tasks.MaxFixed)
	var active, peak atomic.Int64
	var mu sync.Mutex
	err := t6g7HeadItems(&fakeReporter{})(context.Background(), tasks.Fixed(want, "test"), want+10,
		t6g7ItemsOptions{HeadWorkers: want}, func(context.Context, int) error {
			n := active.Add(1)
			defer active.Add(-1)
			mu.Lock()
			peak.Store(max(peak.Load(), n))
			mu.Unlock()
			deadline := time.Now().Add(3 * time.Second)
			for active.Load() < want && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			return nil
		})
	require.NoError(t, err)
	require.EqualValues(t, want, peak.Load())
}

// The version-group-primary repair core stops before it plans a group once the
// lease lapses (an error, nothing written, the hold released), and a live core with no
// interlock refuses before it plans or writes.
func TestVersionGroupPrimaryRepairCore_StopsWritingWhenTheLeaseLapses(t *testing.T) {
	newEnv := func(t *testing.T) (*t6g7Probe, vgPrimaryRepairEnv) {
		p := newT6G7Probe(t)
		seedVGPrimaryRig(t, p)
		base := t6g7HeadEnv(&fakeReporter{}, true)
		base.OpID = "op-r2"
		return p, vgPrimaryRepairEnv{
			t6g7Env: base,
			Store:   p.store.(vgPrimaryRepairReads),
			Writer:  p.rec,
			RootDir: p.root,
			Prober:  p.prober,
		}
	}
	params := vgPrimaryRepairParams{Apply: true, GroupIDs: []string{"vg-double"}}

	p, env := newEnv(t)
	released := false
	env.StandDown = func(context.Context, string) (t6g7Hold, error) {
		return t6g7Hold{Lost: func() bool { return true }, Release: func() { released = true }}, nil
	}
	rep, err := versionGroupPrimaryRepairCore(context.Background(), params, env)
	require.ErrorContains(t, err, "scan stand-down lease lost")
	require.Zero(t, rep.Applied)
	require.Empty(t, p.rec.snapshot(), "a group was written after the lease lapsed")
	require.True(t, released, "the lapsed hold was not released")

	p, env = newEnv(t)
	_, err = versionGroupPrimaryRepairCore(context.Background(), params, env)
	require.ErrorContains(t, err, "no interlock configured")
	require.Empty(t, p.rec.snapshot())
}
