// file: internal/plugins/acoustid/worker_hub.go
// version: 1.6.0
// guid: b2279415-b876-42b0-97f0-bea586ad4923
// last-edited: 2026-09-19

package acoustid

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// The remote fingerprint worker lease manager (windowed-fingerprint design
// (d), (d2), (e); PR 6).
//
// A live acoustid.window-backfill attaches itself to the plugin's WorkerHub
// for the length of its run and hands it each tier's work list. From then on
// the server lane and every remote worker consume the SAME list:
//
//   - the server lane (RunItems) claims an item before cutting it and skips
//     one a lease already holds;
//   - a lease takes items the server lane has not claimed, head of tier first
//     (requeued items before fresh ones);
//   - every item has exactly one owner at a time (pending, server, leased,
//     writing, done), switched under WorkerHub.mu, so no file is ever cut by
//     both lanes at once and no result is written twice concurrently.
//
// Nothing here is persisted: completion is derived from the fpwin: rows, so a
// restart simply re-plans. When no live run is attached every endpoint answers
// ErrNoWindowRun (HTTP 503).
//
// Results are validated on the server (decode, frame count, decoded length,
// window specs, tool versions, and an os.Stat of the SERVER's file against the
// job's size and mtime) and written through storeWindows, the same call and
// the same per-ref stripe the server lane writes through. A worker's word is
// never enough to tombstone a file: not_found is re-checked by the server,
// decode errors and repeated timeouts are handed to the server lane, which
// makes its own tombstone decision.
//
// Remote-only runs (WindowBackfillParams.RemoteOnly; owner rule: nothing is
// decoded on the server): the server lane cuts nothing. It waits on each item
// in order (awaitRemote) until a worker finished it, and marks done WITHOUT a
// write every item a worker may not have (not remote-eligible, or routed to
// the server lane by a decode error, rejection, mount not_found, repeated
// timeouts, ...): it is "deferred", gets no tombstone, and a later run plans it
// again. The run's "server" pair is the configured reference pair, and
// calibration relaxes accordingly (see buildCalibration and Hello).

var hubLog = logger.New("acoustid.worker-hub")

// Errors the HTTP layer maps to status codes (defined in workerapi so the
// handler package need not import this plugin).
var (
	ErrNoWindowRun      = workerapi.ErrNoRun
	ErrRunChanged       = workerapi.ErrRunChanged
	ErrLeaseGone        = workerapi.ErrLeaseGone
	ErrToolsNotAllowed  = workerapi.ErrToolsNotAllowed
	ErrTooManyLeases    = workerapi.ErrTooManyLeases
	ErrBadWorkerRequest = workerapi.ErrBadRequest
)

// maxWorkerIDLen bounds the worker ID, which is logged and stamped on rows.
const maxWorkerIDLen = 128

// calibrationFiles is how many calibration files hello offers.
const calibrationFiles = 3

// calibrationCandidates is how many current files the plan keeps for hello
// to choose from: some may be worker-made or have moved since.
const calibrationCandidates = 20

// calibrationHeadBytes is how much of a calibration file is hashed.
const calibrationHeadBytes = 64 << 10

// hubSweepEvery is the sweeper period; a variable so tests can shorten it.
var hubSweepEvery = workerapi.SweepEvery

// hubStat is the stat Lease runs on each picked file; a variable so a test
// can make it slow.
var hubStat = os.Stat

// maxRemoteTimeouts: the second timeout sends a job to the server lane.
const maxRemoteTimeouts = 2

// queue item owner states.
type qState uint8

const (
	qPending qState = iota // nobody holds it
	qServer                // the server lane is cutting it
	qLeased                // a remote lease holds it
	qWriting               // a remote result is being validated and written
	qDone                  // finished for this run
)

// remote eligibility, computed once per item on first sight by a lease.
const (
	remoteUnknown int8 = iota
	remoteYes
	remoteNo
)

type queueItem struct {
	state      qState
	lease      string // owning lease while qLeased
	touched    bool   // ever leased: holds the checkpoint back until done
	serverOnly bool   // never offered to a worker again this run
	// whyServer is why a worker may not have the item (not eligible, or the
	// reason it became serverOnly): the deferral reason in a remote-only run.
	whyServer string
	timeouts  int
	remote    int8
	jobs      []string // job IDs issued for it; pruned when it is done
	rel       string   // root-relative path when remote == remoteYes
	dur       fingerprint.DurationUsed
	specs     []fingerprint.WindowSpec
}

type workerLease struct {
	id       string
	worker   string
	expires  time.Time
	jobs     []string
	items    []int // every item picked for it, so a sweep finds them even before jobs exist
	open     int   // items still qLeased by this lease
	versions workerapi.ToolVersions
}

type workerJob struct {
	id       string
	lease    string
	worker   string
	idx      int
	ref      database.FingerprintWindowRef
	size     int64
	mtime    int64
	dur      fingerprint.DurationUsed
	specs    []fingerprint.WindowSpec
	versions workerapi.ToolVersions
	// boot: issued during a remote-only bootstrap (no reference windows yet),
	// under claim epoch. Its ok result counts only while that claim is the
	// live one, or when that claim is the one that defined the reference.
	boot  bool
	epoch int
}

// hubRun is one attached live run. Every field below mu-guarded is only
// touched with WorkerHub.mu held.
type hubRun struct {
	p     *Plugin
	tally *windowTally
	// server is the pair the run's windows are judged against: the server's
	// own tools, or in a remote-only run the configured reference pair.
	server  fingerprint.ToolVersionInfo
	allowed []workerapi.ToolVersions
	roots   []pathutil.PathVar
	calib   []windowItem
	probe   libraryScanProbe // nil: no library.scan gate (tests)

	// remoteOnly: the server lane decodes nothing (see the package comment).
	remoteOnly bool
	// runID is this run's random ID (Hello answers it; lease, renew and
	// results must echo it). The claim, epochs and bootstrapped set below
	// exist only for this run, so a worker from an earlier run must hello
	// and pass its gate again before it may lease.
	runID    string
	headHash func(string) (string, error)
	// identity are present libroot files offered identity-only (no windows)
	// while a remote-only run bootstraps its reference windows.
	identity []windowItem

	// calibMu guards the calibration and bootstrap state below. It may be
	// taken with or without mu held (never mu while holding it), and nothing
	// does I/O under it: calibration files are built unlocked, then swapped
	// in, so a slow NFS stat or head read never stalls the hub.
	calibMu    sync.Mutex
	calibBuilt bool // calibOut is final for this run
	calibOut   []workerapi.CalibrationFile
	calibExtra []windowItem // remote-only: files written this run with the reference pair
	// refExists: some stored window was cut with exactly the reference pair,
	// current or not (the plan's census), or a worker wrote reference windows
	// this run. While false a remote-only run bootstraps; once true it never
	// does again (unless the owner passes rebootstrap_reference).
	refExists bool
	// bootWorker holds the ONE bootstrap claim until bootExpires. The
	// claimant's hello, lease, renew and results calls extend it; a claimant
	// that vanishes loses it after workerapi.LeaseTTL of silence.
	//
	// Trusted-key model: worker_id is whatever the caller says. Any holder
	// of the API key can hello under any worker_id and reported tool pair,
	// and so can take a lapsed claim or impersonate the claimant. The claim
	// orders honest workers; it is not authentication.
	bootWorker  string
	bootExpires time.Time
	// bootEpoch numbers claims: it moves on every grant to a new holder, and
	// a bootstrap job carries the epoch it was issued under, so moving the
	// claim invalidates every job the old claimant holds. refEpoch is the
	// epoch whose worker wrote the first reference windows (0: the reference
	// came from the store).
	bootEpoch int
	refEpoch  int
	// bootstrapped: workers given bootstrap=true, with their epoch. Once the
	// reference exists, one that did not define it may not lease again until
	// a hello has handed it the real calibration (so it must pass parity).
	bootstrapped map[string]int

	cancel    context.CancelFunc
	sweepDone chan struct{}
	changed   chan struct{} // cap 1: an item became done or was requeued
	// bcast is closed (and replaced) on the same events as changed, for the
	// many awaitRemote waiters of a remote-only run. mu-guarded.
	bcast chan struct{}

	// mu-guarded
	gen       int // bumped by beginTier
	tier      int
	items     []windowItem
	st        []queueItem
	leaseNext int
	requeue   []int            // stack: last pushed is leased first
	open      map[int]struct{} // touched && !done
	inflight  int              // qLeased + qWriting
	leases    map[string]*workerLease
	jobs      map[string]*workerJob
	doneCount int // items in qDone this tier

	// Worker contact, for the no-worker and pending graces (mu-guarded;
	// kept across tiers). Only lease, renew and results are progress
	// contact; a hello is not (a pending worker hellos forever).
	attachedAt  time.Time
	lastContact time.Time
	contacted   bool
	// pendingSince: first reference_pending hello since the last progress
	// contact (zero: none). pendingSeen: who was told to wait, for the error.
	pendingSince time.Time
	pendingSeen  map[string]pendingWorker
}

// pendingWorker is a worker last told reference_pending at at.
type pendingWorker struct {
	pair string
	at   time.Time
}

// hubMode is how a live run attaches.
type hubMode struct {
	remoteOnly bool
	// identity: identity-only calibration candidates (remote-only bootstrap).
	identity []windowItem
	// refExists: the plan found current exact-reference-pair windows.
	refExists bool
}

// WorkerHub is the lease manager. The zero value is not usable; Plugin owns
// one (Plugin.WorkerHub).
type WorkerHub struct {
	mu sync.Mutex
	// headHash hashes a calibration file's first 64 KiB; each run copies it
	// at attach. Injected rather than a package variable so a test can make
	// it slow without racing the run that reads it.
	headHash func(string) (string, error)
	now      func() time.Time
	run      *hubRun
}

func newWorkerHub() *WorkerHub { return &WorkerHub{now: time.Now, headHash: headSHA256} }

// WorkerHub returns the plugin's lease manager, for the HTTP layer.
func (p *Plugin) WorkerHub() *WorkerHub {
	p.hubOnce.Do(func() { p.hub = newWorkerHub() })
	return p.hub
}

// allowedToolVersions is the server's own pair plus the configured
// allowlist: the fingerprint.ToolsEquivalent class.
func allowedToolVersions(server fingerprint.ToolVersionInfo) []workerapi.ToolVersions {
	out := []workerapi.ToolVersions{{Fpcalc: server.Fpcalc, FFmpeg: server.FFmpeg}}
	for _, p := range fingerprint.ParseToolPairs(config.AppConfig.FingerprintWorkerToolVersions) {
		if p != server {
			out = append(out, workerapi.ToolVersions{Fpcalc: p.Fpcalc, FFmpeg: p.FFmpeg})
		}
	}
	return out
}

// windowVersionAllowed reports whether a stored window's tool pair counts as
// current: equivalent to the server's pair under fingerprint.ToolsEquivalent,
// the same rule WindowSetSimilarity applies, so a window that is "current" is
// always comparable with a server-cut one. versions nil means unknown (a dry
// run without tools) and accepts anything, as before.
func windowVersionAllowed(fp, ff string, versions *fingerprint.ToolVersionInfo) bool {
	return versions == nil || fingerprint.ToolsEquivalent(fingerprint.ToolVersionInfo{Fpcalc: fp, FFmpeg: ff}, *versions)
}

func versionsAllowed(allowed []workerapi.ToolVersions, fp, ff string) bool {
	for _, a := range allowed {
		if a.Fpcalc == fp && a.FFmpeg == ff {
			return true
		}
	}
	return false
}

// ---- op side ----

// attach makes run the live run the endpoints serve and starts the sweeper.
// The op's ConcurrencyKey guarantees one live run at a time.
//
// In a remote-only run wt carries only Versions, the reference pair: the
// equivalence class and the lease allowlist are built from it, and the
// server's own pair (never resolved) is in neither.
func (h *WorkerHub) attach(ctx context.Context, p *Plugin, wt fingerprint.WindowTools, tally *windowTally, calib []windowItem, mode hubMode) {
	sctx, cancel := context.WithCancel(ctx)
	r := &hubRun{
		p:          p,
		tally:      tally,
		server:     wt.Versions,
		allowed:    allowedToolVersions(wt.Versions),
		roots:      pathutil.PathVars(config.AppConfig.RootDir),
		calib:      calib,
		remoteOnly: mode.remoteOnly,
		identity:   mode.identity,
		runID:      newID(),
		headHash:   h.headHash,
		refExists:  mode.refExists,
		attachedAt: h.now(),
		// bootstrapped/pendingSeen are guarded by calibMu/mu respectively.
		bootstrapped: map[string]int{},
		pendingSeen:  map[string]pendingWorker{},
		probe:        p.currentScanProbe(),
		cancel:       cancel,
		sweepDone:    make(chan struct{}),
		changed:      make(chan struct{}, 1),
		bcast:        make(chan struct{}),
		open:         map[int]struct{}{},
		leases:       map[string]*workerLease{},
		jobs:         map[string]*workerJob{},
	}
	h.mu.Lock()
	h.run = r
	h.mu.Unlock()
	go h.sweepLoop(sctx, r, hubSweepEvery)
}

// detach ends the run: every endpoint answers ErrNoWindowRun again.
func (h *WorkerHub) detach() {
	h.mu.Lock()
	r := h.run
	h.run = nil
	h.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.sweepDone
	}
}

func (h *WorkerHub) sweepLoop(ctx context.Context, r *hubRun, every time.Duration) {
	defer close(r.sweepDone)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.sweep()
		}
	}
}

// beginTier hands the hub one tier's work list. items[i].qi must be i. Any
// lease state of the previous tier is dropped: the op drains a tier before it
// starts the next, so nothing in it is still owed.
func (h *WorkerHub) beginTier(tier int, items []windowItem) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil {
		return
	}
	r.gen++
	r.tier = tier
	r.items = items
	r.st = make([]queueItem, len(items))
	r.leaseNext = 0
	r.requeue = nil
	r.open = map[int]struct{}{}
	r.inflight = 0
	r.leases = map[string]*workerLease{}
	r.jobs = map[string]*workerJob{}
	r.doneCount = 0
}

// claimServer is the server lane's claim: true when the item was pending and
// is now the server's. A detached hub (tests, dry runs) always grants it.
func (h *WorkerHub) claimServer(idx int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil || idx < 0 || idx >= len(r.st) {
		return true
	}
	if r.st[idx].state != qPending {
		return false
	}
	r.setState(idx, qServer)
	return true
}

// serverDone marks an item the server lane finished.
func (h *WorkerHub) serverDone(idx int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil || idx < 0 || idx >= len(r.st) {
		return
	}
	r.setState(idx, qDone)
}

// takeBacklog claims, for the server lane, every requeued item nobody holds.
func (h *WorkerHub) takeBacklog() []windowItem { return h.takeBacklogN(-1, false) }

// takeBacklogN claims up to limit (<0: all) requeued items for the server
// lane. With inPass set (the tier's pass is still running) it takes only
// what workers must not have again (server-only items), plus every requeued
// item when no lease is open (no worker is around to take them); the rest
// stay at the head of the tier for workers. Taking them during the pass is
// what lets the checkpoint move past them before the tier ends.
func (h *WorkerHub) takeBacklogN(limit int, inPass bool) []windowItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil {
		return nil
	}
	idle := len(r.leases) == 0
	var out []windowItem
	for idx := range r.open {
		if limit >= 0 && len(out) >= limit {
			break
		}
		st := &r.st[idx]
		if st.state != qPending || (inPass && !st.serverOnly && !idle) {
			continue
		}
		r.setState(idx, qServer)
		out = append(out, r.items[idx])
	}
	return out
}

// inFlight is how many items remote workers hold or are writing.
func (h *WorkerHub) inFlight() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run == nil {
		return 0
	}
	return h.run.inflight
}

// changedCh is signalled when an item finishes or is requeued.
func (h *WorkerHub) changedCh() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run == nil {
		return nil
	}
	return h.run.changed
}

// checkpointMark caps RunItems' contiguous-completion watermark below the
// first item a remote worker has touched and not finished: the server lane
// "completes" a leased item by skipping it, and a resume cursor past it would
// drop it from the resumed run.
func (h *WorkerHub) checkpointMark(mark int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run == nil {
		return mark
	}
	for idx := range h.run.open {
		if idx < mark {
			mark = idx
		}
	}
	return mark
}

// setState moves an item between owners, keeping inflight, open and the
// owning lease's open count in step. Called with mu held.
func (r *hubRun) setState(idx int, s qState) {
	it := &r.st[idx]
	old := it.state
	if old == s {
		return
	}
	if old == qLeased {
		if l := r.leases[it.lease]; l != nil {
			l.open--
			if l.open <= 0 {
				delete(r.leases, l.id)
			}
		}
		it.lease = ""
	}
	busy := func(x qState) bool { return x == qLeased || x == qWriting }
	if busy(old) && !busy(s) {
		r.inflight--
	}
	if !busy(old) && busy(s) {
		r.inflight++
	}
	it.state = s
	switch {
	case s == qDone:
		r.doneCount++
		delete(r.open, idx)
		// Finished: its job records are no longer needed (a repost is
		// recognized from the stored rows, see duplicateOfStored).
		for _, jid := range it.jobs {
			delete(r.jobs, jid)
		}
		it.jobs = nil
		r.signal()
	case s == qLeased:
		it.touched = true
		r.open[idx] = struct{}{}
	}
}

// requeue returns an item to the head of its tier. Called with mu held.
func (r *hubRun) requeueItem(idx int) {
	r.setState(idx, qPending)
	r.requeue = append(r.requeue, idx)
	r.signal()
}

func (r *hubRun) signal() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
	close(r.bcast)
	r.bcast = make(chan struct{})
}

// awaitRemote is the remote-only server lane for one item: it cuts nothing.
// It returns once a worker finished the item, or marks the item done without
// a write and returns the deferral reason when no worker may have it (not
// remote-eligible, or serverOnly after a decode error, rejection, mount
// not_found, repeated timeouts, ...). A deferred item gets no tombstone, so a
// later run plans it again.
//
// Called by RunItems' workers in item order, so the run's checkpoint
// watermark is the unbroken prefix of items that are finished or deferred:
// it moves past deferred items and never past one a worker still owes.
// It reports no progress itself: every waiter can sit on one stuck item
// while other items finish, so liveness comes from the tier's single
// heartbeat (windowRun.remoteHeartbeat), not from here.
func (h *WorkerHub) awaitRemote(ctx context.Context, idx int) (string, error) {
	for {
		h.mu.Lock()
		r := h.run
		if r == nil || idx < 0 || idx >= len(r.st) {
			h.mu.Unlock()
			return "", nil
		}
		st := &r.st[idx]
		if st.state == qDone {
			h.mu.Unlock()
			return "", nil
		}
		if st.state == qPending && !r.remoteEligible(idx) {
			why := st.whyServer
			if why == "" {
				why = "server_only"
			}
			r.setState(idx, qDone)
			h.mu.Unlock()
			return why, nil
		}
		ch := r.bcast
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ch:
		}
	}
}

// tierCounts is the current tier's resolved, leased and total item counts.
func (h *WorkerHub) tierCounts() (done, leased, total int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run == nil {
		return 0, 0, 0
	}
	return h.run.doneCount, h.run.inflight, len(h.run.st)
}

// windowNoWorkerMargin is added to the lease TTL for the gap allowed once a
// worker has made contact: a worker between leases (backoff, restart) is
// silent for up to about one TTL.
const windowNoWorkerMargin = 2 * time.Minute

// noWorkerExpired reports, as a message, that a remote-only run has waited
// too long without any worker: none contacted it within grace of the start,
// or, after contact, none for max(grace, LeaseTTL+margin) with nothing
// leased. Empty when the run should keep waiting.
//
// While workers are only being told reference_pending (every one waits on a
// reference nobody can cut), pendingGrace governs instead: that much time
// with no progress contact since the first such hello ends the run, naming
// the missing pair and the waiting workers.
func (h *WorkerHub) noWorkerExpired(grace, pendingGrace time.Duration) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil {
		return ""
	}
	now := h.now()
	if !r.pendingSince.IsZero() {
		if gap := now.Sub(r.pendingSince); gap > pendingGrace {
			var waiting []string
			for id, pw := range r.pendingSeen {
				if !pw.at.Before(r.pendingSince) {
					waiting = append(waiting, id+" ("+pw.pair+")")
				}
			}
			sort.Strings(waiting)
			return fmt.Sprintf("no worker with the reference pair %s/%s made progress for %s (pending grace %s): every worker is waiting on reference_pending (waiting workers: %s)",
				r.server.Fpcalc, r.server.FFmpeg, gap.Round(time.Second), pendingGrace, strings.Join(waiting, ", "))
		}
		return ""
	}
	if !r.contacted {
		if gap := now.Sub(r.attachedAt); gap > grace {
			return fmt.Sprintf("no fp-worker called the worker API in the %s since this run started (grace %s); start fp-worker with the reference pair, then queue the run again",
				gap.Round(time.Second), grace)
		}
		return ""
	}
	limit := max(grace, workerapi.LeaseTTL+windowNoWorkerMargin)
	if gap := now.Sub(r.lastContact); r.inflight == 0 && gap > limit {
		return fmt.Sprintf("no fp-worker has called the worker API for %s with nothing leased (limit %s); every worker seems gone",
			gap.Round(time.Second), limit)
	}
	return ""
}

// noteContact records progress contact (a lease, renew or results call)
// and clears the pending clock. Called with mu held.
func (r *hubRun) noteContact(now time.Time) {
	r.lastContact, r.contacted = now, true
	r.pendingSince = time.Time{}
}

// refreshClaim extends the bootstrap claim when worker holds it (a live
// claim only: a lapsed one is never revived). Called only after the call
// was validated (tool pair, lease ownership). Takes calibMu; mu may be held.
func (r *hubRun) refreshClaim(worker string, now time.Time) {
	r.calibMu.Lock()
	defer r.calibMu.Unlock()
	if worker != "" && worker == r.bootWorker && now.Before(r.bootExpires) {
		r.bootExpires = now.Add(workerapi.LeaseTTL)
	}
}

// notePending records a hello answered reference_pending.
func (h *WorkerHub) notePending(r *hubRun, req workerapi.HelloRequest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run != r {
		return
	}
	now := h.now()
	if r.pendingSince.IsZero() {
		r.pendingSince = now
	}
	if _, ok := r.pendingSeen[req.WorkerID]; ok || len(r.pendingSeen) < 64 {
		r.pendingSeen[req.WorkerID] = pendingWorker{pair: req.FpcalcVersion + "/" + req.FFmpegVersion, at: now}
	}
}

// touch records progress contact for calls that take mu only briefly.
func (h *WorkerHub) touch(r *hubRun) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run == r {
		r.noteContact(h.now())
	}
}

// bootJobState is what a job issued now carries: whether the run is still
// bootstrapping, and under which claim epoch.
func (r *hubRun) bootJobState() (bool, int) {
	r.calibMu.Lock()
	defer r.calibMu.Unlock()
	return r.remoteOnly && !r.refExists, r.bootEpoch
}

// bootstrapResultOK: an ok result of a bootstrap job is taken only while its
// claim is the live one, held by the job's worker (a claimant whose claim
// lapsed or moved is superseded, even for items nobody re-leased), and, once
// the reference exists, only from the claim that defined it.
func (r *hubRun) bootstrapResultOK(j *workerJob, now time.Time) bool {
	if !j.boot {
		return true
	}
	r.calibMu.Lock()
	defer r.calibMu.Unlock()
	if !r.refExists {
		return j.worker == r.bootWorker && j.epoch == r.bootEpoch && now.Before(r.bootExpires)
	}
	return j.epoch == r.refEpoch
}

// bootstrapLeaseOK: in a remote-only run with no reference windows, only the
// worker holding the live bootstrap claim, with the reference pair, may
// lease. Called with mu held.
func (r *hubRun) bootstrapLeaseOK(req workerapi.LeaseRequest, now time.Time) bool {
	r.calibMu.Lock()
	defer r.calibMu.Unlock()
	if r.refExists {
		// A worker that bootstrapped under a claim that did not define the
		// reference never passed parity against it: it must hello again.
		e, ok := r.bootstrapped[req.WorkerID]
		return !ok || e == r.refEpoch
	}
	return r.bootWorker != "" && req.WorkerID == r.bootWorker && now.Before(r.bootExpires) &&
		req.FpcalcVersion == r.server.Fpcalc && req.FFmpegVersion == r.server.FFmpeg
}

// markServerOnly routes an item to the server lane for the rest of the run.
// Called with mu held.
func (r *hubRun) markServerOnly(idx int, why string) {
	st := &r.st[idx]
	st.serverOnly = true
	if st.whyServer == "" {
		st.whyServer = why
	}
}

// remoteEligible decides once whether an item may go to a worker: under
// libroot (v1), a clean relative path, and a duration the row already knows
// (a worker cannot ffprobe for the server). Called with mu held; string work
// only, no I/O.
func (r *hubRun) remoteEligible(idx int) bool {
	st := &r.st[idx]
	if st.remote == remoteUnknown {
		st.remote = remoteNo
		it := r.items[idx]
		root, rel, ok := pathutil.SplitRoot(it.Path, r.roots)
		switch {
		case !ok || root != "libroot":
			st.whyServer = "not_under_libroot"
		default:
			dur, err := fingerprint.ChooseDuration(it.FpDuration, it.Duration, nil)
			if err != nil {
				st.whyServer = "unknown_duration"
				break
			}
			specs, perr := fingerprint.PlanWindows(dur, fingerprint.WindowSetWS1)
			if perr != nil || len(specs) == 0 {
				st.whyServer = "no_window_plan"
				break
			}
			st.remote, st.rel, st.dur, st.specs = remoteYes, rel, dur, specs
		}
	}
	return st.remote == remoteYes && !st.serverOnly
}

// pick marks up to n items leased by leaseID, requeued items first.
func (r *hubRun) pick(n int, leaseID string) []int {
	var out []int
	for len(out) < n && len(r.requeue) > 0 {
		idx := r.requeue[len(r.requeue)-1]
		r.requeue = r.requeue[:len(r.requeue)-1]
		if r.st[idx].state == qPending && r.remoteEligible(idx) {
			r.setState(idx, qLeased)
			r.st[idx].lease = leaseID
			out = append(out, idx)
		}
	}
	for len(out) < n && r.leaseNext < len(r.st) {
		idx := r.leaseNext
		r.leaseNext++
		// A pending item that is not eligible stays pending: the server
		// lane's in-order walk reaches it.
		if r.st[idx].state == qPending && r.remoteEligible(idx) {
			r.setState(idx, qLeased)
			r.st[idx].lease = leaseID
			out = append(out, idx)
		}
	}
	return out
}

// sweep reclaims every expired lease, requeueing its unfinished jobs at the
// head of the tier.
func (h *WorkerHub) sweep() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil {
		return 0
	}
	now := h.now()
	n := 0
	for id, l := range r.leases {
		if now.Before(l.expires) {
			continue
		}
		// By item, not by job: a lease swept while Lease is still statting
		// its files has items but no jobs yet.
		k := 0
		for _, idx := range l.items {
			if r.st[idx].state == qLeased && r.st[idx].lease == id {
				r.requeueItem(idx)
				k++
			}
		}
		delete(r.leases, id)
		n += k
		hubLog.Info("lease %s of worker %s expired; %d job(s) requeued at the head of the tier", id, logger.SanitizeLogValue(l.worker), k)
	}
	return n
}

// ---- HTTP side ----

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b[:])
}

func (h *WorkerHub) liveRun() (*hubRun, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run == nil {
		return nil, ErrNoWindowRun
	}
	return h.run, nil
}

// Hello returns the root map, the pipeline and tool allowlist, and the
// calibration files for the worker's startup gate.
func (h *WorkerHub) Hello(_ context.Context, req workerapi.HelloRequest) (*workerapi.HelloResponse, error) {
	r, err := h.liveRun()
	if err != nil {
		return nil, err
	}
	c := r.calibration(req, h.now)
	if c.pending {
		h.notePending(r, req)
	}
	resp := &workerapi.HelloResponse{
		Pipeline:         fingerprint.WindowPipelineID,
		WindowSet:        fingerprint.WindowSetWS1,
		ToolVersions:     r.allowed,
		Calibration:      c.files,
		Bootstrap:        c.bootstrap,
		ReferencePending: c.pending,
		Waiting:          c.waiting,
		RunID:            r.runID,
		Limits: workerapi.Limits{
			MaxJobsPerLease:    workerapi.MaxJobsPerLease,
			LeaseTTLSec:        int(workerapi.LeaseTTL / time.Second),
			RenewEverySec:      int(workerapi.RenewEvery / time.Second),
			MaxLeasesPerWorker: workerapi.MaxLeasesPerWorker,
			MaxBodyBytes:       workerapi.MaxBodyBytes,
		},
	}
	for _, v := range r.roots {
		resp.Roots = append(resp.Roots, workerapi.Root{ID: v.Name, Remote: v.Name == "libroot"})
	}
	if r.remoteOnly {
		resp.ReferenceTools = &workerapi.ToolVersions{Fpcalc: r.server.Fpcalc, FFmpeg: r.server.FFmpeg}
	}
	if resp.Calibration == nil {
		resp.Calibration = []workerapi.CalibrationFile{}
	}
	return resp, nil
}

// helloCalibration is what Hello offers one caller.
type helloCalibration struct {
	files     []workerapi.CalibrationFile
	bootstrap bool   // identity-only files; the caller holds the bootstrap claim
	pending   bool   // nothing to calibrate against yet: retry hello later
	waiting   string // why pending
}

// calibration decides what Hello offers the caller.
//
// A normal run builds once and keeps the result. A remote-only run keeps a
// result once it carries reference windows; until then:
//
//   - when reference windows exist (the plan's census found current
//     exact-reference-pair windows, or a worker wrote some this run) but none
//     is readable right now, the caller is told to wait: the run never
//     bootstraps again over existing reference windows;
//   - otherwise it is bootstrapping, and exactly ONE caller, a valid worker
//     ID with exactly the reference pair, gets the bootstrap claim and the
//     identity-only files. Every other caller is told to wait until the
//     claimant's windows exist, and then has to pass the byte-parity gate
//     against them. A claim nobody refreshes lapses after LeaseTTL.
//
// Files are built with no lock held (stats and 64 KiB head reads over NFS)
// and swapped in under calibMu.
func (r *hubRun) calibration(req workerapi.HelloRequest, now func() time.Time) helloCalibration {
	r.calibMu.Lock()
	if r.calibBuilt {
		out := r.calibOut
		delete(r.bootstrapped, req.WorkerID) // it gets the real gate now
		r.calibMu.Unlock()
		return helloCalibration{files: out}
	}
	cands := append(append([]windowItem(nil), r.calib...), r.calibExtra...)
	r.calibMu.Unlock()

	built := r.buildCalibration(cands) // I/O, unlocked

	r.calibMu.Lock()
	if !r.calibBuilt && (len(built) > 0 || !r.remoteOnly) {
		r.calibOut, r.calibBuilt = built, true
	}
	if r.calibBuilt {
		out := r.calibOut
		delete(r.bootstrapped, req.WorkerID)
		r.calibMu.Unlock()
		return helloCalibration{files: out}
	}
	if r.refExists {
		r.calibMu.Unlock()
		return helloCalibration{pending: true, waiting: "reference windows exist but none usable (no file still matches the size and mtime they were cut from); " +
			"re-run with a fresh file set, or clear them deliberately / queue the run with rebootstrap_reference: true"}
	}
	t := now()
	if r.bootWorker != "" && !t.Before(r.bootExpires) {
		hubLog.Warn("bootstrap claim of worker %s lapsed (no contact for %s); the next reference-pair worker may claim it",
			logger.SanitizeLogValue(r.bootWorker), workerapi.LeaseTTL)
		r.bootWorker = ""
	}
	isRef := req.FpcalcVersion == r.server.Fpcalc && req.FFmpegVersion == r.server.FFmpeg
	switch {
	case !isRef || !validWorkerID(req.WorkerID):
		r.calibMu.Unlock()
		return helloCalibration{pending: true, waiting: fmt.Sprintf("no reference windows exist yet; a worker with exactly fpcalc %s + ffmpeg %s must cut them first", r.server.Fpcalc, r.server.FFmpeg)}
	case r.bootWorker != "" && r.bootWorker != req.WorkerID:
		r.calibMu.Unlock()
		return helloCalibration{pending: true, waiting: "another reference-pair worker holds the bootstrap; this worker is parity-checked against its windows once they exist"}
	}
	if r.bootWorker != req.WorkerID {
		// A new holder: a new epoch, so every job an earlier claimant was
		// issued is superseded (bootstrapResultOK).
		r.bootEpoch++
		hubLog.Warn("bootstrap claim %d granted to worker %s (fpcalc %s + ffmpeg %s): its windows become the reference, with no byte-parity check",
			r.bootEpoch, logger.SanitizeLogValue(req.WorkerID), r.server.Fpcalc, r.server.FFmpeg)
	}
	r.bootWorker, r.bootExpires = req.WorkerID, t.Add(workerapi.LeaseTTL)
	r.bootstrapped[req.WorkerID] = r.bootEpoch
	r.calibMu.Unlock()
	return helloCalibration{files: r.buildIdentityCalibration(), bootstrap: true} // I/O, unlocked
}

// buildIdentityCalibration offers present libroot files with NO windows: the
// remote-only bootstrap, when no reference-pair window exists yet. They prove
// only a worker's root mapping (size, mtime, first 64 KiB); the worker gate
// lets the reference pair alone pass on that (workerclient.calibrationTargets).
func (r *hubRun) buildIdentityCalibration() []workerapi.CalibrationFile {
	out := []workerapi.CalibrationFile{}
	for _, it := range r.identity {
		root, rel, ok := pathutil.SplitRoot(it.Path, r.roots)
		if !ok || root != "libroot" {
			continue
		}
		fi, err := os.Stat(it.Path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		head, err := r.headHash(it.Path)
		if err != nil {
			continue
		}
		out = append(out, workerapi.CalibrationFile{Root: root, RelB64: base64.StdEncoding.EncodeToString([]byte(rel)), Rel: rel,
			Size: fi.Size(), MtimeUnix: fi.ModTime().Unix(), Head64K: head, Windows: []workerapi.CalibrationWindow{}})
		if len(out) == calibrationFiles {
			break
		}
	}
	return out
}

// buildCalibration reads the calibration candidates: files under libroot
// whose stored windows are current. A candidate whose bytes moved since is
// dropped.
func (r *hubRun) buildCalibration(cands []windowItem) []workerapi.CalibrationFile {
	out := []workerapi.CalibrationFile{}
	for _, it := range cands {
		root, rel, ok := pathutil.SplitRoot(it.Path, r.roots)
		if !ok || root != "libroot" {
			continue
		}
		stored, err := r.p.store.GetFingerprintWindows(database.FileWindowRef(it.FileID))
		if err != nil {
			continue
		}
		fi, err := os.Stat(it.Path)
		if err != nil || fi.Size() != it.Size || fi.ModTime().Unix() != it.MtimeUnix {
			continue
		}
		head, err := r.headHash(it.Path)
		if err != nil {
			continue
		}
		cf := workerapi.CalibrationFile{Root: root, RelB64: base64.StdEncoding.EncodeToString([]byte(rel)), Rel: rel,
			Size: it.Size, MtimeUnix: it.MtimeUnix, Head64K: head}
		serverMade := true
		for _, w := range stored {
			if w.Kind != database.WindowKindWindow {
				continue
			}
			// The reference a worker must reproduce is the SERVER's own
			// print, cut by the server's own tools: a worker-made window
			// (even an allowlisted one) would let a worker calibrate
			// against another worker.
			//
			// Remote-only runs replace that rule on purpose: the server
			// cuts nothing, so the reference is the configured reference
			// PAIR (r.server), whoever ran it. Only windows stamped with
			// exactly that pair qualify; an allowlisted pair's windows
			// never do.
			workerMade := strings.HasPrefix(w.Host, workerHostPrefix) && !r.remoteOnly
			if workerMade || w.FpcalcVersion != r.server.Fpcalc || w.FFmpegVersion != r.server.FFmpeg {
				serverMade = false
				break
			}
		}
		if !serverMade {
			continue
		}
		for _, w := range stored {
			if w.Kind != database.WindowKindWindow || w.Pipeline != fingerprint.WindowPipelineID ||
				w.SourceSize != it.Size || w.SourceMtimeUnix != it.MtimeUnix {
				continue
			}
			sum := sha256.Sum256(w.Raw)
			cf.Windows = append(cf.Windows, workerapi.CalibrationWindow{
				Window: workerapi.Window{Kind: string(w.Kind), SlotBP: w.SlotBP, OffsetSec: w.OffsetSec,
					LengthSec: w.LengthSec, CoversWhole: w.CoversWhole},
				RawSHA256:    hex.EncodeToString(sum[:]),
				Frames:       w.Frames,
				ToolVersions: workerapi.ToolVersions{Fpcalc: w.FpcalcVersion, FFmpeg: w.FFmpegVersion},
			})
		}
		if len(cf.Windows) > 0 {
			out = append(out, cf)
		}
		if len(out) == calibrationFiles {
			break
		}
	}
	return out
}

func headSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyN(h, f, calibrationHeadBytes); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validWorkerID(id string) bool {
	if id == "" || len(id) > maxWorkerIDLen {
		return false
	}
	for _, c := range id {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// Lease hands out up to MaxJobsPerLease jobs. A nil response with a nil error
// means nothing is leaseable right now (HTTP 204).
func (h *WorkerHub) Lease(_ context.Context, req workerapi.LeaseRequest) (*workerapi.LeaseResponse, error) {
	if !validWorkerID(req.WorkerID) {
		return nil, fmt.Errorf("%w: worker_id must be 1-%d printable ASCII characters", ErrBadWorkerRequest, maxWorkerIDLen)
	}
	n := req.MaxJobs
	if n <= 0 || n > workerapi.MaxJobsPerLease {
		n = workerapi.MaxJobsPerLease
	}

	h.mu.Lock()
	r := h.run
	if r == nil {
		h.mu.Unlock()
		return nil, ErrNoWindowRun
	}
	if req.RunID != r.runID {
		h.mu.Unlock()
		return nil, ErrRunChanged
	}
	now := h.now()
	if req.Pipeline != fingerprint.WindowPipelineID || !versionsAllowed(r.allowed, req.FpcalcVersion, req.FFmpegVersion) {
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: pipeline=%q fpcalc=%q ffmpeg=%q", ErrToolsNotAllowed, req.Pipeline, req.FpcalcVersion, req.FFmpegVersion)
	}
	// Remote-only bootstrap: until reference windows exist, nobody but the
	// holder of the bootstrap claim has anything to have passed a parity gate
	// against, so only it may lease. The worker enforces the same rule on
	// itself (it waits on reference_pending); this holds against one that
	// skipped its gate.
	if r.remoteOnly && !r.bootstrapLeaseOK(req, now) {
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: only the worker holding the live bootstrap claim (fpcalc=%q ffmpeg=%q) may lease until reference windows exist, "+
			"and a worker that bootstrapped under a claim that did not define them must restart so it passes the parity gate",
			ErrToolsNotAllowed, r.server.Fpcalc, r.server.FFmpeg)
	}
	// Only a call that passed the gates is progress, and only it extends
	// the claim: a wrong-pair call under the claimant's ID must not keep a
	// claim alive.
	r.noteContact(now)
	r.refreshClaim(req.WorkerID, now)
	// Same gate as the server lane (waitForLibraryScan): nothing new is cut
	// while a library.scan may be rewriting the files. 204: retry later.
	if r.probe != nil && r.probe.LibraryScanRunning() {
		h.mu.Unlock()
		return nil, nil
	}
	held := 0
	for _, l := range r.leases {
		if l.worker == req.WorkerID {
			held++
		}
	}
	if held >= workerapi.MaxLeasesPerWorker {
		h.mu.Unlock()
		return nil, ErrTooManyLeases
	}
	leaseID := newID()
	// expires is set now, not after the stats below: the sweeper may run
	// while this call is between its two critical sections.
	lease := &workerLease{id: leaseID, worker: req.WorkerID, expires: h.now().Add(workerapi.LeaseTTL),
		versions: workerapi.ToolVersions{Fpcalc: req.FpcalcVersion, FFmpeg: req.FFmpegVersion}}
	// Registered before the stats so setState's lease accounting sees it.
	r.leases[leaseID] = lease
	picked := r.pick(n, leaseID)
	lease.open = len(picked)
	lease.items = picked
	gen := r.gen
	type cand struct {
		idx  int
		path string
	}
	cands := make([]cand, len(picked))
	for i, idx := range picked {
		cands[i] = cand{idx, r.items[idx].Path}
	}
	if len(picked) == 0 {
		delete(r.leases, leaseID)
	}
	h.mu.Unlock()
	if len(picked) == 0 {
		return nil, nil
	}

	// Stat outside the lock: the job carries the size and mtime the server
	// sees now, and the worker refuses the job if its mount disagrees.
	stats := make([]os.FileInfo, len(cands))
	for i, c := range cands {
		if fi, err := hubStat(c.path); err == nil && fi.Mode().IsRegular() {
			stats[i] = fi
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run != r || r.gen != gen {
		return nil, nil // the tier moved on while we statted
	}
	if r.leases[leaseID] != lease {
		// Swept while we statted (the stats outlasted the TTL): the sweep
		// requeued its items by lease; make sure none is left behind.
		for _, c := range cands {
			if st := &r.st[c.idx]; st.state == qLeased && st.lease == leaseID {
				r.requeueItem(c.idx)
			}
		}
		return nil, nil
	}
	resp := &workerapi.LeaseResponse{LeaseID: leaseID}
	for i, c := range cands {
		st := &r.st[c.idx]
		if st.state != qLeased || st.lease != leaseID {
			continue
		}
		if stats[i] == nil {
			// Unstattable here: the server lane owns the diagnosis.
			r.markServerOnly(c.idx, "server_stat_failed")
			r.requeueItem(c.idx)
			continue
		}
		it := r.items[c.idx]
		jid := fmt.Sprintf("%s-%d", leaseID, len(lease.jobs))
		j := &workerJob{id: jid, lease: leaseID, worker: req.WorkerID, idx: c.idx, ref: database.FileWindowRef(it.FileID),
			size: stats[i].Size(), mtime: stats[i].ModTime().Unix(), dur: st.dur, specs: st.specs, versions: lease.versions}
		j.boot, j.epoch = r.bootJobState()
		r.jobs[jid] = j
		st.jobs = append(st.jobs, jid)
		lease.jobs = append(lease.jobs, jid)
		wj := workerapi.Job{JobID: jid, Ref: string(j.ref), Root: "libroot",
			RelB64: base64.StdEncoding.EncodeToString([]byte(st.rel)), Rel: strings.ToValidUTF8(st.rel, "�"),
			Size: j.size, MtimeUnix: j.mtime, DurationSec: j.dur.Sec, DurationSource: string(j.dur.Source),
			WindowSet: fingerprint.WindowSetWS1}
		for _, s := range j.specs {
			wj.Windows = append(wj.Windows, workerapi.Window{Kind: string(s.Kind), SlotBP: s.SlotBP,
				OffsetSec: s.OffsetSec, LengthSec: s.LengthSec, CoversWhole: s.CoversWhole})
		}
		resp.Jobs = append(resp.Jobs, wj)
	}
	if len(resp.Jobs) == 0 {
		delete(r.leases, leaseID)
		return nil, nil
	}
	lease.expires = h.now().Add(workerapi.LeaseTTL)
	resp.ExpiresAt = lease.expires
	return resp, nil
}

// Renew extends a lease by LeaseTTL. ErrLeaseGone when it expired or was
// reclaimed (or every job in it is resolved).
func (h *WorkerHub) Renew(leaseID string, req workerapi.RenewRequest) (*workerapi.RenewResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil {
		return nil, ErrNoWindowRun
	}
	if req.RunID != r.runID {
		return nil, ErrRunChanged
	}
	l := r.leases[leaseID]
	now := h.now()
	// Another worker's lease answers exactly like a missing one.
	if l == nil || !now.Before(l.expires) || l.worker != req.WorkerID {
		return nil, ErrLeaseGone
	}
	r.noteContact(now)
	r.refreshClaim(req.WorkerID, now)
	l.expires = now.Add(workerapi.LeaseTTL)
	return &workerapi.RenewResponse{LeaseID: leaseID, ExpiresAt: l.expires}, nil
}

// Release hands unfinished jobs back to the head of the tier.
func (h *WorkerHub) Release(leaseID string, req workerapi.ReleaseRequest) (*workerapi.ReleaseResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil {
		return nil, ErrNoWindowRun
	}
	l := r.leases[leaseID]
	if l == nil || l.worker != req.WorkerID {
		return nil, ErrLeaseGone
	}
	want := map[string]bool{}
	for _, id := range req.JobIDs {
		want[id] = true
	}
	n := 0
	for _, jid := range append([]string(nil), l.jobs...) {
		if len(want) > 0 && !want[jid] {
			continue
		}
		if j := r.jobs[jid]; j != nil && r.st[j.idx].state == qLeased && r.st[j.idx].lease == leaseID {
			r.requeueItem(j.idx)
			n++
		}
	}
	return &workerapi.ReleaseResponse{Requeued: n}, nil
}

// Results applies each posted job result and answers one status per job.
func (h *WorkerHub) Results(_ context.Context, req workerapi.ResultsRequest) (*workerapi.ResultsResponse, error) {
	if !validWorkerID(req.WorkerID) {
		return nil, fmt.Errorf("%w: worker_id must be 1-%d printable ASCII characters", ErrBadWorkerRequest, maxWorkerIDLen)
	}
	if len(req.Results) > 4*workerapi.MaxJobsPerLease {
		return nil, fmt.Errorf("%w: %d results in one request", ErrBadWorkerRequest, len(req.Results))
	}
	r, err := h.liveRun()
	if err != nil {
		return nil, err
	}
	if req.RunID != r.runID {
		return nil, ErrRunChanged
	}
	h.touch(r)
	resp := &workerapi.ResultsResponse{Statuses: make([]workerapi.JobStatus, 0, len(req.Results))}
	for _, res := range req.Results {
		// r is the run whose ID this request carried; a result is never
		// applied to a run that attached since.
		status, reason := h.applyResult(r, req.WorkerID, res)
		resp.Statuses = append(resp.Statuses, workerapi.JobStatus{JobID: res.JobID, Status: status, Reason: reason})
	}
	return resp, nil
}

// resultDecision is what one posted result does to its item.
type resultDecision struct {
	status, reason string
	next           qState // qDone or qPending (requeued at the head of the tier)
	serverOnly     bool
	why            string // with serverOnly: the deferral reason in a remote-only run
	timeout        bool
	written        bool // the windows were stored
}

// noteReferenceWrite records that reference windows now exist (the
// bootstrap is over) and makes the file a calibration candidate for later
// Hellos of this remote-only run, until the calibration is final. Called with
// WorkerHub.mu held; calibMu nests inside it, is never held while taking mu,
// and is never held across I/O, so this cannot stall behind a slow Hello.
func (r *hubRun) noteReferenceWrite(it windowItem, j *workerJob) {
	r.calibMu.Lock()
	defer r.calibMu.Unlock()
	if j.boot && !r.refExists {
		r.refEpoch = j.epoch
	}
	r.refExists = true
	if r.calibBuilt || len(r.calibExtra) >= calibrationCandidates {
		return
	}
	it.Size, it.MtimeUnix = j.size, j.mtime
	r.calibExtra = append(r.calibExtra, it)
}

// applyResult is one job's result. Ownership moves under mu; validation, the
// server's stat and the store write happen outside it with the item in
// qWriting, so no other lane can touch the file meanwhile and a slow stat never
// stalls the hub.
//
// r is the run the request was checked against: if another run has attached
// since (a detach and re-attach mid-batch), the result is stale.
func (h *WorkerHub) applyResult(r *hubRun, worker string, res workerapi.JobResult) (string, string) {
	switch res.Outcome {
	case workerapi.OutcomeOK, workerapi.OutcomeNotFound, workerapi.OutcomeDecodeError,
		workerapi.OutcomeTimeout, workerapi.OutcomeStale, workerapi.OutcomeRejected:
	default:
		return workerapi.StatusRejected, "bad_outcome"
	}
	h.mu.Lock()
	if h.run == nil {
		h.mu.Unlock()
		return workerapi.StatusStale, "no_run"
	}
	if h.run != r {
		h.mu.Unlock()
		return workerapi.StatusStale, workerapi.RunChangedMarker
	}
	j := r.jobs[res.JobID]
	if j == nil {
		h.mu.Unlock()
		// Pruned when its file finished, or from another run: a repost of
		// what is stored is still a duplicate.
		if res.Outcome == workerapi.OutcomeOK {
			return r.repostOfStored(res)
		}
		return workerapi.StatusStale, "unknown_job"
	}
	if j.worker != worker {
		h.mu.Unlock()
		return workerapi.StatusRejected, "not_your_job"
	}
	if res.Ref != string(j.ref) {
		h.mu.Unlock()
		return workerapi.StatusRejected, "ref_mismatch"
	}
	now := h.now()
	if res.Outcome == workerapi.OutcomeOK && !r.bootstrapResultOK(j, now) {
		// An unchecked bootstrap worker whose claim lapsed, moved, or did
		// not define the reference: its prints are never stored, even for
		// an item nobody re-leased. Stale, so the worker carries on.
		h.mu.Unlock()
		hubLog.Warn("worker %s job %s: bootstrap claim superseded; result dropped", logger.SanitizeLogValue(worker), j.id)
		return workerapi.StatusStale, "superseded_bootstrap"
	}
	r.refreshClaim(worker, now)
	st := &r.st[j.idx]
	it := r.items[j.idx]
	gen := r.gen
	switch {
	case st.state == qDone:
		h.mu.Unlock()
		if res.Outcome != workerapi.OutcomeOK {
			return workerapi.StatusDuplicate, "already_done"
		}
		return r.repostOfStored(res)
	case st.state == qWriting:
		h.mu.Unlock()
		return workerapi.StatusStale, "in_flight"
	case st.state == qServer:
		h.mu.Unlock()
		return workerapi.StatusStale, "server_claimed"
	case st.serverOnly:
		// Already routed to the server lane (decode error, bad print,
		// second timeout): no worker result is taken for it any more.
		h.mu.Unlock()
		if res.Outcome == workerapi.OutcomeOK {
			return workerapi.StatusRejected, "server_lane"
		}
		return workerapi.StatusStale, "server_lane"
	case st.state == qLeased && st.lease != j.lease:
		// A late result from an expired lease whose file was re-leased:
		// the newer lease owns it now.
		h.mu.Unlock()
		return workerapi.StatusStale, "superseded"
	}
	// Pending (the lease expired and nobody re-leased it: a late result, taken
	// when the server's stat still matches) or leased by this job's own lease.
	r.setState(j.idx, qWriting)
	h.mu.Unlock()

	var d resultDecision
	if res.Outcome == workerapi.OutcomeOK {
		d = r.writeResult(j, res, it)
	} else {
		d = r.decideErrorOutcome(j, res, it)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run != r || r.gen != gen {
		return d.status, d.reason
	}
	st = &r.st[j.idx]
	if d.timeout {
		st.timeouts++
		if st.timeouts >= maxRemoteTimeouts {
			d.serverOnly, d.reason, d.why = true, "server_lane", "repeated_timeouts"
		}
	}
	if d.serverOnly {
		r.markServerOnly(j.idx, d.why)
	}
	if d.next == qDone {
		if d.written && r.remoteOnly && j.versions.Fpcalc == r.server.Fpcalc && j.versions.FFmpeg == r.server.FFmpeg {
			r.noteReferenceWrite(it, j)
		}
		r.setState(j.idx, qDone)
	} else {
		r.requeueItem(j.idx)
	}
	return d.status, d.reason
}

// decideErrorOutcome handles a worker-reported failure. Only the server ever
// decides that a file is gone or cannot be decoded: not_found is re-statted
// here, decode errors and worker-side path refusals go to the server lane
// (which retries and makes its own tombstone decision), and a second timeout
// does too.
func (r *hubRun) decideErrorOutcome(j *workerJob, res workerapi.JobResult, it windowItem) resultDecision {
	hubLog.Debug("worker %s job %s: %s: %s", logger.SanitizeLogValue(j.worker), j.id, res.Outcome, logger.SanitizeLogValue(res.Error))
	switch res.Outcome {
	case workerapi.OutcomeNotFound:
		fi, err := os.Stat(it.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Gone on the server too: nothing to cut, and no tombstone (a
			// tombstone describes a file that exists but cannot be read).
			r.tally.transient.Add(1)
			return resultDecision{status: workerapi.StatusAccepted, reason: "gone_on_server", next: qDone}
		case err != nil:
			return resultDecision{status: workerapi.StatusAccepted, reason: "server_lane", next: qPending, serverOnly: true, why: "server_stat_failed"}
		case fi.Size() == j.size && fi.ModTime().Unix() == j.mtime:
			// The server sees the file as leased: the worker's mount is the
			// problem, so only the server cuts it.
			return resultDecision{status: workerapi.StatusAccepted, reason: "server_lane", next: qPending, serverOnly: true, why: "worker_not_found"}
		default:
			return resultDecision{status: workerapi.StatusAccepted, reason: "changed_requeued", next: qPending}
		}
	case workerapi.OutcomeDecodeError:
		return resultDecision{status: workerapi.StatusAccepted, reason: "server_lane", next: qPending, serverOnly: true, why: "worker_decode_error"}
	case workerapi.OutcomeRejected:
		return resultDecision{status: workerapi.StatusAccepted, reason: "server_lane", next: qPending, serverOnly: true, why: "worker_rejected"}
	case workerapi.OutcomeTimeout:
		return resultDecision{status: workerapi.StatusAccepted, reason: "requeued", next: qPending, timeout: true}
	default: // OutcomeStale
		return resultDecision{status: workerapi.StatusAccepted, reason: "requeued", next: qPending}
	}
}

// writeResult validates an ok result, re-stats the server's file and writes
// the windows through storeWindows (the server lane's own write).
//
// The row's host is the LEASEHOLDER (j.worker), which applyResult has
// already checked equals the poster.
func (r *hubRun) writeResult(j *workerJob, res workerapi.JobResult, it windowItem) resultDecision {
	prints, why := validateWorkerResult(j, res)
	if why != "" {
		hubLog.Warn("worker %s job %s (%s) rejected: %s", logger.SanitizeLogValue(j.worker), j.id, j.ref, why)
		return resultDecision{status: workerapi.StatusRejected, reason: why, next: qPending, serverOnly: true, why: "invalid_worker_result"}
	}
	fi, err := os.Stat(it.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.tally.transient.Add(1)
		return resultDecision{status: workerapi.StatusStale, reason: "gone_on_server", next: qDone}
	case err != nil:
		return resultDecision{status: workerapi.StatusStale, reason: "server_stat_failed", next: qPending, serverOnly: true, why: "server_stat_failed"}
	case fi.Size() != j.size || fi.ModTime().Unix() != j.mtime:
		r.tally.stale.Add(1)
		return resultDecision{status: workerapi.StatusStale, reason: "changed_on_server", next: qPending}
	}
	it.Size, it.MtimeUnix = j.size, j.mtime
	err = r.p.storeWindows(it, prints, windowProvenance{host: workerHostPrefix + j.worker, leaseID: j.lease}, r.tally)
	switch {
	case err == nil:
		return resultDecision{status: workerapi.StatusAccepted, next: qDone, written: true}
	case errors.Is(err, database.ErrFingerprintWindowRowGone):
		return resultDecision{status: workerapi.StatusStale, reason: "row_gone", next: qDone}
	default:
		return resultDecision{status: workerapi.StatusStale, reason: "server_write_failed", next: qPending, serverOnly: true, why: "server_write_failed"}
	}
}

// workerHostPrefix marks a window a remote worker computed.
const workerHostPrefix = "fp-worker:"

// repostOfStored answers an ok result for a job the hub no longer tracks
// (pruned when its file finished, or from an earlier run): byte-identical,
// slot for slot, to the stored windows of its ref is a duplicate; different
// from stored windows is refused; with nothing stored it is an unknown job.
// Nothing is ever written from here.
func (r *hubRun) repostOfStored(res workerapi.JobResult) (string, string) {
	if !strings.HasPrefix(res.Ref, "f:") || len(res.Windows) == 0 {
		return workerapi.StatusStale, "unknown_job"
	}
	stored, err := r.p.store.GetFingerprintWindows(database.FingerprintWindowRef(res.Ref))
	if err != nil {
		return workerapi.StatusStale, "unknown_job"
	}
	bySlot := map[int][]byte{}
	for _, w := range stored {
		if w.Kind == database.WindowKindWindow && w.Pipeline == fingerprint.WindowPipelineID {
			bySlot[w.SlotBP] = w.Raw
		}
	}
	if len(bySlot) == 0 {
		return workerapi.StatusStale, "unknown_job"
	}
	if len(bySlot) != len(res.Windows) {
		return workerapi.StatusRejected, "conflicts_with_stored"
	}
	for _, w := range res.Windows {
		raw, err := base64.StdEncoding.DecodeString(w.RawB64)
		prev, ok := bySlot[w.SlotBP]
		if err != nil || !ok || string(raw) != string(prev) {
			return workerapi.StatusRejected, "conflicts_with_stored"
		}
	}
	return workerapi.StatusDuplicate, ""
}

// validateWorkerResult checks an ok result against its job and decodes it. It
// returns the prints, or a non-empty reason.
func validateWorkerResult(j *workerJob, res workerapi.JobResult) ([]*fingerprint.WindowPrint, string) {
	if res.Pipeline != fingerprint.WindowPipelineID {
		return nil, "pipeline_mismatch"
	}
	if res.FpcalcVersion != j.versions.Fpcalc || res.FFmpegVersion != j.versions.FFmpeg {
		return nil, "tool_versions_mismatch"
	}
	if len(res.Windows) != len(j.specs) {
		return nil, "window_count_mismatch"
	}
	bySlot := make(map[int]workerapi.WindowResult, len(res.Windows))
	for _, w := range res.Windows {
		if _, dup := bySlot[w.SlotBP]; dup {
			return nil, "duplicate_slot"
		}
		bySlot[w.SlotBP] = w
	}
	out := make([]*fingerprint.WindowPrint, 0, len(j.specs))
	for _, s := range j.specs {
		w, ok := bySlot[s.SlotBP]
		if !ok {
			return nil, "window_slot_mismatch"
		}
		if w.Kind != string(s.Kind) || w.CoversWhole != s.CoversWhole ||
			math.Abs(w.OffsetSec-s.OffsetSec) > 0.0005 || math.Abs(w.LengthSec-s.LengthSec) > 0.0005 {
			return nil, "window_spec_mismatch"
		}
		if w.Algorithm != fingerprint.WindowAlgorithm {
			return nil, "algorithm_mismatch"
		}
		raw, err := base64.StdEncoding.DecodeString(w.RawB64)
		if err != nil {
			return nil, "raw_not_base64"
		}
		if len(raw) == 0 || len(raw)%4 != 0 {
			return nil, "raw_not_uint32_frames"
		}
		frames := len(raw) / 4
		if w.Frames != frames {
			return nil, "frame_count_mismatch"
		}
		if frames < workerapi.MinFrames {
			return nil, "too_few_frames"
		}
		if math.IsNaN(w.DecodedSec) || math.IsInf(w.DecodedSec, 0) ||
			math.Abs(w.DecodedSec-s.LengthSec) > workerapi.DecodedTolerance*s.LengthSec {
			return nil, "decoded_length_mismatch"
		}
		out = append(out, &fingerprint.WindowPrint{
			Kind:            s.Kind,
			SlotBP:          s.SlotBP,
			WindowSet:       s.WindowSet,
			OffsetSec:       s.OffsetSec,
			LengthSec:       s.LengthSec,
			DecodedSec:      w.DecodedSec,
			CoversWhole:     s.CoversWhole,
			DurationUsedSec: j.dur.Sec,
			DurationSource:  j.dur.Source,
			Frames:          frames,
			Raw:             raw,
			Algorithm:       fingerprint.WindowAlgorithm,
			Pipeline:        fingerprint.WindowPipelineID,
			FpcalcVersion:   res.FpcalcVersion,
			FFmpegVersion:   res.FFmpegVersion,
		})
	}
	return out, ""
}
