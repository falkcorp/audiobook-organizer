// file: core/kernel/supervisor.go
// version: 1.0.6
// guid: e84396f3-c126-49af-a149-17cc1398aeac
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/sdnotify"
	"github.com/falkcorp/mediacore/module"
)

// The supervisor's defaults. Each is a config key's default (06 §6.8,
// 05-health §5H.4); until config lands (F3) nothing overrides them except
// R1Values.ShutdownBudget.
const (
	defaultShutdownBudget = 25 * time.Second // core.shutdown.budget
	httpDrain             = 10 * time.Second // core.shutdown.http_drain
	startTimeout          = 60 * time.Second // core.lifecycle.start_timeout
	restartExitCode       = 75               // core.lifecycle.restart_exit_code
	// lateStopFloor is the least time the final close of metrics and logging
	// gets once the budget is spent, so buffered records still reach their
	// outputs. It is the one step with a floor: Stop hooks reached after the
	// deadline get none (stopHooks), so a stop overruns the budget by at most
	// this much. It is also the longest a start rollback waits for an
	// abandoned Start (awaitLateStart).
	lateStopFloor = time.Second
)

// errRestartRequested is what Run returns after a restart request's stop
// order when Config.Exit returns (a test's); in production the process has
// exited with restartExitCode by then, so no caller outside the kernel can
// observe it and it is not exported.
var errRestartRequested = errors.New("kernel: restart requested")

var errRunTwice = errors.New("kernel: Run called more than once")

// ReadinessNotifier receives the supervisor's readiness signals (05-health
// §5H.3.4): Status while not ready (STATUS=), Ready once the listeners are up
// and startup has completed (READY=1), Stopping when shutdown begins
// (STOPPING=1). Config.Notifier's default is systemdNotifier, which sends
// them over NOTIFY_SOCKET (05-health §5H.10, Landing R1). A returned error is
// logged at WARN and never stops the app: the signals are not durable.
type ReadinessNotifier interface {
	Ready() error
	Status(string) error
	Stopping() error
}

// systemdNotifier is the default ReadinessNotifier: the systemd notify
// protocol over NOTIFY_SOCKET. Every method is a no-op returning nil when the
// variable is unset (Type=simple, tests, a developer machine). It keeps no
// state, so Run's goroutine and the hooks' ExtendStart calls never contend.
type systemdNotifier struct{}

func (systemdNotifier) Ready() error          { return sdnotify.Ready() }
func (systemdNotifier) Status(s string) error { return sdnotify.Status(s) }
func (systemdNotifier) Stopping() error       { return sdnotify.Stopping() }

// The STATUS= lines `systemctl status` shows once the app is up and once it
// is stopping. They are HEAD's, byte for byte (the server sent them from
// server_lifecycle.go until R1-16), and fixed strings only: anyone who can
// run `systemctl status` reads them, so nothing here may carry a path, a
// version, a count or a credential.
const (
	statusServing  = "Serving; readiness is reported by the health endpoint"
	statusStopping = "Shutting down"
)

// Server is one listener as the supervisor drives it: *httpapi.Listener
// (Config.Listen's default) or a test's stand-in.
type Server interface {
	Addr() net.Addr
	Serve() error
	Shutdown(ctx context.Context) error
}

var _ Server = (*httpapi.Listener)(nil)

// Lifecycle carries the supervisor's one restart path (06 §6.5.2 "Restart
// request", OPS8-12).
type Lifecycle struct {
	once      sync.Once
	reason    string
	requested chan struct{}
	ready     atomic.Bool  // set by Run at start step 8 (readiness)
	log       *slog.Logger // the kernel's log, set at P5 before any module code
}

func newLifecycle() *Lifecycle { return &Lifecycle{requested: make(chan struct{})} }

// RequestRestart asks the running app to restart: Run logs the reason, runs
// the stop order within core.shutdown.budget and then exits with
// core.lifecycle.restart_exit_code (75), so the unit's Restart=on-failure
// brings the service back; never exit 0, which would leave a staged swap
// untaken. The first request's reason wins; later ones are no-ops.
//
// A request before the app is ready (start step 8) is logged at ERROR and
// ignored, and does not use up the first-reason slot: a module that asks
// from Register, a hook's Start or a worker's first moments would otherwise
// restart the app in a loop, every boot asking again.
func (l *Lifecycle) RequestRestart(reason string) {
	if !l.ready.Load() {
		if l.log != nil {
			l.log.Error("kernel: restart requested before the app was ready; ignored", "reason", reason)
		}
		return
	}
	l.once.Do(func() {
		l.reason = reason
		close(l.requested)
	})
}

// moduleHook is a Hook with the module that registered it.
type moduleHook struct {
	module module.ID
	hook   module.Hook
}

// moduleWorker is a Worker with the module that registered it.
type moduleWorker struct {
	module module.ID
	worker module.Worker
}

// Run starts the app in the order of 06 §6.5.2 that exists at R1 (core
// background services, module hooks, module workers, R1Values then the
// listener, readiness), blocks until ctx is done, a StopAppOnExit worker
// exits, the listener fails or a restart is requested, and then stops in the
// reverse order within core.shutdown.budget (05-health §5H.4).
//
// Its error is the cause of the stop (nil for ctx cancellation, the worker's
// or the listener's error otherwise) joined with whatever went wrong while
// stopping: a Stop hook's error and every step abandoned at the budget, each
// also logged at ERROR. A clean shutdown after ctx cancellation returns nil.
// If a hook's Start fails or outlives core.lifecycle.start_timeout, the hooks
// already started are stopped in reverse and Run returns the joined error.
func (k *Kernel) Run(ctx context.Context) error {
	if !k.ran.CompareAndSwap(false, true) {
		return errRunTwice
	}
	k.signal(ctx, "status", k.notifier.Status("starting"))
	// (1) Core background services: the metrics Provider has served since
	// P5; the realtime hub lands with realtime.
	// (2) Module hooks, modules in ID order, each module's in registration
	// order (the reset hook arrives with storage).
	started, late, err := k.startHooks(ctx)
	if err != nil {
		// R1Values (and core.shutdown.budget with it) is read at start step
		// 7, after the hooks, so a start failure rolls back within core's
		// default budget. A hook whose Start was abandoned has not started:
		// the rollback waits for it only briefly (awaitLateStart).
		deadline := time.Now().Add(defaultShutdownBudget)
		started = append(started, k.awaitLateStart(ctx, late, deadline)...)
		return errors.Join(err, k.stopHooks(ctx, started, deadline), k.closeCore(ctx, deadline))
	}
	// (3) Module workers. Steps (3a) to (6) arrive with storage, health,
	// events and tasks.
	ws := k.startWorkers(ctx)
	// (7) Listeners: R1Values once (05-config §1.12.1), then the listener.
	v, err := k.r1Values()
	budget := v.ShutdownBudget
	if budget == 0 {
		budget = defaultShutdownBudget
	}
	var srv Server
	// A second owner of a family refused since New (an instrument created
	// through the process-global MeterProvider once the app installed it, or
	// in a hook's Start) still fails boot: nothing is served yet (05-metrics
	// §8, TestFamilyTaken_ThroughGlobalProvider).
	familyErr := k.familyTaken("before the listener opened")
	switch {
	case familyErr != nil:
		err = familyErr
	case err != nil:
		err = fmt.Errorf("kernel: app.Options.R1Values: %w", err)
	case budget < 0:
		err = fmt.Errorf("kernel: core.shutdown.budget %s is negative", v.ShutdownBudget)
		budget = defaultShutdownBudget
	default:
		if srv, err = k.listen(httpapi.ListenOptions{Addr: v.Listen, TLSCertFile: v.TLSCertFile, TLSKeyFile: v.TLSKeyFile},
			k.handler, k.coreLog("http")); err != nil {
			err = fmt.Errorf("kernel: listener: %w", err)
		}
	}
	if err != nil {
		deadline := time.Now().Add(budget)
		return errors.Join(err, k.stopWorkers(ctx, ws, deadline), k.stopHooks(ctx, started, deadline),
			k.closeCore(ctx, deadline))
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()
	k.log.InfoContext(ctx, "kernel: listening", "addr", srv.Addr().String())
	// (8) Readiness. The readiness core and its /readyz arrive with health
	// (F2); until then readiness is the notifier's READY=1.
	k.signal(ctx, "ready", k.notifier.Ready())
	k.signal(ctx, "status", k.notifier.Status(statusServing))
	k.log.InfoContext(ctx, "kernel: ready")
	k.lifecycle.ready.Store(true)

	var cause error
	serveDone, restart := false, false
	select {
	case <-ctx.Done():
	case cause = <-ws.stopApp:
	case err := <-served:
		serveDone = true
		cause = fmt.Errorf("kernel: listener stopped serving: %w", err)
		if err == nil {
			cause = errors.New("kernel: listener stopped serving")
		}
	case <-k.lifecycle.requested:
		restart = true
		k.log.WarnContext(ctx, "kernel: restart requested", "reason", k.lifecycle.reason)
	}
	deadline := time.Now().Add(budget)
	stopErr := k.stop(ctx, deadline, srv, served, serveDone, ws, started)
	if restart {
		k.exit(restartExitCode)
		return errors.Join(fmt.Errorf("%w: %s", errRestartRequested, k.lifecycle.reason), stopErr)
	}
	return errors.Join(cause, stopErr)
}

// stop is the stop order of 06 §6.5.2 that exists at R1: readiness false
// (STOPPING=1, 05-health §5H.3.4 step 3), the listener's drain within
// min(core.shutdown.http_drain, remaining), workers cancelled and awaited,
// module hooks' Stop in exact reverse start order, then metrics and logging,
// which close regardless of the budget. The scheduler, the task drain,
// Bus.Close, step (3a)'s components and the realtime hub arrive with their
// packages, and so do their budget slices.
func (k *Kernel) stop(ctx context.Context, deadline time.Time, srv Server, served <-chan error, serveDone bool,
	ws *workerSet, started []moduleHook) error {
	var errs []error
	k.signal(ctx, "stopping", k.notifier.Stopping())
	k.signal(ctx, "status", k.notifier.Status(statusStopping))
	k.log.InfoContext(ctx, "kernel: stopping", "budget", time.Until(deadline).Round(time.Millisecond).String())

	drain := time.Now().Add(httpDrain)
	if deadline.Before(drain) {
		drain = deadline
	}
	dctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), drain)
	if err := srv.Shutdown(dctx); err != nil {
		k.log.ErrorContext(ctx, "kernel: listener drain abandoned; connections closed", "err", err)
		errs = append(errs, fmt.Errorf("kernel: listener drain: %w", err))
	}
	cancel()
	if !serveDone {
		if ok, err := waitUntil(served, deadline); !ok {
			k.log.ErrorContext(ctx, "kernel: listener abandoned at the shutdown budget")
			errs = append(errs, fmt.Errorf("kernel: listener abandoned: %w", context.DeadlineExceeded))
		} else if err != nil {
			k.log.WarnContext(ctx, "kernel: listener returned an error while stopping", "err", err)
		}
	}
	errs = append(errs, k.stopWorkers(ctx, ws, deadline), k.stopHooks(ctx, started, deadline), k.closeCore(ctx, deadline))
	return errors.Join(errs...)
}

// lateStart is a hook whose Start was abandoned (it outlived
// core.lifecycle.start_timeout or Run's ctx) and may still return nil.
type lateStart struct {
	hook   moduleHook
	result <-chan error
}

// startWindow is the time the hook Start now running has left, the one
// deadline of the start step that Deps.ExtendStart can move. A context's
// deadline cannot move, so the hook's ctx carries none: the window's timer
// cancels it with the cause context.DeadlineExceeded when the deadline
// passes. Hooks start one at a time, so one window serves the whole boot.
//
// extend only moves the deadline later and only for the module whose Start is
// running, so a step that makes real progress is not abandoned at
// core.lifecycle.start_timeout while a hung step is still abandoned once its
// own budget runs out (there is deliberately no ticker: HEAD's rule, see
// sdnotify.StartupStepBudget). It is safe for concurrent use: the hook's
// goroutine calls extend while the timer's goroutine reads the deadline.
type startWindow struct {
	mu       sync.Mutex
	owner    module.ID
	active   bool
	gen      uint64 // of the open window: a stale timer callback compares it
	deadline time.Time
	moved    bool // extend moved deadline later since begin; kept after close
	timer    *time.Timer
	expire   func()
}

// begin opens the window for owner's Start with d to run and calls expire,
// at most once and without holding the lock, when the deadline passes with
// the window still open. The returned func closes the window.
func (w *startWindow) begin(owner module.ID, d time.Duration, expire func()) (end func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gen++
	gen := w.gen
	w.owner, w.active, w.deadline, w.moved, w.expire = owner, true, time.Now().Add(d), false, expire
	w.timer = time.AfterFunc(d, func() { w.fire(gen) })
	return func() { w.close(gen) }
}

// close ends window gen (a no-op for a window that already ended).
func (w *startWindow) close(gen uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gen != gen || !w.active {
		return
	}
	w.active, w.expire = false, nil
	w.timer.Stop()
}

// fire is the timer's callback for window gen: it re-arms the timer when
// extend moved the deadline past it, and expires the window otherwise.
func (w *startWindow) fire(gen uint64) {
	w.mu.Lock()
	if w.gen != gen || !w.active {
		w.mu.Unlock()
		return
	}
	if rest := time.Until(w.deadline); rest > 0 {
		w.timer.Reset(rest)
		w.mu.Unlock()
		return
	}
	expire := w.expire
	w.active, w.expire = false, nil
	w.mu.Unlock()
	expire()
}

// extend moves the deadline to at least d from now, never earlier than it
// is, when owner's Start is the one running. It reports whether it did.
func (w *startWindow) extend(owner module.ID, d time.Duration) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.active || w.owner != owner {
		return false
	}
	if later := time.Now().Add(d); later.After(w.deadline) {
		w.deadline, w.moved = later, true
	}
	return true
}

// wasExtended reports whether the latest window's deadline was moved later by
// extend. Hooks start one at a time, so after a hook's window closes it is
// that hook's.
func (w *startWindow) wasExtended() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.moved
}

// startCtx is the ctx a hook's Start gets: Run's ctx, cancelled by the start
// window. Err reports context.DeadlineExceeded when the window expired, as a
// ctx with a deadline would, so a Start that returns ctx.Err() reports the
// same error it always did; context.Cause says the same.
type startCtx struct{ context.Context }

func (c startCtx) Err() error {
	err := c.Context.Err()
	if err != nil && errors.Is(context.Cause(c.Context), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

// startHooks starts every hook in order, each in its own goroutine within a
// start window of core.lifecycle.start_timeout that the hook's own
// Deps.ExtendStart calls extend, so a Start that never returns is abandoned
// when its window closes. It returns the hooks whose Start succeeded (a hook
// with no Start counts as started, so its Stop runs) and, when a Start was
// abandoned, that hook.
func (k *Kernel) startHooks(ctx context.Context) ([]moduleHook, *lateStart, error) {
	var started []moduleHook
	for _, h := range k.hooks {
		if h.hook.Start != nil {
			began := time.Now()
			wctx, cancel := context.WithCancelCause(ctx)
			end := k.startWin.begin(h.module, startTimeout, func() { cancel(context.DeadlineExceeded) })
			sctx := startCtx{wctx}
			pending, err := k.await(sctx, h, "start", h.hook.Start)
			end()
			cancel(nil)
			switch {
			case pending != nil && errors.Is(context.Cause(wctx), context.DeadlineExceeded):
				took := time.Since(began).Round(time.Second)
				k.log.ErrorContext(ctx, "kernel: hook start exceeded core.lifecycle.start_timeout",
					"module", h.module, "hook", h.hook.Name, "timeout", startTimeout.String(), "took", took.String())
				extended := ""
				if k.startWin.wasExtended() {
					extended = ", as extended by ExtendStart"
				}
				return started, &lateStart{hook: h, result: pending}, fmt.Errorf("kernel: hook %s start: exceeded core.lifecycle.start_timeout %s%s (after %s): %w",
					h.hook.Name, startTimeout, extended, took, context.DeadlineExceeded)
			case pending != nil:
				return started, &lateStart{hook: h, result: pending}, fmt.Errorf("kernel: hook %s start: %w", h.hook.Name, sctx.Err())
			case err != nil:
				k.log.ErrorContext(ctx, "kernel: hook start failed", "module", h.module, "hook", h.hook.Name, "err", err)
				return started, nil, fmt.Errorf("kernel: hook %s start: %w", h.hook.Name, err)
			}
		}
		started = append(started, h)
	}
	return started, nil, nil
}

// awaitLateStart handles the hook whose Start was abandoned, and returns it
// when it turned out started, to be appended to the started hooks. A hook is
// started once its Start has returned nil (06 §6.5.1's "every already-started
// hook is stopped in reverse order" reads serviceregistry.Container.Start,
// which adds a service to its started list only then), and the budget is for
// the started hooks' own Stops (05-health §5H.4). So the rollback waits for
// the abandoned Start only until min(deadline, now+lateStopFloor): a Start
// that returned nil by then is the newest started hook, so it is stopped
// first; one still running is left with an ERROR, the earlier hooks get the
// remaining budget with a live ctx, and if it returns nil later a background
// goroutine calls its Stop.
func (k *Kernel) awaitLateStart(ctx context.Context, late *lateStart, deadline time.Time) []moduleHook {
	if late == nil {
		return nil
	}
	h := late.hook
	wait := time.Now().Add(lateStopFloor)
	if deadline.Before(wait) {
		wait = deadline
	}
	ok, startErr := waitUntil(late.result, wait)
	switch {
	case !ok:
		k.log.ErrorContext(ctx, "kernel: abandoned hook start still running; the rollback goes on, and its Stop runs if the Start returns nil",
			"module", h.module, "hook", h.hook.Name)
		k.stopInBackground(ctx, []moduleHook{h}, deadline, late.result)
	case startErr == nil:
		return []moduleHook{h}
	}
	return nil
}

// stopHooks stops started hooks in exact reverse order, each within the
// budget's deadline; a Stop still running at the deadline is abandoned with
// an ERROR naming it, and the order continues. The hooks reached after the
// deadline get no time of their own (a floor each would overrun the budget
// by one floor per hook): their Stops are still called, in the same reverse
// order, from one background goroutine with an expired ctx, and not awaited,
// so every hook is told to stop and the stop keeps to the budget plus
// closeCore's one floor.
func (k *Kernel) stopHooks(ctx context.Context, started []moduleHook, deadline time.Time) error {
	var hooks []moduleHook
	for _, h := range slices.Backward(started) {
		if h.hook.Stop != nil {
			hooks = append(hooks, h)
		}
	}
	var errs []error
	for i, h := range hooks {
		if !time.Now().Before(deadline) {
			names := make([]string, 0, len(hooks)-i)
			for _, l := range hooks[i:] {
				names = append(names, l.hook.Name)
			}
			k.log.ErrorContext(ctx, "kernel: hook stops reached after the shutdown budget; called, not awaited",
				"hooks", strings.Join(names, ", "))
			errs = append(errs, fmt.Errorf("kernel: hooks %s stopped after the shutdown budget, not awaited: %w",
				strings.Join(names, ", "), context.DeadlineExceeded))
			k.stopInBackground(ctx, hooks[i:], deadline, nil)
			break
		}
		sctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		pending, err := k.await(sctx, h, "stop", h.hook.Stop)
		cancel()
		switch {
		case pending != nil:
			k.log.ErrorContext(ctx, "kernel: hook stop abandoned at the shutdown budget", "module", h.module, "hook", h.hook.Name)
			errs = append(errs, fmt.Errorf("kernel: hook %s stop abandoned: %w", h.hook.Name, context.DeadlineExceeded))
		case err != nil:
			k.log.ErrorContext(ctx, "kernel: hook stop failed", "module", h.module, "hook", h.hook.Name, "err", err)
			errs = append(errs, fmt.Errorf("kernel: hook %s stop: %w", h.hook.Name, err))
		}
	}
	return errors.Join(errs...)
}

// stopInBackground calls each hook's Stop in order from one goroutine with a
// ctx that ends at deadline (already past it for the hooks stopHooks hands
// over), unawaited. With started set, it first waits for that abandoned
// Start's result and stops only if it was nil. A Stop's error or panic is
// logged; the primary log output outlives closeCore.
func (k *Kernel) stopInBackground(ctx context.Context, hooks []moduleHook, deadline time.Time, started <-chan error) {
	lctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	go func() {
		defer cancel()
		if started != nil {
			if err := <-started; err != nil {
				return // it never started
			}
		}
		for _, h := range hooks {
			if err := k.protect(h.module, h.hook.Name, "stop", func() error { return h.hook.Stop(lctx) }); err != nil {
				k.log.Error("kernel: late hook stop failed", "module", h.module, "hook", h.hook.Name, "err", err)
			}
		}
	}()
}

// await runs fn(ctx) in its own goroutine, turning a panic into an error,
// and waits until it returns or ctx is done. pending is nil when fn returned
// (err is its result); otherwise fn was abandoned and pending delivers its
// result whenever it returns.
func (k *Kernel) await(ctx context.Context, h moduleHook, op string, fn func(context.Context) error) (pending <-chan error, err error) {
	ch := make(chan error, 1)
	go func() {
		ch <- k.protect(h.module, h.hook.Name, op, func() error { return fn(ctx) })
	}()
	select {
	case err := <-ch:
		return nil, err
	case <-ctx.Done():
		return ch, nil
	}
}

// protect calls fn, turning a panic into an error and logging its stack.
func (k *Kernel) protect(mod module.ID, name, op string, fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			k.log.Error("kernel: panic recovered", "module", mod, "name", name, "op", op, "panic", fmt.Sprint(p),
				"stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return fn()
}

// workerSet is the running workers of one Run.
type workerSet struct {
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	stopApp chan error // the first StopAppOnExit exit; buffered, later ones dropped

	mu      sync.Mutex
	running map[string]bool // "<module>/<worker>", for the abandonment log
}

// startWorkers starts every worker under sync.WaitGroup.Go (06 §6.4.6). Their
// ctx carries Run's values but not its cancellation: the stop order cancels
// it after the listener has drained.
func (k *Kernel) startWorkers(ctx context.Context) *workerSet {
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ws := &workerSet{ctx: wctx, cancel: cancel, stopApp: make(chan error, 1), running: make(map[string]bool)}
	for _, w := range k.workers {
		ws.running[string(w.module)+"/"+w.worker.Name] = true
		ws.wg.Go(func() { k.supervise(ws, w) })
	}
	return ws
}

// supervise runs one worker and applies its WorkerPolicy to an early exit
// (06 §6.4.3): DegradeOnExit logs and the app keeps serving (the health mark
// "<ID>.<worker>" arrives with health's readiness core); StopAppOnExit begins
// the graceful shutdown and Run returns the worker's error.
func (k *Kernel) supervise(ws *workerSet, w moduleWorker) {
	err := k.protect(w.module, w.worker.Name, "run", func() error { return w.worker.Run(ws.ctx) })
	ws.mu.Lock()
	delete(ws.running, string(w.module)+"/"+w.worker.Name)
	ws.mu.Unlock()
	if ws.ctx.Err() != nil {
		if err != nil && !errors.Is(err, context.Canceled) {
			k.log.Warn("kernel: worker returned an error while stopping", "module", w.module, "worker", w.worker.Name, "err", err)
		}
		return
	}
	cause := fmt.Errorf("kernel: worker %s of module %s exited early", w.worker.Name, w.module)
	if err != nil {
		cause = fmt.Errorf("kernel: worker %s of module %s exited early: %w", w.worker.Name, w.module, err)
	}
	switch w.worker.Policy {
	case module.StopAppOnExit:
		k.log.Error("kernel: worker exited; stopping the app (StopAppOnExit)", "module", w.module, "worker", w.worker.Name, "err", err)
		select {
		case ws.stopApp <- cause:
		default:
		}
	default: // DegradeOnExit: the registrar refuses any other policy
		k.log.Error("kernel: worker exited; module degraded (DegradeOnExit)", "module", w.module, "worker", w.worker.Name, "err", err)
	}
}

// stopWorkers cancels the workers' ctx and waits for them until deadline; the
// ones still running then are abandoned with an ERROR naming them.
func (k *Kernel) stopWorkers(ctx context.Context, ws *workerSet, deadline time.Time) error {
	ws.cancel()
	done := make(chan error, 1)
	go func() {
		ws.wg.Wait()
		done <- nil
	}()
	if ok, err := waitUntil(done, deadline); ok {
		return err
	}
	ws.mu.Lock()
	names := make([]string, 0, len(ws.running))
	for n := range ws.running {
		names = append(names, n)
	}
	ws.mu.Unlock()
	slices.Sort(names)
	k.log.ErrorContext(ctx, "kernel: workers abandoned at the shutdown budget", "workers", strings.Join(names, ", "))
	return fmt.Errorf("kernel: workers %s abandoned: %w", strings.Join(names, ", "), context.DeadlineExceeded)
}

// r1Values calls Config.R1Values once; nil means every value at its
// default. It is app code: a panic is recovered into an error, so the stop
// order still runs.
func (k *Kernel) r1Values() (v R1Values, err error) {
	if k.cfg.R1Values == nil {
		return R1Values{}, nil
	}
	err = k.protect("", "R1Values", "call", func() error {
		v = k.cfg.R1Values()
		return nil
	})
	return v, err
}

// signal logs a failed readiness signal; it never fails the app.
func (k *Kernel) signal(ctx context.Context, which string, err error) {
	if err != nil {
		k.log.WarnContext(ctx, "kernel: readiness signal failed", "signal", which, "err", err)
	}
}

// waitUntil receives from ch until deadline; ok is false at the deadline.
func waitUntil(ch <-chan error, deadline time.Time) (ok bool, err error) {
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case err := <-ch:
		return true, err
	case <-t.C:
		return false, nil
	}
}

// atLeastFloor is deadline, or lateStopFloor from now when that is later.
func atLeastFloor(deadline time.Time) time.Time {
	if floor := time.Now().Add(lateStopFloor); floor.After(deadline) {
		return floor
	}
	return deadline
}
