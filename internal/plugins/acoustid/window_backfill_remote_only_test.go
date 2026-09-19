// file: internal/plugins/acoustid/window_backfill_remote_only_test.go
// version: 1.0.0
// guid: 609372fb-2803-46d8-a413-c4263e5f71be
// last-edited: 2026-09-19

package acoustid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
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

// addOutside adds a present file outside RootDir (so not under libroot).
func (e *wbEnv) addOutside(name string) string {
	e.t.Helper()
	dir := e.t.TempDir()
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

func refLeaseReq(worker string, n int) workerapi.LeaseRequest {
	return workerapi.LeaseRequest{WorkerID: worker, MaxJobs: n, Pipeline: fingerprint.WindowPipelineID,
		FpcalcVersion: refPair.Fpcalc, FFmpegVersion: refPair.FFmpeg}
}

// fakeRefWorker leases with refPair until ctx ends and answers every job
// with decide(job).
func fakeRefWorker(ctx context.Context, hub *WorkerHub, decide func(workerapi.Job) workerapi.JobResult) {
	for ctx.Err() == nil {
		resp, err := hub.Lease(ctx, refLeaseReq("mac1", 4))
		if err != nil || resp == nil {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		var rs []workerapi.JobResult
		for _, j := range resp.Jobs {
			rs = append(rs, decide(j))
		}
		_, _ = hub.Results(ctx, workerapi.ResultsRequest{WorkerID: "mac1", Results: rs})
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
		hubMode{remoteOnly: true, identity: plan.identity})
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
	require.Equal(t, map[string]int{"unknown_duration": 1, "not_under_libroot": 1}, res.deferred)
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
	require.EqualValues(t, 2, res.deferred["unknown_duration"]+res.deferred["not_under_libroot"])
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
	// A normal run cuts the file with the server's own tools.
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
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
	hello, err := h.hub.Hello(context.Background())
	require.NoError(t, err)
	require.NotContains(t, hello.ToolVersions, workerapi.ToolVersions{Fpcalc: e.tools.Versions.Fpcalc, FFmpeg: e.tools.Versions.FFmpeg})
	_, err = h.hub.Lease(context.Background(), h.leaseReq("srv", 1))
	require.ErrorIs(t, err, ErrToolsNotAllowed, "the server's own pair may not lease in remote-only")
	req := refLeaseReq("allowed", 1)
	req.FpcalcVersion, req.FFmpegVersion = allow.Fpcalc, allow.FFmpeg
	_, err = h.hub.Lease(context.Background(), req)
	require.ErrorIs(t, err, ErrToolsNotAllowed, "no reference windows yet: an allowlisted pair has nothing to be parity-checked against")
	resp, err := h.hub.Lease(context.Background(), refLeaseReq("ref", 1))
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
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true}) // server-pair windows
	require.NoError(t, err)
	e.addFileWith("", "lib/a.m4b", fakeAudio(3600, 70<<10), 3600)
	e.addFileWith("", "lib/b.m4b", fakeAudio(3600, 70<<10), 3600)
	noLocalTools(e)
	h := attachRemoteOnlyHub(t, e)

	hello, err := h.hub.Hello(context.Background())
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
	resp, err := h.hub.Lease(context.Background(), refLeaseReq("mac1", 1))
	require.NoError(t, err)
	require.NotNil(t, resp)
	st := h.post("mac1", refResult(h, resp.Jobs[0], 3))
	require.Equal(t, workerapi.StatusAccepted, st[0].Status, st[0].Reason)

	hello, err = h.hub.Hello(context.Background())
	require.NoError(t, err)
	require.False(t, hello.Bootstrap, "reference windows exist now: the byte-parity gate applies")
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
	_, err = h.hub.Lease(context.Background(), req)
	require.NoError(t, err, "with reference windows offered, an allowlisted pair may lease after its parity gate")
}
