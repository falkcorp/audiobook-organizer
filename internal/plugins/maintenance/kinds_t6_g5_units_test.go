// file: internal/plugins/maintenance/kinds_t6_g5_units_test.go
// version: 1.0.0
// guid: 2f4fb7e6-9d63-4c0e-8a57-2a1d6c0b3e91
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// t6g5WritingKinds are the HEAD ids of the group's writing kinds; the three
// ReadOnly ones (metadata-refresh, missing-file-audit, missing-file-repair)
// carry no mode contract to refuse a key for.
var t6g5WritingKinds = []string{
	"itunes.clone-into-library",
	"itunes.playlist-import",
	"itunes.regroup",
	"maintenance.repair-library-state",
	"maintenance.mark-missing-files",
	"maintenance.merge-same-path-dupes",
	"maintenance.metadata-cache-reap",
	"maintenance.missing-file-repoint",
	"maintenance.move-book-file-rows",
	"maintenance.purge-empty-narrators",
	"maintenance.split-joined-narrators",
}

// TestT6G5Kinds_EveryWritingKindRefusesModeKeys: the mode comes from the start
// request, so a params key that carries it is refused before anything runs.
// Clone's HEAD apply/rollback/repair keys are selectors, not the mode, so it
// refuses only the dry-run keys.
func TestT6G5Kinds_EveryWritingKindRefusesModeKeys(t *testing.T) {
	for _, headID := range t6g5WritingKinds {
		keys := []string{"apply", "dry_run", "dryRun", "DRY_RUN"}
		if headID == "itunes.clone-into-library" {
			keys = []string{"dry_run", "dryRun", "DRY_RUN"}
		}
		for _, key := range keys {
			t.Run(headID+"/"+key, func(t *testing.T) {
				f := newT6G5Fake()
				d := t6g5KindByName(t, (&Plugin{}).t6g5Kinds(f.deps(), nil), headID)
				rc := newT6G5Host(d.Kind(), tasks.ModeLive, &t6g5Gate{})
				_, err := t6g5Run(t, d, rc, `{"`+key+`":true}`)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "the run's mode comes from the start request")
				assert.Empty(t, f.handed(), "refused before a writer is built")
				assert.Empty(t, f.writer.called())
			})
		}
	}
}

func TestT6G5Kinds_CloneAcceptsItsSelectorKeys(t *testing.T) {
	for _, key := range []string{"apply", "rollback", "repair"} {
		f := newT6G5Fake()
		d := t6g5KindByName(t, (&Plugin{}).t6g5Kinds(f.deps(), nil), "itunes.clone-into-library")
		_, err := t6g5Run(t, d, newT6G5Host(d.Kind(), tasks.ModePreview, &t6g5Gate{}), `{"`+key+`":true}`)
		if err != nil {
			assert.NotContains(t, err.Error(), "the run's mode comes from the start request", key)
		}
	}
}

// --- the native stand-down lease and hold ---

func TestT6G5NativeLease(t *testing.T) {
	const key tasks.Key = "books.library-scan"
	const kind tasks.Kind = "books.maintenance.test"

	t.Run("takes the configured key and releases it", func(t *testing.T) {
		holds := &t6g5Holds{}
		rc, _ := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, holds)
		lease, err := t6g5NativeLease(rc, t6g5Deps{ScanKey: key})(context.Background(), "apply")
		require.NoError(t, err)
		keys, released := holds.snapshot()
		assert.Equal(t, []tasks.Key{key}, keys)
		assert.Zero(t, released)
		lease.Release()
		_, released = holds.snapshot()
		assert.Equal(t, 1, released)
	})

	t.Run("Lost follows the run context", func(t *testing.T) {
		holds := &t6g5Holds{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rc, _ := newT6G5HostCtx(ctx, kind, tasks.ModeLive, &t6g5Gate{}, holds)
		lease, err := t6g5NativeLease(rc, t6g5Deps{ScanKey: key})(ctx, "apply")
		require.NoError(t, err)
		assert.False(t, lease.Lost(), "a live context is not a lost lease")
		cancel()
		assert.True(t, lease.Lost(), "a canceled run (fenced) must abort its remaining writes")
	})

	t.Run("refuses without a key and takes nothing", func(t *testing.T) {
		holds := &t6g5Holds{}
		rc, _ := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, holds)
		lease, err := t6g5NativeLease(rc, t6g5Deps{})(context.Background(), "merge dupes")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no scan stand-down key configured, refusing to merge dupes without it")
		keys, _ := holds.snapshot()
		assert.Empty(t, keys)
		assert.NotPanics(t, func() { lease.Release(); _ = lease.Lost() })
	})

	t.Run("a refused stand-down is an error naming the reason", func(t *testing.T) {
		boom := errors.New("scan would not stand down")
		holds := &t6g5Holds{err: boom}
		rc, _ := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, holds)
		_, err := t6g5NativeLease(rc, t6g5Deps{ScanKey: key})(context.Background(), "apply")
		require.ErrorIs(t, err, boom)
		assert.Contains(t, err.Error(), "apply")
	})
}

func TestT6G5NativeHold(t *testing.T) {
	const key tasks.Key = "books.library-scan"
	const kind tasks.Kind = "books.maintenance.test"

	t.Run("takes the key, hands the run's own progress and releases on Finish", func(t *testing.T) {
		holds := &t6g5Holds{}
		rc, _ := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, holds)
		var got []string
		env := t6g5Base{Progress: func(done, total int, msg string) { got = append(got, msg) }}
		held, err := t6g5NativeHold(rc, t6g5Deps{ScanKey: key}, env)(context.Background(), "refresh")
		require.NoError(t, err)
		keys, released := holds.snapshot()
		assert.Equal(t, []tasks.Key{key}, keys)
		assert.Zero(t, released)
		require.NoError(t, held.Progress.UpdateProgress(1, 2, "halfway"))
		assert.Equal(t, []string{"halfway"}, got)
		runErr := errors.New("run failed")
		assert.Same(t, runErr, held.Finish(runErr), "Finish passes the run's error through")
		_, released = holds.snapshot()
		assert.Equal(t, 1, released)
	})

	t.Run("refuses without a key and takes nothing", func(t *testing.T) {
		holds := &t6g5Holds{}
		rc, _ := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, holds)
		_, err := t6g5NativeHold(rc, t6g5Deps{}, t6g5Base{})(context.Background(), "refresh")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no scan stand-down key configured, refusing to refresh without it")
		keys, _ := holds.snapshot()
		assert.Empty(t, keys)
	})
}

// --- worker pools: the declared parallelism is what runs ---

// t6g5Peak runs n items through run and returns the most that ran at once. With
// gate > 0 the first gate items wait for each other, so a pool that really runs
// gate workers reaches that peak deterministically (and one that does not times
// out instead of passing by luck).
func t6g5Peak(t *testing.T, n, gate int, run func(fn func(ctx context.Context, i int) error) error) int {
	t.Helper()
	var cur, peak atomic.Int32
	var arrived sync.WaitGroup
	arrived.Add(gate)
	var once = make([]sync.Once, n)
	fn := func(ctx context.Context, i int) error {
		c := cur.Add(1)
		defer cur.Add(-1)
		for {
			p := peak.Load()
			if c <= p || peak.CompareAndSwap(p, c) {
				break
			}
		}
		if i < gate {
			once[i].Do(arrived.Done)
			done := make(chan struct{})
			go func() { arrived.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				return errors.New("workers never ran together")
			}
		}
		time.Sleep(5 * time.Millisecond)
		return nil
	}
	require.NoError(t, run(fn))
	return int(peak.Load())
}

func TestT6G5NativeItems_Parallelism(t *testing.T) {
	const kind tasks.Kind = "books.maintenance.test"

	t.Run("Fixed(3) runs exactly three at once", func(t *testing.T) {
		rc, fr := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, nil)
		peak := t6g5Peak(t, 6, 3, func(fn func(context.Context, int) error) error {
			return t6g5NativeItems(rc)(context.Background(), 6, t6g5ItemsOpts{Pool: t6g5FixedPool(3, "test")}, fn)
		})
		assert.Equal(t, 3, peak)
		acquires, _ := fr.CPUStats()
		assert.Zero(t, acquires, "fixed workers take no CPU slot")
	})

	t.Run("a pool of one is sequential", func(t *testing.T) {
		rc, _ := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, nil)
		peak := t6g5Peak(t, 5, 0, func(fn func(context.Context, int) error) error {
			return t6g5NativeItems(rc)(context.Background(), 5, t6g5ItemsOpts{Pool: t6g5FixedPool(1, "test")}, fn)
		})
		assert.Equal(t, 1, peak)
	})

	t.Run("the CPU pool takes one CPU slot per item", func(t *testing.T) {
		rc, fr := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, nil)
		fr.SetCPUSlots(2)
		peak := t6g5Peak(t, 8, 0, func(fn func(context.Context, int) error) error {
			return t6g5NativeItems(rc)(context.Background(), 8, t6g5ItemsOpts{Pool: t6g5CPUPool()}, fn)
		})
		acquires, maxInUse := fr.CPUStats()
		assert.Equal(t, 8, acquires)
		assert.LessOrEqual(t, maxInUse, 2)
		assert.LessOrEqual(t, peak, 2)
	})

	t.Run("an item's own error comes back, not the wrapper", func(t *testing.T) {
		rc, _ := newT6G5HostCtx(context.Background(), kind, tasks.ModeLive, &t6g5Gate{}, nil)
		boom := errors.New("item failed")
		err := t6g5NativeItems(rc)(context.Background(), 3, t6g5ItemsOpts{Pool: t6g5FixedPool(1, "test")},
			func(context.Context, int) error { return boom })
		require.ErrorIs(t, err, boom)
	})
}

func TestT6G5HeadItems_Parallelism(t *testing.T) {
	run := func(workers, n, gate int) int {
		return t6g5Peak(t, n, gate, func(fn func(context.Context, int) error) error {
			return t6g5HeadItems(&fakeReporter{})(context.Background(), n,
				t6g5ItemsOpts{Pool: t6g5Pool{Workers: workers, Par: tasks.Fixed(max(workers, 1), "test")}}, fn)
		})
	}
	assert.Equal(t, 1, run(0, 5, 0), "Workers 0 is sequential, as HEAD's RunItems clamps it")
	assert.Equal(t, 1, run(1, 5, 0), "Workers 1 is sequential")
	assert.Equal(t, 3, run(3, 6, 3), "Workers 3 runs three at once")
}
