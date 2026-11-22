// file: internal/plugins/acoustid/kinds_literal_test.go
// version: 1.0.0
// guid: 4b3f0c1e-9d52-4a6e-8f17-2c5e7a90d3b4
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// The parity tests in kinds_core_test.go run HEAD and the native shell over the
// SAME core, so they agree on whatever the core does. Everything here asserts a
// literal the core must produce, so a change to the core itself cannot pass by
// changing both sides.

// ---- scanCore ----

type scriptedScanEngine struct{ calls [][2]int }

func (e scriptedScanEngine) AcoustIDScan(_ context.Context, progress func(done, total int)) error {
	for _, c := range e.calls {
		progress(c[0], c[1])
	}
	return nil
}

type recordingCandidates struct {
	got   *database.CandidateFilter
	total int
}

func (r recordingCandidates) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	*r.got = f
	return nil, r.total, nil
}

func TestAcoustidCore_ScanFramesAndFilterAreLiteral(t *testing.T) {
	rep := &coreReporter{}
	var filter database.CandidateFilter
	res, err := scanCore(context.Background(), scanEnv{
		kindEnv: headEnv(rep, true),
		// A total of 0 is "unknown yet" and must not become a frame.
		Engine:     scriptedScanEngine{calls: [][2]int{{5, 0}, {2, 10}}},
		Candidates: recordingCandidates{got: &filter, total: 3},
	})
	require.NoError(t, err)
	require.Equal(t, scanResult{PendingCandidates: 3}, res)
	require.Equal(t, database.CandidateFilter{EntityType: "book", Status: "pending", Layer: "acoustid", Limit: 1}, filter)

	bars, notes := headBarAndNotes(rep.frames)
	require.Equal(t, []string{"Starting AcoustID fingerprint scan...", "Scanning books: 2 / 10",
		"AcoustID scan complete — 3 pending candidate(s)"}, notes)
	require.Equal(t, []barFrame{{Done: 0, Total: 1, Known: true}, {Done: 2, Total: 10, Known: true}, {Done: 1, Total: 1, Known: true}},
		bars, "the last frame is a full bar")
}

// ---- window core ----

func TestAcoustidCore_WindowCoreRefusesALiveRunWithoutAWriter(t *testing.T) {
	e := newWBEnv(t)
	env := e.plugin.headWindowEnv(&coreReporter{}, true)
	env.Writer = nil
	_, err := windowBackfillCore(context.Background(), WindowBackfillParams{Live: true}, env)
	require.EqualError(t, err, "window-backfill: no writer for a live run")
}

func TestAcoustidCore_WindowKindNeedsAStore(t *testing.T) {
	k := newKit()
	d := k.deps(nil)
	d.Store = nil
	def := byKind(t, (&Plugin{}).Kinds(d, nil), windowBackfillKind)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, ``)
	require.EqualError(t, err, string(windowBackfillKind)+": KindDeps.Store is not set")
}

// TestAcoustidCore_WindowNativeParamsReachTheCore: every setting a caller can
// pass arrives in the core's params, and the mode is the run's.
func TestAcoustidCore_WindowNativeParamsReachTheCore(t *testing.T) {
	np := WindowBackfillNativeParams{
		Concurrency: 7, RemoteOnly: true, AllowServerDecode: true,
		NoWorkerGraceSec: 11, PendingGraceSec: 13, RebootstrapReference: true,
	}
	require.Equal(t, WindowBackfillParams{
		Live: true, Concurrency: 7, RemoteOnly: true, AllowServerDecode: true,
		NoWorkerGraceSec: 11, PendingGraceSec: 13, RebootstrapReference: true,
	}, np.coreParams(true))
	require.False(t, np.coreParams(false).Live)
	require.Equal(t, WindowBackfillParams{}, WindowBackfillNativeParams{}.coreParams(false))
}

// TestAcoustidCore_WindowNativeRunHonoursItsParams drives the native kind with
// hooks that record what the core asked of them, so the wiring from the decoded
// params through the shell is covered end to end, not only the mapping.
func TestAcoustidCore_WindowNativeRunHonoursItsParams(t *testing.T) {
	run := func(t *testing.T, params string, refTools bool) (remoteOnly, live *bool, err error) {
		t.Helper()
		k := newKit()
		d := k.deps(guardedStore(t))
		d.Window = WindowKindDeps{
			ReferenceToolsSet: func() bool { return refTools },
			Tools: func(_ context.Context, ro, lv bool) (fingerprint.WindowTools, error) {
				remoteOnly, live = &ro, &lv
				return fingerprint.WindowTools{}, errors.New("tools stub")
			},
		}
		def := byKind(t, (&Plugin{}).Kinds(d, nil), windowBackfillKind)
		_, err = runKind(t, def, tasks.ModeLive, &sentinelWriter{}, params)
		return remoteOnly, live, err
	}

	refusal := "fingerprint_window_reference_tools is set"
	_, _, err := run(t, ``, true)
	require.ErrorContains(t, err, refusal, "a plain live run with a reference pair is refused")

	ro, lv, err := run(t, `{"allow_server_decode":true}`, true)
	require.EqualError(t, err, "window-backfill: tools stub", "allow_server_decode reaches the core")
	require.NotNil(t, ro)
	require.False(t, *ro)
	require.True(t, *lv)

	ro, lv, err = run(t, `{"remote_only":true}`, true)
	require.EqualError(t, err, "window-backfill: tools stub", "remote_only reaches the core")
	require.True(t, *ro)
	require.True(t, *lv)
}

func TestAcoustidCore_WindowNativeRunPassesConcurrencyToTheHook(t *testing.T) {
	e := newWBEnv(t)
	e.addFileWith("", "arkham/Batman Year One.m4b", fakeAudio(1800, 4096), 1800)
	k := newKit()
	var asked []int
	d := k.deps(e.store)
	d.Window.Workers = func(n int) int {
		asked = append(asked, n)
		return 1
	}
	def := byKind(t, e.plugin.Kinds(d, nil), windowBackfillKind)
	h := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
	_, err := runKindResult(t, def, h, `{"concurrency":7}`)
	require.NoError(t, err)
	require.Equal(t, []int{7}, asked)
}

func TestAcoustidCore_WindowHooksDefaultToHEADsWorkerSizing(t *testing.T) {
	h := (&Plugin{}).windowHooks(WindowKindDeps{})
	require.Equal(t, windowWorkers(5), h.Workers(5))
	require.Equal(t, 5, h.Workers(5))
}

// ---- window write paths ----

func TestAcoustidCore_CountWriteErrSeparatesGoneFromFailed(t *testing.T) {
	var tl windowTally
	it := windowItem{FileID: "f-1"}
	countWriteErrVia(it, fmt.Errorf("replace: %w", database.ErrFingerprintWindowRowGone), &tl)
	require.EqualValues(t, 1, tl.rowGone.Load())
	require.EqualValues(t, 0, tl.writeErrors.Load(), "a gone row is not a write failure")
	countWriteErrVia(it, errors.New("disk full"), &tl)
	require.EqualValues(t, 1, tl.rowGone.Load())
	require.EqualValues(t, 1, tl.writeErrors.Load())
}

type failureCapture struct {
	kindWriter
	got  *database.FingerprintWindowFailure
	fail error
}

func (c *failureCapture) RecordFingerprintWindowFailure(f *database.FingerprintWindowFailure) error {
	c.got = f
	return c.fail
}

func TestAcoustidCore_StoreFailureTruncatesTheDetailAndCountsTheOutcome(t *testing.T) {
	it := windowItem{FileID: "f-1"}
	call := func(w kindWriter, msg string, tl *windowTally) {
		storeFailureVia(w, it, 0, fingerprint.DurationUsed{}, errors.New(msg), fingerprint.WindowTools{}, "gotham-host", tl)
	}

	var tl windowTally
	fc := &failureCapture{}
	call(fc, strings.Repeat("x", 3000), &tl)
	require.NotNil(t, fc.got)
	require.Len(t, fc.got.Detail, 1024)
	require.EqualValues(t, 1, tl.failed.Load())

	fc = &failureCapture{}
	call(fc, strings.Repeat("y", 1024), &tl)
	require.Len(t, fc.got.Detail, 1024, "exactly the limit is kept whole")
	call(fc, "short", &tl)
	require.Equal(t, "short", fc.got.Detail)
	require.EqualValues(t, 3, tl.failed.Load())

	// A store that refuses the tombstone is a write error, not a failure.
	tl = windowTally{}
	call(&failureCapture{fail: errors.New("disk full")}, "decode failed", &tl)
	require.EqualValues(t, 0, tl.failed.Load())
	require.EqualValues(t, 1, tl.writeErrors.Load())
	call(&failureCapture{fail: database.ErrFingerprintWindowRowGone}, "decode failed", &tl)
	require.EqualValues(t, 1, tl.rowGone.Load())
	require.EqualValues(t, 0, tl.failed.Load())
}

// ---- window planner ----

// failingWindowReads is a store whose stored-window read fails.
type failingWindowReads struct{ kindStore }

func (failingWindowReads) GetFingerprintWindows(database.FingerprintWindowRef) ([]database.FingerprintWindow, error) {
	return nil, errors.New("read failed")
}

// sequentialItems runs fn for every index in order and stops at the first error,
// without checking ctx itself, so a planner that does not check it shows.
func sequentialItems(ctx context.Context, _ tasks.Parallelism, n int, _ kindItemsOptions, fn func(context.Context, int) error) error {
	for i := range n {
		if err := fn(ctx, i); err != nil {
			return err
		}
	}
	return nil
}

func TestAcoustidCore_PlannerRefusesANilStore(t *testing.T) {
	_, err := planWindowBackfillVia(context.Background(), nil, sequentialItems, windowHooks{}, nil)
	require.EqualError(t, err, "window-backfill: database store not available")
}

func TestAcoustidCore_PlannerLeavesAFileWhoseWindowsCannotBeReadOutOfTheRun(t *testing.T) {
	e := newWBEnv(t)
	e.addFileWith("", "arkham/Batman Year One.m4b", fakeAudio(1800, 4096), 1800)
	host := e.plugin.windowHooks(WindowKindDeps{})

	plan, err := planWindowBackfillVia(context.Background(), failingWindowReads{e.store}, sequentialItems, host, nil)
	require.NoError(t, err)
	require.Equal(t, 1, plan.excluded["plan_error"], "a read failure is excluded, never planned or called current")
	for tier, items := range plan.tiers {
		require.Empty(t, items, "tier %d: nothing is planned for a file whose state is unknown", tier)
	}
}

func TestAcoustidCore_PlannerStopsOnACancelledContext(t *testing.T) {
	e := newWBEnv(t)
	e.addFileWith("", "arkham/Batman Year One.m4b", fakeAudio(1800, 4096), 1800)
	host := e.plugin.windowHooks(WindowKindDeps{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := planWindowBackfillVia(ctx, e.store, sequentialItems, host, nil)
	require.ErrorIs(t, err, context.Canceled)
}

// ---- backfill restore ----

// TestAcoustidCore_BackfillStartsFreshWhenRestoreCannotAnswer: a checkpoint that
// fails to load, or is absent, means a fresh start even if the loader wrote a
// cursor into the state before it said so.
func TestAcoustidCore_BackfillStartsFreshWhenRestoreCannotAnswer(t *testing.T) {
	cases := map[string]func(any) (bool, error){
		"garbled": func(v any) (bool, error) {
			*(v.(*BackfillParams)) = BackfillParams{AfterBookID: "book-00001"}
			return true, errors.New("garbled checkpoint")
		},
		"absent": func(v any) (bool, error) {
			*(v.(*BackfillParams)) = BackfillParams{AfterBookID: "book-00001"}
			return false, nil
		},
	}
	for name, restore := range cases {
		t.Run(name, func(t *testing.T) {
			fx := backfillFixture(t, 4)
			fx.store.UpdateBookFileFunc = func(string, *database.BookFile) error { return nil }
			env := headBackfillEnvFor(&Plugin{store: fx.store}, &coreReporter{})
			env.Restore = restore
			_, err := backfillCore(context.Background(), nil, env)
			require.NoError(t, err)
			fx.mu.Lock()
			defer fx.mu.Unlock()
			require.Len(t, fx.visits, 4, "every book was visited, none skipped by the discarded cursor")
		})
	}
}
