// file: core/library/watch.go
// version: 1.0.0
// guid: 06785e21-5df4-4d22-b086-1c461887e004
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchSource is fsnotify behind a seam: the tests inject its errors (the
// overflow) and a failing Add (ENOSPC) without a kernel that produces them.
type watchSource interface {
	Add(dir string) error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

type watchSourceFunc func() (watchSource, error)

type fsnotifySource struct{ w *fsnotify.Watcher }

func newFsnotifySource() (watchSource, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return fsnotifySource{w: w}, nil
}

func (s fsnotifySource) Add(dir string) error          { return s.w.Add(dir) }
func (s fsnotifySource) Events() <-chan fsnotify.Event { return s.w.Events }
func (s fsnotifySource) Errors() <-chan error          { return s.w.Errors }
func (s fsnotifySource) Close() error                  { return s.w.Close() }

// Rescan reasons (05-library 5L.8).
const (
	rescanOverflow  = "overflow"
	rescanAddFailed = "add_failed"
	rescanPoll      = "poll"
)

// maxWaitFactor caps how long a steady stream of events can postpone a batch:
// this many debounce periods after the first event of the batch.
const maxWaitFactor = 12

// Watch yields debounced batches of changed directories until ctx is done or
// the root is replaced or removed by a reload (05-library 5L.3.7). It is a pull
// iterator: batches are delivered on the consumer's goroutine, so a slow
// consumer lets events queue in the kernel, which overflows into an
// EventRescan instead of growing a queue here. Events lost, a watch that could
// not be added and a poll tick all yield EventRescan: lost events cause a
// rescan, never silence.
func (m *manager) Watch(ctx context.Context, id RootID, opts WatchOptions) (iter.Seq[[]Event], error) {
	r, err := m.need(Path{Root: id})
	if err != nil {
		return nil, err
	}
	if !m.enabled(r.spec.Owner) {
		return nil, fmt.Errorf("%w: %s", ErrOwnerDisabled, r.spec.Owner)
	}
	if r.spec.Watch == WatchOff {
		return nil, fmt.Errorf("%w: watching is off for %s", ErrNotSupported, id)
	}
	set := m.reg.Load().set
	w := &watcher{m: m, r: r, opts: opts, debounce: set.debounce, poll: set.pollInterval, exts: normalizeExtensions(opts.Extensions)}
	if opts.Debounce > 0 {
		w.debounce = opts.Debounce
	}
	return func(yield func([]Event) bool) { w.run(ctx, yield) }, nil
}

// watcher is one Watch iteration.
type watcher struct {
	m        *manager
	r        *Root
	opts     WatchOptions
	debounce time.Duration
	poll     time.Duration
	exts     []string

	src     watchSource
	polling bool
	dirs    map[string]struct{} // changed directories of the pending batch
	rescan  string              // reason, when a rescan is pending
}

func (w *watcher) fail(err error) {
	if w.opts.OnError != nil {
		w.opts.OnError(err)
	}
	w.m.log.Warn("library: watch", "root", w.r.spec.ID, "err", err)
}

func (w *watcher) run(ctx context.Context, yield func([]Event) bool) {
	r := w.r
	w.dirs = map[string]struct{}{}
	mode := r.spec.Watch
	if mode == WatchAuto && isNetworkFS(r.abs) {
		mode = WatchPoll
	}
	var pollTick <-chan time.Time
	var ticker *time.Ticker
	startPolling := func(reason string, initial bool) {
		if w.polling {
			return
		}
		w.polling = true
		r.polling.Store(true)
		ticker = time.NewTicker(w.poll)
		pollTick = ticker.C
		if initial {
			w.rescan = reason
		}
	}
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
		if w.polling {
			r.polling.Store(false)
		}
		if w.src != nil {
			if err := w.src.Close(); err != nil {
				w.fail(err)
			}
		}
	}()
	var events <-chan fsnotify.Event
	var errs <-chan error
	if mode == WatchPoll {
		startPolling(rescanPoll, false)
	} else if err := w.open(ctx); err != nil {
		if !resourceExhausted(err) {
			w.fail(fmt.Errorf("start watching %s: %w", r.spec.ID, err))
			return
		}
		w.fail(err)
		startPolling(rescanAddFailed, true)
	} else {
		events, errs = w.src.Events(), w.src.Errors()
	}
	var quiet, capT *time.Timer
	var quietC, capC <-chan time.Time
	arm := func() {
		if quiet == nil {
			quiet = time.NewTimer(w.debounce)
		} else {
			quiet.Reset(w.debounce)
		}
		quietC = quiet.C
		if capT == nil {
			capT = time.NewTimer(w.debounce * maxWaitFactor)
			capC = capT.C
		}
	}
	flush := func() bool {
		if quiet != nil {
			quiet.Stop()
		}
		if capT != nil {
			capT.Stop()
			capT = nil
		}
		quietC, capC = nil, nil
		batch := w.takeBatch()
		return len(batch) == 0 || yield(batch)
	}
	if w.rescan != "" && !flush() { // the add_failed rescan owed at once
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.gone:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if w.onEvent(ctx, ev, startPolling) {
				arm()
			}
		case err, ok := <-errs:
			if !ok {
				return
			}
			w.fail(err)
			w.rescan = rescanOverflow
			arm()
		case <-pollTick:
			w.rescan = rescanPoll
			if !flush() {
				return
			}
		case <-quietC:
			if !flush() {
				return
			}
		case <-capC:
			if !flush() {
				return
			}
		}
	}
}

// resourceExhausted is "no more watches here": the kernel's limit, the file
// descriptor limits, or no memory.
func resourceExhausted(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE) ||
		errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOMEM)
}

// open creates the source and adds a watch on the root and, if Recursive, on
// every directory below it that Walk would enter (AppDir, Quarantine and
// hidden directories are not).
func (w *watcher) open(ctx context.Context) error {
	newSrc := w.m.seam.watchSrc
	if newSrc == nil {
		newSrc = newFsnotifySource
	}
	src, err := newSrc()
	if err != nil {
		return err
	}
	w.src = src
	if err := src.Add(w.r.abs); err != nil {
		return err
	}
	if !w.opts.Recursive {
		return nil
	}
	for e, werr := range w.r.rw.Walk(ctx, ".", WalkOptions{SkipHidden: true}) {
		if werr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.fail(werr)
			continue
		}
		if !e.Info.IsDir() {
			continue
		}
		if err := src.Add(filepath.Join(w.r.abs, filepath.FromSlash(e.Path.Rel))); err != nil {
			if resourceExhausted(err) {
				return err
			}
			w.fail(err) // a directory that vanished between the listing and the watch
		}
	}
	return nil
}

// onEvent records the directory an event changed, and adds a watch for a new
// directory. It reports whether the event starts or extends a batch.
func (w *watcher) onEvent(ctx context.Context, ev fsnotify.Event, startPolling func(string, bool)) bool {
	if ev.Op&^fsnotify.Chmod == 0 {
		return false
	}
	rel, err := filepath.Rel(w.r.abs, ev.Name)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	rel = filepath.ToSlash(rel)
	if w.hiddenOrExcluded(path.Dir(rel)) || isOwnTemp(path.Base(rel)) {
		return false
	}
	if ev.Op&fsnotify.Create != 0 {
		if st, lerr := w.r.rw.Lstat(rel); lerr == nil && st.IsDir() {
			if hiddenDir(path.Base(rel)) || w.r.base.excluded(rel) {
				return false
			}
			if w.opts.Recursive {
				if aerr := w.src.Add(ev.Name); aerr != nil {
					if resourceExhausted(aerr) {
						w.fail(aerr)
						startPolling(rescanAddFailed, true)
						return true
					}
					w.fail(aerr)
				}
			}
			// Files may have landed in it before the watch did: report it.
			w.dirs[rel] = struct{}{}
			return true
		}
	}
	if len(w.exts) > 0 && !matchExt(w.exts, path.Base(rel)) {
		return false
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	w.dirs[dir] = struct{}{}
	return true
}

func (w *watcher) hiddenOrExcluded(dir string) bool {
	if dir == "." || dir == "" {
		return false
	}
	if w.r.base.excluded(dir) {
		return true
	}
	for _, seg := range strings.Split(dir, "/") {
		if hiddenDir(seg) {
			return true
		}
	}
	return false
}

// isOwnTemp is a temp or aside file library itself makes next to a destination:
// its events are the transfer's, not a change the consumer should rescan for.
func isOwnTemp(base string) bool {
	return strings.HasPrefix(base, tmpPrefix) || strings.Contains(base, asideInfix)
}

// takeBatch builds the batch of pending changes (a rescan first) and counts it.
func (w *watcher) takeBatch() []Event {
	var batch []Event
	if w.rescan != "" {
		batch = append(batch, Event{Kind: EventRescan, Dir: Path{Root: w.r.spec.ID}})
		w.m.ctr.rescan(w.r.spec.ID, w.rescan)
		w.m.ctr.watchEvent(w.r.spec.ID, EventRescan)
		w.rescan = ""
	}
	dirs := make([]string, 0, len(w.dirs))
	for d := range w.dirs {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	for _, d := range dirs {
		batch = append(batch, Event{Kind: EventChanged, Dir: Path{Root: w.r.spec.ID, Rel: d}})
		w.m.ctr.watchEvent(w.r.spec.ID, EventChanged)
	}
	clear(w.dirs)
	return batch
}
