// file: internal/plugins/acoustid/worker_hub_test.go
// version: 1.1.0
// guid: 23143bc2-39af-48f3-a47c-8c1f392e2ab9
// last-edited: 2026-09-19

package acoustid

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
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

// testClock is a concurrency-safe settable clock for the hub.
type testClock struct{ off atomic.Int64 }

func (c *testClock) now() time.Time          { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *testClock) advance(d time.Duration) { c.off.Add(int64(d)) }

// hubEnv is a wbEnv whose plugin hub is attached to a hand-built tier, the
// way a live run attaches it, without running the server lane.
type hubEnv struct {
	*wbEnv
	hub   *WorkerHub
	clock *testClock
	tally *windowTally
	items []windowItem
}

func withRootDir(t *testing.T, dir string) {
	t.Helper()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = dir
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
}

func newHubEnv(t *testing.T, nFiles int) *hubEnv {
	t.Helper()
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	for i := range nFiles {
		e.addFile("", filepath.Join("book", string(rune('a'+i))+".m4b"), true, 3600)
	}
	return attachHub(t, e)
}

func attachHub(t *testing.T, e *wbEnv) *hubEnv {
	t.Helper()
	plan, err := e.plugin.planWindowBackfill(context.Background(), &wbReporter{}, &e.tools.Versions)
	require.NoError(t, err)
	items := append([]windowItem(nil), plan.tiers[windowTierPresent]...)
	for i := range items {
		items[i].qi = i
	}
	h := &hubEnv{wbEnv: e, hub: e.plugin.WorkerHub(), clock: &testClock{}, tally: &windowTally{}, items: items}
	h.hub.now = h.clock.now
	h.hub.attach(context.Background(), e.plugin, e.tools, h.tally, plan.calibration)
	t.Cleanup(h.hub.detach)
	h.hub.beginTier(windowTierPresent, items)
	return h
}

func (h *hubEnv) leaseReq(worker string, n int) workerapi.LeaseRequest {
	return workerapi.LeaseRequest{WorkerID: worker, MaxJobs: n, Pipeline: fingerprint.WindowPipelineID,
		FpcalcVersion: h.tools.Versions.Fpcalc, FFmpegVersion: h.tools.Versions.FFmpeg}
}

func (h *hubEnv) lease(worker string, n int) *workerapi.LeaseResponse {
	h.t.Helper()
	resp, err := h.hub.Lease(context.Background(), h.leaseReq(worker, n))
	require.NoError(h.t, err)
	return resp
}

// okResult is a valid result for job: frames frames per window, fill byte
// seed, decoded length equal to the window length.
func (h *hubEnv) okResult(job workerapi.Job, frames int, seed uint32) workerapi.JobResult {
	res := workerapi.JobResult{JobID: job.JobID, Ref: job.Ref, Outcome: workerapi.OutcomeOK,
		Pipeline: fingerprint.WindowPipelineID, FpcalcVersion: h.tools.Versions.Fpcalc, FFmpegVersion: h.tools.Versions.FFmpeg}
	for _, w := range job.Windows {
		raw := make([]byte, frames*4)
		for i := range frames {
			binary.LittleEndian.PutUint32(raw[i*4:], seed+uint32(i)+uint32(w.SlotBP))
		}
		res.Windows = append(res.Windows, workerapi.WindowResult{Window: w, DecodedSec: w.LengthSec, Frames: frames,
			Algorithm: fingerprint.WindowAlgorithm, RawB64: base64.StdEncoding.EncodeToString(raw)})
	}
	return res
}

func (h *hubEnv) post(worker string, rs ...workerapi.JobResult) []workerapi.JobStatus {
	h.t.Helper()
	resp, err := h.hub.Results(context.Background(), workerapi.ResultsRequest{WorkerID: worker, Results: rs})
	require.NoError(h.t, err)
	return resp.Statuses
}

func (h *hubEnv) fileID(job workerapi.Job) string { return job.Ref[2:] }

func (h *hubEnv) state(idx int) qState {
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	return h.hub.run.st[idx].state
}

func jobIdx(h *hubEnv, job workerapi.Job) int {
	for i, it := range h.items {
		if "f:"+it.FileID == job.Ref {
			return i
		}
	}
	h.t.Fatalf("job %s names no item", job.Ref)
	return -1
}

// ---- tests ----

func TestWorkerHub_NoRunIsUnavailable(t *testing.T) {
	h := newWorkerHub()
	_, err := h.Lease(context.Background(), workerapi.LeaseRequest{WorkerID: "w1"})
	require.ErrorIs(t, err, ErrNoWindowRun)
	_, err = h.Hello(context.Background())
	require.ErrorIs(t, err, ErrNoWindowRun)
	_, err = h.Renew("x", workerapi.RenewRequest{WorkerID: "w1"})
	require.ErrorIs(t, err, ErrNoWindowRun)
	_, err = h.Results(context.Background(), workerapi.ResultsRequest{WorkerID: "w1"})
	require.ErrorIs(t, err, ErrNoWindowRun)
	require.True(t, h.claimServer(0), "a detached hub never blocks the server lane")
}

func TestWorkerHub_LeaseCarriesRootRelativeJobs(t *testing.T) {
	h := newHubEnv(t, 3)
	resp := h.lease("w1", 0)
	require.NotNil(t, resp)
	require.Len(t, resp.Jobs, 3)
	for _, j := range resp.Jobs {
		require.Equal(t, "libroot", j.Root)
		rel, err := base64.StdEncoding.DecodeString(j.RelB64)
		require.NoError(t, err)
		require.False(t, filepath.IsAbs(string(rel)), "wire path must be root-relative")
		require.Equal(t, filepath.Join(h.lib, string(rel)), h.items[jobIdx(h, j)].Path)
		require.Len(t, j.Windows, 3, "a 1 h file gets slots 1000/5000/9000")
		fi, err := os.Stat(h.items[jobIdx(h, j)].Path)
		require.NoError(t, err)
		require.Equal(t, fi.Size(), j.Size)
		require.Equal(t, fi.ModTime().Unix(), j.MtimeUnix)
	}
	require.Nil(t, h.lease("w2", 0), "everything is leased: 204")
}

func TestWorkerHub_ToolsMustBeAllowlisted(t *testing.T) {
	h := newHubEnv(t, 1)
	req := h.leaseReq("w1", 1)
	req.Pipeline = "fpcalc-direct/v0"
	_, err := h.hub.Lease(context.Background(), req)
	require.ErrorIs(t, err, ErrToolsNotAllowed)

	req = h.leaseReq("w1", 1)
	req.FpcalcVersion = "1.6.1"
	_, err = h.hub.Lease(context.Background(), req)
	require.ErrorIs(t, err, ErrToolsNotAllowed)

	prev := config.AppConfig.FingerprintWorkerToolVersions
	t.Cleanup(func() { config.AppConfig.FingerprintWorkerToolVersions = prev })
	config.AppConfig.FingerprintWorkerToolVersions = []string{"1.6.1/" + h.tools.Versions.FFmpeg}
	h.hub.detach()
	h2 := attachHub(t, h.wbEnv)
	resp, err := h2.hub.Lease(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestWorkerHub_AtMostTwoLeasesPerWorker(t *testing.T) {
	h := newHubEnv(t, 5)
	require.NotNil(t, h.lease("w1", 1))
	require.NotNil(t, h.lease("w1", 1))
	_, err := h.hub.Lease(context.Background(), h.leaseReq("w1", 1))
	require.ErrorIs(t, err, ErrTooManyLeases)
	require.NotNil(t, h.lease("w2", 1), "the cap is per worker")
}

func TestWorkerHub_RenewExpiryReclaim(t *testing.T) {
	h := newHubEnv(t, 2)
	resp := h.lease("w1", 1)
	require.Len(t, resp.Jobs, 1)
	first := resp.Jobs[0]

	h.clock.advance(workerapi.RenewEvery)
	rn, err := h.hub.Renew(resp.LeaseID, workerapi.RenewRequest{WorkerID: "w1"})
	require.NoError(t, err)
	require.True(t, rn.ExpiresAt.After(resp.ExpiresAt), "renew extends the lease")

	require.Zero(t, h.hub.sweep(), "a live lease is not reclaimed")
	h.clock.advance(workerapi.LeaseTTL + time.Second)
	require.Equal(t, 1, h.hub.sweep())
	_, err = h.hub.Renew(resp.LeaseID, workerapi.RenewRequest{WorkerID: "w1"})
	require.ErrorIs(t, err, ErrLeaseGone, "renewing a reclaimed lease is 410")
	_, err = h.hub.Release(resp.LeaseID, workerapi.ReleaseRequest{WorkerID: "w1"})
	require.ErrorIs(t, err, ErrLeaseGone)
	require.Equal(t, qPending, h.state(jobIdx(h, first)))

	// Requeued at the head of the tier: the next lease gets it first.
	again := h.lease("w2", 1)
	require.Equal(t, first.Ref, again.Jobs[0].Ref)
}

func TestWorkerHub_ReleaseRequeues(t *testing.T) {
	h := newHubEnv(t, 3)
	resp := h.lease("w1", 3)
	rel, err := h.hub.Release(resp.LeaseID, workerapi.ReleaseRequest{WorkerID: "w1", JobIDs: []string{resp.Jobs[0].JobID}})
	require.NoError(t, err)
	require.Equal(t, 1, rel.Requeued)
	require.Equal(t, 2, h.hub.inFlight())
	rel, err = h.hub.Release(resp.LeaseID, workerapi.ReleaseRequest{WorkerID: "w1"})
	require.NoError(t, err)
	require.Equal(t, 2, rel.Requeued)
	require.Zero(t, h.hub.inFlight())
}

func TestWorkerHub_AcceptedDuplicateAndConflict(t *testing.T) {
	h := newHubEnv(t, 1)
	resp := h.lease("w1", 1)
	job := resp.Jobs[0]
	res := h.okResult(job, 160, 7)

	st := h.post("w1", res)
	require.Equal(t, workerapi.StatusAccepted, st[0].Status, st[0].Reason)
	ws := h.windows(h.fileID(job))
	require.Len(t, ws, 3)
	for _, w := range ws {
		require.Equal(t, "fp-worker:w1", w.Host)
		require.Equal(t, resp.LeaseID, w.LeaseID)
		require.Equal(t, fingerprint.WindowPipelineID, w.Pipeline)
		require.Equal(t, 160, w.Frames)
	}
	require.EqualValues(t, 1, h.tally.written.Load(), "remote writes count in the run's census")
	require.Equal(t, qDone, h.state(0))

	st = h.post("w1", res)
	require.Equal(t, workerapi.StatusDuplicate, st[0].Status, "a byte-identical repost is a duplicate")

	st = h.post("w1", h.okResult(job, 160, 99))
	require.Equal(t, workerapi.StatusRejected, st[0].Status)
	require.Equal(t, "conflicts_with_stored", st[0].Reason)
	require.Equal(t, ws, h.windows(h.fileID(job)), "a conflicting repost must not overwrite")
}

func TestWorkerHub_LateResultAcceptedWhenFileUnchanged(t *testing.T) {
	h := newHubEnv(t, 2)
	resp := h.lease("w1", 1)
	job := resp.Jobs[0]
	h.clock.advance(workerapi.LeaseTTL + time.Second)
	require.Equal(t, 1, h.hub.sweep())

	st := h.post("w1", h.okResult(job, 160, 1))
	require.Equal(t, workerapi.StatusAccepted, st[0].Status, st[0].Reason)
	require.Len(t, h.windows(h.fileID(job)), 3)
	require.Equal(t, qDone, h.state(jobIdx(h, job)))
}

func TestWorkerHub_LateResultAfterReleaseIsSuperseded(t *testing.T) {
	h := newHubEnv(t, 1)
	old := h.lease("w1", 1)
	h.clock.advance(workerapi.LeaseTTL + time.Second)
	h.hub.sweep()
	fresh := h.lease("w2", 1)
	require.Equal(t, old.Jobs[0].Ref, fresh.Jobs[0].Ref)

	st := h.post("w1", h.okResult(old.Jobs[0], 160, 1))
	require.Equal(t, workerapi.StatusStale, st[0].Status)
	require.Equal(t, "superseded", st[0].Reason)
	require.Empty(t, h.windows(h.fileID(old.Jobs[0])))

	st = h.post("w2", h.okResult(fresh.Jobs[0], 160, 1))
	require.Equal(t, workerapi.StatusAccepted, st[0].Status, st[0].Reason)
}

func TestWorkerHub_StaleWhenServerFileChanged(t *testing.T) {
	h := newHubEnv(t, 1)
	job := h.lease("w1", 1).Jobs[0]
	path := h.items[0].Path
	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, later, later))

	st := h.post("w1", h.okResult(job, 160, 1))
	require.Equal(t, workerapi.StatusStale, st[0].Status)
	require.Equal(t, "changed_on_server", st[0].Reason)
	require.Empty(t, h.windows(h.fileID(job)), "a print of bytes the server no longer has must not be stored")
	require.Equal(t, qPending, h.state(0), "re-planned: requeued")

	again := h.lease("w1", 1).Jobs[0]
	require.Equal(t, later.Unix(), again.MtimeUnix, "the next lease carries the new mtime")
}

func TestWorkerHub_ValidationRejectsBadPrints(t *testing.T) {
	cases := map[string]func(*workerapi.JobResult){
		"too_few_frames": func(r *workerapi.JobResult) {
			raw := make([]byte, (workerapi.MinFrames-1)*4)
			for i := range r.Windows {
				r.Windows[i].RawB64 = base64.StdEncoding.EncodeToString(raw)
				r.Windows[i].Frames = workerapi.MinFrames - 1
			}
		},
		"decoded_length_mismatch": func(r *workerapi.JobResult) { r.Windows[1].DecodedSec = r.Windows[1].LengthSec * 0.9 },
		"frame_count_mismatch":    func(r *workerapi.JobResult) { r.Windows[0].Frames++ },
		"raw_not_uint32_frames": func(r *workerapi.JobResult) {
			r.Windows[0].RawB64 = base64.StdEncoding.EncodeToString(make([]byte, 401))
		},
		"raw_not_base64":         func(r *workerapi.JobResult) { r.Windows[0].RawB64 = "!!" },
		"window_spec_mismatch":   func(r *workerapi.JobResult) { r.Windows[2].OffsetSec += 1 },
		"window_count_mismatch":  func(r *workerapi.JobResult) { r.Windows = r.Windows[:2] },
		"tool_versions_mismatch": func(r *workerapi.JobResult) { r.FFmpegVersion = "7.0" },
		"pipeline_mismatch":      func(r *workerapi.JobResult) { r.Pipeline = "other" },
		"algorithm_mismatch":     func(r *workerapi.JobResult) { r.Windows[0].Algorithm = 1 },
	}
	for want, mutate := range cases {
		t.Run(want, func(t *testing.T) {
			h := newHubEnv(t, 1)
			job := h.lease("w1", 1).Jobs[0]
			res := h.okResult(job, 160, 1)
			mutate(&res)
			st := h.post("w1", res)
			require.Equal(t, workerapi.StatusRejected, st[0].Status)
			require.Equal(t, want, st[0].Reason)
			require.Empty(t, h.windows(h.fileID(job)))
			require.Nil(t, h.lease("w1", 1), "a worker that sent a bad print does not get the file again")
			require.Len(t, h.hub.takeBacklog(), 1, "the server lane cuts it instead")
		})
	}
}

func TestWorkerHub_ResultMustEchoTheJobsRef(t *testing.T) {
	h := newHubEnv(t, 2)
	jobs := h.lease("w1", 2).Jobs
	res := h.okResult(jobs[0], 160, 1)
	res.Ref = jobs[1].Ref
	st := h.post("w1", res)
	require.Equal(t, workerapi.StatusRejected, st[0].Status)
	require.Equal(t, "ref_mismatch", st[0].Reason)
	st = h.post("w1", workerapi.JobResult{JobID: "nope", Ref: jobs[0].Ref, Outcome: workerapi.OutcomeOK})
	require.Equal(t, workerapi.StatusStale, st[0].Status)
	require.Equal(t, "unknown_job", st[0].Reason)
}

func TestWorkerHub_NotFoundNeverTombstones(t *testing.T) {
	h := newHubEnv(t, 2)
	jobs := h.lease("w1", 2).Jobs

	// Present on the server: the worker's mount is wrong; server lane only.
	st := h.post("w1", workerapi.JobResult{JobID: jobs[0].JobID, Ref: jobs[0].Ref, Outcome: workerapi.OutcomeNotFound})
	require.Equal(t, workerapi.StatusAccepted, st[0].Status)
	require.Equal(t, "server_lane", st[0].Reason)

	// Gone on the server too: resolved, still no tombstone.
	require.NoError(t, os.Remove(h.items[jobIdx(h, jobs[1])].Path))
	st = h.post("w1", workerapi.JobResult{JobID: jobs[1].JobID, Ref: jobs[1].Ref, Outcome: workerapi.OutcomeNotFound})
	require.Equal(t, "gone_on_server", st[0].Reason)
	require.Equal(t, qDone, h.state(jobIdx(h, jobs[1])))

	for _, j := range jobs {
		f, err := h.store.GetFingerprintWindowFailure(database.FingerprintWindowRef(j.Ref))
		require.NoError(t, err)
		require.Nil(t, f, "a worker's not_found must never become a tombstone")
		require.Empty(t, h.windows(h.fileID(j)))
	}
	require.Nil(t, h.lease("w2", 2), "the present file is not re-offered to workers")
	back := h.hub.takeBacklog()
	require.Len(t, back, 1)
	require.Equal(t, h.items[jobIdx(h, jobs[0])].FileID, back[0].FileID)
}

func TestWorkerHub_DecodeErrorGoesToServerLane(t *testing.T) {
	h := newHubEnv(t, 1)
	job := h.lease("w1", 1).Jobs[0]
	st := h.post("w1", workerapi.JobResult{JobID: job.JobID, Ref: job.Ref, Outcome: workerapi.OutcomeDecodeError, Error: "boom"})
	require.Equal(t, "server_lane", st[0].Reason)
	f, err := h.store.GetFingerprintWindowFailure(database.FingerprintWindowRef(job.Ref))
	require.NoError(t, err)
	require.Nil(t, f, "the server retries before any tombstone")
	require.Nil(t, h.lease("w1", 1))
	require.Len(t, h.hub.takeBacklog(), 1)
}

func TestWorkerHub_SecondTimeoutGoesToServerLane(t *testing.T) {
	h := newHubEnv(t, 1)
	job := h.lease("w1", 1).Jobs[0]
	st := h.post("w1", workerapi.JobResult{JobID: job.JobID, Ref: job.Ref, Outcome: workerapi.OutcomeTimeout})
	require.Equal(t, "requeued", st[0].Reason)
	job2 := h.lease("w1", 1).Jobs[0]
	require.Equal(t, job.Ref, job2.Ref, "the first timeout re-queues for workers")
	st = h.post("w1", workerapi.JobResult{JobID: job2.JobID, Ref: job2.Ref, Outcome: workerapi.OutcomeTimeout})
	require.Equal(t, "server_lane", st[0].Reason)
	require.Nil(t, h.lease("w1", 1))
	require.Len(t, h.hub.takeBacklog(), 1)
}

func TestWorkerHub_ServerAndLeaseNeverShareAFile(t *testing.T) {
	h := newHubEnv(t, 3)
	require.True(t, h.hub.claimServer(0))
	resp := h.lease("w1", 3)
	require.Len(t, resp.Jobs, 2, "a server-claimed file is never leased")
	for _, j := range resp.Jobs {
		require.False(t, h.hub.claimServer(jobIdx(h, j)), "a leased file is never claimed by the server")
	}
}

func TestWorkerHub_CheckpointNeverPassesAnOpenLease(t *testing.T) {
	h := newHubEnv(t, 4)
	require.True(t, h.hub.claimServer(0))
	resp := h.lease("w1", 1) // items[1]
	require.Equal(t, 1, jobIdx(h, resp.Jobs[0]))
	require.True(t, h.hub.claimServer(2))
	require.Equal(t, 1, h.hub.checkpointMark(3), "the watermark stops below the leased item")
	h.post("w1", h.okResult(resp.Jobs[0], 160, 1))
	require.Equal(t, 3, h.hub.checkpointMark(3))
}

func TestWorkerHub_HelloOffersRootsToolsAndCalibration(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	_, done := e.addFileWith("", "cal/one.m4b", fakeAudio(3600, 70<<10), 3600)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	e.addFile("", "todo/two.m4b", true, 3600)
	h := attachHub(t, e)

	hello, err := h.hub.Hello(context.Background())
	require.NoError(t, err)
	require.Equal(t, []workerapi.Root{{ID: "libroot", Remote: true}, {ID: "books", Remote: false}}, hello.Roots)
	require.Equal(t, fingerprint.WindowPipelineID, hello.Pipeline)
	require.Contains(t, hello.ToolVersions, workerapi.ToolVersions{Fpcalc: e.tools.Versions.Fpcalc, FFmpeg: e.tools.Versions.FFmpeg})
	require.Len(t, hello.Calibration, 1)
	cal := hello.Calibration[0]
	rel, _ := base64.StdEncoding.DecodeString(cal.RelB64)
	require.Equal(t, "cal/one.m4b", string(rel))
	require.NotEmpty(t, cal.Windows)
	require.Len(t, cal.Head64K, 64)
	require.Equal(t, len(e.windows(done)), len(cal.Windows))
}

// TestWorkerHub_ConcurrentLeaseResultsSweepAndServerLane runs every entry
// point at once under -race and then checks that each item ended done once
// and that no file was written by two owners.
func TestWorkerHub_ConcurrentLeaseResultsSweepAndServerLane(t *testing.T) {
	h := newHubEnv(t, 40)
	prevSweep := hubSweepEvery
	hubSweepEvery = time.Millisecond
	t.Cleanup(func() { hubSweepEvery = prevSweep })
	h.hub.detach()
	h = attachHub(t, h.wbEnv) // restart the sweeper at the short period

	var serverDone atomic.Int64
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Server lane: walks the list in order, like RunItems.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range h.items {
			if h.hub.claimServer(i) {
				serverDone.Add(1)
				h.hub.serverDone(i)
			}
		}
	}()
	// Clock pusher: expires some leases mid-flight.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			h.clock.advance(workerapi.LeaseTTL / 4)
			time.Sleep(time.Millisecond)
		}
	}()
	// Workers: lease, post some results, time some out, release the rest.
	for w := range 4 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := "w" + string(rune('0'+w))
			for ctx.Err() == nil {
				resp, err := h.hub.Lease(ctx, h.leaseReq(id, 3))
				if err != nil {
					time.Sleep(time.Millisecond)
					continue
				}
				if resp == nil {
					return
				}
				for k, j := range resp.Jobs {
					switch k % 3 {
					case 0:
						h.post(id, h.okResult(j, 160, 1))
					case 1:
						h.post(id, workerapi.JobResult{JobID: j.JobID, Ref: j.Ref, Outcome: workerapi.OutcomeTimeout})
					}
				}
				_, _ = h.hub.Renew(resp.LeaseID, workerapi.RenewRequest{WorkerID: id})
				_, _ = h.hub.Release(resp.LeaseID, workerapi.ReleaseRequest{WorkerID: id})
			}
		}(w)
	}
	wg.Wait()
	// Drain like the op: the server lane takes everything handed back.
	for {
		back := h.hub.takeBacklog()
		for _, it := range back {
			h.hub.serverDone(it.qi)
		}
		if len(back) == 0 && h.hub.inFlight() == 0 {
			break
		}
		h.clock.advance(workerapi.LeaseTTL * 2)
		h.hub.sweep()
	}
	for i := range h.items {
		require.Equal(t, qDone, h.state(i), "item %d", i)
	}
	require.Equal(t, len(h.items), h.hub.checkpointMark(len(h.items)))
}

// TestWindowBackfill_RemoteWorkerSharesTheQueue runs the real op with a
// remote worker leasing through the hub while the server lane runs, one lease
// abandoned mid-run so the drain has to pick it up.
func TestWindowBackfill_RemoteWorkerSharesTheQueue(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	var ids []string
	for i := range 12 {
		// "slow" makes the fake ffmpeg sleep, so the worker gets a share.
		_, id := e.addFile("", filepath.Join("slow", string(rune('a'+i))+".m4b"), true, 3600)
		ids = append(ids, id)
	}
	prevSweep, prevPoll := hubSweepEvery, windowDrainPoll
	hubSweepEvery, windowDrainPoll = 5*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { hubSweepEvery, windowDrainPoll = prevSweep, prevPoll })
	clock := &testClock{}
	hub := e.plugin.WorkerHub()
	hub.now = clock.now

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var remoteAccepted atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		h := &hubEnv{wbEnv: e, hub: hub, clock: clock}
		abandoned := false
		for ctx.Err() == nil {
			resp, err := hub.Lease(ctx, h.leaseReq("mac1", 2))
			if err != nil || resp == nil {
				time.Sleep(2 * time.Millisecond)
				if err == nil && remoteAccepted.Load() > 0 {
					return
				}
				continue
			}
			if !abandoned {
				// Walk away from the first lease; expire it.
				abandoned = true
				clock.advance(workerapi.LeaseTTL + time.Second)
				continue
			}
			var rs []workerapi.JobResult
			for _, j := range resp.Jobs {
				rs = append(rs, h.okResult(j, 160, 3))
			}
			st, err := hub.Results(ctx, workerapi.ResultsRequest{WorkerID: "mac1", Results: rs})
			if err == nil {
				for _, s := range st.Statuses {
					if s.Status == workerapi.StatusAccepted {
						remoteAccepted.Add(1)
					}
				}
			}
		}
	}()
	res, err := e.run(ctx, nil, WindowBackfillParams{Live: true, Concurrency: 1})
	cancel()
	wg.Wait()
	require.NoError(t, err)
	require.EqualValues(t, 12, res.written, "every file written once, by one lane or the other")
	remote := 0
	for _, id := range ids {
		ws := e.windows(id)
		require.Len(t, ws, 3, "file %s", id)
		if ws[0].Host == "fp-worker:mac1" {
			remote++
		}
	}
	require.Positive(t, remote, "the remote worker consumed part of the same queue")
	require.EqualValues(t, remote, remoteAccepted.Load())
	_, herr := hub.Hello(context.Background())
	require.ErrorIs(t, herr, ErrNoWindowRun, "the op detaches when it ends")
}

// TestWindowBackfill_AllowlistedWorkerVersionsAreCurrent: windows a worker
// wrote with an allowlisted tool pair are current, so the server lane never
// recomputes them; a pair that is not allowlisted still re-plans.
func TestWindowBackfill_AllowlistedWorkerVersionsAreCurrent(t *testing.T) {
	e := newWBEnv(t)
	_, id := e.addFile("", "a/x.m4b", true, 300)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	workerPair := e.tools.Versions.Fpcalc + "/" + e.tools.Versions.FFmpeg

	prev := config.AppConfig.FingerprintWorkerToolVersions
	t.Cleanup(func() { config.AppConfig.FingerprintWorkerToolVersions = prev })
	config.AppConfig.FingerprintWorkerToolVersions = []string{workerPair}
	e.tools.Versions = fingerprint.ToolVersionInfo{Fpcalc: "1.6.0-server", FFmpeg: "8.0.1-server"}
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Zero(t, res.written, "allowlisted windows must count as current")
	require.Equal(t, 1, res.plan.current[windowTierPresent])

	config.AppConfig.FingerprintWorkerToolVersions = nil
	res, err = e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, res.written, "without the allowlist the old pair is stale")
	require.Equal(t, "8.0.1-server", e.windows(id)[0].FFmpegVersion)
}

// ---- review round 1 ----

type scanSwitch struct{ running atomic.Bool }

func (f *scanSwitch) LibraryScanRunning() bool { return f.running.Load() }

// TestWorkerHub_NoLeasesDuringLibraryScan: remote leases honour the same
// library.scan gate as the server lane (waitForLibraryScan): 204 while a scan
// runs, leases again once it ends.
func TestWorkerHub_NoLeasesDuringLibraryScan(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	e.addFile("", "book/a.m4b", true, 3600)
	probe := &scanSwitch{}
	probe.running.Store(true)
	e.plugin.setScanProbe(probe)
	h := attachHub(t, e)
	require.Nil(t, h.lease("w1", 1), "no lease while library.scan runs")
	require.Zero(t, h.hub.inFlight())
	probe.running.Store(false)
	require.NotNil(t, h.lease("w1", 1))
}

// TestWorkerHub_SlowStatPastTTLDoesNotStrandItems: Lease stats outside the
// lock; if the lease expires and is swept meanwhile, its files must go back
// to the queue, not stay qLeased under a lease nobody holds.
func TestWorkerHub_SlowStatPastTTLDoesNotStrandItems(t *testing.T) {
	h := newHubEnv(t, 2)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	prev := hubStat
	hubStat = func(p string) (os.FileInfo, error) {
		once.Do(func() { close(entered); <-release })
		return os.Stat(p)
	}
	t.Cleanup(func() { hubStat = prev })

	got := make(chan *workerapi.LeaseResponse, 1)
	go func() {
		resp, _ := h.hub.Lease(context.Background(), h.leaseReq("w1", 2))
		got <- resp
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Lease never statted through hubStat")
	}
	h.clock.advance(workerapi.LeaseTTL + time.Second)
	h.hub.sweep()
	close(release)
	resp := <-got
	require.Nil(t, resp, "a lease swept while it was being built must not be handed out")
	require.Zero(t, h.hub.inFlight(), "no file may stay leased to a swept lease")
	for i := range h.items {
		require.Equal(t, qPending, h.state(i))
	}
	require.NotNil(t, h.lease("w2", 2), "the files are leaseable again")
}

// TestWorkerHub_CalibrationUsesOnlyServerMadeWindows: hello never offers a
// worker's print as the reference a worker must reproduce, and each offered
// window names the server tool pair that cut it.
func TestWorkerHub_CalibrationUsesOnlyServerMadeWindows(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	_, srv := e.addFileWith("", "cal/server.m4b", fakeAudio(3600, 70<<10), 3600)
	_, wrk := e.addFileWith("", "cal/worker.m4b", fakeAudio(3600, 70<<10), 3600)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	// Rewrite one file's windows as a worker would have written them.
	ws := e.windows(wrk)
	for i := range ws {
		ws[i].Host = "fp-worker:mac1"
	}
	require.NoError(t, e.store.ReplaceFingerprintWindows(database.FileWindowRef(wrk), ws))
	h := attachHub(t, e)

	hello, err := h.hub.Hello(context.Background())
	require.NoError(t, err)
	require.Len(t, hello.Calibration, 1)
	rel, _ := base64.StdEncoding.DecodeString(hello.Calibration[0].RelB64)
	require.Equal(t, "cal/server.m4b", string(rel))
	for _, w := range hello.Calibration[0].Windows {
		require.Equal(t, e.tools.Versions.Fpcalc, w.Fpcalc)
		require.Equal(t, e.tools.Versions.FFmpeg, w.FFmpeg)
	}
	_ = srv
}

func TestWorkerHub_OnlyTheLeaseholderMayRenewReleaseOrPost(t *testing.T) {
	h := newHubEnv(t, 1)
	resp := h.lease("w1", 1)
	_, err := h.hub.Renew(resp.LeaseID, workerapi.RenewRequest{WorkerID: "w2"})
	require.ErrorIs(t, err, ErrLeaseGone)
	_, err = h.hub.Release(resp.LeaseID, workerapi.ReleaseRequest{WorkerID: "w2"})
	require.ErrorIs(t, err, ErrLeaseGone)
	st := h.post("w2", h.okResult(resp.Jobs[0], 160, 1))
	require.Equal(t, workerapi.StatusRejected, st[0].Status)
	require.Equal(t, "not_your_job", st[0].Reason)
	require.Empty(t, h.windows(h.fileID(resp.Jobs[0])))
	st = h.post("w1", h.okResult(resp.Jobs[0], 160, 1))
	require.Equal(t, workerapi.StatusAccepted, st[0].Status, st[0].Reason)
	require.Equal(t, "fp-worker:w1", h.windows(h.fileID(resp.Jobs[0]))[0].Host)
}

// TestWorkerHub_ServerLaneJobRejectsALaterWorkerOK: once a file was routed to
// the server lane (decode error here), a worker's later ok for it is refused
// even before the server lane claims it.
func TestWorkerHub_ServerLaneJobRejectsALaterWorkerOK(t *testing.T) {
	h := newHubEnv(t, 1)
	job := h.lease("w1", 1).Jobs[0]
	h.post("w1", workerapi.JobResult{JobID: job.JobID, Ref: job.Ref, Outcome: workerapi.OutcomeDecodeError})
	st := h.post("w1", h.okResult(job, 160, 1))
	require.Equal(t, workerapi.StatusRejected, st[0].Status)
	require.Equal(t, "server_lane", st[0].Reason)
	require.Empty(t, h.windows(h.fileID(job)))
	require.Len(t, h.hub.takeBacklog(), 1)
}

// TestWorkerHub_DoneJobsArePrunedButRepostsStayDuplicates: the job table does
// not grow with finished work, and a byte-identical repost after pruning is
// still recognized as a duplicate from the stored rows.
func TestWorkerHub_DoneJobsArePrunedButRepostsStayDuplicates(t *testing.T) {
	h := newHubEnv(t, 1)
	job := h.lease("w1", 1).Jobs[0]
	res := h.okResult(job, 160, 5)
	require.Equal(t, workerapi.StatusAccepted, h.post("w1", res)[0].Status)
	h.hub.mu.Lock()
	n := len(h.hub.run.jobs)
	h.hub.mu.Unlock()
	require.Zero(t, n, "a finished job is pruned")
	require.Equal(t, workerapi.StatusDuplicate, h.post("w1", res)[0].Status)
	other := h.post("w1", h.okResult(job, 160, 6))[0]
	require.NotEqual(t, workerapi.StatusAccepted, other.Status)
	require.NotEqual(t, workerapi.StatusDuplicate, other.Status)
}

// TestWindowBackfill_CheckpointAdvancesPastServerRequeuedItem: a file a worker
// hands to the server lane (decode error) is cut during the tier's pass, so
// the resume checkpoint moves past it before the tier ends instead of being
// pinned below it until the final drain.
func TestWindowBackfill_CheckpointAdvancesPastServerRequeuedItem(t *testing.T) {
	e := newWBEnv(t)
	withRootDir(t, e.lib)
	for i := range 8 {
		e.addFile("", filepath.Join("slow", string(rune('a'+i))+".m4b"), true, 3600)
	}
	prevEvery := windowCheckpointEvery
	windowCheckpointEvery = 1
	t.Cleanup(func() { windowCheckpointEvery = prevEvery })
	hub := e.plugin.WorkerHub()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	handed := make(chan string, 1)
	go func() {
		for ctx.Err() == nil {
			resp, err := hub.Lease(ctx, workerapi.LeaseRequest{WorkerID: "mac1", MaxJobs: 1, Pipeline: fingerprint.WindowPipelineID,
				FpcalcVersion: e.tools.Versions.Fpcalc, FFmpegVersion: e.tools.Versions.FFmpeg})
			if err != nil || resp == nil {
				time.Sleep(time.Millisecond)
				continue
			}
			j := resp.Jobs[0]
			// Hold the job until the server lane has walked two files past
			// it, then hand it back: the server's in-order pass is behind it.
			rel, _ := base64.StdEncoding.DecodeString(j.RelB64)
			k := int(rel[len("slow/")] - 'a')
			later := filepath.Join(e.lib, "slow", string(rune('a'+k+2))+".m4b")
			for ctx.Err() == nil && !slices.Contains(e.invoked(), later) {
				time.Sleep(5 * time.Millisecond)
			}
			_, _ = hub.Results(ctx, workerapi.ResultsRequest{WorkerID: "mac1", Results: []workerapi.JobResult{
				{JobID: j.JobID, Ref: j.Ref, Outcome: workerapi.OutcomeDecodeError}}})
			handed <- j.Ref[2:]
			return
		}
	}()
	rep := &wbReporter{}
	_, err := e.run(ctx, rep, WindowBackfillParams{Live: true, Concurrency: 1})
	require.NoError(t, err)
	fileID := <-handed
	rep.mu.Lock()
	defer rep.mu.Unlock()
	passed := false
	for _, cp := range rep.checkpoints {
		if cp.Resume != nil && cp.Resume.Tier == windowTierPresent && cp.Resume.AfterFileID >= fileID {
			passed = true
		}
	}
	require.True(t, passed, "no in-tier checkpoint ever passed the handed-back file %s: %+v", fileID, rep.checkpoints)
}
