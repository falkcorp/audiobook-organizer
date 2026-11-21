// file: internal/books/roots/watch_test.go
// version: 0.1.0
// guid: 5a1c6e0a-3a77-4b4f-9a3e-0d9a2b7e4c11
// last-edited: 2026-11-20

package roots_test

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"

	"github.com/falkcorp/audiobook-organizer/internal/books/roots"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// fakeSource is a roots.WatchSource whose sequences the test drives.
type fakeSource struct {
	mu    sync.Mutex
	calls map[library.RootID]int
	opts  map[library.RootID]library.WatchOptions
	errs  map[library.RootID]error
	cur   map[library.RootID]chan []library.Event
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		calls: map[library.RootID]int{}, opts: map[library.RootID]library.WatchOptions{},
		errs: map[library.RootID]error{}, cur: map[library.RootID]chan []library.Event{},
	}
}

func (f *fakeSource) Watch(ctx context.Context, id library.RootID, o library.WatchOptions) (iter.Seq[[]library.Event], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[id]++
	f.opts[id] = o
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	ch := make(chan []library.Event)
	f.cur[id] = ch
	return func(yield func([]library.Event) bool) {
		for {
			select {
			case <-ctx.Done():
				return
			case b, ok := <-ch:
				if !ok || !yield(b) {
					return
				}
			}
		}
	}, nil
}

func (f *fakeSource) callCount(id library.RootID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

func (f *fakeSource) setErr(id library.RootID, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[id] = err
}

// send delivers a batch to the root's live sequence, waiting for one to exist.
func (f *fakeSource) send(t *testing.T, id library.RootID, b []library.Event) {
	t.Helper()
	var ch chan []library.Event
	waitFor(t, "a live watch of "+string(id), func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		ch = f.cur[id]
		return ch != nil
	})
	select {
	case ch <- b:
	case <-time.After(5 * time.Second):
		t.Fatalf("batch to %s was not taken", id)
	}
}

// end finishes the root's live sequence, as a registry reload does.
func (f *fakeSource) end(id library.RootID) {
	f.mu.Lock()
	ch := f.cur[id]
	f.cur[id] = nil
	f.mu.Unlock()
	close(ch)
}

type desiredSet struct {
	mu  sync.Mutex
	ids []library.RootID
	err error
}

func (d *desiredSet) set(ids ...library.RootID) { d.mu.Lock(); d.ids, d.err = ids, nil; d.mu.Unlock() }
func (d *desiredSet) fail(err error)            { d.mu.Lock(); d.err = err; d.mu.Unlock() }
func (d *desiredSet) get(context.Context) ([]library.RootID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]library.RootID(nil), d.ids...), d.err
}

type changes struct {
	mu sync.Mutex
	cs []roots.Change
}

func (c *changes) handle(_ context.Context, ch roots.Change) {
	c.mu.Lock()
	c.cs = append(c.cs, ch)
	c.mu.Unlock()
}
func (c *changes) all() []roots.Change {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]roots.Change(nil), c.cs...)
}

func newTestWatcher(t *testing.T, src *fakeSource, d *desiredSet, h roots.Handler, mod func(*roots.WatcherOptions)) *roots.Watcher {
	t.Helper()
	o := roots.WatcherOptions{
		Source: src, Handler: h, Desired: d.get, Extensions: []string{".m4b", ".mp3"},
		ResubscribeDelay: time.Millisecond, Interval: time.Hour,
	}
	if mod != nil {
		mod(&o)
	}
	w, err := roots.NewWatcher(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	return w
}

func TestWatcher_ReconcileStartsDesiredAndStopsTheRest(t *testing.T) {
	src, d := newFakeSource(), &desiredSet{}
	w := newTestWatcher(t, src, d, (&changes{}).handle, nil)
	ctx := context.Background()
	d.set("import-1", "import-2")
	w.Reconcile(ctx)
	if got := w.Watched(); len(got) != 2 || got[0] != "import-1" || got[1] != "import-2" {
		t.Fatalf("Watched = %v", got)
	}
	waitFor(t, "both watches subscribed", func() bool { return src.callCount("import-1") == 1 && src.callCount("import-2") == 1 })
	o := src.opts["import-1"]
	if !o.Recursive || len(o.Extensions) != 2 || o.Extensions[0] != ".m4b" {
		t.Errorf("watch options = %+v; want recursive with the audio extensions", o)
	}
	d.set("import-2")
	w.Reconcile(ctx)
	if got := w.Watched(); len(got) != 1 || got[0] != "import-2" {
		t.Errorf("after removing import-1: Watched = %v", got)
	}
	w.Reconcile(ctx) // unchanged: no second subscription
	if n := src.callCount("import-2"); n != 1 {
		t.Errorf("import-2 subscribed %d times; want 1", n)
	}
}

func TestWatcher_BatchesReachTheHandlerAndARescanIsATrigger(t *testing.T) {
	src, d, cs := newFakeSource(), &desiredSet{}, &changes{}
	w := newTestWatcher(t, src, d, cs.handle, nil)
	d.set("import-1")
	w.Reconcile(context.Background())
	dirA := library.Path{Root: "import-1", Rel: "Year One"}
	src.send(t, "import-1", []library.Event{{Kind: library.EventChanged, Dir: dirA}})
	rootDir := library.Path{Root: "import-1"}
	src.send(t, "import-1", []library.Event{{Kind: library.EventRescan, Dir: rootDir}})
	src.send(t, "import-1", []library.Event{{Kind: library.EventInvalid}}) // nothing to act on: not delivered
	waitFor(t, "two changes", func() bool { return len(cs.all()) == 2 })
	got := cs.all()
	if got[0].Rescan || got[0].Root != "import-1" || len(got[0].Dirs) != 1 || got[0].Dirs[0] != dirA {
		t.Errorf("first change = %+v", got[0])
	}
	if !got[1].Rescan || len(got[1].Dirs) != 1 || got[1].Dirs[0] != rootDir {
		t.Errorf("rescan change = %+v; want Rescan with the root path (a rescan is never dropped)", got[1])
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(cs.all()); n != 2 {
		t.Errorf("an event with no kind was delivered: %d changes", n)
	}
}

func TestWatcher_ResubscribesWhenAReloadEndsTheSequence(t *testing.T) {
	src, d, cs := newFakeSource(), &desiredSet{}, &changes{}
	w := newTestWatcher(t, src, d, cs.handle, nil)
	d.set("import-1")
	w.Reconcile(context.Background())
	waitFor(t, "first subscription", func() bool { return src.callCount("import-1") == 1 })
	src.end("import-1")
	waitFor(t, "second subscription", func() bool { return src.callCount("import-1") == 2 })
	src.send(t, "import-1", []library.Event{{Kind: library.EventChanged, Dir: library.Path{Root: "import-1", Rel: "Hush"}}})
	waitFor(t, "a change after the resubscribe", func() bool { return len(cs.all()) == 1 })
	if got := w.Watched(); len(got) != 1 {
		t.Errorf("Watched after a resubscribe = %v", got)
	}
}

func TestWatcher_AFailedStartEndsTheWatchAndTheNextReconcileRetries(t *testing.T) {
	src, d := newFakeSource(), &desiredSet{}
	var mu sync.Mutex
	var errs []error
	w := newTestWatcher(t, src, d, (&changes{}).handle, func(o *roots.WatcherOptions) {
		o.OnError = func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }
	})
	ctx := context.Background()
	src.setErr("import-3", library.ErrNotSupported) // watching off for the root
	d.set("import-3")
	w.Reconcile(ctx)
	waitFor(t, "the watch to end", func() bool { return len(w.Watched()) == 0 })
	mu.Lock()
	if len(errs) != 1 || !errors.Is(errs[0], library.ErrNotSupported) {
		t.Errorf("OnError = %v; want one ErrNotSupported", errs)
	}
	mu.Unlock()
	src.setErr("import-3", nil)
	w.Reconcile(ctx)
	waitFor(t, "the retry", func() bool { return src.callCount("import-3") == 2 && len(w.Watched()) == 1 })
}

func TestWatcher_ADesiredSetErrorKeepsTheRunningWatches(t *testing.T) {
	src, d := newFakeSource(), &desiredSet{}
	var mu sync.Mutex
	var errs []error
	w := newTestWatcher(t, src, d, (&changes{}).handle, func(o *roots.WatcherOptions) {
		o.OnError = func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }
	})
	ctx := context.Background()
	d.set("import-1")
	w.Reconcile(ctx)
	d.fail(errors.New("the roots could not be listed"))
	w.Reconcile(ctx)
	if got := w.Watched(); len(got) != 1 {
		t.Errorf("a failed enumeration tore the watches down: %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 1 {
		t.Errorf("OnError calls = %d; want 1", len(errs))
	}
}

func TestWatcher_StopIsTerminalAndWaits(t *testing.T) {
	src, d := newFakeSource(), &desiredSet{}
	w := newTestWatcher(t, src, d, (&changes{}).handle, nil)
	ctx := context.Background()
	d.set("import-1", "import-2")
	w.Reconcile(ctx)
	waitFor(t, "subscriptions", func() bool { return src.callCount("import-1") == 1 && src.callCount("import-2") == 1 })
	w.Stop()
	if got := w.Watched(); len(got) != 0 {
		t.Errorf("Watched after Stop = %v", got)
	}
	w.Stop() // idempotent
	d.set("import-1", "import-2", "import-3")
	w.Reconcile(ctx)
	if got := w.Watched(); len(got) != 0 || src.callCount("import-3") != 0 {
		t.Errorf("Reconcile after Stop started a watch: %v", got)
	}
	if err := w.Run(ctx); !errors.Is(err, roots.ErrWatcherStopped) {
		t.Errorf("Run after Stop = %v; want ErrWatcherStopped", err)
	}
}

func TestWatcher_RunReconcilesOnTheTickAndStopsWithItsContext(t *testing.T) {
	src, d := newFakeSource(), &desiredSet{}
	w := newTestWatcher(t, src, d, (&changes{}).handle, func(o *roots.WatcherOptions) { o.Interval = 5 * time.Millisecond })
	d.set("import-1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	waitFor(t, "the first reconcile", func() bool { return len(w.Watched()) == 1 })
	d.set("import-1", "import-2") // picked up on a tick, with no restart
	waitFor(t, "the tick to add import-2", func() bool { return len(w.Watched()) == 2 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v on its own context's end", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if len(w.Watched()) != 2 {
		t.Errorf("Run's exit stopped the watches; only Stop does")
	}
}

func TestWatcher_ReconcileRacingStop(t *testing.T) {
	for range 30 {
		src, d := newFakeSource(), &desiredSet{}
		w := newTestWatcher(t, src, d, (&changes{}).handle, nil)
		d.set("import-1", "import-2", "import-3")
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { w.Reconcile(context.Background()) })
		}
		w.Stop()
		wg.Wait()
		w.Stop()
		if got := w.Watched(); len(got) != 0 {
			t.Fatalf("a watch survived Stop: %v", got)
		}
	}
}

func TestNewWatcher_RequiresItsParts(t *testing.T) {
	src, d := newFakeSource(), &desiredSet{}
	h := (&changes{}).handle
	for name, o := range map[string]roots.WatcherOptions{
		"no source":  {Handler: h, Desired: d.get},
		"no handler": {Source: src, Desired: d.get},
		"no desired": {Source: src, Handler: h},
	} {
		if _, err := roots.NewWatcher(o); err == nil {
			t.Errorf("%s: NewWatcher accepted it", name)
		}
	}
}
