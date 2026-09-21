// file: internal/plugins/acoustid/window_backfill_remote_only_test.go
// version: 1.4.0
// guid: 609372fb-2803-46d8-a413-c4263e5f71be
// last-edited: 2026-09-20

package acoustid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
)

// refPair is the configured reference pair in these tests: distinct from the
// fake server tools (e.tools.Versions), so any use of the server's own pair
// shows up.
var refPair = fingerprint.ToolVersionInfo{Fpcalc: "1.6.1-ref", FFmpeg: "9.0.2-ref"}

// withRemoteOnly turns on the worker API and configures refPair.
func withRemoteOnly(t *testing.T) {
	t.Helper()
	prevOn, prevRef := config.AppConfig.FingerprintRemoteWorkersEnabled, config.AppConfig.FingerprintWindowReferenceTools
	prevAllow := config.AppConfig.FingerprintWorkerToolVersions
	prevClass := fingerprint.EquivalentToolPairs()
	t.Cleanup(func() {
		config.AppConfig.FingerprintRemoteWorkersEnabled = prevOn
		config.AppConfig.FingerprintWindowReferenceTools = prevRef
		config.AppConfig.FingerprintWorkerToolVersions = prevAllow
		fingerprint.SetToolEquivalence(fingerprint.ToolVersionInfo{}, prevClass)
	})
	config.AppConfig.FingerprintRemoteWorkersEnabled = true
	config.AppConfig.FingerprintWindowReferenceTools = refPair.Fpcalc + "/" + refPair.FFmpeg
}

// noLocalTools makes any resolution of local tools fail the run and counts
// it: a remote-only run must never ask for fpcalc or ffmpeg.
func noLocalTools(e *wbEnv) *atomic.Int64 {
	var calls atomic.Int64
	e.plugin.windowToolsFn = func(context.Context) (fingerprint.WindowTools, error) {
		calls.Add(1)
		return fingerprint.WindowTools{}, errors.New("remote-only run resolved local tools")
	}
	return &calls
}

// addOutside adds a present file outside EVERY known root (its own TempDir, not
// under RootDir nor RootDir's parent), so it is genuinely unreachable by a
// worker rather than merely outside libroot.
func (e *wbEnv) addOutside(name string) string {
	e.t.Helper()
	// NOT t.TempDir(): that returns a SIBLING of e.lib under the same per-test
	// parent, and the parent is exactly the "books" root, so such a file is
	// legitimately reachable now and this helper would no longer mean what its
	// name says. os.MkdirTemp("") lands outside the test's own tree.
	dir, err := os.MkdirTemp("", "wb-outside-")
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, name)
	require.NoError(e.t, os.WriteFile(path, []byte("audio:"+name), 0o644))
	b, err := e.store.CreateBook(&database.Book{Title: "Outside " + name, FilePath: dir})
	require.NoError(e.t, err)
	f := &database.BookFile{BookID: b.ID, FilePath: path, Format: "m4b", Duration: 3600}
	require.NoError(e.t, e.store.CreateBookFile(f))
	return f.ID
}

// refResult is a valid ok result for job, stamped with refPair.
func refResult(h *hubEnv, job workerapi.Job, seed uint32) workerapi.JobResult {
	res := h.okResult(job, 160, seed)
	res.FpcalcVersion, res.FFmpegVersion = refPair.Fpcalc, refPair.FFmpeg
	return res
}

// refHello is a hello from worker running refPair.
func refHello(worker string) workerapi.HelloRequest {
	return workerapi.HelloRequest{WorkerID: worker, FpcalcVersion: refPair.Fpcalc, FFmpegVersion: refPair.FFmpeg}
}

// helloUntilReady calls hello as worker until it is not pending (a bootstrap
// or a normal calibration), as fp-worker does.
func helloUntilReady(ctx context.Context, hub *WorkerHub, worker string) *workerapi.HelloResponse {
	for ctx.Err() == nil {
		resp, err := hub.Hello(ctx, refHello(worker))
		if err == nil && !resp.ReferencePending {
			return resp
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

func refLeaseReq(worker string, n int) workerapi.LeaseRequest {
	return workerapi.LeaseRequest{WorkerID: worker, MaxJobs: n, Pipeline: fingerprint.WindowPipelineID,
		FpcalcVersion: refPair.Fpcalc, FFmpegVersion: refPair.FFmpeg}
}

// fakeRefWorker leases with refPair until ctx ends and answers every job
// with decide(job).
func fakeRefWorker(ctx context.Context, hub *WorkerHub, decide func(workerapi.Job) workerapi.JobResult) {
	helloUntilReady(ctx, hub, "mac1")
	for ctx.Err() == nil {
		resp, err := hub.Lease(ctx, withRunL(hub, refLeaseReq("mac1", 4)))
		if err != nil || resp == nil {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		var rs []workerapi.JobResult
		for _, j := range resp.Jobs {
			rs = append(rs, decide(j))
		}
		_, _ = hub.Results(ctx, withRunS(hub, workerapi.ResultsRequest{WorkerID: "mac1", Results: rs}))
	}
}

// attachRemoteOnlyHub attaches the plugin hub the way a remote-only live run
// does: planned against refPair, wt carrying only refPair.
func attachRemoteOnlyHub(t *testing.T, e *wbEnv) *hubEnv {
	t.Helper()
	plan, err := e.plugin.planWindowBackfill(context.Background(), &wbReporter{}, &refPair)
	require.NoError(t, err)
	items := append([]windowItem(nil), plan.tiers[windowTierPresent]...)
	for i := range items {
		items[i].qi = i
	}
	h := &hubEnv{wbEnv: e, hub: e.plugin.WorkerHub(), clock: &testClock{}, tally: &windowTally{}, items: items}
	h.hub.now = h.clock.now
	h.hub.attach(context.Background(), e.plugin, fingerprint.WindowTools{Versions: refPair}, h.tally, plan.calibration,
		hubMode{remoteOnly: true, identity: plan.identity, refExists: plan.refAny})
	t.Cleanup(h.hub.detach)
	h.hub.beginTier(windowTierPresent, items)
	return h
}

// TestWindowBackfill_RemoteOnly_ServerDecodesNothing: a remote-only run never
// resolves local tools and never runs the local cut path; workers write the
// eligible files and the rest are deferred, not cut and not tombstoned.
func TestWindowBackfill_RemoteOnly_ServerDecodesNothing(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	calls := noLocalTools(e)
	_, a := e.addFile("", "lib/a.m4b", true, 3600)
	_, b := e.addFile("", "lib/b.m4b", true, 3600)
	_, unknown := e.addFile("", "lib/unknown.m4b", true, 0)
	outside := e.addOutside("outside.m4b")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wctx, stopWorker := context.WithCancel(ctx)
	h := &hubEnv{wbEnv: e, hub: e.plugin.WorkerHub()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fakeRefWorker(wctx, h.hub, func(j workerapi.Job) workerapi.JobResult { return refResult(h, j, 7) })
	}()
	res, err := e.run(ctx, nil, WindowBackfillParams{Live: true, RemoteOnly: true, Concurrency: 2})
	stopWorker()
	<-done
	require.NoError(t, err)
	require.Zero(t, calls.Load(), "a remote-only run resolved local tools")
	require.Empty(t, e.invoked(), "a remote-only run ran the local cut path")
	require.EqualValues(t, 2, res.written)
	for _, id := range []string{a, b} {
		ws := e.windows(id)
		require.NotEmpty(t, ws, "file %s", id)
		require.Equal(t, "fp-worker:mac1", ws[0].Host)
		require.Equal(t, refPair.FFmpeg, ws[0].FFmpegVersion)
	}
	require.Equal(t, map[string]int{"unknown_duration": 1, "not_under_any_root": 1}, res.deferred)
	for _, id := range []string{unknown, outside} {
		require.Empty(t, e.windows(id), "deferred file %s was written", id)
		require.Nil(t, e.tombstone(id), "deferred file %s was tombstoned", id)
	}
}

// TestWindowBackfill_RemoteOnly_DeferredItemsEndTheTierAndMoveTheCheckpoint:
// files a worker hands back (decode error) and files no worker may have are
// deferred without a write or a tombstone, the tier ends once only they are
// left, and the in-tier checkpoint moves past them.
func TestWindowBackfill_RemoteOnly_DeferredItemsEndTheTierAndMoveTheCheckpoint(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	calls := noLocalTools(e)
	prevEvery := windowCheckpointEvery
	windowCheckpointEvery = 1
	t.Cleanup(func() { windowCheckpointEvery = prevEvery })
	var ids []string
	for i := range 4 {
		_, id := e.addFile("", filepath.Join("lib", "u"+string(rune('a'+i))+".m4b"), true, 0)
		ids = append(ids, id)
	}
	_, bad := e.addFile("", "lib/bad.m4b", true, 3600)
	ids = append(ids, bad)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wctx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		fakeRefWorker(wctx, e.plugin.WorkerHub(), func(j workerapi.Job) workerapi.JobResult {
			return workerapi.JobResult{JobID: j.JobID, Ref: j.Ref, Outcome: workerapi.OutcomeDecodeError, Error: "Invalid data"}
		})
	}()
	rep := &wbReporter{}
	res, err := e.run(ctx, rep, WindowBackfillParams{Live: true, RemoteOnly: true, Concurrency: 2})
	stopWorker()
	<-done
	require.NoError(t, err, "the tier must end once only deferred files are left")
	require.Zero(t, calls.Load())
	require.Empty(t, e.invoked(), "a handed-back file must not be cut by the server")
	require.Equal(t, map[string]int{"unknown_duration": 4, "worker_decode_error": 1}, res.deferred)
	require.Zero(t, res.written)
	require.Zero(t, res.failed)
	for _, id := range ids {
		require.Empty(t, e.windows(id))
		require.Nil(t, e.tombstone(id), "deferred file %s must not be tombstoned", id)
	}
	last := slices.Max(ids)
	rep.mu.Lock()
	defer rep.mu.Unlock()
	passed := false
	for _, cp := range rep.checkpoints {
		require.True(t, cp.RemoteOnly, "a checkpoint must keep remote_only, or a resume would decode on the server")
		if cp.Resume != nil && cp.Resume.Tier == windowTierPresent && cp.Resume.AfterFileID == last {
			passed = true
		}
	}
	require.True(t, passed, "no in-tier checkpoint reached the last deferred file %s: %+v", last, rep.checkpoints)
}

// TestWindowBackfill_RemoteOnly_NoWorkerOnlyDeferredStillEnds: with nothing a
// worker may take, the run ends without any worker attached.
func TestWindowBackfill_RemoteOnly_NoWorkerOnlyDeferredStillEnds(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	e.addFile("", "lib/u1.m4b", true, 0)
	e.addOutside("o1.m4b")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := e.run(ctx, nil, WindowBackfillParams{Live: true, RemoteOnly: true})
	require.NoError(t, err)
	require.EqualValues(t, 2, res.deferred["unknown_duration"]+res.deferred["not_under_any_root"])
}

// TestWindowBackfill_RemoteOnly_WaitsForWorkersInsteadOfFinishingGreen: an
// eligible file with no worker attached is never "done": the run waits (here
// until its context ends) rather than ending having written nothing.
func TestWindowBackfill_RemoteOnly_WaitsForWorkersInsteadOfFinishingGreen(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	_, id := e.addFile("", "lib/a.m4b", true, 3600)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := e.run(ctx, nil, WindowBackfillParams{Live: true, RemoteOnly: true})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, e.windows(id))
	require.Empty(t, e.invoked())
}

// TestWindowBackfill_RemoteOnly_RefusesWithoutWorkerAPIOrReference: a live
// remote-only run fails fast, before planning, when no worker could lease or
// no reference pair is configured; a dry run needs neither the worker API
// nor any local tool.
func TestWindowBackfill_RemoteOnly_RefusesWithoutWorkerAPIOrReference(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	calls := noLocalTools(e)
	e.addFile("", "lib/a.m4b", true, 3600)

	config.AppConfig.FingerprintRemoteWorkersEnabled = false
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, RemoteOnly: true})
	require.ErrorContains(t, err, "fingerprint_remote_workers_enabled")
	res, err := e.run(context.Background(), nil, WindowBackfillParams{RemoteOnly: true})
	require.NoError(t, err, "a dry run does not need the worker API")
	require.Len(t, res.plan.tiers[windowTierPresent], 1)

	config.AppConfig.FingerprintRemoteWorkersEnabled = true
	for _, ref := range []string{"", "1.6.1", "1.6.1/9.0.2/x", "/9.0.2"} {
		config.AppConfig.FingerprintWindowReferenceTools = ref
		_, err = e.run(context.Background(), nil, WindowBackfillParams{Live: true, RemoteOnly: true})
		require.ErrorContains(t, err, "fingerprint_window_reference_tools", "reference %q", ref)
	}
	require.Zero(t, calls.Load())
	require.Empty(t, e.invoked())
}

// TestWindowBackfill_RemoteOnly_ReferencePairIsTheClassNotTheServerPair: the
// equivalence class and the lease allowlist are built from the reference
// pair, the server's own pair is in neither, and windows the server cut with
// its own pair are re-planned rather than compared as equivalent.
func TestWindowBackfill_RemoteOnly_ReferencePairIsTheClassNotTheServerPair(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	allow := fingerprint.ToolVersionInfo{Fpcalc: "1.6.1-allow", FFmpeg: "9.0.1-allow"}
	config.AppConfig.FingerprintWorkerToolVersions = []string{allow.Fpcalc + "/" + allow.FFmpeg}
	e.addFile("", "lib/a.m4b", true, 3600)
	// A normal run cuts the file with the server's own tools (deliberately:
	// a reference pair is configured).
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, AllowServerDecode: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, res.written)
	calls := noLocalTools(e)

	res, err = e.run(context.Background(), nil, WindowBackfillParams{RemoteOnly: true})
	require.NoError(t, err)
	require.Zero(t, calls.Load())
	class := fingerprint.EquivalentToolPairs()
	require.Contains(t, class, refPair)
	require.Contains(t, class, allow)
	require.NotContains(t, class, e.tools.Versions, "the server cuts nothing in remote-only: its pair must not join the class")
	require.False(t, fingerprint.ToolsEquivalent(e.tools.Versions, refPair))
	require.Zero(t, res.plan.current[windowTierPresent], "server-pair windows are not current against the reference pair")
	require.Len(t, res.plan.tiers[windowTierPresent], 1)

	h := attachRemoteOnlyHub(t, e)
	hello, err := h.hub.Hello(context.Background(), workerapi.HelloRequest{})
	require.NoError(t, err)
	require.NotContains(t, hello.ToolVersions, workerapi.ToolVersions{Fpcalc: e.tools.Versions.Fpcalc, FFmpeg: e.tools.Versions.FFmpeg})
	_, err = h.hub.Lease(context.Background(), withRunL(h.hub, h.leaseReq("srv", 1)))
	require.ErrorIs(t, err, ErrToolsNotAllowed, "the server's own pair may not lease in remote-only")
	req := refLeaseReq("allowed", 1)
	req.FpcalcVersion, req.FFmpegVersion = allow.Fpcalc, allow.FFmpeg
	_, err = h.hub.Lease(context.Background(), withRunL(h.hub, req))
	require.ErrorIs(t, err, ErrToolsNotAllowed, "no reference windows yet: an allowlisted pair has nothing to be parity-checked against")
	boot, err := h.hub.Hello(context.Background(), refHello("ref"))
	require.NoError(t, err)
	require.True(t, boot.Bootstrap)
	resp, err := h.hub.Lease(context.Background(), withRunL(h.hub, refLeaseReq("ref", 1)))
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Jobs, 1)
}

// TestWorkerHub_RemoteOnly_HelloBootstrapsThenOffersReferenceWindows: with no
// reference-pair window anywhere, Hello offers identity-only calibration files
// and announces the bootstrap; server-pair windows are never offered. Once a
// reference worker has written windows, Hello offers those (worker-made, which
// a normal run never allows) and an allowlisted pair may lease.
func TestWorkerHub_RemoteOnly_HelloBootstrapsThenOffersReferenceWindows(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	allow := fingerprint.ToolVersionInfo{Fpcalc: "1.6.1-allow", FFmpeg: "9.0.1-allow"}
	config.AppConfig.FingerprintWorkerToolVersions = []string{allow.Fpcalc + "/" + allow.FFmpeg}
	e.addFileWith("", "lib/srv.m4b", fakeAudio(3600, 70<<10), 3600)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, AllowServerDecode: true}) // server-pair windows
	require.NoError(t, err)
	e.addFileWith("", "lib/a.m4b", fakeAudio(3600, 70<<10), 3600)
	e.addFileWith("", "lib/b.m4b", fakeAudio(3600, 70<<10), 3600)
	noLocalTools(e)
	h := attachRemoteOnlyHub(t, e)

	hello, err := h.hub.Hello(context.Background(), refHello("mac1"))
	require.NoError(t, err)
	require.True(t, hello.Bootstrap)
	require.Equal(t, &workerapi.ToolVersions{Fpcalc: refPair.Fpcalc, FFmpeg: refPair.FFmpeg}, hello.ReferenceTools)
	require.NotEmpty(t, hello.Calibration)
	for _, cf := range hello.Calibration {
		require.Equal(t, "libroot", cf.Root)
		require.Empty(t, cf.Windows, "bootstrap calibration is identity only")
		fh, err := os.Open(filepath.Join(e.lib, cf.Rel))
		require.NoError(t, err)
		sum := sha256.New()
		_, err = io.CopyN(sum, fh, calibrationHeadBytes)
		_ = fh.Close()
		require.NoError(t, err)
		require.Equal(t, hex.EncodeToString(sum.Sum(nil)), cf.Head64K)
		fi, err := os.Stat(filepath.Join(e.lib, cf.Rel))
		require.NoError(t, err)
		require.Equal(t, fi.Size(), cf.Size)
		require.Equal(t, fi.ModTime().Unix(), cf.MtimeUnix)
	}

	// The reference worker writes one file.
	resp, err := h.hub.Lease(context.Background(), withRunL(h.hub, refLeaseReq("mac1", 1)))
	require.NoError(t, err)
	require.NotNil(t, resp)
	st := h.post("mac1", refResult(h, resp.Jobs[0], 3))
	require.Equal(t, workerapi.StatusAccepted, st[0].Status, st[0].Reason)

	hello, err = h.hub.Hello(context.Background(), refHello("mac2"))
	require.NoError(t, err)
	require.False(t, hello.Bootstrap, "reference windows exist now: the byte-parity gate applies")
	require.False(t, hello.ReferencePending)
	require.Len(t, hello.Calibration, 1)
	cal := hello.Calibration[0]
	require.Equal(t, resp.Jobs[0].RelB64, cal.RelB64)
	require.NotEmpty(t, cal.Windows)
	for _, w := range cal.Windows {
		require.Equal(t, workerapi.ToolVersions{Fpcalc: refPair.Fpcalc, FFmpeg: refPair.FFmpeg}, w.ToolVersions)
	}
	require.Equal(t, "fp-worker:mac1", h.windows(h.fileID(resp.Jobs[0]))[0].Host)

	req := refLeaseReq("allowed", 1)
	req.FpcalcVersion, req.FFmpegVersion = allow.Fpcalc, allow.FFmpeg
	_, err = h.hub.Lease(context.Background(), withRunL(h.hub, req))
	require.NoError(t, err, "with reference windows offered, an allowlisted pair may lease after its parity gate")
}

// ---- review round 1 ----

// hbReporter counts the remote-only heartbeat's progress reports.
type hbReporter struct {
	*wbReporter
	beats atomic.Int64
}

func (r *hbReporter) UpdateProgress(_, _ int, msg string) error {
	if strings.Contains(msg, "heartbeat") {
		r.beats.Add(1)
	}
	return nil
}

// TestWindowBackfill_RemoteOnly_HeartbeatWhileAHeadLeaseIsStuck: worker A
// leases the head of the tier and crashes, so every RunItems waiter sits on
// its items while worker B keeps finishing later ones. The op must keep
// reporting progress on a fixed period (the watchdog kills a silent op), and
// it must survive until A's lease expires and B finishes A's files.
func TestWindowBackfill_RemoteOnly_HeartbeatWhileAHeadLeaseIsStuck(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	const n = 120
	for i := range n {
		e.addFile("", fmt.Sprintf("lib/f%03d.m4b", i), true, 3600)
	}
	prevSweep, prevPoll := hubSweepEvery, windowDrainPoll
	hubSweepEvery, windowDrainPoll = 5*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { hubSweepEvery, windowDrainPoll = prevSweep, prevPoll })
	clock := &testClock{}
	hub := e.plugin.WorkerHub()
	hub.now = clock.now
	h := &hubEnv{wbEnv: e, hub: hub, clock: clock}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep := &hbReporter{wbReporter: &wbReporter{}}
	var beatsWhileStuck atomic.Int64
	go func() {
		// A: claim the bootstrap, lease the 8 head files, write only the
		// last (so reference windows exist), then crash holding 7.
		if helloUntilReady(ctx, hub, "A") == nil {
			return
		}
		var resp *workerapi.LeaseResponse
		for ctx.Err() == nil && resp == nil {
			resp, _ = hub.Lease(ctx, withRunL(hub, refLeaseReq("A", 8)))
		}
		if resp == nil {
			return
		}
		last := resp.Jobs[len(resp.Jobs)-1]
		_, _ = hub.Results(ctx, withRunS(hub, workerapi.ResultsRequest{WorkerID: "A", Results: []workerapi.JobResult{refResult(h, last, 1)}}))
		// B works through everything else, one file every 2 ms.
		helloUntilReady(ctx, hub, "B")
		start := rep.beats.Load()
		for ctx.Err() == nil {
			r, err := hub.Lease(ctx, withRunL(hub, refLeaseReq("B", 1)))
			if err != nil || r == nil {
				break // only A's stuck files are left
			}
			_, _ = hub.Results(ctx, withRunS(hub, workerapi.ResultsRequest{WorkerID: "B", Results: []workerapi.JobResult{refResult(h, r.Jobs[0], 2)}}))
			time.Sleep(2 * time.Millisecond)
		}
		beatsWhileStuck.Store(rep.beats.Load() - start)
		// A's lease expires; the sweep hands its files back and B does them.
		clock.advance(workerapi.LeaseTTL + time.Second)
		for ctx.Err() == nil {
			r, err := hub.Lease(ctx, withRunL(hub, refLeaseReq("B", 4)))
			if err != nil || r == nil {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			var rs []workerapi.JobResult
			for _, j := range r.Jobs {
				rs = append(rs, refResult(h, j, 3))
			}
			_, _ = hub.Results(ctx, withRunS(hub, workerapi.ResultsRequest{WorkerID: "B", Results: rs}))
		}
	}()
	res, err := e.plugin.windowBackfill(ctx, rep, WindowBackfillParams{Live: true, RemoteOnly: true, Concurrency: 4})
	cancel()
	require.NoError(t, err)
	require.EqualValues(t, n, res.written)
	require.GreaterOrEqual(t, beatsWhileStuck.Load(), int64(5),
		"no fixed-period heartbeat while every waiter sat on a crashed worker's lease")
}

// TestWorkerHub_RemoteOnly_OneBootstrapClaimAtATime: two reference-pair
// workers hello at once with no reference windows; exactly one gets the
// bootstrap, the other (and any non-reference pair) is told to wait and may
// not lease. A claimant that goes silent loses the claim after the lease TTL.
func TestWorkerHub_RemoteOnly_OneBootstrapClaimAtATime(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	for i := range 3 {
		e.addFileWith("", fmt.Sprintf("lib/b%d.m4b", i), fakeAudio(3600, 70<<10), 3600)
	}
	h := attachRemoteOnlyHub(t, e)
	var wg sync.WaitGroup
	got := make([]*workerapi.HelloResponse, 2)
	for i, id := range []string{"llm1", "mac1"} {
		wg.Go(func() {
			r, err := h.hub.Hello(context.Background(), refHello(id))
			require.NoError(t, err)
			got[i] = r
		})
	}
	wg.Wait()
	require.NotEqual(t, got[0].Bootstrap, got[1].Bootstrap, "exactly one worker may bootstrap")
	claimant, other := "llm1", "mac1"
	if got[1].Bootstrap {
		claimant, other = "mac1", "llm1"
		got[0], got[1] = got[1], got[0]
	}
	require.NotEmpty(t, got[0].Calibration)
	require.True(t, got[1].ReferencePending)
	require.Empty(t, got[1].Calibration)
	require.NotEmpty(t, got[1].Waiting)

	nonRef := workerapi.HelloRequest{WorkerID: "x", FpcalcVersion: "1.6.1", FFmpegVersion: "9.0.1"}
	r, err := h.hub.Hello(context.Background(), nonRef)
	require.NoError(t, err)
	require.True(t, r.ReferencePending)
	require.False(t, r.Bootstrap)

	_, err = h.hub.Lease(context.Background(), withRunL(h.hub, refLeaseReq(other, 1)))
	require.ErrorIs(t, err, ErrToolsNotAllowed, "only the claimant may lease during the bootstrap")
	resp, err := h.hub.Lease(context.Background(), withRunL(h.hub, refLeaseReq(claimant, 1)))
	require.NoError(t, err)
	require.NotNil(t, resp)

	// The claimant vanishes: after the TTL the claim is released.
	h.clock.advance(workerapi.LeaseTTL + time.Second)
	r, err = h.hub.Hello(context.Background(), refHello(other))
	require.NoError(t, err)
	require.True(t, r.Bootstrap, "a lapsed claim must pass to the next reference-pair worker")
	_, err = h.hub.Lease(context.Background(), withRunL(h.hub, refLeaseReq(claimant, 1)))
	require.ErrorIs(t, err, ErrToolsNotAllowed, "the lapsed claimant may not lease without reference windows")
}

// TestWorkerHub_RemoteOnly_ExistingReferenceWindowsNeverBootstrapAgain: a
// later run over a store that already has reference-pair windows picks its
// calibration by the EXACT reference pair (not by equivalence), and never
// bootstraps again, even while no reference file happens to be readable.
func TestWorkerHub_RemoteOnly_ExistingReferenceWindowsNeverBootstrapAgain(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	allow := fingerprint.ToolVersionInfo{Fpcalc: "1.6.1-allow", FFmpeg: "9.0.1-allow"}
	config.AppConfig.FingerprintWorkerToolVersions = []string{allow.Fpcalc + "/" + allow.FFmpeg}
	var allowIDs []string
	for i := range 5 {
		_, id := e.addFileWith("", fmt.Sprintf("lib/allow%d.m4b", i), fakeAudio(3600, 70<<10), 3600)
		allowIDs = append(allowIDs, id)
	}
	_, refID := e.addFileWith("", "lib/ref.m4b", fakeAudio(3600, 70<<10), 3600)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, AllowServerDecode: true})
	require.NoError(t, err)
	restamp := func(id string, v fingerprint.ToolVersionInfo) {
		ws := e.windows(id)
		for i := range ws {
			ws[i].FpcalcVersion, ws[i].FFmpegVersion, ws[i].Host = v.Fpcalc, v.FFmpeg, "fp-worker:mac1"
		}
		require.NoError(t, e.store.ReplaceFingerprintWindows(database.FileWindowRef(id), ws))
	}
	for _, id := range allowIDs {
		restamp(id, allow)
	}
	restamp(refID, refPair)
	fingerprint.SetToolEquivalence(refPair, []fingerprint.ToolVersionInfo{allow})

	plan, err := e.plugin.planWindowBackfill(context.Background(), &wbReporter{}, &refPair)
	require.NoError(t, err)
	require.Equal(t, 6, plan.current[windowTierPresent], "allowlisted windows are current by equivalence")
	require.True(t, plan.exactCurrent)
	require.Len(t, plan.calibration, 1, "only exact-reference-pair files may be calibration candidates")
	require.Equal(t, refID, plan.calibration[0].FileID)

	h := attachRemoteOnlyHub(t, e)
	refPath := filepath.Join(e.lib, "lib/ref.m4b")
	fi, err := os.Stat(refPath)
	require.NoError(t, err)
	// The one reference file is unreadable as planned right now.
	require.NoError(t, os.Chtimes(refPath, fi.ModTime(), fi.ModTime().Add(time.Hour)))
	r, err := h.hub.Hello(context.Background(), refHello("mac1"))
	require.NoError(t, err)
	require.False(t, r.Bootstrap, "reference windows exist in the store: never bootstrap again")
	require.True(t, r.ReferencePending)
	require.NoError(t, os.Chtimes(refPath, fi.ModTime(), fi.ModTime()))
	r, err = h.hub.Hello(context.Background(), refHello("mac2"))
	require.NoError(t, err)
	require.False(t, r.Bootstrap)
	require.False(t, r.ReferencePending)
	require.Len(t, r.Calibration, 1)
	require.NotEmpty(t, r.Calibration[0].Windows, "the next worker must pass byte parity")
	for _, w := range r.Calibration[0].Windows {
		require.Equal(t, workerapi.ToolVersions{Fpcalc: refPair.Fpcalc, FFmpeg: refPair.FFmpeg}, w.ToolVersions)
	}
}

// TestWindowBackfill_ReferenceConfiguredRefusesServerDecode: with a reference
// pair configured, a live run without remote_only would re-cut the library
// on the server; it is refused unless allow_server_decode says otherwise.
func TestWindowBackfill_ReferenceConfiguredRefusesServerDecode(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	_, id := e.addFile("", "lib/a.m4b", true, 3600)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.ErrorContains(t, err, "allow_server_decode")
	require.Empty(t, e.invoked(), "refused before anything was cut")
	res, err := e.run(context.Background(), nil, WindowBackfillParams{})
	require.NoError(t, err, "a dry run decodes nothing and is allowed")
	require.Zero(t, res.written)
	res, err = e.run(context.Background(), nil, WindowBackfillParams{Live: true, AllowServerDecode: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, res.written)
	require.NotEmpty(t, e.windows(id))
}

// startRemoteOnlyRun runs a remote-only live backfill in the background with
// the hub on clock and a fast heartbeat, and waits until the hub is attached.
func startRemoteOnlyRun(t *testing.T, e *wbEnv, clock *testClock, p WindowBackfillParams) (context.Context, chan error) {
	t.Helper()
	prevPoll := windowDrainPoll
	windowDrainPoll = 5 * time.Millisecond
	t.Cleanup(func() { windowDrainPoll = prevPoll })
	hub := e.plugin.WorkerHub()
	hub.now = clock.now
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	done := make(chan error, 1)
	finished := make(chan struct{})
	// Stop the run and wait for it before the poll interval is restored:
	// its heartbeat reads windowDrainPoll.
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	go func() {
		defer close(finished)
		_, err := e.run(ctx, nil, p)
		done <- err
	}()
	for {
		if _, err := hub.liveRun(); err == nil {
			hub.mu.Lock()
			ready := len(hub.run.st) > 0
			hub.mu.Unlock()
			if ready {
				return ctx, done
			}
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWindowBackfill_RemoteOnly_NoWorkerGraceFailsFast: with no worker ever
// calling, the run ends with a clear error after the grace instead of holding
// acoustid.fingerprint for 72 h.
func TestWindowBackfill_RemoteOnly_NoWorkerGraceFailsFast(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	e.addFile("", "lib/a.m4b", true, 3600)
	clock := &testClock{}
	_, done := startRemoteOnlyRun(t, e, clock, WindowBackfillParams{Live: true, RemoteOnly: true, NoWorkerGraceSec: 60})
	clock.advance(61 * time.Second)
	err := <-done
	require.ErrorIs(t, err, errNoRemoteWorkers)
	require.ErrorContains(t, err, "no fp-worker called the worker API")
}

// TestWindowBackfill_RemoteOnly_WorkerGapShorterThanTTLDoesNotFail: once a
// worker has made contact, a silence longer than the grace but shorter than
// the lease TTL plus the margin must not end the run.
func TestWindowBackfill_RemoteOnly_WorkerGapShorterThanTTLDoesNotFail(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	e.addFile("", "lib/a.m4b", true, 3600)
	e.addFile("", "lib/b.m4b", true, 3600)
	clock := &testClock{}
	ctx, done := startRemoteOnlyRun(t, e, clock, WindowBackfillParams{Live: true, RemoteOnly: true, NoWorkerGraceSec: 60})
	hub := e.plugin.WorkerHub()
	h := &hubEnv{wbEnv: e, hub: hub, clock: clock}
	one := func() {
		t.Helper()
		resp, err := hub.Lease(ctx, withRunL(hub, refLeaseReq("mac1", 1)))
		require.NoError(t, err)
		require.NotNil(t, resp)
		_, err = hub.Results(ctx, withRunS(hub, workerapi.ResultsRequest{WorkerID: "mac1", Results: []workerapi.JobResult{refResult(h, resp.Jobs[0], 1)}}))
		require.NoError(t, err)
	}
	require.NotNil(t, helloUntilReady(ctx, hub, "mac1"))
	one()
	// Nothing leased; the worker is quiet for 9 min (> grace, < TTL+margin).
	clock.advance(9 * time.Minute)
	time.Sleep(100 * time.Millisecond) // ~20 heartbeats
	select {
	case err := <-done:
		t.Fatalf("run ended during a worker gap shorter than the lease TTL: %v", err)
	default:
	}
	one()
	require.NoError(t, <-done)
}

// TestWorkerHub_RemoteOnly_SlowHelloDoesNotStallResults: a bootstrap hello
// reading its calibration files over a slow mount must not hold any lock the
// lease and results paths need.
func TestWorkerHub_RemoteOnly_SlowHelloDoesNotStallResults(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	e.addFileWith("", "lib/a.m4b", fakeAudio(3600, 70<<10), 3600)
	e.addFileWith("", "lib/b.m4b", fakeAudio(3600, 70<<10), 3600)
	h := attachRemoteOnlyHub(t, e)
	gate := make(chan struct{})
	var release sync.Once
	entered := make(chan struct{}, 1)
	// Injected into this run only (no package global), before the hello
	// goroutine starts, so nothing races the reads.
	h.hub.mu.Lock()
	run := h.hub.run
	h.hub.mu.Unlock()
	run.headHash = func(p string) (string, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
		return headSHA256(p)
	}
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	go func() { _, _ = h.hub.Hello(context.Background(), refHello("mac1")) }()
	<-entered // the hello is now stuck reading a calibration file

	done := make(chan error, 1)
	go func() {
		resp, err := h.hub.Lease(context.Background(), withRunL(h.hub, refLeaseReq("mac1", 1)))
		if err != nil || resp == nil {
			done <- fmt.Errorf("lease: %v", err)
			return
		}
		st, err := h.hub.Results(context.Background(), withRunS(h.hub, workerapi.ResultsRequest{WorkerID: "mac1",
			Results: []workerapi.JobResult{refResult(h, resp.Jobs[0], 1)}}))
		if err == nil && st.Statuses[0].Status != workerapi.StatusAccepted {
			err = fmt.Errorf("status %+v", st.Statuses[0])
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("lease/results stalled behind a slow hello")
	}
	release.Do(func() { close(gate) })
}

// ---- review round 2 ----

// TestWorkerHub_RemoteOnly_SupersededBootstrapWorkerCannotWrite is the
// reviewer's probe: macA claims and leases, sleeps past the TTL (its items are
// swept back), llmB takes the claim, then macA posts. macA's prints must be
// dropped even though nobody re-leased its items; llmB's are taken; and macA,
// which never passed parity against llmB's reference, may not lease again
// until a hello hands it the real calibration.
func TestWorkerHub_RemoteOnly_SupersededBootstrapWorkerCannotWrite(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	for i := range 4 {
		e.addFileWith("", fmt.Sprintf("lib/p%d.m4b", i), fakeAudio(3600, 70<<10), 3600)
	}
	h := attachRemoteOnlyHub(t, e)
	ctx := context.Background()

	r, err := h.hub.Hello(ctx, refHello("macA"))
	require.NoError(t, err)
	require.True(t, r.Bootstrap)
	leaseA, err := h.hub.Lease(ctx, withRunL(h.hub, refLeaseReq("macA", 2)))
	require.NoError(t, err)
	require.Len(t, leaseA.Jobs, 2)

	h.clock.advance(workerapi.LeaseTTL + time.Second)
	require.Equal(t, 2, h.hub.sweep(), "macA's items are swept back to pending")
	r, err = h.hub.Hello(ctx, refHello("llmB"))
	require.NoError(t, err)
	require.True(t, r.Bootstrap, "the lapsed claim passes to llmB")

	st := h.post("macA", refResult(h, leaseA.Jobs[0], 1), refResult(h, leaseA.Jobs[1], 1))
	for _, x := range st {
		require.Equal(t, workerapi.StatusStale, x.Status, "a superseded claimant's result must not be stored")
		require.Equal(t, "superseded_bootstrap", x.Reason)
	}
	for _, j := range leaseA.Jobs {
		require.Empty(t, h.windows(h.fileID(j)), "macA's unchecked prints were stored")
	}

	leaseB, err := h.hub.Lease(ctx, withRunL(h.hub, refLeaseReq("llmB", 2)))
	require.NoError(t, err)
	require.NotNil(t, leaseB)
	st = h.post("llmB", refResult(h, leaseB.Jobs[0], 2))
	require.Equal(t, workerapi.StatusAccepted, st[0].Status, st[0].Reason)

	_, err = h.hub.Lease(ctx, withRunL(h.hub, refLeaseReq("macA", 1)))
	require.ErrorIs(t, err, ErrToolsNotAllowed, "macA bootstrapped under a claim that did not define the reference")
	r, err = h.hub.Hello(ctx, refHello("macA"))
	require.NoError(t, err)
	require.False(t, r.Bootstrap)
	require.NotEmpty(t, r.Calibration)
	require.NotEmpty(t, r.Calibration[0].Windows, "macA must now pass parity against llmB's windows")
	resp, err := h.hub.Lease(ctx, withRunL(h.hub, refLeaseReq("macA", 1)))
	require.NoError(t, err, "after the real calibration macA may lease")
	require.NotNil(t, resp)
}

// TestWorkerHub_RemoteOnly_WrongPairLeaseDoesNotKeepTheClaim: a lease under
// the claimant's worker_id with a pair that is not allowed is refused and
// does not extend the claim.
func TestWorkerHub_RemoteOnly_WrongPairLeaseDoesNotKeepTheClaim(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	e.addFileWith("", "lib/a.m4b", fakeAudio(3600, 70<<10), 3600)
	h := attachRemoteOnlyHub(t, e)
	ctx := context.Background()
	r, err := h.hub.Hello(ctx, refHello("mac1"))
	require.NoError(t, err)
	require.True(t, r.Bootstrap)
	h.clock.advance(workerapi.LeaseTTL - time.Minute)
	bad := refLeaseReq("mac1", 1)
	bad.FpcalcVersion, bad.FFmpegVersion = "0.0", "0.0"
	_, err = h.hub.Lease(ctx, withRunL(h.hub, bad))
	require.ErrorIs(t, err, ErrToolsNotAllowed)
	h.clock.advance(2 * time.Minute)
	r, err = h.hub.Hello(ctx, refHello("llm1"))
	require.NoError(t, err)
	require.True(t, r.Bootstrap, "a refused wrong-pair call must not have kept mac1's claim alive")
}

// TestWindowBackfill_RemoteOnly_PendingGraceFailsNamingWaitingWorkers: a
// worker that only ever hears reference_pending is not progress. The run
// outlives the no-worker grace while it waits, then ends after the pending
// grace, naming the missing pair and the waiting worker.
func TestWindowBackfill_RemoteOnly_PendingGraceFailsNamingWaitingWorkers(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	e.addFile("", "lib/a.m4b", true, 3600)
	clock := &testClock{}
	ctx, done := startRemoteOnlyRun(t, e, clock, WindowBackfillParams{Live: true, RemoteOnly: true, NoWorkerGraceSec: 60, PendingGraceSec: 120})
	hub := e.plugin.WorkerHub()
	waiter := workerapi.HelloRequest{WorkerID: "mac9", FpcalcVersion: "1.6.1", FFmpegVersion: "9.0.1"}
	hello := func() {
		t.Helper()
		r, err := hub.Hello(ctx, waiter)
		require.NoError(t, err)
		require.True(t, r.ReferencePending)
	}
	hello()
	clock.advance(90 * time.Second) // past the no-worker grace, inside the pending grace
	hello()
	time.Sleep(60 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("run ended while the pending grace still ran: %v", err)
	default:
	}
	clock.advance(60 * time.Second)
	hello()
	err := <-done
	require.ErrorIs(t, err, errNoRemoteWorkers)
	require.ErrorContains(t, err, "no worker with the reference pair "+refPair.Fpcalc+"/"+refPair.FFmpeg)
	require.ErrorContains(t, err, "mac9 (1.6.1/9.0.1)")
}

// TestWindowBackfill_RemoteOnly_HelloIsNotProgress: a worker that took the
// bootstrap claim but never leases does not keep the run alive past the
// no-worker grace.
func TestWindowBackfill_RemoteOnly_HelloIsNotProgress(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	e.addFile("", "lib/a.m4b", true, 3600)
	clock := &testClock{}
	ctx, done := startRemoteOnlyRun(t, e, clock, WindowBackfillParams{Live: true, RemoteOnly: true, NoWorkerGraceSec: 60})
	hub := e.plugin.WorkerHub()
	r, err := hub.Hello(ctx, refHello("mac1"))
	require.NoError(t, err)
	require.True(t, r.Bootstrap)
	clock.advance(61 * time.Second)
	_, _ = hub.Hello(ctx, refHello("mac1"))
	err = <-done
	require.ErrorIs(t, err, errNoRemoteWorkers)
}

// staleReferenceEnv stores reference-pair windows for one file and then
// changes the file's mtime, so the store holds reference windows but none is
// usable for calibration.
func staleReferenceEnv(t *testing.T) *wbEnv {
	t.Helper()
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	_, id := e.addFileWith("", "lib/ref.m4b", fakeAudio(3600, 70<<10), 3600)
	e.addFileWith("", "lib/other.m4b", fakeAudio(3600, 70<<10), 3600)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, AllowServerDecode: true})
	require.NoError(t, err)
	ws := e.windows(id)
	for i := range ws {
		ws[i].FpcalcVersion, ws[i].FFmpegVersion, ws[i].Host = refPair.Fpcalc, refPair.FFmpeg, "fp-worker:mac1"
	}
	require.NoError(t, e.store.ReplaceFingerprintWindows(database.FileWindowRef(id), ws))
	path := filepath.Join(e.lib, "lib/ref.m4b")
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(path, fi.ModTime(), fi.ModTime().Add(time.Hour)))
	noLocalTools(e)
	return e
}

// TestWorkerHub_RemoteOnly_StaleReferenceWindowsDoNotReopenBootstrap: stored
// reference windows whose file changed since still count as "a reference
// exists": the run answers reference_pending with the reason instead of
// letting an unchecked worker define a new reference.
func TestWorkerHub_RemoteOnly_StaleReferenceWindowsDoNotReopenBootstrap(t *testing.T) {
	e := staleReferenceEnv(t)
	plan, err := e.plugin.planWindowBackfill(context.Background(), &wbReporter{}, &refPair)
	require.NoError(t, err)
	require.True(t, plan.refAny)
	require.Empty(t, plan.calibration)
	h := attachRemoteOnlyHub(t, e)
	r, err := h.hub.Hello(context.Background(), refHello("mac1"))
	require.NoError(t, err)
	require.False(t, r.Bootstrap, "a changed reference file must not re-open an unchecked bootstrap")
	require.True(t, r.ReferencePending)
	require.Contains(t, r.Waiting, "none usable")
	require.Contains(t, r.Waiting, "rebootstrap_reference")
}

// TestWindowBackfill_RemoteOnly_RebootstrapReferenceIsTheDeliberateEscape:
// only rebootstrap_reference lets a run over unusable reference windows
// bootstrap again.
func TestWindowBackfill_RemoteOnly_RebootstrapReferenceIsTheDeliberateEscape(t *testing.T) {
	for _, tc := range []struct {
		rebootstrap bool
		wantBoot    bool
	}{{false, false}, {true, true}} {
		t.Run(fmt.Sprint("rebootstrap=", tc.rebootstrap), func(t *testing.T) {
			e := staleReferenceEnv(t)
			clock := &testClock{}
			ctx, _ := startRemoteOnlyRun(t, e, clock, WindowBackfillParams{Live: true, RemoteOnly: true, RebootstrapReference: tc.rebootstrap})
			r, err := e.plugin.WorkerHub().Hello(ctx, refHello("mac1"))
			require.NoError(t, err)
			require.Equal(t, tc.wantBoot, r.Bootstrap)
			require.Equal(t, !tc.wantBoot, r.ReferencePending)
		})
	}
}

// ---- review round 3 ----

// TestWorkerHub_RemoteOnly_NewRunRequiresHelloAgain is the reviewer's
// detach/attach probe: macA bootstrapped in run 1 but did not define the
// reference (llmB did). A new run attaches (restart or resume); macA, which
// says hello only at startup, keeps leasing with run 1's ID. Every call is
// refused as a run change until macA hellos the new run and gets the real
// calibration to pass; a worker that sends no run ID is refused the same way.
func TestWorkerHub_RemoteOnly_NewRunRequiresHelloAgain(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	for i := range 4 {
		e.addFileWith("", fmt.Sprintf("lib/r%d.m4b", i), fakeAudio(3600, 70<<10), 3600)
	}
	h := attachRemoteOnlyHub(t, e)
	ctx := context.Background()
	helloA, err := h.hub.Hello(ctx, refHello("macA"))
	require.NoError(t, err)
	require.True(t, helloA.Bootstrap)
	runA := helloA.RunID
	require.NotEmpty(t, runA)
	reqA := refLeaseReq("macA", 2)
	reqA.RunID = runA
	leaseA, err := h.hub.Lease(ctx, reqA)
	require.NoError(t, err)
	require.NotNil(t, leaseA)
	h.clock.advance(workerapi.LeaseTTL + time.Second)
	h.hub.sweep()
	r, err := h.hub.Hello(ctx, refHello("llmB"))
	require.NoError(t, err)
	require.True(t, r.Bootstrap)
	leaseB, err := h.hub.Lease(ctx, withRunL(h.hub, refLeaseReq("llmB", 1)))
	require.NoError(t, err)
	require.Equal(t, workerapi.StatusAccepted, h.post("llmB", refResult(h, leaseB.Jobs[0], 2))[0].Status)

	// The run ends and a new one attaches.
	h.hub.detach()
	h2 := attachRemoteOnlyHub(t, e)
	_, err = h2.hub.Lease(ctx, reqA)
	require.ErrorIs(t, err, ErrRunChanged, "a worker gated in an earlier run must hello again")
	old := reqA
	old.RunID = ""
	_, err = h2.hub.Lease(ctx, old)
	require.ErrorIs(t, err, ErrRunChanged, "a worker that predates run IDs is refused the same way")
	_, err = h2.hub.Renew(leaseA.LeaseID, workerapi.RenewRequest{WorkerID: "macA", RunID: runA})
	require.ErrorIs(t, err, ErrRunChanged)
	_, err = h2.hub.Results(ctx, workerapi.ResultsRequest{WorkerID: "macA", RunID: runA,
		Results: []workerapi.JobResult{refResult(h, leaseA.Jobs[1], 9)}})
	require.ErrorIs(t, err, ErrRunChanged)

	hello2, err := h2.hub.Hello(ctx, refHello("macA"))
	require.NoError(t, err)
	require.NotEqual(t, runA, hello2.RunID)
	require.False(t, hello2.Bootstrap)
	require.NotEmpty(t, hello2.Calibration)
	require.NotEmpty(t, hello2.Calibration[0].Windows, "macA must pass parity against the reference in the new run")
	reqA.RunID = hello2.RunID
	resp, err := h2.hub.Lease(ctx, reqA)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

// addUnderBooksParent adds a present file under RootDir's PARENT — the "books"
// root pathutil.PathVars has always returned alongside "libroot".
func (e *wbEnv) addUnderBooksParent(name string) string {
	e.t.Helper()
	dir := filepath.Dir(e.lib) // …/books, the parent of …/books/audiobook-organizer
	path := filepath.Join(dir, name)
	require.NoError(e.t, os.WriteFile(path, []byte("audio:"+name), 0o644))
	b, err := e.store.CreateBook(&database.Book{Title: "Parent " + name, FilePath: dir})
	require.NoError(e.t, err)
	f := &database.BookFile{BookID: b.ID, FilePath: path, Format: "m4b", Duration: 3600}
	require.NoError(e.t, e.store.CreateBookFile(f))
	return f.ID
}

// TestWindowBackfill_RemoteOnly_FilesUnderBooksParentAreEligible is the
// regression test for the restriction that discarded most of the library.
//
// remoteEligible used to require the "libroot" root specifically. PathVars has
// always returned TWO roots — libroot and its parent books — so every file
// under the parent was refused with "not_under_libroot" and no worker was ever
// offered it. In prod on 2026-09-20 that was 144,708 of 200,729 files walked in
// a single run (72%), refused again on every re-run; a 400-book sample of the
// library held 339 under books against 60 under libroot.
//
// The file here sits under the parent and must now be WRITTEN by a worker. The
// job it receives must also name the root it was split against, or the worker
// resolves the relative path under the wrong mount.
func TestWindowBackfill_RemoteOnly_FilesUnderBooksParentAreEligible(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	withRemoteOnly(t)
	noLocalTools(e)
	parent := e.addUnderBooksParent("parent.m4b")

	var sawRoot atomic.Value
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wctx, stopWorker := context.WithCancel(ctx)
	h := &hubEnv{wbEnv: e, hub: e.plugin.WorkerHub()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fakeRefWorker(wctx, h.hub, func(j workerapi.Job) workerapi.JobResult {
			sawRoot.Store(j.Root)
			return refResult(h, j, 7)
		})
	}()
	res, err := e.run(ctx, nil, WindowBackfillParams{Live: true, RemoteOnly: true, Concurrency: 2})
	stopWorker()
	<-done
	require.NoError(t, err)

	require.EqualValues(t, 1, res.written,
		"a file under the books root must be fingerprinted, not deferred (deferred=%v)", res.deferred)
	require.Empty(t, res.deferred["not_under_any_root"],
		"a file under a KNOWN root must not be refused as unreachable")
	require.NotEmpty(t, e.windows(parent), "no windows were written for the parent-root file")

	// The job must carry the root it was actually split against.
	require.Equal(t, "books", sawRoot.Load(),
		"the job named the wrong root; the worker would resolve rel under the wrong mount")
}
