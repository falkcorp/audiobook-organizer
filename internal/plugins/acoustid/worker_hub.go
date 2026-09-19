// file: internal/plugins/acoustid/worker_hub.go
// version: 1.0.0
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

var hubLog = logger.New("acoustid.worker-hub")

// Errors the HTTP layer maps to status codes (defined in workerapi so the
// handler package need not import this plugin).
var (
	ErrNoWindowRun      = workerapi.ErrNoRun
	ErrLeaseGone        = workerapi.ErrLeaseGone
	ErrToolsNotAllowed  = workerapi.ErrToolsNotAllowed
	ErrTooManyLeases    = workerapi.ErrTooManyLeases
	ErrBadWorkerRequest = workerapi.ErrBadRequest
)

// maxWorkerIDLen bounds the worker ID, which is logged and stamped on rows.
const maxWorkerIDLen = 128

// calibrationFiles is how many calibration files hello offers.
const calibrationFiles = 3

// calibrationHeadBytes is how much of a calibration file is hashed.
const calibrationHeadBytes = 64 << 10

// hubSweepEvery is the sweeper period; a variable so tests can shorten it.
var hubSweepEvery = workerapi.SweepEvery

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
	timeouts   int
	remote     int8
	rel        string // root-relative path when remote == remoteYes
	dur        fingerprint.DurationUsed
	specs      []fingerprint.WindowSpec
}

type workerLease struct {
	id       string
	worker   string
	expires  time.Time
	jobs     []string
	open     int // jobs still qLeased by this lease
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
}

// hubRun is one attached live run. Every field below mu-guarded is only
// touched with WorkerHub.mu held.
type hubRun struct {
	p       *Plugin
	tally   *windowTally
	server  fingerprint.ToolVersionInfo
	allowed []workerapi.ToolVersions
	roots   []pathutil.PathVar
	calib   []windowItem

	calibOnce sync.Once
	calibOut  []workerapi.CalibrationFile

	cancel    context.CancelFunc
	sweepDone chan struct{}
	changed   chan struct{} // cap 1: an item became done or was requeued

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
}

// WorkerHub is the lease manager. The zero value is not usable; Plugin owns
// one (Plugin.WorkerHub).
type WorkerHub struct {
	mu  sync.Mutex
	now func() time.Time
	run *hubRun
}

func newWorkerHub() *WorkerHub { return &WorkerHub{now: time.Now} }

// WorkerHub returns the plugin's lease manager, for the HTTP layer.
func (p *Plugin) WorkerHub() *WorkerHub {
	p.hubOnce.Do(func() { p.hub = newWorkerHub() })
	return p.hub
}

// allowedToolVersions is the server's own pair plus the configured allowlist.
func allowedToolVersions(server fingerprint.ToolVersionInfo) []workerapi.ToolVersions {
	out := []workerapi.ToolVersions{{Fpcalc: server.Fpcalc, FFmpeg: server.FFmpeg}}
	for _, e := range config.AppConfig.FingerprintWorkerToolVersions {
		fp, ff, ok := strings.Cut(strings.TrimSpace(e), "/")
		fp, ff = strings.TrimSpace(fp), strings.TrimSpace(ff)
		if !ok || fp == "" || ff == "" {
			continue
		}
		out = append(out, workerapi.ToolVersions{Fpcalc: fp, FFmpeg: ff})
	}
	return out
}

// windowVersionAllowed reports whether a stored window's tool pair counts as
// current: the server's own pair, or a configured allowlisted pair (proven
// byte-identical by a worker's parity gate). versions nil means unknown (a dry
// run without tools) and accepts anything, as before.
func windowVersionAllowed(fp, ff string, versions *fingerprint.ToolVersionInfo) bool {
	if versions == nil || (fp == versions.Fpcalc && ff == versions.FFmpeg) {
		return true
	}
	for _, e := range config.AppConfig.FingerprintWorkerToolVersions {
		efp, eff, ok := strings.Cut(strings.TrimSpace(e), "/")
		if ok && strings.TrimSpace(efp) == fp && strings.TrimSpace(eff) == ff && fp != "" && ff != "" {
			return true
		}
	}
	return false
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
func (h *WorkerHub) attach(ctx context.Context, p *Plugin, wt fingerprint.WindowTools, tally *windowTally, calib []windowItem) {
	sctx, cancel := context.WithCancel(ctx)
	r := &hubRun{
		p:         p,
		tally:     tally,
		server:    wt.Versions,
		allowed:   allowedToolVersions(wt.Versions),
		roots:     pathutil.PathVars(config.AppConfig.RootDir),
		calib:     calib,
		cancel:    cancel,
		sweepDone: make(chan struct{}),
		changed:   make(chan struct{}, 1),
		open:      map[int]struct{}{},
		leases:    map[string]*workerLease{},
		jobs:      map[string]*workerJob{},
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
func (h *WorkerHub) takeBacklog() []windowItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil {
		return nil
	}
	var out []windowItem
	for idx := range r.open {
		if r.st[idx].state == qPending {
			r.setState(idx, qServer)
			out = append(out, r.items[idx])
		}
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
		delete(r.open, idx)
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
		if ok && root == "libroot" {
			if dur, err := fingerprint.ChooseDuration(it.FpDuration, it.Duration, nil); err == nil {
				if specs, perr := fingerprint.PlanWindows(dur, fingerprint.WindowSetWS1); perr == nil && len(specs) > 0 {
					st.remote, st.rel, st.dur, st.specs = remoteYes, rel, dur, specs
				}
			}
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
		k := 0
		for _, jid := range l.jobs {
			if j := r.jobs[jid]; j != nil && r.st[j.idx].state == qLeased && r.st[j.idx].lease == id {
				r.requeueItem(j.idx)
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
func (h *WorkerHub) Hello(_ context.Context) (*workerapi.HelloResponse, error) {
	r, err := h.liveRun()
	if err != nil {
		return nil, err
	}
	r.calibOnce.Do(func() { r.calibOut = r.buildCalibration() })
	resp := &workerapi.HelloResponse{
		Pipeline:     fingerprint.WindowPipelineID,
		WindowSet:    fingerprint.WindowSetWS1,
		ToolVersions: r.allowed,
		Calibration:  r.calibOut,
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
	if resp.Calibration == nil {
		resp.Calibration = []workerapi.CalibrationFile{}
	}
	return resp, nil
}

// buildCalibration reads the calibration candidates the plan chose: files
// under libroot whose stored windows are current. A candidate whose bytes
// moved since is dropped.
func (r *hubRun) buildCalibration() []workerapi.CalibrationFile {
	out := []workerapi.CalibrationFile{}
	for _, it := range r.calib {
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
		head, err := headSHA256(it.Path)
		if err != nil {
			continue
		}
		cf := workerapi.CalibrationFile{Root: root, RelB64: base64.StdEncoding.EncodeToString([]byte(rel)), Rel: rel,
			Size: it.Size, MtimeUnix: it.MtimeUnix, Head64K: head}
		for _, w := range stored {
			if w.Kind != database.WindowKindWindow || w.Pipeline != fingerprint.WindowPipelineID ||
				w.SourceSize != it.Size || w.SourceMtimeUnix != it.MtimeUnix {
				continue
			}
			sum := sha256.Sum256(w.Raw)
			cf.Windows = append(cf.Windows, workerapi.CalibrationWindow{
				Window: workerapi.Window{Kind: string(w.Kind), SlotBP: w.SlotBP, OffsetSec: w.OffsetSec,
					LengthSec: w.LengthSec, CoversWhole: w.CoversWhole},
				RawSHA256: hex.EncodeToString(sum[:]),
				Frames:    w.Frames,
			})
		}
		if len(cf.Windows) > 0 {
			out = append(out, cf)
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
	if req.Pipeline != fingerprint.WindowPipelineID || !versionsAllowed(r.allowed, req.FpcalcVersion, req.FFmpegVersion) {
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: pipeline=%q fpcalc=%q ffmpeg=%q", ErrToolsNotAllowed, req.Pipeline, req.FpcalcVersion, req.FFmpegVersion)
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
		if fi, err := os.Stat(c.path); err == nil && fi.Mode().IsRegular() {
			stats[i] = fi
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run != r || r.gen != gen {
		return nil, nil // the tier moved on while we statted
	}
	resp := &workerapi.LeaseResponse{LeaseID: leaseID}
	for i, c := range cands {
		st := &r.st[c.idx]
		if st.state != qLeased || st.lease != leaseID {
			continue
		}
		if stats[i] == nil {
			// Unstattable here: the server lane owns the diagnosis.
			st.serverOnly = true
			r.requeueItem(c.idx)
			continue
		}
		it := r.items[c.idx]
		jid := fmt.Sprintf("%s-%d", leaseID, len(lease.jobs))
		j := &workerJob{id: jid, lease: leaseID, worker: req.WorkerID, idx: c.idx, ref: database.FileWindowRef(it.FileID),
			size: stats[i].Size(), mtime: stats[i].ModTime().Unix(), dur: st.dur, specs: st.specs, versions: lease.versions}
		r.jobs[jid] = j
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
func (h *WorkerHub) Renew(leaseID string) (*workerapi.RenewResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.run
	if r == nil {
		return nil, ErrNoWindowRun
	}
	l := r.leases[leaseID]
	now := h.now()
	if l == nil || !now.Before(l.expires) {
		return nil, ErrLeaseGone
	}
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
	if l == nil {
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
	if _, err := h.liveRun(); err != nil {
		return nil, err
	}
	resp := &workerapi.ResultsResponse{Statuses: make([]workerapi.JobStatus, 0, len(req.Results))}
	for _, res := range req.Results {
		status, reason := h.applyResult(req.WorkerID, res)
		resp.Statuses = append(resp.Statuses, workerapi.JobStatus{JobID: res.JobID, Status: status, Reason: reason})
	}
	return resp, nil
}

// resultDecision is what one posted result does to its item.
type resultDecision struct {
	status, reason string
	next           qState // qDone or qPending (requeued at the head of the tier)
	serverOnly     bool
	timeout        bool
}

// applyResult is one job's result. Ownership moves under mu; validation, the
// server's stat and the store write happen outside it with the item in
// qWriting, so no other lane can touch the file meanwhile and a slow stat never
// stalls the hub.
func (h *WorkerHub) applyResult(worker string, res workerapi.JobResult) (string, string) {
	switch res.Outcome {
	case workerapi.OutcomeOK, workerapi.OutcomeNotFound, workerapi.OutcomeDecodeError,
		workerapi.OutcomeTimeout, workerapi.OutcomeStale, workerapi.OutcomeRejected:
	default:
		return workerapi.StatusRejected, "bad_outcome"
	}
	h.mu.Lock()
	r := h.run
	if r == nil {
		h.mu.Unlock()
		return workerapi.StatusStale, "no_run"
	}
	j := r.jobs[res.JobID]
	if j == nil {
		h.mu.Unlock()
		return workerapi.StatusStale, "unknown_job"
	}
	if res.Ref != string(j.ref) {
		h.mu.Unlock()
		return workerapi.StatusRejected, "ref_mismatch"
	}
	st := &r.st[j.idx]
	it := r.items[j.idx]
	gen := r.gen
	switch {
	case st.state == qDone:
		h.mu.Unlock()
		if res.Outcome != workerapi.OutcomeOK {
			return workerapi.StatusDuplicate, "already_done"
		}
		return r.compareWithStored(j, res)
	case st.state == qWriting:
		h.mu.Unlock()
		return workerapi.StatusStale, "in_flight"
	case st.state == qServer:
		h.mu.Unlock()
		return workerapi.StatusStale, "server_claimed"
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
		d = r.writeResult(worker, j, res, it)
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
			d.serverOnly, d.reason = true, "server_lane"
		}
	}
	if d.serverOnly {
		st.serverOnly = true
	}
	if d.next == qDone {
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
			return resultDecision{status: workerapi.StatusAccepted, reason: "server_lane", next: qPending, serverOnly: true}
		case fi.Size() == j.size && fi.ModTime().Unix() == j.mtime:
			// The server sees the file as leased: the worker's mount is the
			// problem, so only the server cuts it.
			return resultDecision{status: workerapi.StatusAccepted, reason: "server_lane", next: qPending, serverOnly: true}
		default:
			return resultDecision{status: workerapi.StatusAccepted, reason: "changed_requeued", next: qPending}
		}
	case workerapi.OutcomeDecodeError, workerapi.OutcomeRejected:
		return resultDecision{status: workerapi.StatusAccepted, reason: "server_lane", next: qPending, serverOnly: true}
	case workerapi.OutcomeTimeout:
		return resultDecision{status: workerapi.StatusAccepted, reason: "requeued", next: qPending, timeout: true}
	default: // OutcomeStale
		return resultDecision{status: workerapi.StatusAccepted, reason: "requeued", next: qPending}
	}
}

// writeResult validates an ok result, re-stats the server's file and writes
// the windows through storeWindows (the server lane's own write).
func (r *hubRun) writeResult(worker string, j *workerJob, res workerapi.JobResult, it windowItem) resultDecision {
	prints, why := validateWorkerResult(j, res)
	if why != "" {
		hubLog.Warn("worker %s job %s (%s) rejected: %s", logger.SanitizeLogValue(worker), j.id, j.ref, why)
		return resultDecision{status: workerapi.StatusRejected, reason: why, next: qPending, serverOnly: true}
	}
	fi, err := os.Stat(it.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.tally.transient.Add(1)
		return resultDecision{status: workerapi.StatusStale, reason: "gone_on_server", next: qDone}
	case err != nil:
		return resultDecision{status: workerapi.StatusStale, reason: "server_stat_failed", next: qPending, serverOnly: true}
	case fi.Size() != j.size || fi.ModTime().Unix() != j.mtime:
		r.tally.stale.Add(1)
		return resultDecision{status: workerapi.StatusStale, reason: "changed_on_server", next: qPending}
	}
	it.Size, it.MtimeUnix = j.size, j.mtime
	err = r.p.storeWindows(it, prints, windowProvenance{host: "fp-worker:" + worker, leaseID: j.lease}, r.tally)
	switch {
	case err == nil:
		return resultDecision{status: workerapi.StatusAccepted, next: qDone}
	case errors.Is(err, database.ErrFingerprintWindowRowGone):
		return resultDecision{status: workerapi.StatusStale, reason: "row_gone", next: qDone}
	default:
		return resultDecision{status: workerapi.StatusStale, reason: "server_write_failed", next: qPending, serverOnly: true}
	}
}

// compareWithStored answers a result for a job already done: byte-identical
// to what is stored is a duplicate; anything else is refused, never written.
func (r *hubRun) compareWithStored(j *workerJob, res workerapi.JobResult) (string, string) {
	prints, why := validateWorkerResult(j, res)
	if why != "" {
		return workerapi.StatusRejected, why
	}
	stored, err := r.p.store.GetFingerprintWindows(j.ref)
	if err != nil {
		return workerapi.StatusStale, "server_read_failed"
	}
	bySlot := map[int][]byte{}
	for _, w := range stored {
		if w.Kind == database.WindowKindWindow && w.Pipeline == fingerprint.WindowPipelineID {
			bySlot[w.SlotBP] = w.Raw
		}
	}
	if len(bySlot) == 0 {
		return workerapi.StatusStale, "already_resolved"
	}
	if len(bySlot) != len(prints) {
		return workerapi.StatusRejected, "conflicts_with_stored"
	}
	for _, wp := range prints {
		raw, ok := bySlot[wp.SlotBP]
		if !ok || string(raw) != string(wp.Raw) {
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
