// file: internal/diag/flight/flight.go
// version: 1.1.1
// guid: 77f150f0-6e2a-4983-9ab6-3b77b267821f
// last-edited: 2026-10-19

// Package flight runs one runtime/trace.FlightRecorder for the life of the
// server and writes its window to disk when a watchdog notices a stall.
//
// Two incident classes went silent before this existed: the 2026-07-05 dedup
// run that sat at 100% of one core for hours, and the 2026-09-24/25 search
// index wedge. Both were noticed by a watchdog (the ops registry's stuck /
// never_reported strikes, and the search-index stall check) that could only
// log. With the recorder running, the same moment now also leaves the last
// few seconds of runtime trace in <data dir>/traces/, which `go tool trace`
// opens, next to a goroutine dump (debug=2) taken just after it.
//
// Invariants:
//   - At most one recorder is active per process (a runtime/trace rule), and
//     the API allows one WriteTo at a time. Snapshot enforces the second with
//     the snapshotting flag and simply drops a request that would overlap.
//   - Snapshot never blocks its caller. The watchdog goroutines that call it
//     are the only thing watching a wedged process, so they must not wait on
//     disk; the write happens on a goroutine of its own.
//   - A failure to start tracing never stops the server: Start returns the
//     error for the caller to log, and every method stays safe to call.
//   - Every method is nil-receiver safe and a no-op on a disabled recorder.
//
// What a snapshot is: a pair of files sharing one basename,
// <UTC time>-<reason>.trace and <UTC time>-<reason>.goroutines.txt. MaxFiles
// counts pairs; rotation removes both files of the oldest pair together.
//
// Repeat stalls back off per reason: the gap between two snapshots of the same
// reason starts at MinGap and doubles on every repeat up to 2h, and resets once
// the reason has been quiet (no requests, dropped ones included) for as long as
// the current gap. The first snapshot of each reason in a process is pinned:
// rotation never deletes it, so a flapping reason cannot push the first
// capture of an incident out of the directory. Pinned pairs have their own cap
// (MaxPinned) on top of MaxFiles.
//
// Recording is switched off by ABK_FLIGHT_RECORDER=off, by Config.Disabled,
// or by an empty Config.Dir. List and Open stay usable on a recorder that is
// not recording when Dir is set, so traces captured before the switch was
// flipped can still be downloaded.
package flight

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime/pprof"
	"runtime/trace"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// EnvSwitch names the environment variable that turns recording off when set
// to "off" (case-insensitive). It is the no-code rollback for this package.
const EnvSwitch = "ABK_FLIGHT_RECORDER"

// Defaults. Prod data sits on ZFS with snapshots, so deleted traces keep
// occupying space until the snapshots expire; keep these small.
//
// defaultMaxBytes is 8 MiB, not the 32 MiB of owner decision D38: measured on
// the scanner core, 32 MiB cost 73-86 MB of RSS and 33-65 MB per snapshot file
// while 4 MiB cost 17 MB and 4.8 MB, and MaxBytes does not change CPU. 8 MiB
// keeps the recorder on by default at a bounded cost.
const (
	defaultMinAge    = 10 * time.Second
	defaultMaxBytes  = 8 << 20
	defaultMaxFiles  = 10
	defaultMaxPinned = 4
	defaultMinGap    = 5 * time.Minute
	maxGapCeiling    = 2 * time.Hour
	stopWait         = 5 * time.Second
	timeLayout       = "20060102T150405Z"

	// dropLogEvery bounds the "request dropped" log to one line per drop kind
	// and reason per interval, so a stall that re-fires every watchdog tick
	// cannot flood the log.
	dropLogEvery = time.Minute
	// maxCovered bounds how many extra reasons one snapshot's name records.
	// 4 reasons of up to 40 bytes plus separators keep the longest name under
	// the 255-byte file name limit.
	maxCovered  = 3
	coveredJoin = "-and-"
	traceSuffix = ".trace"
	gorSuffix   = ".goroutines.txt"
	tmpSuffix   = ".tmp"
)

var (
	reasonPattern = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	// filePattern is the only shape Open, List and rotation accept: a bare
	// name with no separator, so neither a request nor a stray file can
	// reach outside Dir or delete something the recorder did not write.
	// Group 1 is the pair's basename, group 2 the file kind.
	filePattern = regexp.MustCompile(`^([0-9]{8}T[0-9]{6}Z-[a-z0-9_-]+)(\.trace|\.goroutines\.txt)$`)
)

var flightLog = logger.New("flight-recorder")

// Config configures a Recorder. Zero values take the defaults above.
type Config struct {
	// Dir is where snapshots are written. Empty disables recording.
	Dir string
	// MinAge is the window the recorder tries to keep (default 10s).
	MinAge time.Duration
	// MaxBytes caps the window and takes precedence over MinAge (default
	// 8 MiB). runtime/trace treats it as a hint: the runtime always keeps at
	// least the last complete generation (about 1 s of trace), so a busy
	// process holds, and a snapshot file reaches, more than MaxBytes. Observed
	// files ran about 1.1x MaxBytes at 4 MiB and up to 2x at 32 MiB.
	MaxBytes uint64
	// MaxFiles is how many unpinned snapshots (trace plus goroutine dump) Dir
	// keeps; the oldest go first (default 10).
	MaxFiles int
	// MaxPinned is how many first-of-their-reason snapshots rotation never
	// deletes, on top of MaxFiles (default 4: the three callers' reasons plus
	// "unknown"). A negative value disables pinning.
	MaxPinned int
	// MinGap is the minimum time between two snapshots for the same reason
	// until a repeat doubles it (default 5m; the doubling stops at 2h).
	MinGap time.Duration
	// Disabled turns recording off, as ABK_FLIGHT_RECORDER=off does.
	Disabled bool
}

// FileInfo describes one snapshot file in Dir.
type FileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modified"`
}

// reasonState is the per-reason rate-limit and pin bookkeeping, guarded by
// Recorder.mu.
type reasonState struct {
	// last is when this reason was last snapshotted. Zero means "no snapshot
	// in the current run of repeats": the first one, or the first after a
	// quiet period.
	last time.Time
	// lastSeen is the last request for this reason, dropped ones included: a
	// stall that keeps re-firing is not quiet even while it is rate limited.
	lastSeen time.Time
	// gap is the minimum spacing enforced after last.
	gap time.Duration
	// pinDone is set once this reason's first snapshot of the process has been
	// considered for pinning. It survives quiet resets.
	pinDone bool
}

// Recorder owns the process's flight recorder and its snapshot directory.
type Recorder struct {
	dir       string
	maxFiles  int
	maxPinned int
	minGap    time.Duration
	disabled  bool
	fr        *trace.FlightRecorder
	clock     func() time.Time // injectable for tests
	stopWait  time.Duration    // injectable for tests
	// dropf logs a dropped request; injectable for tests.
	dropf func(format string, args ...any)
	// afterTrace, when set, runs on the writer goroutine after the trace file
	// is written and before the covered reasons are frozen. Tests use it to
	// hold a write open.
	afterTrace func()

	// snapshotting is set while a write is in flight. Set under mu, cleared
	// by the writer goroutine under mu.
	snapshotting atomic.Bool
	inflight     sync.WaitGroup

	mu         sync.Mutex
	recording  bool // Start succeeded and Stop has not run
	stopped    bool
	reasons    map[string]*reasonState
	pinned     map[string]struct{} // pair basenames rotation must keep
	writing    string              // reason of the in-flight write
	covered    []string            // other reasons requested during the in-flight write
	coverFroze bool                // the in-flight write has fixed its name
	dropLog    map[string]time.Time
}

// New builds a Recorder without starting it. Recording begins at Start, so
// constructing one (as every test server does) costs nothing.
func New(cfg Config) *Recorder {
	r := &Recorder{
		dir:       cfg.Dir,
		maxFiles:  cfg.MaxFiles,
		maxPinned: cfg.MaxPinned,
		minGap:    cfg.MinGap,
		clock:     time.Now,
		stopWait:  stopWait,
		dropf:     flightLog.Info,
		reasons:   make(map[string]*reasonState),
		pinned:    make(map[string]struct{}),
		dropLog:   make(map[string]time.Time),
	}
	if r.maxFiles <= 0 {
		r.maxFiles = defaultMaxFiles
	}
	if r.maxPinned == 0 {
		r.maxPinned = defaultMaxPinned
	}
	if r.minGap <= 0 {
		r.minGap = defaultMinGap
	}
	r.disabled = cfg.Disabled || cfg.Dir == "" ||
		strings.EqualFold(strings.TrimSpace(os.Getenv(EnvSwitch)), "off")
	if r.disabled {
		return r
	}
	minAge, maxBytes := cfg.MinAge, cfg.MaxBytes
	if minAge <= 0 {
		minAge = defaultMinAge
	}
	if maxBytes == 0 {
		maxBytes = defaultMaxBytes
	}
	r.fr = trace.NewFlightRecorder(trace.FlightRecorderConfig{MinAge: minAge, MaxBytes: maxBytes})
	return r
}

// Start creates Dir (mode 0750) and starts recording. It returns nil on a
// disabled recorder. On error the recorder stays usable and records nothing;
// the caller logs the error and carries on.
func (r *Recorder) Start() error {
	if r == nil || r.disabled {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return errors.New("flight recorder: already stopped")
	}
	if r.recording {
		return nil
	}
	if err := os.MkdirAll(r.dir, 0o750); err != nil {
		return fmt.Errorf("flight recorder: create trace dir: %w", err)
	}
	if err := r.fr.Start(); err != nil {
		return fmt.Errorf("flight recorder: start: %w", err)
	}
	r.recording = true
	flightLog.Info("flight recorder started: dir=%s max_files=%d max_pinned=%d min_gap=%s", r.dir, r.maxFiles, r.maxPinned, r.minGap)
	return nil
}

// Snapshot asks for the current window to be written to Dir, tagged with
// reason. It returns at once. The request is dropped, with a rate-limited Info
// line saying why, when the recorder is not recording, when the same reason
// was snapshotted less than its current gap ago (MinGap, doubling on repeats),
// or when a snapshot is already being written. In the last case the request
// is remembered: if the reason was otherwise allowed, the in-flight snapshot's
// name records that it covers this reason too. A reason that is not
// [a-z0-9_-]{1,40} is recorded as "unknown".
func (r *Recorder) Snapshot(reason string) {
	if r == nil || r.disabled {
		return
	}
	if !reasonPattern.MatchString(reason) {
		reason = "unknown"
	}
	r.mu.Lock()
	now := r.clock()
	if !r.recording {
		log := r.shouldLogDropLocked(now, "not-recording", reason)
		r.mu.Unlock()
		if log {
			r.dropf("snapshot %s dropped: the flight recorder is not recording", reason)
		}
		return
	}
	if !r.admitLocked(reason, now) {
		log := r.shouldLogDropLocked(now, "rate-limited", reason)
		r.mu.Unlock()
		if log {
			r.dropf("snapshot %s dropped: inside the rate-limit gap for this reason", reason)
		}
		return
	}
	if r.snapshotting.Load() {
		covered := false
		if reason != r.writing && !r.coverFroze {
			covered = r.noteCoveredLocked(reason)
		}
		log := r.shouldLogDropLocked(now, "in-flight", reason)
		r.mu.Unlock()
		if log {
			if covered {
				r.dropf("snapshot %s not written separately: a snapshot is in flight and its name will record that it covers this reason", reason)
			} else {
				r.dropf("snapshot %s dropped: a snapshot is already being written", reason)
			}
		}
		return
	}
	r.snapshotting.Store(true)
	r.inflight.Add(1)
	r.writing = reason
	r.covered = nil
	r.coverFroze = false
	r.mu.Unlock()

	go r.write(now, reason)
}

// admitLocked applies the per-reason gap. It always records the request in
// lastSeen. A reason that has been quiet for as long as its current gap starts
// over: the next snapshot is a first, at MinGap spacing. Callers hold mu.
func (r *Recorder) admitLocked(reason string, now time.Time) bool {
	st := r.reasons[reason]
	if st == nil {
		st = &reasonState{gap: r.minGap}
		r.reasons[reason] = st
	}
	quiet := !st.lastSeen.IsZero() && now.Sub(st.lastSeen) >= st.gap
	st.lastSeen = now
	if quiet {
		st.last = time.Time{}
		st.gap = r.minGap
	}
	return st.last.IsZero() || now.Sub(st.last) >= st.gap
}

// noteCoveredLocked remembers reason as covered by the in-flight write. It
// reports whether the reason was recorded. Callers hold mu.
func (r *Recorder) noteCoveredLocked(reason string) bool {
	for _, c := range r.covered {
		if c == reason {
			return true
		}
	}
	if len(r.covered) >= maxCovered {
		return false
	}
	r.covered = append(r.covered, reason)
	return true
}

// shouldLogDropLocked reports whether a dropped request of this kind and
// reason should be logged now, and stamps it if so. Callers hold mu.
func (r *Recorder) shouldLogDropLocked(now time.Time, kind, reason string) bool {
	key := kind + "/" + reason
	if last, ok := r.dropLog[key]; ok && now.Sub(last) < dropLogEvery {
		return false
	}
	r.dropLog[key] = now
	return true
}

// write runs on its own goroutine: one WriteTo into a temp file, the goroutine
// dump, the renames, then rotation. It logs its outcome and never panics.
func (r *Recorder) write(at time.Time, reason string) {
	defer r.inflight.Done()
	defer func() {
		r.mu.Lock()
		r.snapshotting.Store(false)
		r.mu.Unlock()
	}()
	defer func() {
		if p := recover(); p != nil {
			flightLog.Warn("snapshot %s panicked: %v", reason, p)
		}
	}()

	// Temp names end in .tmp, so List and rotation never see a partial file.
	prefix := filepath.Join(r.dir, at.UTC().Format(timeLayout)+"-"+reason)
	traceTmp := prefix + traceSuffix + tmpSuffix
	n, err := r.writeTrace(traceTmp)
	if err != nil {
		_ = os.Remove(traceTmp)
		flightLog.Warn("snapshot %s failed: %v", reason, err)
		return
	}
	// The goroutine dump follows the trace and is skipped when the trace
	// failed: a persistent write failure (a full disk) must not cost a
	// stop-the-world goroutine dump on every retry.
	dumpTmp := prefix + gorSuffix + tmpSuffix
	haveDump := true
	if derr := r.writeGoroutines(dumpTmp); derr != nil {
		_ = os.Remove(dumpTmp)
		haveDump = false
		flightLog.Warn("goroutine dump for %s failed: %v", reason, derr)
	}
	if r.afterTrace != nil {
		r.afterTrace()
	}

	// Fix the name now: reasons requested while this write ran are part of it.
	covered := r.freezeCovered(reason)
	var base strings.Builder
	base.WriteString(filepath.Base(prefix))
	for _, c := range covered {
		base.WriteString(coveredJoin + c)
	}
	final := filepath.Join(r.dir, base.String())
	if err := os.Rename(traceTmp, final+traceSuffix); err != nil {
		_ = os.Remove(traceTmp)
		_ = os.Remove(dumpTmp)
		flightLog.Warn("snapshot %s failed: %v", reason, err)
		return
	}
	if haveDump {
		if err := os.Rename(dumpTmp, final+gorSuffix); err != nil {
			_ = os.Remove(dumpTmp)
			flightLog.Warn("goroutine dump for %s not saved: %v", reason, err)
		}
	}

	// Only now is the reason rate limited: a failed write leaves it free to
	// retry on the next strike.
	r.recordSuccess(at, base.String(), append([]string{reason}, covered...))
	flightLog.Info("wrote %s (%d bytes of trace, goroutine dump %t)", base.String()+traceSuffix, n, haveDump)
	r.rotate()
}

// freezeCovered fixes the in-flight write's covered reasons (excluding its own
// reason) and stops accepting more.
func (r *Recorder) freezeCovered(own string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.coverFroze = true
	var out []string
	for _, c := range r.covered {
		if c != own {
			out = append(out, c)
		}
	}
	r.covered = nil
	return out
}

// recordSuccess stamps each reason as snapshotted at `at` (doubling its gap
// when it is a repeat) and pins the pair for any reason whose first snapshot
// this is.
func (r *Recorder) recordSuccess(at time.Time, base string, reasons []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, reason := range reasons {
		st := r.reasons[reason]
		if st == nil {
			st = &reasonState{gap: r.minGap, lastSeen: at}
			r.reasons[reason] = st
		}
		if !st.last.IsZero() {
			st.gap = min(st.gap*2, maxGapCeiling)
		}
		st.last = at
		if !st.pinDone {
			st.pinDone = true
			if _, already := r.pinned[base]; already || (r.maxPinned > 0 && len(r.pinned) < r.maxPinned) {
				r.pinned[base] = struct{}{}
			}
		}
	}
}

func (r *Recorder) writeTrace(path string) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, err
	}
	n, werr := r.fr.WriteTo(f)
	cerr := f.Close()
	if werr != nil {
		return n, werr
	}
	return n, cerr
}

// writeGoroutines writes the full goroutine dump (debug=2: every stack, with
// the wait reason and how long each goroutine has waited).
func (r *Recorder) writeGoroutines(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	werr := pprof.Lookup("goroutine").WriteTo(f, 2)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// rotate deletes the oldest unpinned snapshots (both files of a pair together)
// until at most maxFiles remain. Pinned pairs are never deleted. Only names
// matching filePattern are considered; anything else in Dir is left alone.
// Names sort chronologically because they start with the UTC time.
func (r *Recorder) rotate() {
	pairs, err := r.pairs()
	if err != nil {
		flightLog.Warn("rotation skipped: %v", err)
		return
	}
	r.mu.Lock()
	var unpinned []pair
	for _, p := range pairs {
		if _, keep := r.pinned[p.base]; !keep {
			unpinned = append(unpinned, p)
		}
	}
	r.mu.Unlock()
	for len(unpinned) > r.maxFiles {
		for _, name := range unpinned[0].files {
			if err := os.Remove(filepath.Join(r.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				flightLog.Warn("could not remove old snapshot %s: %v", name, err)
			}
		}
		unpinned = unpinned[1:]
	}
}

// pair is one snapshot: the files in Dir that share a basename.
type pair struct {
	base  string
	files []string
}

// pairs groups the snapshot files in Dir by basename, oldest first.
func (r *Recorder) pairs() ([]pair, error) {
	names, err := r.snapshotNames()
	if err != nil {
		return nil, err
	}
	var out []pair
	idx := map[string]int{}
	for _, name := range names {
		base := filePattern.FindStringSubmatch(name)[1]
		i, ok := idx[base]
		if !ok {
			i = len(out)
			idx[base] = i
			out = append(out, pair{base: base})
		}
		out[i].files = append(out[i].files, name)
	}
	return out, nil
}

// snapshotNames returns the snapshot file names in Dir, oldest first.
func (r *Recorder) snapshotNames() ([]string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && filePattern.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// List returns the snapshot files in Dir, newest first (a pair's trace and
// goroutine dump are separate entries). A missing Dir is an empty list, not an
// error.
func (r *Recorder) List() ([]FileInfo, error) {
	if r == nil || r.dir == "" {
		return nil, nil
	}
	names, err := r.snapshotNames()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(names))
	for i := len(names) - 1; i >= 0; i-- {
		info, err := os.Lstat(filepath.Join(r.dir, names[i]))
		if err != nil {
			continue // rotated away between ReadDir and Lstat
		}
		out = append(out, FileInfo{Name: names[i], Size: info.Size(), ModTime: info.ModTime()})
	}
	return out, nil
}

// Open opens one snapshot file for reading. name must be a bare snapshot file
// name (filePattern); anything else, including a path or a symbolic link, is
// fs.ErrNotExist. The file is opened through an os.Root rooted at Dir, so even
// a link planted in Dir between the check and the open cannot lead outside it.
func (r *Recorder) Open(name string) (fs.File, error) {
	if r == nil || r.dir == "" || !filePattern.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return nil, err
	}
	// A file already opened stays valid after the Root is closed.
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fs.ErrNotExist
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// Stop ends recording. It first waits up to 5s for an in-flight snapshot.
// trace.FlightRecorder documents that Stop blocks until a concurrent WriteTo
// returns, but in go1.27.2 it does not: Stop clears the recorder's state and
// its enabled flag, a plain bool, without waiting, so Stop concurrent with
// WriteTo is a data race. The wait here is what serializes them. Past the cap
// the recorder is left running rather than stopped under a live WriteTo: the
// process is shutting down and a slow disk must not hold it. Safe to call more
// than once.
func (r *Recorder) Stop() {
	if r == nil || r.disabled {
		return
	}
	r.mu.Lock()
	wasRecording := r.recording
	r.recording = false
	r.stopped = true
	r.mu.Unlock()
	if !wasRecording {
		return
	}
	// No Snapshot can Add after this point: it checks recording under mu.
	done := make(chan struct{})
	go func() {
		r.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		r.fr.Stop()
		flightLog.Info("flight recorder stopped")
	case <-time.After(r.stopWait):
		flightLog.Warn("snapshot still writing after %s; leaving the flight recorder running for the exit", r.stopWait)
	}
}
