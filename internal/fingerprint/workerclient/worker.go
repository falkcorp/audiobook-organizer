// file: internal/fingerprint/workerclient/worker.go
// version: 1.1.0
// guid: 201f885e-8d5c-40a3-8022-a6a19631e9d9
// last-edited: 2026-09-19

package workerclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

var log = logger.New("fp-worker")

// notFoundRecheckAfter: this many jobs in a row that fail on the mount
// (not_found, or an I/O error such as EIO/ESTALE/ETIMEDOUT) re-run the mount
// checks (design (d2) "Re-checks"): a vanished or flapping mount looks like a
// run of missing or unreadable files.
const notFoundRecheckAfter = 20

// resultBatchMax caps the results in one POST (the server refuses more than
// 4 x MaxJobsPerLease).
const resultBatchMax = 100

// bodyMargin is kept free under the server's body cap for the envelope.
const bodyMargin = 256 << 10

// maxPostAttempts is how many times one results batch is tried.
const maxPostAttempts = 5

// errDrained: a drain arrived during the startup gate; Run returns nil.
var errDrained = errors.New("fp-worker: drained")

// maxErrorLen bounds a result's error text.
const maxErrorLen = 512

// worker is one Run.
type worker struct {
	cfg   Config
	api   *apiClient
	roots map[string]string // root name -> resolved local mount
	calib []workerapi.CalibrationFile

	maxBatchBytes int
	renewEvery    time.Duration
	maxLeases     int

	sem    chan struct{}
	queued atomic.Int64 // leased jobs not started yet
	wake   chan struct{}
	// badStreak counts consecutive jobs that failed on the mount itself
	// (not_found, or an I/O error); see notFoundRecheckAfter.
	badStreak atomic.Int64
	recheckC  chan struct{}

	stopOnce sync.Once
	stop     chan struct{} // closed when draining begins
	fatalMu  sync.Mutex
	fatal    error

	tally struct {
		sync.Mutex
		statuses map[string]int
		outcomes map[string]int
	}
}

// Run starts the worker and blocks until it has drained. ctx is the hard
// stop: cancelling it abandons in-flight cuts (their leases expire on the
// server and the jobs are re-queued there). Closing drain is the graceful
// stop (SIGTERM/SIGINT): no new lease or job is started, jobs already being
// cut finish, their results are posted, every job not started is released
// back to the server, and Run returns nil.
//
// Run returns an error, and the process should exit non-zero, when the
// startup gate fails (writable or wrong mount, tool pair not allowlisted,
// parity mismatch), on a 409/401/403/404 from the server, or when a periodic
// mount re-check fails mid-run.
func Run(ctx context.Context, drain <-chan struct{}, cfg Config) error {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return err
	}
	base, err := checkServerURL(cfg.ServerURL)
	if err != nil {
		return err
	}
	roots, err := resolveRoots(cfg.Roots)
	if err != nil {
		return err
	}
	w := &worker{
		cfg:      cfg,
		api:      &apiClient{base: base, key: cfg.APIKey, hc: cfg.HTTPClient},
		roots:    roots,
		sem:      make(chan struct{}, cfg.Concurrency),
		wake:     make(chan struct{}, 1),
		recheckC: make(chan struct{}, 1),
		stop:     make(chan struct{}),
	}
	w.tally.statuses = map[string]int{}
	w.tally.outcomes = map[string]int{}
	defer w.beginStop() // ends the goroutine below on every return path
	go func() {
		select {
		case <-drain:
			log.Info("drain requested: finishing in-flight jobs, then releasing the rest")
		case <-ctx.Done():
		case <-w.stop:
		}
		w.beginStop()
	}()

	log.Info("fp-worker %s starting: server %s, fpcalc %s, ffmpeg %s, concurrency %d",
		cfg.WorkerID, base.Redacted(), cfg.Versions.Fpcalc, cfg.Versions.FFmpeg, cfg.Concurrency)
	for name, r := range roots {
		if err := w.checkMount(name, r); err != nil {
			return err
		}
		log.Info("root %s -> %s: read-only mount verified", name, r)
	}

	hello, err := w.helloWithRetry(ctx)
	if err != nil {
		return err
	}
	if hello == nil {
		return nil // drained before the server had a run to serve
	}
	if err := w.checkHello(hello); err != nil {
		return err
	}
	w.maxBatchBytes = workerapi.MaxBodyBytes - bodyMargin
	if hello.Limits.MaxBodyBytes > 2*bodyMargin && hello.Limits.MaxBodyBytes-bodyMargin < w.maxBatchBytes {
		w.maxBatchBytes = hello.Limits.MaxBodyBytes - bodyMargin
	}
	w.renewEvery = workerapi.RenewEvery
	if hello.Limits.RenewEverySec > 0 {
		w.renewEvery = time.Duration(hello.Limits.RenewEverySec) * time.Second
	}
	if cfg.renewEvery > 0 {
		w.renewEvery = cfg.renewEvery
	}
	w.maxLeases = workerapi.MaxLeasesPerWorker
	if hello.Limits.MaxLeasesPerWorker > 0 {
		w.maxLeases = hello.Limits.MaxLeasesPerWorker
	}

	cfs, err := w.calibrationTargets(hello)
	if err != nil {
		return err
	}
	paths, err := w.checkCalibrationFiles(cfs)
	if err != nil {
		return err
	}
	w.calib = cfs
	if err := w.parity(ctx, cfs, paths, hello.WindowSet); err != nil {
		if errors.Is(err, errDrained) {
			log.Info("drained during the parity gate; nothing was leased")
			return nil
		}
		return err
	}
	nWin := 0
	for _, cf := range cfs {
		nWin += len(cf.Windows)
	}
	log.Info("parity gate passed: %d calibration windows over %d files reproduced byte for byte", nWin, len(cfs))

	go w.recheckLoop(ctx)
	w.loop(ctx)
	w.logTally()
	return w.fatalErr()
}

func (w *worker) beginStop() { w.stopOnce.Do(func() { close(w.stop) }) }

func (w *worker) stopping() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

// fail records the first fatal error and starts draining.
func (w *worker) fail(err error) {
	w.fatalMu.Lock()
	if w.fatal == nil {
		w.fatal = err
		log.Error("stopping: %v", err)
	}
	w.fatalMu.Unlock()
	w.beginStop()
}

func (w *worker) fatalErr() error {
	w.fatalMu.Lock()
	defer w.fatalMu.Unlock()
	return w.fatal
}

func (w *worker) signalWake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// backoff is exponential with jitter: attempt n waits a random duration in
// [d/2, d) where d = min * 2^n, capped at max.
func (w *worker) backoff(n int) time.Duration {
	d := w.cfg.backoffMin
	for i := 0; i < n && d < w.cfg.backoffMax; i++ {
		d *= 2
	}
	d = min(d, w.cfg.backoffMax)
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half)
}

// sleep waits d or until draining starts; false means stop.
func (w *worker) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-w.stop:
		return false
	case <-ctx.Done():
		return false
	}
}

// helloWithRetry waits for a live window-backfill (503) with backoff; nil
// hello with nil error means a drain arrived first.
func (w *worker) helloWithRetry(ctx context.Context) (*workerapi.HelloResponse, error) {
	for attempt := 0; ; attempt++ {
		h, st, err := w.api.hello(ctx)
		if err == nil && h != nil {
			return h, nil
		}
		if fatal := fatalStatus(st, err); fatal != nil {
			return nil, fatal
		}
		d := w.backoff(attempt)
		log.Info("hello: %s; retrying in %s", describeWait(st, err), d.Round(time.Second))
		if !w.sleep(ctx, d) {
			return nil, ctx.Err()
		}
	}
}

// fatalStatus maps the statuses that must end the worker.
func fatalStatus(st int, err error) error {
	switch st {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (%v)", ErrAuth, err)
	case http.StatusNotFound:
		return ErrAPIUnavailable
	case http.StatusConflict:
		return fmt.Errorf("%w (%v)", ErrToolsNotAllowed, err)
	case http.StatusBadRequest:
		return fmt.Errorf("fp-worker: the server refused the request: %v", err)
	}
	return nil
}

func describeWait(st int, err error) string {
	switch st {
	case http.StatusNoContent:
		return "nothing to lease (queue empty or a library scan is running)"
	case http.StatusServiceUnavailable:
		return "no acoustid.window-backfill is running on the server"
	case http.StatusTooManyRequests:
		return "server says too many leases or requests"
	}
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("HTTP %d", st)
}

// recheckLoop re-runs the mount checks every recheckEvery and when a run of
// not_found jobs asks for it; a failure drains the worker and makes Run fail.
func (w *worker) recheckLoop(ctx context.Context) {
	t := time.NewTicker(w.cfg.recheckEvery)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.recheckC:
		}
		if err := w.recheck(); err != nil {
			w.fail(fmt.Errorf("mount re-check failed: %w", err))
			return
		}
		log.Debug("mount re-check passed")
	}
}

// loop leases while there is capacity: at most maxLeases open leases, and a
// new one only once fewer than Concurrency leased jobs are still waiting to
// start, so the worker keeps a short backlog without hoarding jobs other
// workers (and the server lane) could run.
func (w *worker) loop(ctx context.Context) {
	var wg sync.WaitGroup
	leaseDone := make(chan struct{}, w.maxLeases)
	held := 0
	attempt := 0
	defer wg.Wait()
	for {
		for held >= w.maxLeases || w.queued.Load() >= int64(w.cfg.Concurrency) {
			select {
			case <-w.stop:
				return
			case <-ctx.Done():
				return
			case <-leaseDone:
				held--
			case <-w.wake:
			}
		}
		if w.stopping() {
			return
		}
		resp, st, err := w.api.lease(ctx, workerapi.LeaseRequest{
			WorkerID: w.cfg.WorkerID, MaxJobs: w.cfg.MaxJobs,
			FpcalcVersion: w.cfg.Versions.Fpcalc, FFmpegVersion: w.cfg.Versions.FFmpeg,
			Pipeline: fingerprint.WindowPipelineID,
		})
		if err == nil && resp != nil && len(resp.Jobs) > 0 {
			attempt = 0
			held++
			w.queued.Add(int64(len(resp.Jobs)))
			log.Info("lease %s: %d jobs, expires %s", resp.LeaseID, len(resp.Jobs), resp.ExpiresAt.Format(time.RFC3339))
			wg.Add(1)
			go func() {
				defer wg.Done()
				w.runLease(ctx, resp)
				leaseDone <- struct{}{}
			}()
			continue
		}
		if fatal := fatalStatus(st, err); fatal != nil {
			w.fail(fatal)
			return
		}
		d := w.backoff(attempt)
		attempt++
		log.Debug("lease: %s; retrying in %s", describeWait(st, err), d.Round(time.Millisecond))
		// Sleep, but count leases that finish meanwhile.
		t := time.NewTimer(d)
	wait:
		for {
			select {
			case <-t.C:
				break wait
			case <-leaseDone:
				held--
			case <-w.stop:
				t.Stop()
				return
			case <-ctx.Done():
				t.Stop()
				return
			}
		}
	}
}

// leaseState is one open lease.
type leaseState struct {
	id   string
	gone atomic.Bool // the server answered 410: expired or reclaimed
}

// runLease cuts one lease's jobs through the shared semaphore, renews the
// lease while it works, posts results in batches, and on a drain releases
// every job it did not start.
func (w *worker) runLease(ctx context.Context, resp *workerapi.LeaseResponse) {
	ls := &leaseState{id: resp.LeaseID}
	renewCtx, stopRenew := context.WithCancel(ctx)
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		w.renewLoop(renewCtx, ls)
	}()

	results := make(chan workerapi.JobResult, len(resp.Jobs))
	postDone := make(chan struct{})
	go func() {
		defer close(postDone)
		w.batchAndPost(ctx, ls, results)
	}()

	var jobs sync.WaitGroup
	var unstarted []string
	for i, job := range resp.Jobs {
		acquired := false
		select {
		case w.sem <- struct{}{}:
			acquired = true
		case <-w.stop:
		case <-ctx.Done():
		}
		if acquired && (w.stopping() || ls.gone.Load() || ctx.Err() != nil) {
			<-w.sem
			acquired = false
		}
		if !acquired {
			for _, j := range resp.Jobs[i:] {
				unstarted = append(unstarted, j.JobID)
			}
			break
		}
		w.queued.Add(-1)
		w.signalWake()
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			defer func() { <-w.sem }()
			res := w.process(ctx, job)
			w.countOutcome(res.Outcome)
			results <- res
		}()
	}
	if len(unstarted) > 0 {
		w.queued.Add(-int64(len(unstarted)))
		w.signalWake()
	}
	jobs.Wait()
	close(results)
	<-postDone
	stopRenew()
	<-renewDone

	if len(unstarted) == 0 {
		return
	}
	if ls.gone.Load() {
		log.Info("lease %s: %d unstarted jobs were already reclaimed by the server", ls.id, len(unstarted))
		return
	}
	// The release must go out even while draining; only a hard stop skips it
	// (the lease then expires and the server re-queues the jobs itself).
	if ctx.Err() != nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rr, _, err := w.api.release(rctx, ls.id, workerapi.ReleaseRequest{WorkerID: w.cfg.WorkerID, JobIDs: unstarted})
	if err != nil {
		log.Warn("lease %s: release of %d unstarted jobs failed (they are re-queued when the lease expires): %v", ls.id, len(unstarted), err)
		return
	}
	log.Info("lease %s: released %d unstarted jobs (%d re-queued)", ls.id, len(unstarted), rr.Requeued)
}

// renewLoop renews the lease every renewEvery until ctx ends. A 410 marks the
// lease gone: jobs not yet started are left alone (the server re-queued
// them), while jobs in flight still finish and post, since the server takes
// a late result whose file still matches.
func (w *worker) renewLoop(ctx context.Context, ls *leaseState) {
	t := time.NewTicker(w.renewEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, err := w.api.renew(ctx, ls.id, workerapi.RenewRequest{WorkerID: w.cfg.WorkerID})
		switch {
		case err == nil:
			log.Debug("lease %s renewed", ls.id)
		case st == http.StatusGone:
			log.Warn("lease %s expired or was reclaimed; finishing jobs in flight, starting no more from it", ls.id)
			ls.gone.Store(true)
			return
		case ctx.Err() != nil:
			return
		default:
			if fatal := fatalStatus(st, err); fatal != nil {
				w.fail(fatal)
				return
			}
			log.Warn("lease %s: renew failed, retrying next tick: %v", ls.id, err)
		}
	}
}

// batchAndPost collects results and posts them in batches bounded by body
// size and count, and at least every flushEvery.
func (w *worker) batchAndPost(ctx context.Context, ls *leaseState, results <-chan workerapi.JobResult) {
	var batch []workerapi.JobResult
	size := 0
	t := time.NewTicker(w.cfg.flushEvery)
	defer t.Stop()
	flush := func() {
		if len(batch) > 0 {
			w.post(ctx, ls, batch)
			batch, size = nil, 0
		}
	}
	for {
		select {
		case res, ok := <-results:
			if !ok {
				flush()
				return
			}
			n := encodedSize(res)
			if n > w.maxBatchBytes {
				// Cannot be posted at all; tell the server so the file goes
				// to its own lane instead of looping through workers.
				res = workerapi.JobResult{JobID: res.JobID, Ref: res.Ref, Outcome: workerapi.OutcomeDecodeError,
					Error:    fmt.Sprintf("result is %d bytes, over the %d-byte request cap", n, w.maxBatchBytes),
					Pipeline: res.Pipeline, FpcalcVersion: res.FpcalcVersion, FFmpegVersion: res.FFmpegVersion}
				n = encodedSize(res)
			}
			if len(batch) > 0 && (size+n > w.maxBatchBytes || len(batch) >= resultBatchMax) {
				flush()
			}
			batch = append(batch, res)
			size += n
		case <-t.C:
			flush()
		}
	}
}

func encodedSize(r workerapi.JobResult) int {
	b, err := json.Marshal(r)
	if err != nil {
		return 0
	}
	return len(b) + 1
}

// post sends one batch, retrying transport failures and 429/5xx a few times.
// Posting continues while draining (that is the point of a drain); only the
// hard stop cuts it short.
func (w *worker) post(ctx context.Context, ls *leaseState, batch []workerapi.JobResult) {
	req := workerapi.ResultsRequest{WorkerID: w.cfg.WorkerID, LeaseID: ls.id, Results: batch}
	for attempt := 0; ; attempt++ {
		resp, st, err := w.api.results(ctx, req)
		if err == nil {
			w.countStatuses(ls.id, resp.Statuses)
			return
		}
		if ctx.Err() != nil {
			log.Warn("lease %s: hard stop; %d results not posted", ls.id, len(batch))
			return
		}
		if fatal := fatalStatus(st, err); fatal != nil {
			if st == http.StatusBadRequest {
				log.Error("lease %s: server refused %d results: %v", ls.id, len(batch), err)
				return
			}
			w.fail(fatal)
			return
		}
		if st == http.StatusRequestEntityTooLarge {
			log.Error("lease %s: %d results over the server's body cap: %v", ls.id, len(batch), err)
			return
		}
		if st == http.StatusServiceUnavailable {
			log.Warn("lease %s: the window backfill ended; %d results dropped (the server re-plans them)", ls.id, len(batch))
			return
		}
		if attempt == maxPostAttempts-1 {
			log.Error("lease %s: gave up posting %d results after %d attempts (%s); the lease will expire and the server re-queues them",
				ls.id, len(batch), maxPostAttempts, describeWait(st, err))
			return
		}
		d := w.backoff(attempt)
		log.Warn("lease %s: posting %d results failed (%s); retrying in %s", ls.id, len(batch), describeWait(st, err), d.Round(time.Millisecond))
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}

func (w *worker) countStatuses(leaseID string, sts []workerapi.JobStatus) {
	w.tally.Lock()
	defer w.tally.Unlock()
	for _, s := range sts {
		w.tally.statuses[s.Status]++
		if s.Status == workerapi.StatusRejected {
			log.Warn("lease %s job %s rejected by the server: %s", leaseID, s.JobID, s.Reason)
		}
	}
}

func (w *worker) countOutcome(o string) {
	w.tally.Lock()
	w.tally.outcomes[o]++
	w.tally.Unlock()
}

func (w *worker) logTally() {
	w.tally.Lock()
	defer w.tally.Unlock()
	log.Info("fp-worker done: outcomes %s; server statuses %s", fmtCounts(w.tally.outcomes), fmtCounts(w.tally.statuses))
}

func fmtCounts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

// process runs one job and always returns a result for it. Outcomes (design
// (d), (d2)):
//
//   - rejected: the root is not configured, rel_b64 is invalid, or the path
//     fails validation or escapes the mount. Nothing is opened.
//   - not_found: neither the exact name nor its NFC/NFD alternate exists.
//   - stale: the file's size or mtime here differ from the job's.
//   - timeout: a window hit the per-window timeout, or a transient I/O
//     failure repeated.
//   - decode_error: ffmpeg or fpcalc failed, or the print was too short.
func (w *worker) process(ctx context.Context, job workerapi.Job) workerapi.JobResult {
	res := workerapi.JobResult{JobID: job.JobID, Ref: job.Ref, Pipeline: fingerprint.WindowPipelineID,
		FpcalcVersion: w.cfg.Versions.Fpcalc, FFmpegVersion: w.cfg.Versions.FFmpeg}
	fail := func(outcome, msg string) workerapi.JobResult {
		res.Outcome, res.Error, res.Windows = outcome, w.scrub(msg), nil
		return res
	}
	mount, ok := w.roots[job.Root]
	if !ok {
		return fail(workerapi.OutcomeRejected, "path: root "+job.Root+" is not configured on this worker")
	}
	relB, err := base64.StdEncoding.DecodeString(job.RelB64)
	if err != nil {
		return fail(workerapi.OutcomeRejected, "path: rel_b64 is not valid base64")
	}
	if len(job.Windows) == 0 {
		return fail(workerapi.OutcomeRejected, "job has no windows")
	}
	p, err := w.resolve(mount, string(relB))
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			w.mountTrouble()
			return fail(workerapi.OutcomeNotFound, "not found: "+err.Error())
		case errors.Is(err, pathutil.ErrBadRelPath), errors.Is(err, pathutil.ErrEscapesRoot):
			return fail(workerapi.OutcomeRejected, "path: "+err.Error())
		default:
			// EIO, ESTALE, ETIMEDOUT...: the mount, not the file. Re-queue
			// (a timeout is not a verdict and does not send the file to the
			// server lane on the first occurrence).
			w.mountTrouble()
			return fail(workerapi.OutcomeTimeout, "i/o: "+err.Error())
		}
	}

	// Design (d2) step 5: open read-only and fstat; the job is refused as
	// stale unless the size and mtime are exactly the server's. This is also
	// what keeps the NFC/NFD fallback in resolve from ever fingerprinting a
	// different file under this job's ref.
	f, err := w.cfg.openFile(p)
	if err != nil {
		w.mountTrouble()
		if errors.Is(err, fs.ErrNotExist) {
			return fail(workerapi.OutcomeNotFound, "not found: "+err.Error())
		}
		return fail(workerapi.OutcomeTimeout, "i/o: open: "+err.Error())
	}
	fi, err := f.Stat()
	_ = f.Close()
	if err != nil {
		w.mountTrouble()
		return fail(workerapi.OutcomeTimeout, "i/o: fstat: "+err.Error())
	}
	w.badStreak.Store(0)
	if !fi.Mode().IsRegular() {
		return fail(workerapi.OutcomeRejected, "path: not a regular file")
	}
	if fi.Size() != job.Size || fi.ModTime().Unix() != job.MtimeUnix {
		return fail(workerapi.OutcomeStale, fmt.Sprintf("size/mtime %d/%d here, job has %d/%d",
			fi.Size(), fi.ModTime().Unix(), job.Size, job.MtimeUnix))
	}

	dur := fingerprint.DurationUsed{Sec: job.DurationSec, Source: fingerprint.DurationSource(job.DurationSource)}
	for _, jw := range job.Windows {
		spec := fingerprint.WindowSpec{Kind: fingerprint.WindowKind(jw.Kind), SlotBP: jw.SlotBP,
			OffsetSec: jw.OffsetSec, LengthSec: jw.LengthSec, CoversWhole: jw.CoversWhole,
			WindowSet: job.WindowSet, Duration: dur}
		wp, err := w.cutWithRetry(ctx, p, spec)
		if err != nil {
			return fail(classifyCutError(err), fmt.Sprintf("slot %d: %v", jw.SlotBP, err))
		}
		res.Windows = append(res.Windows, workerapi.WindowResult{
			// The job's window is echoed verbatim: the server compares it
			// with its own spec.
			Window:     jw,
			DecodedSec: wp.DecodedSec,
			Frames:     wp.Frames,
			Algorithm:  wp.Algorithm,
			RawB64:     base64.StdEncoding.EncodeToString(wp.Raw),
		})
	}
	// Re-stat after the cut: a file rewritten or replaced while ffmpeg read
	// it gives a print of neither version.
	after, err := os.Stat(p)
	switch {
	case err != nil:
		return fail(workerapi.OutcomeTimeout, "i/o: stat after cut: "+err.Error())
	case !os.SameFile(fi, after) || after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()):
		return fail(workerapi.OutcomeStale, fmt.Sprintf("file changed while it was cut (size/mtime %d/%d -> %d/%d)",
			fi.Size(), fi.ModTime().Unix(), after.Size(), after.ModTime().Unix()))
	}
	res.Outcome = workerapi.OutcomeOK
	return res
}

// mountTrouble counts a job that failed on the mount itself; a long enough
// run asks recheckLoop to re-verify the mount and calibration files.
func (w *worker) mountTrouble() {
	if w.badStreak.Add(1) > notFoundRecheckAfter {
		w.badStreak.Store(0)
		select {
		case w.recheckC <- struct{}{}:
		default:
		}
	}
}

// cutWithRetry cuts a window, retrying once after a transient failure.
func (w *worker) cutWithRetry(ctx context.Context, path string, spec fingerprint.WindowSpec) (*fingerprint.WindowPrint, error) {
	wp, err := w.cfg.Cut(ctx, path, spec)
	if err == nil || !errors.Is(err, fingerprint.ErrWindowTransient) || ctx.Err() != nil {
		return wp, err
	}
	t := time.NewTimer(w.cfg.retryDelay)
	select {
	case <-t.C:
	case <-ctx.Done():
		t.Stop()
		return nil, err
	}
	return w.cfg.Cut(ctx, path, spec)
}

// classifyCutError maps a FileWindow failure to a job outcome.
func classifyCutError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return workerapi.OutcomeTimeout
	case errors.Is(err, fingerprint.ErrWindowTransient):
		// A read failure that repeated: re-queue it (the server sends a
		// second timeout to its own lane) rather than call the file bad.
		return workerapi.OutcomeTimeout
	case errors.Is(err, fingerprint.ErrWindowToolsMissing):
		return workerapi.OutcomeRejected
	default:
		// ErrWindowFFmpeg, ErrWindowFpcalc, ErrWindowParse,
		// ErrWindowShortDecode, ErrFingerprintTooShort and anything else.
		return workerapi.OutcomeDecodeError
	}
}

// resolve maps a root-relative path onto the mount (pathutil.JoinRoot:
// validated, symlinks resolved, containment re-checked).
//
// The exact bytes always win. Only when they do not exist is the path looked
// up component by component, each component taking its exact bytes if they
// exist and otherwise its NFC or NFD spelling (unicodeAlternatePath), because
// a Mac client can see a name in a different normalization than the server's
// ZFS stored it, and a directory and the file in it need not agree. Design
// (d2) says the worker never normalizes, and on a byte-preserving share an
// NFC and an NFD name are two different files; the fallback is safe only
// because (1) it is never consulted when the exact path exists, and a
// component's exact bytes are preferred when they exist, so each of two
// coexisting spellings resolves to itself, and (2) process fstat-checks
// whatever it opened against the job's size and mtime and answers stale on
// any difference, so a different file reached through the alternate spelling
// is never fingerprinted under this job's ref. not_found is reported only
// when no spelling exists.
func (w *worker) resolve(mount, rel string) (string, error) {
	p, err := w.cfg.joinRoot(mount, rel)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return p, err
	}
	alt, ok, aerr := unicodeAlternatePath(mount, rel)
	if aerr != nil {
		return "", aerr
	}
	if !ok {
		return "", err
	}
	return w.cfg.joinRoot(mount, alt)
}

// unicodeAlternatePath looks rel up one component at a time. Each
// component tries its exact bytes first and then its NFC and NFD spellings,
// backtracking when a choice leads nowhere (a directory can exist in both
// forms with the file under only one of them), so a directory and a file
// stored in different forms are both found. ok is false when no combination
// exists, or when the only one found is rel itself. A non-ENOENT error while
// looking is returned as is (the mount, not the name). A name that is not
// valid UTF-8 has no alternates. The caller still runs the result through
// JoinRoot (containment) and process's size/mtime check. At most 3 spellings
// per component are tried, so the search is bounded by 3^depth lstats, and
// it runs only after the exact path was already missing.
func unicodeAlternatePath(mount, rel string) (alt string, ok bool, err error) {
	if !utf8.ValidString(rel) || pathutil.ValidateRel(rel) != nil {
		return "", false, nil
	}
	comps := strings.Split(rel, "/")
	chosen := make([]string, len(comps))
	var walk func(i int, dir string) (bool, error)
	walk = func(i int, dir string) (bool, error) {
		if i == len(comps) {
			return true, nil
		}
		for _, cand := range componentSpellings(comps[i]) {
			next := filepath.Join(dir, cand)
			if _, lerr := os.Lstat(next); lerr != nil {
				if errors.Is(lerr, fs.ErrNotExist) {
					continue
				}
				return false, lerr
			}
			chosen[i] = cand
			found, werr := walk(i+1, next)
			if werr != nil || found {
				return found, werr
			}
		}
		return false, nil
	}
	found, err := walk(0, mount)
	if err != nil || !found {
		return "", false, err
	}
	alt = strings.Join(chosen, "/")
	return alt, alt != rel, nil
}

// componentSpellings is c, then its NFC and NFD forms where they differ.
func componentSpellings(c string) []string {
	out := []string{c}
	for _, f := range []norm.Form{norm.NFC, norm.NFD} {
		if alt := f.String(c); !slices.Contains(out, alt) {
			out = append(out, alt)
		}
	}
	return out
}

// scrub removes this host's mount paths from a message bound for the server
// or a log (design (d2): a Mac path is never sent back), makes it valid
// UTF-8, and bounds it.
func (w *worker) scrub(msg string) string {
	type pair struct{ local, name string }
	var pairs []pair
	for name, r := range w.roots {
		pairs = append(pairs, pair{r, "$" + name})
		if c := w.cfg.Roots[name]; c != "" && c != r {
			pairs = append(pairs, pair{c, "$" + name})
		}
	}
	// Longest first, so a nested mount is replaced before its parent.
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i].local) > len(pairs[j].local) })
	for _, p := range pairs {
		msg = strings.ReplaceAll(msg, p.local, p.name)
	}
	msg = strings.ToValidUTF8(msg, "?")
	if len(msg) > maxErrorLen {
		msg = strings.ToValidUTF8(msg[:maxErrorLen], "") + "..."
	}
	return msg
}
