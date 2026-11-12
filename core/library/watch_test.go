// file: core/library/watch_test.go
// version: 1.0.0
// guid: b70fbc3e-49eb-403d-9e4c-2435e3f8b451
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/falkcorp/mediacore/storage"
)

// fakeSource is the kernel's change feed, in the test's hands.
type fakeSource struct {
	mu      sync.Mutex
	dirs    []string
	events  chan fsnotify.Event
	errs    chan error
	failAdd func(dir string) error
	closed  bool
	made    int
}

func newFakeSource() *fakeSource {
	return &fakeSource{events: make(chan fsnotify.Event, 256), errs: make(chan error, 8)}
}

func (f *fakeSource) source() (watchSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.made++
	return f, nil
}

func (f *fakeSource) Add(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAdd != nil {
		if err := f.failAdd(dir); err != nil {
			return err
		}
	}
	f.dirs = append(f.dirs, dir)
	return nil
}
func (f *fakeSource) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeSource) Errors() <-chan error          { return f.errs }
func (f *fakeSource) Close() error                  { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }
func (f *fakeSource) watched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.dirs)
}
func (f *fakeSource) isClosed() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.closed }

// watchRun is one running Watch: its batches arrive on out, and out closes
// when the iteration ends.
type watchRun struct {
	r      *rig
	root   *Root
	fake   *fakeSource
	out    chan []Event
	cancel context.CancelFunc
	errs   []error
	mu     sync.Mutex
}

// startWatch builds a rig inside the synctest bubble (so the watcher's timers
// are the bubble's) and starts a Watch of books-library.
func startWatch(t *testing.T, st *store, mode WatchMode, opts WatchOptions, tune func(*settings)) *watchRun {
	t.Helper()
	return startWatchN(t, st, mode, opts, tune, 0)
}

// startWatchN is startWatch with a consumer that stops after stopAfter batches
// (0 = never).
func startWatchN(t *testing.T, st *store, mode WatchMode, opts WatchOptions, tune func(*settings), stopAfter int) *watchRun {
	t.Helper()
	w := &watchRun{fake: newFakeSource(), out: make(chan []Event, 64)}
	sp := spec("books-library", books, RoleLibrary)
	sp.Watch = mode
	w.r = newRig(t, st, rigOpts{seams: seams{watchSrc: w.fake.source}}, sp)
	if tune != nil {
		set := defaultSettings()
		set.roots = w.r.specs
		tune(&set)
		if err := w.r.m.apply(ctxbg(), set); err != nil {
			t.Fatal(err)
		}
	}
	w.root, _ = w.r.m.Root("books-library")
	opts.OnError = func(err error) { w.mu.Lock(); w.errs = append(w.errs, err); w.mu.Unlock() }
	ctx, cancel := context.WithCancel(t.Context())
	w.cancel = cancel
	seq2, err := w.r.m.Watch(ctx, "books-library", opts)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(w.out)
		n := 0
		for b := range seq2 {
			w.out <- b
			if n++; stopAfter > 0 && n >= stopAfter {
				return
			}
		}
	}()
	synctest.Wait()
	return w
}

func (w *watchRun) ev(rel string, op fsnotify.Op) {
	w.fake.events <- fsnotify.Event{Name: filepath.Join(w.root.abs, filepath.FromSlash(rel)), Op: op}
}

// next is the batch that has arrived, or nil.
func (w *watchRun) next() []Event {
	synctest.Wait()
	select {
	case b := <-w.out:
		return b
	default:
		return nil
	}
}

func dirsOf(b []Event) (out []string) {
	for _, e := range b {
		if e.Kind == EventChanged {
			out = append(out, e.Dir.Rel)
		}
	}
	return out
}

func TestWatchDebounceCoalesces(t *testing.T) {
	st := newStore(t)
	synctest.Test(t, func(t *testing.T) {
		const deb = 100 * time.Millisecond
		w := startWatch(t, st, WatchNotify, WatchOptions{Debounce: deb}, nil)
		defer w.cancel()
		// Three events in two directories coalesce into one sorted batch, and
		// nothing is delivered before the quiet period ends.
		w.ev("Gotham/01.m4b", fsnotify.Create)
		w.ev("Gotham/02.m4b", fsnotify.Write)
		w.ev("root.m4b", fsnotify.Create)
		time.Sleep(deb - time.Millisecond)
		if b := w.next(); b != nil {
			t.Fatalf("a batch before the quiet period: %v", b)
		}
		time.Sleep(time.Millisecond)
		b := w.next()
		if got := dirsOf(b); !slices.Equal(got, []string{"", "Gotham"}) {
			t.Fatalf("batch = %v, want the root and Gotham once each", b)
		}
		if b := w.next(); b != nil {
			t.Fatalf("a second batch with no new events: %v", b)
		}
		// Each event restarts the quiet period.
		w.ev("Gotham/03.m4b", fsnotify.Create)
		time.Sleep(80 * time.Millisecond)
		w.ev("Gotham/04.m4b", fsnotify.Create)
		time.Sleep(80 * time.Millisecond)
		if b := w.next(); b != nil {
			t.Fatalf("a batch while events kept arriving: %v", b)
		}
		time.Sleep(20 * time.Millisecond)
		if got := dirsOf(w.next()); !slices.Equal(got, []string{"Gotham"}) {
			t.Fatalf("batch after the quiet period = %v", got)
		}
		// A steady stream cannot postpone a batch for ever: the batch is cut
		// maxWaitFactor debounce periods after its first event.
		first := time.Now()
		var cut time.Time
		for i := 0; i < 40 && cut.IsZero(); i++ {
			w.ev("Arkham/x.m4b", fsnotify.Write)
			time.Sleep(deb / 2)
			if b := w.next(); b != nil {
				cut = time.Now()
				if got := dirsOf(b); !slices.Equal(got, []string{"Arkham"}) {
					t.Errorf("capped batch = %v", got)
				}
			}
		}
		if cut.IsZero() {
			t.Fatal("a continuous stream never produced a batch")
		}
		if d := cut.Sub(first); d < maxWaitFactor*deb || d > maxWaitFactor*deb+deb {
			t.Errorf("capped batch after %v, want about %v", d, maxWaitFactor*deb)
		}
	})
}

func TestWatchFiltersWhatItReports(t *testing.T) {
	st := newStore(t)
	synctest.Test(t, func(t *testing.T) {
		const deb = 10 * time.Millisecond
		w := startWatch(t, st, WatchNotify, WatchOptions{Debounce: deb, Extensions: []string{"m4b"}}, nil)
		defer w.cancel()
		for _, rel := range []string{
			"Gotham/a.mp3",                       // the wrong extension
			"Gotham/.mediacore-tmp-01H",          // a transfer's own temp
			"Gotham/b.m4b.mediacore-replaced-12", // a transfer's own aside
			DefaultAppDir + "/root.json",         // the app's directory
			DefaultQuarantine + "/x/c.m4b",       // the quarantine area
			".hidden/d.m4b",                      // a hidden directory
		} {
			w.ev(rel, fsnotify.Create)
		}
		w.ev("Gotham/chmod.m4b", fsnotify.Chmod)
		w.fake.events <- fsnotify.Event{Name: filepath.Join(filepath.Dir(w.root.abs), "elsewhere", "e.m4b"), Op: fsnotify.Create}
		time.Sleep(2 * deb)
		if b := w.next(); b != nil {
			t.Fatalf("filtered events produced a batch: %v", b)
		}
		w.ev("Gotham/Wanted.M4B", fsnotify.Rename)
		time.Sleep(2 * deb)
		if got := dirsOf(w.next()); !slices.Equal(got, []string{"Gotham"}) {
			t.Errorf("a wanted file's batch = %v", got)
		}
	})
}

func TestWatchOverflowYieldsRescan(t *testing.T) {
	st := newStore(t)
	synctest.Test(t, func(t *testing.T) {
		const deb = 50 * time.Millisecond
		w := startWatch(t, st, WatchNotify, WatchOptions{Debounce: deb}, nil)
		defer w.cancel()
		w.ev("Gotham/a.m4b", fsnotify.Create)
		w.fake.errs <- fsnotify.ErrEventOverflow
		time.Sleep(deb)
		b := w.next()
		if len(b) != 2 || b[0].Kind != EventRescan || b[0].Dir != (Path{Root: "books-library"}) || b[1].Kind != EventChanged {
			t.Fatalf("batch = %+v, want a rescan then the changed directory", b)
		}
		w.mu.Lock()
		reported := len(w.errs)
		w.mu.Unlock()
		if reported != 1 {
			t.Errorf("OnError called %d times, want 1", reported)
		}
		w.r.m.ctr.mu.Lock()
		n := w.r.m.ctr.rescans[watchKey{"books-library", rescanOverflow}]
		w.r.m.ctr.mu.Unlock()
		if n != 1 {
			t.Errorf("overflow rescans counted = %d, want 1", n)
		}
	})
}

func TestWatchFallsBackToPolling(t *testing.T) {
	st := newStore(t)
	synctest.Test(t, func(t *testing.T) {
		const poll = time.Minute
		fake := newFakeSource()
		fake.failAdd = func(string) error { return &os.PathError{Op: "inotify_add_watch", Path: "x", Err: syscall.ENOSPC} }
		sp := spec("books-library", books, RoleLibrary)
		sp.Watch = WatchNotify
		r := newRig(t, st, rigOpts{seams: seams{watchSrc: fake.source}}, sp)
		set := defaultSettings()
		set.roots = r.specs
		set.pollInterval = poll
		if err := r.m.apply(ctxbg(), set); err != nil {
			t.Fatal(err)
		}
		root, _ := r.m.Root("books-library")
		ctx, cancel := context.WithCancel(t.Context())
		seq, err := r.m.Watch(ctx, "books-library", WatchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		out := make(chan []Event, 8)
		go func() {
			defer close(out)
			for b := range seq {
				out <- b
			}
		}()
		synctest.Wait()
		// The watch could not be added: a rescan is owed at once, and the root is
		// degraded while it polls.
		select {
		case b := <-out:
			if len(b) != 1 || b[0].Kind != EventRescan {
				t.Fatalf("first batch = %+v, want one rescan", b)
			}
		default:
			t.Fatal("no immediate rescan after a failed add")
		}
		if !root.polling.Load() {
			t.Error("the root is not marked as polling")
		}
		if st := root.state(ctxbg()); st != stateDegraded {
			t.Errorf("root state = %s, want degraded", st)
		}
		start := time.Now()
		for i := 1; i <= 3; i++ {
			time.Sleep(poll)
			synctest.Wait()
			select {
			case b := <-out:
				if len(b) != 1 || b[0].Kind != EventRescan {
					t.Fatalf("poll batch %d = %+v", i, b)
				}
			default:
				t.Fatalf("no poll rescan %d after %v", i, time.Since(start))
			}
		}
		r.m.ctr.mu.Lock()
		added, polled := r.m.ctr.rescans[watchKey{"books-library", rescanAddFailed}], r.m.ctr.rescans[watchKey{"books-library", rescanPoll}]
		r.m.ctr.mu.Unlock()
		if added != 1 || polled != 3 {
			t.Errorf("rescans counted: add_failed %d (want 1), poll %d (want 3)", added, polled)
		}
		cancel()
		synctest.Wait()
		if _, open := <-out; open {
			t.Error("the iteration did not end")
		}
		if root.polling.Load() {
			t.Error("the root is still marked as polling after the watch ended")
		}
		if !fake.isClosed() {
			t.Error("the source was not closed")
		}
	})
}

func TestWatchPollModeNeverOpensASource(t *testing.T) {
	st := newStore(t)
	synctest.Test(t, func(t *testing.T) {
		w := startWatch(t, st, WatchPoll, WatchOptions{}, func(s *settings) { s.pollInterval = 30 * time.Second })
		defer w.cancel()
		if b := w.next(); b != nil {
			t.Fatalf("a batch before the first tick: %v", b)
		}
		time.Sleep(30 * time.Second)
		if b := w.next(); len(b) != 1 || b[0].Kind != EventRescan {
			t.Fatalf("first poll batch = %+v", b)
		}
		w.fake.mu.Lock()
		made := w.fake.made
		w.fake.mu.Unlock()
		if made != 0 {
			t.Errorf("%d sources were opened for a polled root", made)
		}
	})
}

func TestWatchAddsWatchesForDirectoriesAndNewDirectories(t *testing.T) {
	st := newStore(t)
	synctest.Test(t, func(t *testing.T) {
		sp := spec("books-library", books, RoleLibrary)
		sp.Watch = WatchNotify
		fake := newFakeSource()
		r := newRig(t, st, rigOpts{seams: seams{watchSrc: fake.source}}, sp)
		for _, d := range []string{"Gotham/Arkham", ".hidden/x", DefaultAppDir + "/sub"} {
			if err := os.MkdirAll(r.abs("books-library", d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		root, _ := r.m.Root("books-library")
		run := func(recursive bool) (*fakeSource, chan []Event, context.CancelFunc) {
			f := newFakeSource()
			r.m.seam.watchSrc = f.source
			ctx, cancel := context.WithCancel(t.Context())
			seq, err := r.m.Watch(ctx, "books-library", WatchOptions{Recursive: recursive, Debounce: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			out := make(chan []Event, 8)
			go func() {
				defer close(out)
				for b := range seq {
					out <- b
				}
			}()
			synctest.Wait()
			return f, out, cancel
		}
		// Not recursive: the root only.
		f, _, cancel := run(false)
		if got := f.watched(); !slices.Equal(got, []string{root.abs}) {
			t.Errorf("non-recursive watch added %v, want the root only", got)
		}
		cancel()
		synctest.Wait()
		// Recursive: every directory a walk enters, never the AppDir, the
		// quarantine area or a hidden directory.
		f, out, cancel := run(true)
		want := []string{root.abs, filepath.Join(root.abs, "Gotham"), filepath.Join(root.abs, "Gotham", "Arkham")}
		got := f.watched()
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("recursive watch added %v, want %v", got, want)
		}
		// A directory created later is watched and reported.
		if err := os.MkdirAll(r.abs("books-library", "Gotham/Crime Alley"), 0o755); err != nil {
			t.Fatal(err)
		}
		f.events <- fsnotify.Event{Name: filepath.Join(root.abs, "Gotham", "Crime Alley"), Op: fsnotify.Create}
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		if !slices.Contains(f.watched(), filepath.Join(root.abs, "Gotham", "Crime Alley")) {
			t.Errorf("the new directory was not watched: %v", f.watched())
		}
		select {
		case b := <-out:
			if got := dirsOf(b); !slices.Equal(got, []string{"Gotham/Crime Alley"}) {
				t.Errorf("batch = %v, want the new directory", got)
			}
		default:
			t.Error("the new directory was not reported")
		}
		// A new hidden directory is neither watched nor reported.
		if err := os.MkdirAll(r.abs("books-library", ".secret"), 0o755); err != nil {
			t.Fatal(err)
		}
		before := len(f.watched())
		f.events <- fsnotify.Event{Name: filepath.Join(root.abs, ".secret"), Op: fsnotify.Create}
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		if len(f.watched()) != before {
			t.Error("a hidden directory was watched")
		}
		select {
		case b := <-out:
			t.Errorf("a hidden directory was reported: %v", b)
		default:
		}
		cancel()
	})
}

func TestWatchEndsWhenTheRootIsRetiredOrTheConsumerStops(t *testing.T) {
	st := newStore(t)
	synctest.Test(t, func(t *testing.T) {
		w := startWatch(t, st, WatchNotify, WatchOptions{Debounce: 10 * time.Millisecond}, nil)
		defer w.cancel()
		// The root is removed by a reload: the iteration ends and the source is closed.
		set := defaultSettings()
		set.roots = nil
		if err := w.r.m.apply(ctxbg(), set); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if _, open := <-w.out; open {
			t.Error("the iteration did not end when its root was retired")
		}
		if !w.fake.isClosed() {
			t.Error("the source was not closed")
		}
	})
	synctest.Test(t, func(t *testing.T) {
		// A consumer that stops reading after the first batch ends the
		// iteration, and the source is closed.
		w := startWatchN(t, st, WatchNotify, WatchOptions{Debounce: 10 * time.Millisecond}, nil, 1)
		defer w.cancel()
		w.ev("Gotham/a.m4b", fsnotify.Create)
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		if b := <-w.out; len(b) != 1 {
			t.Errorf("batch = %v", b)
		}
		if _, open := <-w.out; open {
			t.Error("the iteration did not end when its consumer stopped")
		}
		if !w.fake.isClosed() {
			t.Error("the source was not closed")
		}
	})
}

func TestWatchRefusals(t *testing.T) {
	disabled := false
	r := newRig(t, newStore(t), rigOpts{enabled: func(o storage.Owner) bool { return !disabled }},
		withWatch(spec("books-library", books, RoleLibrary), WatchNotify), spec("quiet", books, RoleLibrary))
	if _, err := r.m.Watch(ctxbg(), "nope", WatchOptions{}); !errors.Is(err, ErrUnknownRoot) {
		t.Errorf("unknown root = %v, want ErrUnknownRoot", err)
	}
	if _, err := r.m.Watch(ctxbg(), "quiet", WatchOptions{}); !errors.Is(err, ErrNotSupported) {
		t.Errorf("a root with watching off = %v, want ErrNotSupported", err)
	}
	disabled = true
	if _, err := r.m.Watch(ctxbg(), "books-library", WatchOptions{}); !errors.Is(err, ErrOwnerDisabled) {
		t.Errorf("a disabled owner = %v, want ErrOwnerDisabled", err)
	}
}

func withWatch(s RootSpec, m WatchMode) RootSpec { s.Watch = m; return s }
