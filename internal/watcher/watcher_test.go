// file: internal/watcher/watcher_test.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-30

package watcher

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTimer is the fakeClock's stoppableTimer. Stop is a no-op once the
// timer has fired or already been stopped, matching time.Timer semantics.
type fakeTimer struct {
	c        *fakeClock
	deadline time.Duration
	fn       func()
	fired    bool
	stopped  bool
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

// fakeClock is a test-controlled scanClock. Timers never fire on their own;
// the test advances virtual time explicitly via Advance, which makes the
// debounce trigger deterministic instead of racing the real wall clock
// (see TestDebounceSingleEvent / TestDebounceMultipleEvents below, and
// todo.d/fix-watcher-debounce-test-flake.md for the flake this replaces).
type fakeClock struct {
	mu      sync.Mutex
	now     time.Duration
	pending []*fakeTimer
	wg      sync.WaitGroup
}

func newFakeClock() *fakeClock {
	return &fakeClock{}
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) stoppableTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, deadline: c.now + d, fn: f}
	c.pending = append(c.pending, t)
	return t
}

// Advance moves the fake clock forward by d and fires (each in its own
// goroutine, mirroring time.AfterFunc) any pending timers whose deadline has
// been reached. It blocks until every fired callback has returned, so
// assertions made immediately after Advance see their effects.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	var toFire []*fakeTimer
	remaining := c.pending[:0]
	for _, t := range c.pending {
		if t.stopped || t.fired {
			continue
		}
		if t.deadline <= c.now {
			t.fired = true
			toFire = append(toFire, t)
		} else {
			remaining = append(remaining, t)
		}
	}
	c.pending = remaining
	c.mu.Unlock()

	for _, t := range toFire {
		c.wg.Add(1)
		go func(t *fakeTimer) {
			defer c.wg.Done()
			t.fn()
		}(t)
	}
	c.wg.Wait()
}

// waitForEventsSettled polls w's internal scan generation counter (bumped
// once per relevant fsnotify event) until it stops changing for quietFor,
// or fails the test after timeout. This replaces guessing how long a burst
// of file writes takes to reach the watcher's event loop under load: it
// waits exactly as long as needed instead of a fixed sleep, and never
// produces a false pass because it only reports "settled", not "debounced"
// (the fake clock still requires an explicit Advance to fire the callback).
func waitForEventsSettled(t *testing.T, w *Watcher, quietFor, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastGen uint64
	lastChange := time.Now()
	for {
		w.mu.Lock()
		gen := w.scanGen
		w.mu.Unlock()

		if gen != lastGen {
			lastGen = gen
			lastChange = time.Now()
		}
		if gen > 0 && time.Since(lastChange) >= quietFor {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for watcher events to settle (last scanGen=%d)", gen)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestIsAudioFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"book.mp3", true},
		{"book.m4b", true},
		{"book.m4a", true},
		{"book.flac", true},
		{"book.ogg", true},
		{"book.opus", true},
		{"book.wma", true},
		{"book.aac", true},
		{"book.MP3", true},
		{"book.txt", false},
		{"book.jpg", false},
		{"book", false},
		{".mp3", true},
	}
	for _, tt := range tests {
		if got := IsAudioFile(tt.name); got != tt.want {
			t.Errorf("IsAudioFile(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestDebounceSingleEvent verifies a single relevant fsnotify event produces
// exactly one debounced callback. The debounce timer is driven by a
// fakeClock: the test waits (via waitForEventsSettled) for the real fsnotify
// event to reach scheduleScan, then explicitly advances virtual time to fire
// the timer. No part of the assertion depends on wall-clock margins, so it
// cannot flake under a stalled scheduler the way a sleep-then-read version
// could (see todo.d/fix-watcher-debounce-test-flake.md, now resolved).
func TestDebounceSingleEvent(t *testing.T) {
	dir := t.TempDir()

	const debounce = 100 * time.Millisecond
	var calls atomic.Int32
	fc := newFakeClock()
	w := New(func(rootDir string) {
		calls.Add(1)
	}, debounce)
	w.clock = fc

	if err := w.Start(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Create an audio file.
	f := filepath.Join(dir, "test.mp3")
	if err := os.WriteFile(f, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	waitForEventsSettled(t, w, 50*time.Millisecond, 5*time.Second)
	fc.Advance(debounce)

	if c := calls.Load(); c != 1 {
		t.Errorf("expected 1 callback, got %d", c)
	}
}

// TestDebounceMultipleEvents verifies a rapid burst of relevant fsnotify
// events collapses into exactly one debounced callback. Like
// TestDebounceSingleEvent, the debounce timer is driven by a fakeClock so
// the test controls exactly when it fires — the writes are no longer paced
// against a wall-clock debounce window, which was the source of the flake
// (write burst duration and debounce window had only ~80ms of margin;
// see todo.d/fix-watcher-debounce-test-flake.md, now resolved).
func TestDebounceMultipleEvents(t *testing.T) {
	dir := t.TempDir()

	const debounce = 200 * time.Millisecond
	var calls atomic.Int32
	fc := newFakeClock()
	w := New(func(rootDir string) {
		calls.Add(1)
	}, debounce)
	w.clock = fc

	if err := w.Start(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Rapid-fire create multiple files; no wall-clock pacing needed since
	// the fake clock's debounce timer only fires when Advance is called.
	for i := 0; i < 5; i++ {
		f := filepath.Join(dir, "test"+string(rune('a'+i))+".m4b")
		if err := os.WriteFile(f, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	waitForEventsSettled(t, w, 50*time.Millisecond, 5*time.Second)
	fc.Advance(debounce)

	if c := calls.Load(); c != 1 {
		t.Errorf("expected exactly 1 debounced callback, got %d", c)
	}
}

func TestNonAudioFilesIgnored(t *testing.T) {
	dir := t.TempDir()

	var calls atomic.Int32
	w := New(func(rootDir string) {
		calls.Add(1)
	}, 100*time.Millisecond)

	if err := w.Start(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Create non-audio files only.
	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("img"), 0644)

	time.Sleep(300 * time.Millisecond)

	if c := calls.Load(); c != 0 {
		t.Errorf("expected 0 callbacks for non-audio files, got %d", c)
	}
}

func TestRecursiveWatching(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "author", "book")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	w := New(func(rootDir string) {
		calls.Add(1)
	}, 100*time.Millisecond)

	if err := w.Start(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Create audio file in nested subdir.
	_ = os.WriteFile(filepath.Join(subdir, "chapter1.flac"), []byte("audio"), 0644)

	time.Sleep(300 * time.Millisecond)

	if c := calls.Load(); c != 1 {
		t.Errorf("expected 1 callback for nested dir, got %d", c)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	w := New(func(string) {}, 100*time.Millisecond)
	if err := w.Start(dir); err != nil {
		t.Fatal(err)
	}
	w.Stop()
	w.Stop() // should not panic
}

func TestStartIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	w := New(func(string) {}, 100*time.Millisecond)
	if err := w.Start(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	// Second start should be a no-op.
	if err := w.Start(dir); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteTriggers(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "book.mp3")
	_ = os.WriteFile(f, []byte("data"), 0644)

	var mu sync.Mutex
	var called bool
	w := New(func(string) {
		mu.Lock()
		called = true
		mu.Unlock()
	}, 100*time.Millisecond)

	if err := w.Start(dir); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Give watcher time to register.
	time.Sleep(50 * time.Millisecond)

	_ = os.Remove(f)
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Error("expected callback on file deletion")
	}
}
