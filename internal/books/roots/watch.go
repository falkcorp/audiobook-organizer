// file: internal/books/roots/watch.go
// version: 0.1.0
// guid: e7b81390-45f1-466f-8a6d-5c2cb3434978
// last-edited: 2026-11-20

package roots

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/library"
)

// DefaultReconcileInterval is how often Run re-reads the desired watch set,
// HEAD's watcher.DefaultReconcileInterval: the periodic scan is the catch-all
// and the watchers are the low-latency path.
const DefaultReconcileInterval = 5 * time.Minute

// DefaultResubscribeDelay is how long a watch waits before it subscribes again
// after its sequence ended without the watcher being stopped, which is what a
// registry reload does to a root's watch.
const DefaultResubscribeDelay = time.Second

// ErrWatcherStopped is returned by Run on a Watcher that was stopped.
var ErrWatcherStopped = errors.New("roots: the watcher is stopped")

// WatchSource is the part of library.Scope the watcher calls; library.Scope
// satisfies it.
type WatchSource interface {
	Watch(ctx context.Context, id library.RootID, opts library.WatchOptions) (iter.Seq[[]library.Event], error)
}

var _ WatchSource = library.Scope(nil)

// Change is one batch of a root's watch, for the handler.
type Change struct {
	Root library.RootID
	// Dirs are the coalesced directories whose files changed.
	Dirs []library.Path
	// Rescan is set when library lost events (an overflow, a watch it could not
	// add, a poll tick): the root must be rescanned whole. It is a trigger like
	// any other change, never dropped; Dirs is then the root path.
	Rescan bool
}

// Handler is called, on the watch's own goroutine, with each batch. A slow
// handler delays only its root: library's pull iterator lets events queue in the
// kernel, which overflows into a rescan instead of growing a queue here.
type Handler func(ctx context.Context, c Change)

// WatcherOptions configures a Watcher. Handler and Desired are required.
type WatcherOptions struct {
	Source  WatchSource
	Handler Handler
	// Desired is the set of roots that should be watched now. It is called on
	// every reconcile, not once, so a root added, enabled, disabled or removed
	// at runtime is picked up without a restart. An error is "I do not know the
	// set": the watches already running are kept, since tearing them down on a
	// blip would turn it into a blind spot. Reader.WatchRoots is the usual one.
	Desired func(ctx context.Context) ([]library.RootID, error)
	// Extensions is the audio extension filter passed to every watch, with the
	// leading dot; nil watches every file.
	Extensions []string
	// Debounce overrides core.library.watch.debounce; 0 keeps it.
	Debounce time.Duration
	// OnError receives every start, watch and desired-set failure, besides the
	// log line (the server wires it to the activity log).
	OnError func(error)
	// Interval is Run's reconcile period; 0 is DefaultReconcileInterval.
	Interval time.Duration
	// ResubscribeDelay is the wait before a watch whose sequence ended is
	// subscribed again; 0 is DefaultResubscribeDelay.
	ResubscribeDelay time.Duration
	Logger           *slog.Logger
}

// Watcher keeps one library watch running per desired root. It replaces
// internal/watcher.Supervisor: HEAD's one fsnotify watcher per path became
// library.Scope.Watch, which already walks recursively, skips hidden and app
// directories, debounces and falls back to polling on a network filesystem.
//
// There is one goroutine per watched root. Roots are admin-registered, a
// handful, not a library-scale collection, so this is not an unbounded fan-out
// over data: the number of goroutines is the number of configured roots.
type Watcher struct {
	o    WatcherOptions
	base context.Context
	stop context.CancelFunc

	mu     sync.Mutex
	closed bool
	active map[library.RootID]*run
	wg     sync.WaitGroup
}

// run is one root's watch goroutine.
type run struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewWatcher builds a Watcher; it starts nothing until Reconcile or Run.
func NewWatcher(o WatcherOptions) (*Watcher, error) {
	switch {
	case o.Source == nil:
		return nil, errors.New("roots: a Watcher needs a WatchSource")
	case o.Handler == nil:
		return nil, errors.New("roots: a Watcher needs a Handler")
	case o.Desired == nil:
		return nil, errors.New("roots: a Watcher needs Desired")
	}
	if o.Interval <= 0 {
		o.Interval = DefaultReconcileInterval
	}
	if o.ResubscribeDelay <= 0 {
		o.ResubscribeDelay = DefaultResubscribeDelay
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	w := &Watcher{o: o, active: map[library.RootID]*run{}}
	w.base, w.stop = context.WithCancel(context.Background())
	return w, nil
}

func (w *Watcher) report(err error) {
	w.o.Logger.Warn("roots: watch", "err", err)
	if w.o.OnError != nil {
		w.o.OnError(err)
	}
}

// Run reconciles once at once, then on every interval tick, until ctx is done
// or the watcher is stopped. It does not stop the watches on exit: Stop does,
// so the caller keeps the order at shutdown.
func (w *Watcher) Run(ctx context.Context) error {
	if w.isClosed() {
		return ErrWatcherStopped
	}
	w.Reconcile(ctx)
	t := time.NewTicker(w.o.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.base.Done():
			return ErrWatcherStopped
		case <-t.C:
			w.Reconcile(ctx)
		}
	}
}

// Reconcile brings the running watches in line with the desired set: it starts
// one for every desired root not running and stops every one no longer desired.
// A watch whose goroutine ended on its own (a root library will not watch) is
// started again here. Safe for concurrent use; a no-op once stopped.
func (w *Watcher) Reconcile(ctx context.Context) {
	if w.isClosed() {
		return
	}
	desired, err := w.o.Desired(ctx)
	if err != nil {
		w.report(fmt.Errorf("roots: enumerate the watched roots, keeping the running watches: %w", err))
		return
	}
	want := map[library.RootID]bool{}
	for _, id := range desired {
		if id != "" {
			want[id] = true
		}
	}
	var gone []*run
	w.mu.Lock()
	if w.closed { // Stop may have landed while Desired ran
		w.mu.Unlock()
		return
	}
	for id, r := range w.active {
		if !want[id] {
			gone = append(gone, r)
			delete(w.active, id)
		}
	}
	for id := range want {
		if _, ok := w.active[id]; ok {
			continue
		}
		rctx, cancel := context.WithCancel(w.base)
		r := &run{cancel: cancel, done: make(chan struct{})}
		w.active[id] = r
		w.wg.Add(1)
		go w.watch(rctx, id, r)
	}
	w.mu.Unlock()
	for _, r := range gone {
		r.cancel()
		<-r.done
	}
}

// watch is one root's goroutine: subscribe, hand batches to the handler, and
// subscribe again when the sequence ends without the watch being cancelled (a
// reload replaced the root). A failed subscription ends the goroutine and
// leaves the retry to the next Reconcile, which is where "library will not
// watch this root" (watching off, owner disabled, root gone) is decided afresh.
func (w *Watcher) watch(ctx context.Context, id library.RootID, r *run) {
	defer w.wg.Done()
	defer close(r.done)
	defer func() {
		w.mu.Lock()
		if w.active[id] == r {
			delete(w.active, id)
		}
		w.mu.Unlock()
	}()
	opts := library.WatchOptions{
		Debounce:   w.o.Debounce,
		Extensions: slices.Clone(w.o.Extensions),
		Recursive:  true,
		OnError:    func(err error) { w.report(fmt.Errorf("root %s: %w", id, err)) },
	}
	for ctx.Err() == nil {
		seq, err := w.o.Source.Watch(ctx, id, opts)
		if err != nil {
			w.report(fmt.Errorf("start watching root %s: %w", id, err))
			return
		}
		for batch := range seq {
			if ctx.Err() != nil {
				return
			}
			if c, ok := changeOf(id, batch); ok {
				w.o.Handler(ctx, c)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.o.ResubscribeDelay):
		}
	}
}

// changeOf folds a batch into a Change; a rescan event marks the whole change.
func changeOf(id library.RootID, batch []library.Event) (Change, bool) {
	c := Change{Root: id}
	for _, e := range batch {
		switch e.Kind {
		case library.EventRescan:
			c.Rescan = true
			c.Dirs = append(c.Dirs, e.Dir)
		case library.EventChanged:
			c.Dirs = append(c.Dirs, e.Dir)
		}
	}
	return c, len(c.Dirs) > 0 || c.Rescan
}

// Stop cancels every watch and waits for each goroutine, handler included, to
// return. It is terminal and idempotent: a later Reconcile or Run starts
// nothing, so a tick racing shutdown cannot resurrect a watch nobody stops.
func (w *Watcher) Stop() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.stop()
	w.wg.Wait()
}

func (w *Watcher) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// Watched is the sorted set of roots with a running watch.
func (w *Watcher) Watched() []library.RootID {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]library.RootID, 0, len(w.active))
	for id := range w.active {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
