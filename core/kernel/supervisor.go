// file: core/kernel/supervisor.go
// version: 1.0.0
// guid: e84396f3-c126-49af-a149-17cc1398aeac
// last-edited: 2026-11-07

package kernel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
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
	// lateStopFloor is the least time a stop step gets once the budget is
	// spent: a Stop hook called after the cap, so a hook that releases its
	// resources without blocking still finishes before the stop order moves
	// on, and the final close of metrics and logging.
	lateStopFloor = time.Second
)

// ErrRestartRequested is what Run returns after a restart request's stop
// order when the exit func returns (a test's); in production the process has
// exited with restartExitCode by then.
var ErrRestartRequested = errors.New("kernel: restart requested")

var errRunTwice = errors.New("kernel: Run called more than once")

// ReadinessNotifier receives the supervisor's readiness signals (05-health
// §5H.3.4): Status while not ready (STATUS=), Ready once the listeners are up
// and startup has completed (READY=1), Stopping when shutdown begins
// (STOPPING=1). R1-16 implements it with sdnotify (05-health §5H.10, Landing
// R1); until then the kernel's default sends nothing. A returned error is
// logged at WARN and never stops the app: the signals are not durable.
type ReadinessNotifier interface {
	Ready() error
	Status(string) error
	Stopping() error
}

// noopNotifier is the default ReadinessNotifier.
type noopNotifier struct{}

func (noopNotifier) Ready() error        { return nil }
func (noopNotifier) Status(string) error { return nil }
func (noopNotifier) Stopping() error     { return nil }

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
}

func newLifecycle() *Lifecycle { return &Lifecycle{requested: make(chan struct{})} }

// RequestRestart asks the running app to restart: Run logs the reason, runs
// the stop order within core.shutdown.budget and then exits with
// core.lifecycle.restart_exit_code (75), so the unit's Restart=on-failure
// brings the service back; never exit 0, which would leave a staged swap
// untaken. The first request's reason wins; later ones are no-ops.
func (l *Lifecycle) RequestRestart(reason string) {
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
	started, err := k.startHooks(ctx)
	if err != nil {
		deadline := time.Now().Add(defaultShutdownBudget)
		return errors.Join(err, k.stopHooks(ctx, started, deadline), k.closeCore(ctx, deadline))
	}
	// (3) Module workers. Steps (3a) to (6) arrive with storage, health,
	// events and tasks.
	ws := k.startWorkers(ctx)
	// (7) Listeners: R1Values once (05-config §1.12.1), then the listener.
	v := k.r1Values()
	budget := v.ShutdownBudget
	if budget == 0 {
		budget = defaultShutdownBudget
	}
	var srv Server
	if budget < 0 {
		err = fmt.Errorf("kernel: core.shutdown.budget %s is negative", v.ShutdownBudget)
		budget = defaultShutdownBudget
	} else {
		srv, err = k.listen(httpapi.ListenOptions{Addr: v.Listen, TLSCertFile: v.TLSCertFile, TLSKeyFile: v.TLSKeyFile},
			k.handler, k.coreLog("http"))
	}
	if err != nil {
		deadline := time.Now().Add(budget)
		return errors.Join(fmt.Errorf("kernel: listener: %w", err), k.stopWorkers(ctx, ws, deadline),
			k.stopHooks(ctx, started, deadline), k.closeCore(ctx, deadline))
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()
	k.log.InfoContext(ctx, "kernel: listening", "addr", srv.Addr().String())
	// (8) Readiness. The readiness core and its /readyz arrive with health
	// (F2); until then readiness is the notifier's READY=1.
	k.signal(ctx, "ready", k.notifier.Ready())
	k.log.InfoContext(ctx, "kernel: ready")

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
		return errors.Join(fmt.Errorf("%w: %s", ErrRestartRequested, k.lifecycle.reason), stopErr)
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

// startHooks starts every hook in order, each within startTimeout and its
// own goroutine, so a Start that never returns is abandoned at the timeout.
// It returns the hooks whose Start succeeded (a hook with no Start counts as
// started, so its Stop runs).
func (k *Kernel) startHooks(ctx context.Context) ([]moduleHook, error) {
	var started []moduleHook
	for _, h := range k.hooks {
		if h.hook.Start != nil {
			sctx, cancel := context.WithTimeout(ctx, startTimeout)
			done, err := k.await(sctx, h, "start", h.hook.Start)
			cancel()
			switch {
			case !done && errors.Is(sctx.Err(), context.DeadlineExceeded):
				k.log.ErrorContext(ctx, "kernel: hook start exceeded core.lifecycle.start_timeout",
					"module", h.module, "hook", h.hook.Name, "timeout", startTimeout.String())
				return started, fmt.Errorf("kernel: hook %s start: exceeded core.lifecycle.start_timeout %s: %w",
					h.hook.Name, startTimeout, context.DeadlineExceeded)
			case !done:
				return started, fmt.Errorf("kernel: hook %s start: %w", h.hook.Name, sctx.Err())
			case err != nil:
				k.log.ErrorContext(ctx, "kernel: hook start failed", "module", h.module, "hook", h.hook.Name, "err", err)
				return started, fmt.Errorf("kernel: hook %s start: %w", h.hook.Name, err)
			}
		}
		started = append(started, h)
	}
	return started, nil
}

// stopHooks stops started hooks in exact reverse order. Each Stop's ctx
// carries the budget's deadline (at least lateStopFloor from its call); a
// Stop still running then is abandoned with an ERROR naming it, and the stop
// order continues with the next hook.
func (k *Kernel) stopHooks(ctx context.Context, started []moduleHook, deadline time.Time) error {
	var errs []error
	for _, h := range slices.Backward(started) {
		if h.hook.Stop == nil {
			continue
		}
		sctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), atLeastFloor(deadline))
		done, err := k.await(sctx, h, "stop", h.hook.Stop)
		cancel()
		switch {
		case !done:
			k.log.ErrorContext(ctx, "kernel: hook stop abandoned at the shutdown budget", "module", h.module, "hook", h.hook.Name)
			errs = append(errs, fmt.Errorf("kernel: hook %s stop abandoned: %w", h.hook.Name, context.DeadlineExceeded))
		case err != nil:
			k.log.ErrorContext(ctx, "kernel: hook stop failed", "module", h.module, "hook", h.hook.Name, "err", err)
			errs = append(errs, fmt.Errorf("kernel: hook %s stop: %w", h.hook.Name, err))
		}
	}
	return errors.Join(errs...)
}

// await runs fn(ctx) in its own goroutine, turning a panic into an error, and
// waits until it returns (done) or ctx is done (abandoned: the goroutine is
// left to finish on its own).
func (k *Kernel) await(ctx context.Context, h moduleHook, op string, fn func(context.Context) error) (done bool, err error) {
	ch := make(chan error, 1)
	go func() {
		ch <- k.protect(h.module, h.hook.Name, op, func() error { return fn(ctx) })
	}()
	select {
	case err := <-ch:
		return true, err
	case <-ctx.Done():
		return false, nil
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

// r1Values calls Config.R1Values once; nil means every value at its default.
func (k *Kernel) r1Values() R1Values {
	if k.cfg.R1Values == nil {
		return R1Values{}
	}
	return k.cfg.R1Values()
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
