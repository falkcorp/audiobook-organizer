// file: internal/watcher/supervisor_test.go
// version: 1.1.0
// guid: 3a7e0b52-8f14-4d69-b0c3-51d2e9af7c86
// last-edited: 2026-08-11

package watcher

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeWatcher is a managedWatcher that records Start/Stop instead of touching
// the filesystem, so Supervisor's reconcile logic can be tested without real
// directories or real fsnotify events.
type fakeWatcher struct {
	mu      sync.Mutex
	started string
	stopped bool
	startFn func(string) error
}

func (f *fakeWatcher) Start(rootDir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startFn != nil {
		if err := f.startFn(rootDir); err != nil {
			return err
		}
	}
	f.started = rootDir
	return nil
}

func (f *fakeWatcher) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

func (f *fakeWatcher) wasStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// pathSource is a mutable PathsProvider backing store for the tests.
type pathSource struct {
	mu    sync.Mutex
	paths []string
	err   error
}

func (p *pathSource) provider() ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	out := make([]string, len(p.paths))
	copy(out, p.paths)
	return out, nil
}

func (p *pathSource) set(paths []string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = paths
	p.err = err
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSupervisorWatchesRuntimeAddedImportPath is the regression test for the
// boot-only enumeration bug: server_lifecycle.go read GetAllImportPaths exactly
// once at startup, so an import path added later got no watcher until the
// process restarted. The Supervisor must pick it up on the next reconcile.
func TestSupervisorWatchesRuntimeAddedImportPath(t *testing.T) {
	src := &pathSource{paths: []string{"/library/one"}}

	var mu sync.Mutex
	created := map[string]*fakeWatcher{}
	// The factory records each instance under the path it was started with, so
	// the test can assert on a specific watcher. One watcher per path.
	factory := func() managedWatcher {
		fw := &fakeWatcher{}
		fw.startFn = func(root string) error {
			mu.Lock()
			created[root] = fw
			mu.Unlock()
			return nil
		}
		return fw
	}

	sup := newSupervisorWithFactory(src.provider, factory, time.Hour)

	sup.Reconcile()
	if got := sup.WatchedPaths(); !equalStrings(got, []string{"/library/one"}) {
		t.Fatalf("after first reconcile: watched=%v, want [/library/one]", got)
	}

	// A NEW import path appears at runtime (user added it in the UI).
	src.set([]string{"/library/one", "/library/two"}, nil)
	sup.Reconcile()

	if got := sup.WatchedPaths(); !equalStrings(got, []string{"/library/one", "/library/two"}) {
		t.Fatalf("runtime-added import path was not watched: watched=%v, want [/library/one /library/two]", got)
	}

	// The pre-existing watcher must NOT have been torn down and recreated.
	mu.Lock()
	first := created["/library/one"]
	mu.Unlock()
	if first == nil {
		t.Fatal("expected a watcher to have been started for /library/one")
	}
	if first.wasStopped() {
		t.Error("existing watcher for /library/one was stopped during reconcile; it should have been left alone")
	}
}

// TestSupervisorStopsRemovedPath covers the other half of reconcile: a path
// that is deleted or disabled must have its watcher stopped, not leaked.
func TestSupervisorStopsRemovedPath(t *testing.T) {
	src := &pathSource{paths: []string{"/library/one", "/library/two"}}

	var mu sync.Mutex
	created := map[string]*fakeWatcher{}
	factory := func() managedWatcher {
		fw := &fakeWatcher{}
		fw.startFn = func(root string) error {
			mu.Lock()
			created[root] = fw
			mu.Unlock()
			return nil
		}
		return fw
	}

	sup := newSupervisorWithFactory(src.provider, factory, time.Hour)
	sup.Reconcile()

	src.set([]string{"/library/one"}, nil)
	sup.Reconcile()

	if got := sup.WatchedPaths(); !equalStrings(got, []string{"/library/one"}) {
		t.Fatalf("watched=%v, want [/library/one]", got)
	}
	mu.Lock()
	two := created["/library/two"]
	mu.Unlock()
	if two == nil || !two.wasStopped() {
		t.Error("watcher for the removed path was not stopped")
	}
}

// TestSupervisorProviderErrorIsReportedAndNonDestructive pins the fix for the
// discarded GetAllImportPaths error. The old code was
// `if err == nil && len(importPaths) > 0 { ... }` — a failed read silently
// started zero watchers with no log line. Now the error must reach the error
// hook, and existing watchers must survive the blip.
func TestSupervisorProviderErrorIsReportedAndNonDestructive(t *testing.T) {
	src := &pathSource{paths: []string{"/library/one"}}
	sup := newSupervisorWithFactory(src.provider, func() managedWatcher { return &fakeWatcher{} }, time.Hour)

	var mu sync.Mutex
	var reported []error
	sup.SetErrorHook(func(err error) {
		mu.Lock()
		reported = append(reported, err)
		mu.Unlock()
	})

	sup.Reconcile()
	if got := sup.WatchedPaths(); !equalStrings(got, []string{"/library/one"}) {
		t.Fatalf("watched=%v, want [/library/one]", got)
	}

	boom := errors.New("pebble: read failed")
	src.set(nil, boom)
	sup.Reconcile()

	mu.Lock()
	n := len(reported)
	var first error
	if n > 0 {
		first = reported[0]
	}
	mu.Unlock()

	if n != 1 {
		t.Fatalf("provider error was not reported: got %d reports, want 1", n)
	}
	if !errors.Is(first, boom) {
		t.Errorf("reported error = %v, want %v", first, boom)
	}
	if got := sup.WatchedPaths(); !equalStrings(got, []string{"/library/one"}) {
		t.Errorf("a transient provider error tore down live watchers: watched=%v, want [/library/one]", got)
	}
}

// TestSupervisorStartErrorIsReported ensures a watcher that fails to start is
// reported and not recorded as active (so the next reconcile retries it).
func TestSupervisorStartErrorIsReported(t *testing.T) {
	src := &pathSource{paths: []string{"/library/nope"}}
	boom := errors.New("no such directory")
	sup := newSupervisorWithFactory(src.provider, func() managedWatcher {
		return &fakeWatcher{startFn: func(string) error { return boom }}
	}, time.Hour)

	var mu sync.Mutex
	var reported int
	sup.SetErrorHook(func(error) {
		mu.Lock()
		reported++
		mu.Unlock()
	})

	sup.Reconcile()

	if got := sup.WatchedPaths(); len(got) != 0 {
		t.Errorf("failed watcher was recorded as active: %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if reported != 1 {
		t.Errorf("start failure reports = %d, want 1", reported)
	}
}

// TestSupervisorStopAllIsTerminal guards the shutdown race: the server stops
// watchers BEFORE it waits on its background WaitGroup, so a reconcile tick
// landing in that window must not start fresh watchers nobody will stop.
func TestSupervisorStopAllIsTerminal(t *testing.T) {
	src := &pathSource{paths: []string{"/library/one"}}
	sup := newSupervisorWithFactory(src.provider, func() managedWatcher { return &fakeWatcher{} }, time.Hour)

	sup.Reconcile()
	if got := sup.WatchedPaths(); len(got) != 1 {
		t.Fatalf("watched=%v, want 1 path", got)
	}

	sup.StopAll()
	sup.Reconcile()

	if got := sup.WatchedPaths(); len(got) != 0 {
		t.Errorf("Reconcile after StopAll resurrected watchers: %v", got)
	}
}

// TestSupervisorRunReconcilesOnTicker proves Run actually drives Reconcile on
// its interval — not just once at startup.
func TestSupervisorRunReconcilesOnTicker(t *testing.T) {
	src := &pathSource{paths: []string{"/library/one"}}
	sup := newSupervisorWithFactory(src.provider, func() managedWatcher { return &fakeWatcher{} }, 5*time.Millisecond)

	shutdown := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.Run(shutdown)
	}()
	defer func() {
		close(shutdown)
		<-done
		sup.StopAll()
	}()

	// Add a path AFTER Run has already done its initial reconcile.
	deadline := time.After(2 * time.Second)
	for {
		if len(sup.WatchedPaths()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("initial reconcile never ran")
		case <-time.After(time.Millisecond):
		}
	}
	src.set([]string{"/library/one", "/library/two"}, nil)

	deadline = time.After(2 * time.Second)
	for {
		if equalStrings(sup.WatchedPaths(), []string{"/library/one", "/library/two"}) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("ticker never reconciled the new path: watched=%v", sup.WatchedPaths())
		case <-time.After(time.Millisecond):
		}
	}
}
