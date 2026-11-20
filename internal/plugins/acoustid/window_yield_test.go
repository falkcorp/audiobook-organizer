// file: internal/plugins/acoustid/window_yield_test.go
// version: 0.1.0
// guid: 0b7d6e2a-84c1-4c7e-b1a3-5e3c2f9d7a46
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
)

var errTestYield = errors.New("test: continue in place")

// runYielding is a live window run whose env can yield. dl is the attempt deadline.
func (e *wbEnv) runYielding(ctx context.Context, rep *wbReporter, p WindowBackfillParams, dl func() time.Time) (*windowRunResult, error) {
	env := e.plugin.headWindowEnv(rep, p.Live)
	env.Deadline, env.YieldMargin, env.ContinueInPlace = dl, 10*time.Minute, errTestYield
	return windowBackfillCore(ctx, p, env)
}

func fastYieldPoll(t *testing.T) {
	t.Helper()
	prev := windowYieldPoll
	windowYieldPoll = 5 * time.Millisecond
	t.Cleanup(func() { windowYieldPoll = prev })
}

func ckptEvery(t *testing.T, n int) {
	t.Helper()
	prev := windowCheckpointEvery
	windowCheckpointEvery = n
	t.Cleanup(func() { windowCheckpointEvery = prev })
}

// A run that reaches the yield margin stops admitting files, saves the cursor
// below the first file it did not do and returns the continue sentinel; the
// resumed attempt does the rest without cutting a finished file again, and the
// run's start time rides every checkpoint.
func TestWindowBackfill_YieldsBeforeTheDeadlineAndResumes(t *testing.T) {
	e := newWBEnv(t)
	fastYieldPoll(t)
	ckptEvery(t, 1)
	const n = 14
	var ids []string
	pathOf := map[string]string{}
	for i := range n {
		rel := fmt.Sprintf("slow-%02d/f.m4b", i)
		_, id := e.addFile("", rel, true, 300)
		ids = append(ids, id)
		pathOf[id] = rel
	}
	far := time.Now().Add(time.Hour)
	// Near the deadline once 4 files have been cut.
	dl := func() time.Time {
		seen := map[string]bool{}
		for _, p := range e.invoked() {
			seen[p] = true
		}
		if len(seen) >= 4 {
			return time.Now().Add(time.Minute)
		}
		return far
	}
	rep := &wbReporter{}
	res, err := e.runYielding(context.Background(), rep, WindowBackfillParams{Live: true, Concurrency: 1}, dl)
	require.ErrorIs(t, err, errTestYield)
	require.NotNil(t, res)

	cp := rep.last()
	require.NotNil(t, cp)
	require.NotNil(t, cp.Resume)
	require.False(t, cp.Resume.StartedAt.IsZero(), "the cursor must carry the run's start")
	rep.mu.Lock()
	for _, c := range rep.checkpoints {
		require.NotNil(t, c.Resume)
		require.True(t, c.Resume.StartedAt.Equal(cp.Resume.StartedAt), "a checkpoint dropped or changed StartedAt: %+v", c.Resume)
	}
	rep.mu.Unlock()

	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	finished := map[string]bool{}
	for _, id := range ids {
		if len(e.windows(id)) > 0 {
			finished[id] = true
		}
	}
	require.GreaterOrEqual(t, len(finished), 4)
	require.Less(t, len(finished), n, "the run did not stop admitting work")
	for _, id := range sorted {
		if id <= cp.Resume.AfterFileID {
			require.True(t, finished[id], "cursor %s is past unfinished file %s", cp.Resume.AfterFileID, id)
		} else {
			require.False(t, finished[id], "file %s is past the cursor but was cut", id)
		}
	}

	before := len(e.invoked())
	rep2 := &wbReporter{}
	res2, err := e.runYielding(context.Background(), rep2, *cp, func() time.Time { return far })
	require.NoError(t, err)
	byPath := map[string]string{}
	for id, rel := range pathOf {
		byPath[filepathJoin(e.lib, rel)] = id
	}
	for _, p := range e.invoked()[before:] {
		require.False(t, finished[byPath[p]], "the resumed attempt redid %s", p)
	}
	for _, id := range ids {
		require.NotEmpty(t, e.windows(id))
	}
	require.EqualValues(t, n-len(finished), res2.written)
	if last := rep2.last(); last != nil && last.Resume != nil {
		require.True(t, last.Resume.StartedAt.Equal(cp.Resume.StartedAt), "the resumed attempt changed the run's start")
	}
}

// With the deadline already inside the margin, the attempt still finishes a file
// before it yields: the executor only honours a yield from an attempt that made
// progress, so a yield with none would fail the run.
func TestWindowBackfill_NoYieldBeforeTheFirstFile(t *testing.T) {
	e := newWBEnv(t)
	for i := range 6 {
		e.addFile("", fmt.Sprintf("slow-%02d/f.m4b", i), true, 300)
	}
	res, err := e.runYielding(context.Background(), &wbReporter{}, WindowBackfillParams{Live: true, Concurrency: 1},
		func() time.Time { return time.Now().Add(time.Second) })
	require.ErrorIs(t, err, errTestYield)
	require.GreaterOrEqual(t, res.written, int64(1))
	require.Less(t, res.written, int64(6))
}

// The 72h total bound: a run whose first attempt began more than the budget ago
// cuts nothing, saves its cursor and ends with a budget error, not a yield.
func TestWindowBackfill_TotalBudgetEndsTheRun(t *testing.T) {
	e := newWBEnv(t)
	for i := range 5 {
		e.addFile("", fmt.Sprintf("slow-%02d/f.m4b", i), true, 300)
	}
	rep := &wbReporter{}
	p := WindowBackfillParams{Live: true, Concurrency: 1,
		Resume: &WindowBackfillCursor{StartedAt: time.Now().Add(-windowTotalBudget - time.Minute)}}
	_, err := e.runYielding(context.Background(), rep, p, func() time.Time { return time.Now().Add(time.Hour) })
	require.ErrorIs(t, err, errWindowBudget)
	require.NotErrorIs(t, err, errTestYield)
	require.Empty(t, e.invoked(), "a run past its budget cut a file")
	require.NotNil(t, rep.last(), "the cursor was not saved")

	// One minute inside the budget is not over it.
	p.Resume = &WindowBackfillCursor{StartedAt: time.Now().Add(-windowTotalBudget + time.Hour)}
	_, err = e.runYielding(context.Background(), &wbReporter{}, p, func() time.Time { return time.Now().Add(time.Hour) })
	require.NoError(t, err)
}

// Remote-only: files leased to a worker when the run decides to yield are not
// abandoned. The hub hands out no new lease, the run waits for the held leases
// to be answered (a result is still written), and only then saves the cursor, at
// the first file nobody finished; the resumed attempt leases only what is left.
func TestWindowBackfill_RemoteOnlyYieldDrainsLeasesAndResumes(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	fastYieldPoll(t)
	ckptEvery(t, 1)
	const n = 12
	var ids []string
	for i := range n {
		_, id := e.addFile("", fmt.Sprintf("lib/f%02d.m4b", i), true, 3600)
		ids = append(ids, id)
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	h := &hubEnv{wbEnv: e, hub: e.plugin.WorkerHub()}

	var holding atomic.Bool
	var heldJobs atomic.Int64
	var late atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wdone := make(chan struct{})
	go func() {
		defer close(wdone)
		helloUntilReady(ctx, h.hub, "mac1")
		lease := func(max int) *workerapi.LeaseResponse {
			for ctx.Err() == nil {
				resp, err := h.hub.Lease(ctx, withRunL(h.hub, refLeaseReq("mac1", max)))
				if err == nil && resp != nil {
					return resp
				}
				time.Sleep(2 * time.Millisecond)
			}
			return nil
		}
		post := func(resp *workerapi.LeaseResponse) {
			var rs []workerapi.JobResult
			for _, j := range resp.Jobs {
				rs = append(rs, refResult(h, j, 7))
			}
			_, err := h.hub.Results(ctx, withRunS(h.hub, workerapi.ResultsRequest{WorkerID: "mac1", Results: rs}))
			require.NoError(t, err)
		}
		first := lease(1)
		if first == nil {
			return
		}
		post(first) // one file finished: the attempt has made progress
		held := lease(3)
		if held == nil {
			return
		}
		heldJobs.Store(int64(len(held.Jobs)))
		holding.Store(true)
		// Wait for the run to quiesce the hub, then prove it hands out nothing.
		for ctx.Err() == nil && !hubQuiescing(h.hub) {
			time.Sleep(2 * time.Millisecond)
		}
		resp, err := h.hub.Lease(ctx, withRunL(h.hub, refLeaseReq("mac1", 3)))
		require.NoError(t, err)
		require.Nil(t, resp, "a quiesced hub handed out a lease")
		late.Store(true)
		post(held) // the held leases are answered after the yield began
	}()

	rep := &wbReporter{}
	far := time.Now().Add(time.Hour)
	res, err := e.runYielding(ctx, rep, WindowBackfillParams{Live: true, RemoteOnly: true, Concurrency: 2},
		func() time.Time {
			if holding.Load() {
				return time.Now().Add(time.Minute)
			}
			return far
		})
	<-wdone
	require.ErrorIs(t, err, errTestYield)
	require.True(t, late.Load())
	leased := 1 + int(heldJobs.Load())
	require.GreaterOrEqual(t, leased, 3, "the worker held too little to prove a drain")
	require.EqualValues(t, leased, res.written, "the held leases were not drained before the yield")
	for i, id := range sorted {
		if i < leased {
			require.NotEmpty(t, e.windows(id), "leased file %s lost", id)
		} else {
			require.Empty(t, e.windows(id), "file %s was cut after the yield began", id)
		}
	}
	cp := rep.last()
	require.NotNil(t, cp.Resume)
	// The cursor stops below the first file the run passed over; a leased file
	// past it was finished by the drain and the resumed plan sees it as current.
	require.LessOrEqual(t, cp.Resume.AfterFileID, sorted[leased-1])
	for _, id := range sorted {
		if id <= cp.Resume.AfterFileID {
			require.NotEmpty(t, e.windows(id), "cursor %s is past unfinished file %s", cp.Resume.AfterFileID, id)
		}
	}
	require.True(t, cp.RemoteOnly, "the cursor must keep remote_only")
	require.Zero(t, e.plugin.WorkerHub().inFlight(), "a lease was left held")

	// Resume: a worker answers whatever it is leased; none of the first four is offered.
	var mu sync.Mutex
	var offered []string
	wctx, stop := context.WithCancel(ctx)
	wdone2 := make(chan struct{})
	go func() {
		defer close(wdone2)
		fakeRefWorker(wctx, h.hub, func(j workerapi.Job) workerapi.JobResult {
			mu.Lock()
			offered = append(offered, h.fileID(j))
			mu.Unlock()
			return refResult(h, j, 7)
		})
	}()
	res2, err := e.runYielding(ctx, &wbReporter{}, *cp, func() time.Time { return far })
	stop()
	<-wdone2
	require.NoError(t, err)
	require.EqualValues(t, n-leased, res2.written)
	mu.Lock()
	defer mu.Unlock()
	for _, id := range offered {
		require.NotContains(t, sorted[:leased], id, "the resumed attempt re-leased a finished file")
	}
}

// The native kind wires the yield: near its deadline it returns the kernel's
// continue-in-place sentinel with the cursor saved, and the next attempt, given
// that checkpoint, skips what was done.
func TestWindowBackfillNative_YieldsAndResumes(t *testing.T) {
	e := newWBEnv(t)
	ckptEvery(t, 1)
	const n = 10
	for i := range n {
		e.addFile("", fmt.Sprintf("slow-%02d/f.m4b", i), true, 300)
	}
	k := newKit()
	def := byKind(t, e.plugin.Kinds(k.deps(e.store), nil), windowBackfillKind)

	// A deadline inside windowYieldMargin: near from the start, so the attempt
	// yields after its first finished file.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(windowYieldMargin/2))
	defer cancel()
	h := newKindHost(ctx, def, tasks.ModeLive, &sentinelWriter{})
	out, err := runKindResult(t, def, h, `{"concurrency":1}`)
	require.ErrorIs(t, err, tasks.ErrContinueInPlace)
	_ = out // the harness drops a result that comes with an error
	raw, ok := h.CheckpointJSON("")
	require.True(t, ok, "no cursor saved")
	var cp WindowBackfillParams
	require.NoError(t, json.Unmarshal(raw, &cp))
	require.NotNil(t, cp.Resume)
	require.False(t, cp.Resume.StartedAt.IsZero())
	require.NotEmpty(t, cp.Resume.AfterFileID)

	h2 := newKindHost(context.Background(), def, tasks.ModeLive, &sentinelWriter{})
	require.NoError(t, h2.SetCheckpoint("", json.RawMessage(raw)))
	out2, err := runKindResult(t, def, h2, `{"concurrency":1}`)
	require.NoError(t, err)
	var r2 windowBackfillResult
	decodeResult(t, out2, &r2)
	require.Positive(t, r2.Skipped, "the resumed attempt did not skip the finished prefix")
	require.Equal(t, n, r2.Skipped+int(r2.Written)+int(r2.Failed), "files unaccounted for")

}

func filepathJoin(a, b string) string { return a + "/" + b }

func hubQuiescing(h *WorkerHub) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.run != nil && h.run.quiescing
}

// decodeResult decodes a native run's result (the kernel hands it as raw JSON).
func decodeResult(t *testing.T, out any, into any) {
	t.Helper()
	b, err := json.Marshal(out)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, into))
}
