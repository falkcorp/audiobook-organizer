// file: internal/diag/flight/flight.go
// version: 1.0.0
// guid: 77f150f0-6e2a-4983-9ab6-3b77b267821f
// last-edited: 2026-10-14

// Package flight runs one runtime/trace.FlightRecorder for the life of the
// server and writes its window to disk when a watchdog notices a stall.
//
// Two incident classes went silent before this existed: the 2026-07-05 dedup
// run that sat at 100% of one core for hours, and the 2026-09-24/25 search
// index wedge. Both were noticed by a watchdog (the ops registry's stuck /
// never_reported strikes, and the search-index stall check) that could only
// log. With the recorder running, the same moment now also leaves the last
// ~30 seconds of runtime trace in <data dir>/traces/, which `go tool trace`
// opens.
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

// Defaults (owner decision D38: a 32 MiB ring and at most 10 files). Prod data
// sits on ZFS with snapshots, so deleted traces keep occupying space until the
// snapshots expire; keep both numbers small.
const (
	defaultMinAge   = 30 * time.Second
	defaultMaxBytes = 32 << 20
	defaultMaxFiles = 10
	defaultMinGap   = 5 * time.Minute
	stopWait        = 5 * time.Second
	timeLayout      = "20060102T150405Z"
)

var (
	reasonPattern = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	// filePattern is the only shape Open, List and rotation accept: a bare
	// name with no separator, so neither a request nor a stray file can
	// reach outside Dir or delete something the recorder did not write.
	filePattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[a-z0-9_-]+\.trace$`)
)

var flightLog = logger.New("flight-recorder")

// Config configures a Recorder. Zero values take the defaults above.
type Config struct {
	// Dir is where snapshots are written. Empty disables recording.
	Dir string
	// MinAge is the window the recorder tries to keep (default 30s).
	MinAge time.Duration
	// MaxBytes caps the window and takes precedence over MinAge (default
	// 32 MiB). runtime/trace treats it as a hint.
	MaxBytes uint64
	// MaxFiles is how many snapshots Dir keeps; the oldest go first
	// (default 10).
	MaxFiles int
	// MinGap is the minimum time between two snapshots for the same reason
	// (default 5m).
	MinGap time.Duration
	// Disabled turns recording off, as ABK_FLIGHT_RECORDER=off does.
	Disabled bool
}

// FileInfo describes one snapshot in Dir.
type FileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modified"`
}

// Recorder owns the process's flight recorder and its snapshot directory.
type Recorder struct {
	dir      string
	maxFiles int
	minGap   time.Duration
	disabled bool
	fr       *trace.FlightRecorder
	clock    func() time.Time // injectable for tests

	// snapshotting is set while a WriteTo is in flight. Set under mu, cleared
	// by the writer goroutine.
	snapshotting atomic.Bool
	inflight     sync.WaitGroup

	mu           sync.Mutex
	recording    bool // Start succeeded and Stop has not run
	stopped      bool
	lastByReason map[string]time.Time
}

// New builds a Recorder without starting it. Recording begins at Start, so
// constructing one (as every test server does) costs nothing.
func New(cfg Config) *Recorder {
	r := &Recorder{
		dir:          cfg.Dir,
		maxFiles:     cfg.MaxFiles,
		minGap:       cfg.MinGap,
		clock:        time.Now,
		lastByReason: make(map[string]time.Time),
	}
	if r.maxFiles <= 0 {
		r.maxFiles = defaultMaxFiles
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
	flightLog.Info("flight recorder started: dir=%s max_files=%d min_gap=%s", r.dir, r.maxFiles, r.minGap)
	return nil
}

// Snapshot asks for the current window to be written to Dir, tagged with
// reason. It returns at once. The request is dropped when the recorder is not
// recording, when a snapshot is already being written, or when the same reason
// was snapshotted less than MinGap ago. A reason that is not
// [a-z0-9_-]{1,40} is recorded as "unknown".
func (r *Recorder) Snapshot(reason string) {
	if r == nil || r.disabled {
		return
	}
	if !reasonPattern.MatchString(reason) {
		reason = "unknown"
	}
	r.mu.Lock()
	if !r.recording || r.snapshotting.Load() {
		r.mu.Unlock()
		return
	}
	now := r.clock()
	if last, ok := r.lastByReason[reason]; ok && now.Sub(last) < r.minGap {
		r.mu.Unlock()
		return
	}
	r.lastByReason[reason] = now
	r.snapshotting.Store(true)
	r.inflight.Add(1)
	r.mu.Unlock()

	go r.write(now, reason)
}

// write runs on its own goroutine: one WriteTo into a temp file, a rename,
// then rotation. It logs its outcome and never panics.
func (r *Recorder) write(at time.Time, reason string) {
	defer r.inflight.Done()
	defer r.snapshotting.Store(false)
	defer func() {
		if p := recover(); p != nil {
			flightLog.Warn("snapshot %s panicked: %v", reason, p)
		}
	}()

	name := at.UTC().Format(timeLayout) + "-" + reason + ".trace"
	final := filepath.Join(r.dir, name)
	// The temp name ends in .tmp, so List and rotation never see a partial file.
	tmp := final + ".tmp"
	n, err := r.writeFile(tmp)
	if err == nil {
		err = os.Rename(tmp, final)
	}
	if err != nil {
		_ = os.Remove(tmp)
		flightLog.Warn("snapshot %s failed: %v", reason, err)
		return
	}
	flightLog.Info("wrote %s (%d bytes)", name, n)
	r.rotate()
}

func (r *Recorder) writeFile(path string) (int64, error) {
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

// rotate deletes the oldest snapshots until at most maxFiles remain. Only
// names matching filePattern are considered; anything else in Dir is left
// alone. Names sort chronologically because they start with the UTC time.
func (r *Recorder) rotate() {
	names, err := r.snapshotNames()
	if err != nil {
		flightLog.Warn("rotation skipped: %v", err)
		return
	}
	for len(names) > r.maxFiles {
		if err := os.Remove(filepath.Join(r.dir, names[0])); err != nil && !errors.Is(err, fs.ErrNotExist) {
			flightLog.Warn("could not remove old snapshot %s: %v", names[0], err)
		}
		names = names[1:]
	}
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

// List returns the snapshots in Dir, newest first. A missing Dir is an empty
// list, not an error.
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

// Open opens one snapshot for reading. name must be a bare snapshot file name
// (filePattern); anything else, including a path, is fs.ErrNotExist. The file
// is opened through os.DirFS rooted at Dir, never by joining name onto a path.
func (r *Recorder) Open(name string) (fs.File, error) {
	if r == nil || r.dir == "" || !filePattern.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	f, err := os.DirFS(r.dir).Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// Stop ends recording. It waits up to 5s for an in-flight snapshot first,
// because trace.FlightRecorder.Stop itself blocks until WriteTo returns; past
// the cap it logs and lets the recorder stop in the background so shutdown is
// never held by a slow disk. Safe to call more than once.
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
	case <-time.After(stopWait):
		flightLog.Warn("snapshot still writing after %s; stopping in the background", stopWait)
		go r.fr.Stop()
	}
}
