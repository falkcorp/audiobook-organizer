// file: internal/compactprogress/step_test.go
// version: 1.0.0
// guid: 3f8a1c6e-2d7b-4c90-a5e4-9b1f0d7c2a83
// last-edited: 2026-10-02

package compactprogress

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gib = 1 << 30
const mib = 1 << 20

type fakeReporter struct {
	mu    sync.Mutex
	logs  []string
	items []string
}

func (r *fakeReporter) Log(_ slog.Level, msg string, _ ...slog.Attr) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, msg)
	return nil
}

func (r *fakeReporter) SetCurrentItem(label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, label)
}

func (r *fakeReporter) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logs...)
}

// fakeSource is an injected metrics source: each call advances the in-flight
// compaction by step bytes and counts how often it was sampled.
type fakeSource struct {
	mu    sync.Mutex
	s     Stats
	step  int64
	calls atomic.Int64
}

func (f *fakeSource) stats() Stats {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.s.InProgressBytes += f.step
	return f.s
}

func TestFormatTick(t *testing.T) {
	base := Stats{CompletedBytes: 100 * gib, Count: 40}
	cur := Stats{
		CompletedBytes:  100*gib + 17*gib,
		InProgressBytes: 1 * gib,
		NumInProgress:   3,
		Count:           52,
		EstimatedDebt:   6 * gib,
		L0Files:         12,
		DiskSpaceUsage:  41 * gib,
		TombstoneCount:  1234567,
	}
	got := FormatTick("Compacting main database (1/3)", 4*time.Minute+10*time.Second, base, cur)
	want := "Compacting main database (1/3): 4m10s elapsed, 18.0 GiB compacted (73.7 MiB/s), " +
		"3 compactions in progress (1.0 GiB), 12 finished, est. debt 6.0 GiB, L0 12 files, " +
		"41.0 GiB on disk, ~1,234,567 tombstones"
	assert.Equal(t, want, got)

	idle := FormatTick("X", 500*time.Millisecond, Stats{}, Stats{L0Files: 1})
	assert.Equal(t, "X: 500ms elapsed, 0 B compacted (rate n/a), no compaction running, "+
		"est. debt 0 B, L0 1 file, 0 B on disk, ~0 tombstones", idle)
}

func TestFormatDone(t *testing.T) {
	r := Result{
		Elapsed:  28*time.Minute + 31*time.Second,
		HasStats: true,
		Before:   Stats{LiveTableSize: 31 * gib, DiskSpaceUsage: 32 * gib, TombstoneCount: 5000, CompletedBytes: 0},
		After:    Stats{LiveTableSize: 24 * gib, DiskSpaceUsage: 33 * gib, ObsoleteSize: 9 * gib, TombstoneCount: 12, CompletedBytes: 40 * gib},
	}
	got := FormatDone("Main database", r)
	assert.Equal(t, "Main database: done in 28m31s; live tables 31.0 GiB -> 24.0 GiB (-7.0 GiB), "+
		"on disk 32.0 GiB -> 33.0 GiB (incl. 9.0 GiB obsolete, deleted asynchronously), "+
		"~5,000 -> ~12 tombstones, 40.0 GiB rewritten (23.9 MiB/s)", got)

	failed := FormatDone("OpenLibrary cache", Result{Elapsed: 2 * time.Second, Err: errors.New("boom")})
	assert.Equal(t, "OpenLibrary cache: failed after 2s: boom", failed)
}

func TestHumanBytesAndGrouping(t *testing.T) {
	assert.Equal(t, "512 B", HumanBytes(512))
	assert.Equal(t, "1.5 KiB", HumanBytes(1536))
	assert.Equal(t, "73.0 MiB", HumanBytes(73*mib))
	assert.Equal(t, "999", groupDigits(999))
	assert.Equal(t, "1,000", groupDigits(1000))
	assert.Equal(t, "12,345,678", groupDigits(12345678))
}

func TestMoved(t *testing.T) {
	base := Stats{CompletedBytes: 10, InProgressBytes: 5, Count: 1, EstimatedDebt: 100}
	assert.False(t, Moved(base, base), "flat counters are a stall, not progress")
	assert.True(t, Moved(base, Stats{CompletedBytes: 10, InProgressBytes: 6, Count: 1, EstimatedDebt: 100}),
		"bytes written mid-compaction are progress")
	assert.True(t, Moved(base, Stats{CompletedBytes: 10, InProgressBytes: 5, Count: 2, EstimatedDebt: 100}))
	assert.True(t, Moved(base, Stats{CompletedBytes: 10, InProgressBytes: 5, Count: 1, EstimatedDebt: 99}))
	// A compaction finishing moves its bytes from in-progress to completed;
	// the total must not read as going backwards.
	assert.True(t, Moved(base, Stats{CompletedBytes: 16, InProgressBytes: 0, Count: 2, EstimatedDebt: 100}))
}

const tick = 5 * time.Millisecond

// A fake long-running compaction: the sampler must report while it runs and
// stop sampling the moment it returns.
func TestRunStep_ReportsWhileRunningAndStopsCleanly(t *testing.T) {
	rep := &fakeReporter{}
	src := &fakeSource{s: Stats{NumInProgress: 1}, step: 4 * mib}
	release := make(chan struct{})
	var frames atomic.Int64

	done := make(chan Result, 1)
	go func() {
		done <- RunStep(context.Background(), rep, Step{
			Label:    "Compacting main database (1/3)",
			Run:      func(context.Context) error { <-release; return nil },
			Stats:    src.stats,
			Frame:    func(string) { frames.Add(1) },
			Interval: tick,
		})
	}()

	require.Eventually(t, func() bool { return len(rep.snapshot()) >= 3 && frames.Load() >= 3 },
		2*time.Second, tick, "a running compaction whose bytes grow must log and frame")
	close(release)
	res := <-done
	require.NoError(t, res.Err)

	callsAtReturn := src.calls.Load()
	time.Sleep(20 * tick)
	assert.Equal(t, callsAtReturn, src.calls.Load(), "sampler must not touch the source after RunStep returns")

	logs := rep.snapshot()
	assert.Contains(t, logs[0], "Compacting main database (1/3): ")
	assert.Contains(t, logs[0], "compacted (")
	assert.Contains(t, logs[0], "1 compaction in progress")
	last := logs[len(logs)-1]
	assert.Contains(t, last, ": done in ")
	assert.Contains(t, last, "live tables")
	t.Logf("sample tick: %s", logs[0])
	t.Logf("sample done: %s", last)
}

func TestRunStep_FlatCountersLogButSendNoFrames(t *testing.T) {
	rep := &fakeReporter{}
	src := &fakeSource{s: Stats{NumInProgress: 1, InProgressBytes: 10, Count: 7, EstimatedDebt: 1000}}
	release := make(chan struct{})
	var frames atomic.Int64

	done := make(chan Result, 1)
	go func() {
		done <- RunStep(context.Background(), rep, Step{
			Label: "X", Run: func(context.Context) error { <-release; return nil },
			Stats: src.stats, Frame: func(string) { frames.Add(1) }, Interval: tick,
		})
	}()
	require.Eventually(t, func() bool { return len(rep.snapshot()) >= 3 }, 2*time.Second, tick)
	close(release)
	<-done
	assert.Zero(t, frames.Load(), "a stalled compaction must not stamp liveness")
}

func TestRunStep_StopsSamplingOnCancelAndStillWaitsForRun(t *testing.T) {
	rep := &fakeReporter{}
	src := &fakeSource{step: 1}
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})

	done := make(chan Result, 1)
	go func() {
		done <- RunStep(ctx, rep, Step{
			Label: "X", Run: func(context.Context) error { <-release; return context.Canceled },
			Stats: src.stats, Interval: tick,
		})
	}()
	require.Eventually(t, func() bool { return src.calls.Load() >= 3 }, 2*time.Second, tick)
	cancel()
	time.Sleep(4 * tick) // let the sampler observe the cancel
	afterCancel := src.calls.Load()
	time.Sleep(20 * tick)
	assert.Equal(t, afterCancel, src.calls.Load(), "sampling must stop once ctx is cancelled")

	select {
	case <-done:
		t.Fatal("RunStep returned before Run did")
	default:
	}
	close(release)
	res := <-done
	assert.ErrorIs(t, res.Err, context.Canceled)
	logs := rep.snapshot()
	assert.Contains(t, logs[len(logs)-1], "X: failed after ")
}

func TestRunStep_NoStatsReportsElapsedOnly(t *testing.T) {
	rep := &fakeReporter{}
	release := make(chan struct{})
	done := make(chan Result, 1)
	go func() {
		done <- RunStep(context.Background(), rep, Step{
			Label: "Y", Run: func(context.Context) error { <-release; return nil }, Interval: tick,
		})
	}()
	require.Eventually(t, func() bool { return len(rep.snapshot()) >= 1 }, 2*time.Second, tick)
	close(release)
	res := <-done
	assert.False(t, res.HasStats)
	logs := rep.snapshot()
	assert.Contains(t, logs[0], "Y: ")
	assert.Contains(t, logs[0], "elapsed")
	assert.True(t, strings.HasPrefix(logs[len(logs)-1], "Y: done in "))
	assert.NotContains(t, logs[len(logs)-1], "live tables")
}

// Collect reads real field names from the pinned Pebble version; this pins
// that the mapping compiles and produces sane numbers on a real DB.
func TestCollect_RealPebble(t *testing.T) {
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for i := 0; i < 2000; i++ {
		require.NoError(t, db.Set([]byte{byte(i >> 8), byte(i)}, make([]byte, 512), pebble.NoSync))
	}
	// Two overlapping L0 tables, so the compaction must merge (rewrite)
	// rather than move a lone file down by metadata alone.
	require.NoError(t, db.Flush())
	for i := 0; i < 1000; i++ {
		require.NoError(t, db.Delete([]byte{byte(i >> 8), byte(i)}, pebble.NoSync))
	}
	require.NoError(t, db.Flush())
	before := Collect(db)
	require.NoError(t, db.Compact(context.Background(), nil, []byte{0xff}, false))
	after := Collect(db)
	assert.Greater(t, after.LiveTableSize, int64(0))
	assert.Greater(t, after.WrittenBytes(), before.WrittenBytes(), "a compaction must show written bytes")
	assert.Greater(t, after.BottomLevelSize, int64(0), "a full compaction lands data in the bottom level")
	assert.Equal(t, Stats{}, Collect(nil))
}
