// file: internal/watcher/supervisor.go
// version: 1.1.0
// guid: 6d1f4a08-5c93-4b27-9e10-72a8c4f3bd15
// last-edited: 2026-08-11

package watcher

import (
	"log/slog"
	"sort"
	"sync"
	"time"
)

// DefaultReconcileInterval is how often the Supervisor re-reads the desired
// watch set. It is deliberately short relative to the periodic library scan:
// the scan is the guaranteed catch-all, the watchers are the low-latency path,
// and a path added through the UI should start being watched within minutes
// rather than at the next process restart.
const DefaultReconcileInterval = 5 * time.Minute

// managedWatcher is the slice of *Watcher that Supervisor drives. It exists so
// the reconcile logic can be unit-tested against a fake instead of real
// fsnotify handles (which need real directories and fire on real disk events).
type managedWatcher interface {
	Start(rootDir string) error
	Stop()
}

// PathsProvider returns the set of directories that SHOULD be watched right
// now. It is called on every reconcile tick — NOT once at boot — so that an
// import path added, enabled, disabled or removed at runtime is picked up
// without restarting the process.
//
// An error return is a hard "I don't know what the desired set is". The
// Supervisor treats that as transient and keeps the watchers it already has,
// because tearing down live watchers on a momentary DB read failure would
// silently convert a blip into a permanent blind spot.
type PathsProvider func() ([]string, error)

// Supervisor keeps one Watcher running per desired path, reconciling the live
// set against the desired set on a ticker.
//
// Before this existed, internal/server/server_lifecycle.go enumerated import
// paths exactly once at boot and started watchers from that snapshot, so:
//   - an import path added after boot was never watched, and
//   - a failed enumeration (`if err == nil && len(...) > 0`) silently started
//     zero watchers with no log line at all.
//
// Both of those are handled here instead.
type Supervisor struct {
	paths      PathsProvider
	newWatcher func() managedWatcher
	interval   time.Duration

	// onError, when non-nil, is called with every provider/start failure in
	// addition to the slog line. The server wires this to the activity log so
	// the failure is visible in the UI and not only in journalctl.
	onError func(error)

	mu     sync.Mutex
	active map[string]managedWatcher
	// closed is set by StopAll and makes every later Reconcile a no-op. The
	// server tears watchers down before it waits on its background WaitGroup,
	// so without this a tick landing in that window would happily start fresh
	// watchers that nobody ever stops.
	closed bool
}

// NewSupervisor builds a Supervisor that creates real fsnotify-backed Watchers.
// Pass 0 for debounce to use DefaultDebounce, and 0 for interval to use
// DefaultReconcileInterval.
func NewSupervisor(paths PathsProvider, cb Callback, debounce, interval time.Duration) *Supervisor {
	return newSupervisorWithFactory(paths, func() managedWatcher { return New(cb, debounce) }, interval)
}

// newSupervisorWithFactory is the injectable constructor used by tests.
func newSupervisorWithFactory(paths PathsProvider, factory func() managedWatcher, interval time.Duration) *Supervisor {
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	return &Supervisor{
		paths:      paths,
		newWatcher: factory,
		interval:   interval,
		active:     make(map[string]managedWatcher),
	}
}

// SetErrorHook installs a callback invoked on every provider/start failure.
// Must be called before Run.
func (s *Supervisor) SetErrorHook(fn func(error)) { s.onError = fn }

// Run reconciles once immediately, then on every interval tick, until shutdown
// is closed. It does NOT stop the watchers on exit — call StopAll from the
// process shutdown path so ordering stays under the caller's control.
//
// Intended to be run in a goroutine registered with the caller's WaitGroup.
func (s *Supervisor) Run(shutdown <-chan struct{}) {
	s.Reconcile()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.Reconcile()
		case <-shutdown:
			return
		}
	}
}

// Reconcile brings the live watcher set in line with the provider's desired
// set: start a watcher for every desired path not already watched, stop and
// drop every watched path no longer desired. Safe for concurrent use.
func (s *Supervisor) Reconcile() {
	if s.isClosed() {
		return
	}
	desired, err := s.paths()
	if err != nil {
		// Deliberately NOT silent, and deliberately non-destructive: keep the
		// watchers we have. See PathsProvider's doc comment.
		slog.Error("auto-scan: failed to enumerate watch paths; keeping existing watchers",
			"err", err, "active_watchers", s.count())
		if s.onError != nil {
			s.onError(err)
		}
		return
	}

	desiredSet := make(map[string]bool, len(desired))
	for _, p := range desired {
		if p != "" {
			desiredSet[p] = true
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under the lock: StopAll may have landed while s.paths() ran.
	if s.closed {
		return
	}

	// Stop watchers whose path disappeared or was disabled.
	for path, w := range s.active {
		if !desiredSet[path] {
			w.Stop()
			delete(s.active, path)
			slog.Info("auto-scan: file watcher stopped (path no longer watched)", "path", path)
		}
	}

	// Start watchers for newly desired paths. Watcher.Start is documented
	// safe-to-call-once, so each path gets its own fresh Watcher instance.
	for path := range desiredSet {
		if _, ok := s.active[path]; ok {
			continue
		}
		w := s.newWatcher()
		if startErr := w.Start(path); startErr != nil {
			slog.Warn("auto-scan: failed to start file watcher", "path", path, "err", startErr)
			if s.onError != nil {
				s.onError(startErr)
			}
			continue
		}
		s.active[path] = w
		slog.Info("auto-scan: file watcher started", "path", path)
	}

	if len(s.active) == 0 && len(desiredSet) > 0 {
		slog.Warn("auto-scan: no file watchers running despite configured paths", "desired", len(desiredSet))
	}
}

// StopAll stops every live watcher and empties the set. Idempotent, and
// TERMINAL: any later Reconcile is a no-op, so a tick racing process shutdown
// cannot resurrect watchers nobody will stop.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for path, w := range s.active {
		w.Stop()
		delete(s.active, path)
	}
}

func (s *Supervisor) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// WatchedPaths returns the sorted set of paths currently watched.
func (s *Supervisor) WatchedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.active))
	for p := range s.active {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *Supervisor) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}
