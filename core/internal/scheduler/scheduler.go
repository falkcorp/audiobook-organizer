// file: core/internal/scheduler/scheduler.go
// version: 1.0.1
// guid: e077a1c0-bf41-4e89-bbc0-6e1675ec5276
// last-edited: 2026-11-11

// Package scheduler is core's task scheduler (05-tasks 5T.3.8, F2-04), written
// fresh against tasks.ClockStore and the executor's StartIn/WriteRuns, not
// moved from HEAD's internal/scheduler. One goroutine polls every
// core.tasks.schedule_poll; each scheduled kind has one durable clock; a due
// fire is one storage batch, the clock advance and the run's insert, so a
// crash between them can neither lose nor duplicate a fire.
//
// It is dark: before the tasks flip (F5) nothing in ABO constructs it, and the
// kernel's start rule that constructs it for hello is F2-16's.
//
// What replaces what (HEAD's internal/scheduler, by file name only):
//
//	scheduler.go  scheduler.go's loop, v3_schedule.go's runV3Loop and fireV3
//	clock.go      interval_clock.go, v3_schedule.go's countDue and gate edges
//	cron.go       cron.go and pkg/ops' cron field parser
//	daily.go      daily_at.go
//	window.go     maintenance.go and the maintenance.window op's body
//	validate.go   pkg/ops' schedule checks, as registration rules (5T.3.1)
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Config reads the config values the scheduler re-reads every poll: each
// schedule key's tasks.ScheduleConfig and each core.tasks.windows.<name>
// entry. The kernel adapts core config to it (F2-16). Names coined by F2-04.
type Config interface {
	// Schedule is the current value of a ScheduleFromConfig key. An error
	// (undeclared, undecodable) counts as a schedule error and skips the
	// kind for this poll.
	Schedule(key string) (tasks.ScheduleConfig, error)
	// Window is core.tasks.windows.<name>; ok is false when it is not set.
	Window(name string) (w WindowConfig, ok bool)
}

// WindowConfig is one core.tasks.windows.<name> entry (05-tasks 5T.10): hours
// 0-23 in core.tasks.timezone, wrapping midnight when End <= Start (HEAD's
// IsInMaintenanceWindowAt), and the enable flag.
type WindowConfig struct {
	Start, End int
	Enabled    bool
}

// Metrics records the schedule families. The executor owns them (05-tasks
// 5T.11; executor.ScheduleMetrics), so the kernel passes executor.Schedules(x)
// and the instruments follow the executor's switch: none exist in ABO before
// F5. nil records nothing.
type Metrics interface {
	ScheduleLag(kind tasks.Kind, lag time.Duration)
	ScheduleMissed(kind tasks.Kind, n int64)
	ScheduleError(kind tasks.Kind)
}

// Observer sees every committed scheduler write, in commit order: the
// schedule-parity harness (tasks/taskstest/schedule) records through it. nil
// observes nothing. It is called on the scheduler goroutine (or the window
// run's worker) after the commit; it must not block.
type Observer interface {
	// Advanced: a clock was seeded (had false) or advanced.
	Advanced(kind tasks.Kind, prev tasks.Clock, had bool, next tasks.Clock)
	// Fired: a due instant fired. Run is the run started, or the active run
	// folded into (Folded); zero when the When gate closed or the start
	// failed (Err).
	Fired(f Fire)
	// WindowOpened: a window fired, with the members its run will start.
	WindowOpened(name string, members []tasks.Kind)
}

// Fire is one fire the Observer sees.
type Fire struct {
	Kind    tasks.Kind
	Trigger tasks.Trigger // schedule, on_start, window
	DueAt   time.Time     // zero for on_start
	Missed  int           // due instants counted as missed by this fire
	Run     tasks.RunRef
	Folded  bool  // an active run of the kind and mode existed; nothing was inserted
	Gated   bool  // the When gate was closed: the clock advanced, no run
	Err     error // the start failed; the clock advanced alone
	Parent  tasks.RunID
}

// Options builds a Scheduler. Stores, Starter and Config are required. Names
// coined by F2-04.
type Options struct {
	// Stores are the clock and run stores (NativeStores over c/tasks/):
	// Clocks for the durable clocks, Runs for the active-run fold check.
	Stores tasks.Stores
	// Starter is the executor's StartIn and WriteRuns: every fire stages its
	// clock advance and its run into one batch that WriteRuns commits.
	Starter tasks.Stager
	// NewBatch opens the indexed batch a fire commits; nil: Stores.Runs'
	// NewBatch (NativeStores offers one).
	NewBatch func() *storage.Batch
	// Definitions are every registered kind; those whose Common.Schedule is
	// set are scheduled, and the core.window.<name> kinds among them are
	// the windows.
	Definitions []tasks.Definition
	Config      Config

	// Poll is core.tasks.schedule_poll (restart-bound; 0 = 1m).
	Poll time.Duration
	// Location is core.tasks.timezone (restart-bound; nil = time.Local).
	Location *time.Location
	// Held reports a held kind (a disabled module's, 5T.3.11): its clock is
	// not polled. nil: none is held.
	Held func(tasks.Kind) bool

	Metrics  Metrics
	Observer Observer
	Logger   *slog.Logger
	// Now is the clock source (nil = time.Now). Tick builds Run's ticker
	// (nil = time.NewTicker): tests and the harness drive Boot and Poll
	// directly instead.
	Now  func() time.Time
	Tick func(d time.Duration) (c <-chan time.Time, stop func())
}

// entry is one scheduled kind.
type entry struct {
	kind     tasks.Kind
	sched    *tasks.Schedule
	readOnly bool
}

// Scheduler is the one scheduler goroutine and its state. Its fields are
// fixed at New; the only mutable state is the Run/Stop handshake.
type Scheduler struct {
	opts    Options
	log     *slog.Logger
	entries []entry // scheduled kinds, by kind
	byKind  map[tasks.Kind]entry
	windows []string // registered window names, sorted
	open    func() *storage.Batch

	mu      sync.Mutex
	stop    chan struct{}
	done    chan struct{}
	stopped bool

	// pollMu guards lastPoll: when this process last polled (or booted), the
	// bound SkipMissed measures "came due while up" against.
	pollMu   sync.Mutex
	lastPoll time.Time
}

// batchOpener is NativeStores' run store's batch opener.
type batchOpener interface{ NewBatch() *storage.Batch }

var errNoStores = errors.New("scheduler: Options.Stores.Clocks and Options.Stores.Runs are required")

// New builds a Scheduler. It does not start it.
func New(opts Options) (*Scheduler, error) {
	if opts.Stores.Clocks == nil || opts.Stores.Runs == nil {
		return nil, errNoStores
	}
	if opts.Starter == nil {
		return nil, errors.New("scheduler: Options.Starter is required")
	}
	if opts.Config == nil {
		return nil, errors.New("scheduler: Options.Config is required")
	}
	if opts.NewBatch == nil {
		bo, ok := opts.Stores.Runs.(batchOpener)
		if !ok {
			return nil, errors.New("scheduler: Options.NewBatch is nil and Stores.Runs opens no batch")
		}
		opts.NewBatch = bo.NewBatch
	}
	if opts.Poll <= 0 {
		opts.Poll = time.Minute
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Tick == nil {
		opts.Tick = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Scheduler{opts: opts, log: opts.Logger, byKind: map[tasks.Kind]entry{}, open: opts.NewBatch}
	for _, d := range opts.Definitions {
		if d == nil {
			return nil, errors.New("scheduler: nil definition")
		}
		k := d.Kind()
		if name, ok := tasks.WindowName(k); ok {
			s.windows = append(s.windows, name)
			continue
		}
		c := tasks.CommonOf(d)
		if c.Schedule == nil {
			continue
		}
		if _, dup := s.byKind[k]; dup {
			return nil, fmt.Errorf("scheduler: kind %s registered twice", k)
		}
		e := entry{kind: k, sched: c.Schedule, readOnly: !c.Effects.Declared() || c.Effects.IsReadOnly()}
		s.entries = append(s.entries, e)
		s.byKind[k] = e
	}
	slices.SortFunc(s.entries, func(a, b entry) int { return cmpKind(a.kind, b.kind) })
	slices.Sort(s.windows)
	return s, nil
}

func cmpKind(a, b tasks.Kind) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Run is the scheduler goroutine (06 §6.5.2: the step after the executor's):
// Boot, then one Poll every core.tasks.schedule_poll until ctx ends or Stop is
// called. It returns nil on either. Run may be called once.
func (s *Scheduler) Run(ctx context.Context) error {
	s.mu.Lock()
	switch {
	case s.stopped:
		s.mu.Unlock()
		return errors.New("scheduler: Run after Stop")
	case s.stop != nil:
		s.mu.Unlock()
		return errors.New("scheduler: Run called twice")
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	stop, done := s.stop, s.done
	s.mu.Unlock()
	defer close(done)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	s.Boot(ctx)
	ticks, stopTick := s.opts.Tick(s.opts.Poll)
	defer stopTick()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			s.Poll(ctx)
		}
	}
}

// Stop ends Run and waits for it to return, up to ctx (05-tasks 5T.4 step 4:
// the scheduler stops before the executor drains). A Stop before Run, or a
// second Stop, returns at once; a Run after Stop refuses to start.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	stop, done := s.stop, s.done
	s.mu.Unlock()
	if stop == nil {
		return nil
	}
	close(stop)
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("scheduler: stop: %w", context.Cause(ctx))
	}
}

// Boot fires every OnStart schedule once (HEAD's runV3OnStart and the
// TaskDefinitions' RunOnStart): a kind whose key is enabled, that is not
// held and not a window member, and whose When gate is open, gets one run
// with trigger on_start, and its clock is stamped in the same batch so the
// first poll does not fire it again. Run calls it once; tests and the
// harness call it directly.
func (s *Scheduler) Boot(ctx context.Context) {
	now := s.opts.Now()
	s.pollMu.Lock()
	s.lastPoll = now
	s.pollMu.Unlock()
	for _, e := range s.entries {
		if !e.sched.RunsOnStart() || s.held(e.kind) {
			continue
		}
		eff, err := s.effective(e)
		if err != nil {
			s.scheduleError(e.kind, err)
			continue
		}
		if !eff.enabled || eff.window != "" {
			continue
		}
		s.commitFire(ctx, e.kind, tasks.TriggerOnStart, func(prev tasks.Clock, had bool) (decision, error) {
			next := prev
			if !had {
				next = tasks.Clock{}
			}
			// LastFire and LastRun are stamped by commitFire, and only when
			// the When gate lets the run through.
			next.LastDue = now
			next.GateClosedAt = time.Time{}
			next.ScheduleHash = eff.hash
			return decision{write: true, next: next, fire: true, at: now}, nil
		}, s.startFor(e, tasks.TriggerOnStart, ""), s.opts.Observer)
	}
}

// Poll is one due check of every scheduled kind and every window, at
// Options.Now. A kind's error (config, clock read, start) is counted and
// logged and never stops the others; a held kind is skipped whole.
func (s *Scheduler) Poll(ctx context.Context) {
	now := s.opts.Now()
	s.pollMu.Lock()
	since := s.lastPoll
	s.lastPoll = now
	s.pollMu.Unlock()
	for _, e := range s.entries {
		if ctx.Err() != nil {
			return
		}
		if s.held(e.kind) {
			continue // 5T.3.11: a held kind's clock is not polled
		}
		s.pollEntry(ctx, e, now, since)
	}
	for _, name := range s.windows {
		if ctx.Err() != nil {
			return
		}
		s.pollWindow(ctx, name, now)
	}
}

func (s *Scheduler) held(k tasks.Kind) bool { return s.opts.Held != nil && s.opts.Held(k) }

// effective is e's schedule under the current config: the relay's fixed
// interval, or its key's value checked by effectiveOf.
func (s *Scheduler) effective(e entry) (effective, error) {
	if d := e.sched.Interval(); d > 0 {
		return effective{enabled: true, t: interval(d), hash: hashOf("fixed", d.String())}, nil
	}
	key := e.sched.ConfigKey()
	if key == "" {
		return effective{}, fmt.Errorf("%w: %s has a schedule with no timing", errConfig, e.kind)
	}
	c, err := s.opts.Config.Schedule(key)
	if err != nil {
		return effective{}, fmt.Errorf("scheduler: %s: read %s: %w", e.kind, key, err)
	}
	eff, err := effectiveOf(c, s.opts.Poll, s.opts.Location)
	if err != nil {
		return effective{}, fmt.Errorf("%s (%s): %w", e.kind, key, err)
	}
	if eff.window != "" && !slices.Contains(s.windows, eff.window) {
		return effective{}, fmt.Errorf("%w: %s (%s) joins window %q, which has no registered window kind", errConfig, e.kind, key, eff.window)
	}
	return eff, nil
}

// pollEntry is one due check of e.
func (s *Scheduler) pollEntry(ctx context.Context, e entry, now, since time.Time) {
	eff, err := s.effective(e)
	if err != nil {
		s.scheduleError(e.kind, err)
		return
	}
	if eff.window != "" {
		return // a window member: the window runs it; its own clock is not polled
	}
	in := pollInput{now: now, since: since, poll: s.opts.Poll, loc: s.opts.Location, eff: eff, missed: e.sched.OnMissed()}
	s.commitFire(ctx, e.kind, tasks.TriggerSchedule, func(prev tasks.Clock, had bool) (decision, error) {
		return decide(prev, had, in), nil
	}, s.startFor(e, tasks.TriggerSchedule, ""), s.opts.Observer)
}

// starter stages one run of a fire into b, or reports that it folded.
type starter func(ctx context.Context, b *storage.Batch) (tasks.RunRef, bool, error)

// startFor is e's start: the schedule's mode (Live or Preview; Live for a
// read-only kind that states neither) and params, the system actor of the
// kind's module (StartIn stamps it), trigger tr. Folding is the executor's:
// StartIn applies the kind's DedupePolicy (by default a queued or running run
// of the kind and mode with identical params), and a start that folded comes
// back RunRef.Existing. This package has no fold rule of its own, so a kind's
// Key, Merge and AnyParams policies hold for a scheduled start as for any
// other.
func (s *Scheduler) startFor(e entry, tr tasks.Trigger, parent tasks.RunID) starter {
	mode := tasks.ModeRequestLive
	if e.sched.IsPreview() || (!e.sched.IsLive() && !e.readOnly) {
		mode = tasks.ModeRequestPreview
	}
	return func(ctx context.Context, b *storage.Batch) (tasks.RunRef, bool, error) {
		ref, err := s.opts.Starter.StartIn(ctx, b, e.kind, tasks.StartRequest{
			Mode: mode, Params: e.sched.RunParams(), Trigger: tr, Parent: parent,
		})
		return ref, ref.Existing, err
	}
}

// errNoChange ends a fire's build when its decision writes nothing.
var errNoChange = errors.New("scheduler: no change")

// commitFire is the one write path of a due check (05-tasks 5T.3.8 "a due
// fire is one storage batch"): inside the executor's WriteRuns it reads the
// kind's clock, lets decide choose from that read, stages
// ClockStore.Advance (checked at commit against the read) and, for a fire
// whose When gate is open, the run's StartIn, and commits both or neither.
// A retry re-reads the clock, so a poll that lost the race to another
// writer sees the advanced clock and does not fire; ErrClockConflict at
// staging is the same "another poll won", and is not an error.
//
// A start the executor refuses (refused: a mode, permission or plan rule)
// would abort the whole batch and leave the clock where it was, so the kind
// would retry on every poll; the advance is then committed alone and the
// failure counted (HEAD stamped its clock before calling the trigger for the
// same reason). Any other start error (storage I/O in the fold check or in
// StartIn) aborts the batch: the clock is not advanced and the next poll
// fires.
//
// A clock read error skips the kind for this poll (metric schedule_errors),
// never "due now".
func (s *Scheduler) commitFire(ctx context.Context, kind tasks.Kind, tr tasks.Trigger,
	decideFn func(prev tasks.Clock, had bool) (decision, error), start starter, obs Observer) {
	type result struct {
		prev    tasks.Clock
		had     bool
		d       decision
		ref     tasks.RunRef
		folded  bool
		gated   bool
		started bool
	}
	run := func(withStart bool) (result, error) {
		var res result
		err := s.opts.Starter.WriteRuns(ctx, s.open, func(ctx context.Context, b *storage.Batch) error {
			res = result{}
			prev, had, err := s.opts.Stores.Clocks.Get(ctx, kind)
			if err != nil {
				return &clockReadError{err: err}
			}
			d, err := decideFn(prev, had)
			if err != nil {
				return err
			}
			res.prev, res.had, res.d = prev, had, d
			if !d.write {
				return errNoChange
			}
			fire := d.fire && withStart
			if d.fire {
				if !s.gateOpen(kind) {
					res.gated, fire = true, false
				} else if withStart {
					d.next.LastFire = d.dueAt
					if tr == tasks.TriggerOnStart {
						d.next.LastFire = d.at
					}
					d.next.LastRun = d.at
					res.d = d
				}
			}
			if err := s.opts.Stores.Clocks.Advance(ctx, b, kind, prev, had, d.next); err != nil {
				return err
			}
			if !fire {
				return nil
			}
			ref, folded, err := start(ctx, b)
			if err != nil {
				if refused(err) {
					return &startError{err: err}
				}
				// A storage error (the fold check's read, StartIn's own
				// reads and staging): the whole batch aborts, the clock
				// stays where it was, and the next poll fires.
				return err
			}
			res.ref, res.folded, res.started = ref, folded, true
			return nil
		})
		return res, err
	}
	res, err := run(true)
	if startErr, ok := errors.AsType[*startError](err); ok {
		res, err = run(false)
		if err == nil {
			s.commitObserved(obs, kind, tr, res.prev, res.had, res.d, tasks.RunRef{}, false, false, startErr.err)
			s.scheduleError(kind, fmt.Errorf("scheduler: %s: start: %w", kind, startErr.err))
			return
		}
	}
	switch {
	case err == nil:
		s.commitObserved(obs, kind, tr, res.prev, res.had, res.d, res.ref, res.folded, res.gated, nil)
	case errors.Is(err, errNoChange), errors.Is(err, tasks.ErrClockConflict):
		// Nothing due, or another poll won: no fire.
	default:
		s.scheduleError(kind, fmt.Errorf("scheduler: %s: %w", kind, err))
	}
}

// commitObserved records a committed fire: the metrics and the Observer.
func (s *Scheduler) commitObserved(obs Observer, kind tasks.Kind, tr tasks.Trigger, prev tasks.Clock, had bool, d decision,
	ref tasks.RunRef, folded, gated bool, startErr error) {
	if obs != nil {
		obs.Advanced(kind, prev, had, d.next)
	}
	if d.missed > 0 && s.opts.Metrics != nil {
		s.opts.Metrics.ScheduleMissed(kind, int64(d.missed))
	}
	if !d.fire {
		return
	}
	if !d.dueAt.IsZero() && s.opts.Metrics != nil && startErr == nil && !gated {
		s.opts.Metrics.ScheduleLag(kind, d.next.LastDue.Sub(d.dueAt))
	}
	if obs != nil {
		f := Fire{Kind: kind, Trigger: tr, Missed: d.missed, Run: ref, Folded: folded, Gated: gated, Err: startErr}
		if tr != tasks.TriggerOnStart {
			f.DueAt = d.dueAt
		}
		obs.Fired(f)
	}
}

// gateOpen reads kind's When gate (true without one, and for a window kind).
func (s *Scheduler) gateOpen(kind tasks.Kind) bool {
	e, ok := s.byKind[kind]
	return !ok || e.sched.Open()
}

// clockReadError: the poll's clock read failed.
type clockReadError struct{ err error }

func (e *clockReadError) Error() string { return "scheduler: read clock: " + e.err.Error() }
func (e *clockReadError) Unwrap() error { return e.err }

// refused reports whether a start error is the executor refusing the start
// itself (an unknown or held kind, a permission, a mode, priority or plan
// rule, a write outside the declared effects): retrying it at every poll would fail the
// same way, so commitFire advances the clock alone. Every other error is
// treated as transient and fires at the next poll. A definition's static
// Params that fail to decode is not one of these sentinels: it is retried and
// counted at every poll until the definition is fixed.
func refused(err error) bool {
	for _, target := range []error{
		executor.ErrUnknownKind, executor.ErrHeld, executor.ErrNotPermitted,
		executor.ErrPlanRequired, executor.ErrInvalidPriority, executor.ErrPlanInvalid, executor.ErrPlanTooOld,
		executor.ErrApprovalRequired, executor.ErrPlanRejected, executor.ErrPlanDecided,
		tasks.ErrModeUnset, tasks.ErrOutsideEffects,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// startError: the executor refused a fire's start (refused).
type startError struct{ err error }

func (e *startError) Error() string { return "scheduler: start: " + e.err.Error() }
func (e *startError) Unwrap() error { return e.err }

// scheduleError counts and logs one skipped due check.
func (s *Scheduler) scheduleError(kind tasks.Kind, err error) {
	if s.opts.Metrics != nil {
		s.opts.Metrics.ScheduleError(kind)
	}
	s.log.Warn("scheduler: due check skipped", "kind", string(kind), "err", err)
}
