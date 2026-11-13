// file: core/health/startup.go
// version: 1.0.0
// guid: 7d3a9e18-2c54-4b6f-b0a1-9e8f5c2d4a61
// last-edited: 2026-11-13

package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/internal/sdnotify"
)

// StartupStepBudget is the start time one progressing startup step asks for.
const StartupStepBudget = sdnotify.StartupStepBudget

// StepClass says what a failed startup step does (05-health §5H.3.2).
type StepClass uint8

const (
	Fatal    StepClass = iota + 1 // error stops the process before it serves
	Degraded                      // error logged, Token recorded, startup continues
)

// String is the class's lower-case name.
func (c StepClass) String() string {
	switch c {
	case Fatal:
		return "fatal"
	case Degraded:
		return "degraded"
	}
	return fmt.Sprintf("StepClass(%d)", uint8(c))
}

// StartupStep is one row of a startup table.
type StartupStep struct {
	Name   string // literal used at the Run call site
	Class  StepClass
	Token  string // fixed token for Degraded steps; empty for Fatal
	Reason string // the review record for the class
}

// ErrUnknownStep is what Startup.Run returns for a step name that is not a row
// of the table: a startup step cannot run without a reviewed class (fail
// closed), and fn is not called.
var ErrUnknownStep = errors.New("health: startup step is not in the table")

// degradedComponentPrefix is the component ID prefix of a failed Degraded
// step: core.startup.<token>.
const degradedComponentPrefix = "core.startup."

// Startup is the successor of internal/startup: a per-App table (core rows plus
// the module rows from Registrar.StartupSteps) instead of a package variable.
// Every startup step runs through Run, which applies the step's class. It is
// goroutine-safe: the status handlers read Degraded while startup may still be
// recording.
//
// Attaching a Startup to a Registry (NewStartup with a non-nil one) makes the
// registry not ready, with the token core.kernel:starting, until Complete.
type Startup struct {
	reg *Registry
	log *slog.Logger

	mu       sync.Mutex
	steps    map[string]StartupStep
	degraded map[string]struct{} // failed Degraded steps' tokens
}

// NewStartup builds a table from steps and attaches it to reg (nil reg keeps
// the table without readiness effects). A duplicate Name, a Fatal step with a
// Token, a Degraded step without a valid Token (the grammar of a Reason) or a
// Class that is neither is an error and builds nothing.
func NewStartup(reg *Registry, log *slog.Logger, steps ...StartupStep) (*Startup, error) {
	s := &Startup{reg: reg, log: log, steps: map[string]StartupStep{}, degraded: map[string]struct{}{}}
	if err := s.Add(steps...); err != nil {
		return nil, err
	}
	if reg != nil {
		reg.setStartup(true, nil)
	}
	return s, nil
}

// Add adds rows to the table (a module's Registrar.StartupSteps), all or none.
func (s *Startup) Add(steps ...StartupStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]struct{}{}
	for _, st := range steps {
		if err := st.validate(); err != nil {
			return err
		}
		if _, dup := s.steps[st.Name]; dup {
			return fmt.Errorf("health: startup step %q is already in the table", st.Name)
		}
		if _, dup := seen[st.Name]; dup {
			return fmt.Errorf("health: startup step %q is listed twice", st.Name)
		}
		seen[st.Name] = struct{}{}
	}
	for _, st := range steps {
		s.steps[st.Name] = st
	}
	return nil
}

// validate checks one row.
func (st StartupStep) validate() error {
	if st.Name == "" || strings.TrimSpace(st.Name) != st.Name {
		return fmt.Errorf("health: startup step name %q is empty or has surrounding space", st.Name)
	}
	switch st.Class {
	case Fatal:
		if st.Token != "" {
			return fmt.Errorf("health: fatal startup step %q has a token; only Degraded steps report one", st.Name)
		}
	case Degraded:
		if !validReason(st.Token) {
			return fmt.Errorf("health: degraded startup step %q needs a token of [a-z0-9_]{1,%d}", st.Name, maxReasonLen)
		}
	default:
		return fmt.Errorf("health: startup step %q has no class", st.Name)
	}
	return nil
}

// Steps is the table, sorted by name (archtest.StartupSteps reads it).
func (s *Startup) Steps() []StartupStep {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StartupStep, 0, len(s.steps))
	for _, st := range s.steps {
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b StartupStep) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Run runs fn as the startup step called name. A name that is not a row is
// ErrUnknownStep and fn does not run. fn's error from a Fatal step is returned
// wrapped, so the caller stops before it serves. From a Degraded step it is
// logged at error level, the step's token is recorded, component
// core.startup.<token> is Degraded with that token as its Reason, and Run
// returns nil.
func (s *Startup) Run(ctx context.Context, name string, fn func(context.Context) error) error {
	s.mu.Lock()
	st, ok := s.steps[name]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownStep, name)
	}
	err := fn(ctx)
	if err == nil {
		if st.Class == Degraded && s.reg != nil {
			s.reg.metrics.recordStartup(st.Name, false)
		}
		return nil
	}
	if st.Class == Fatal {
		return fmt.Errorf("startup step %s: %w", name, err)
	}
	if s.log != nil {
		s.log.ErrorContext(ctx, "startup step failed; continuing degraded", "step", name, "token", st.Token, "err", err)
	}
	s.markDegraded(ctx, st)
	return nil
}

// markDegraded records st's token and reports its component.
func (s *Startup) markDegraded(ctx context.Context, st StartupStep) {
	s.mu.Lock()
	s.degraded[st.Token] = struct{}{}
	tokens := s.tokensLocked()
	s.mu.Unlock()
	if s.reg == nil {
		return
	}
	s.reg.metrics.recordStartup(st.Name, true)
	id := ComponentID(degradedComponentPrefix + st.Token)
	res := Result{Status: StatusDegraded, Reason: st.Token, Detail: "startup step " + st.Name + " failed"}
	// Two steps may share a token, so the component may already exist: that
	// answer is expected and report below sets its result. Any other refusal
	// (an invalid token) is logged, not swallowed.
	if _, err := s.reg.Register(Check{ID: id, Title: "Startup step " + st.Name}); err != nil && !errors.Is(err, ErrComponentExists) && s.log != nil {
		s.log.ErrorContext(ctx, "startup degraded component not registered", "step", st.Name, "token", st.Token, "err", err)
	}
	s.reg.report(id, res)
	s.reg.mu.Lock()
	pending := s.reg.startupPending
	s.reg.mu.Unlock()
	s.reg.setStartup(pending, tokens)
}

// Degraded is the tokens of the Degraded steps that failed, sorted and without
// repeats.
func (s *Startup) Degraded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokensLocked()
}

func (s *Startup) tokensLocked() []string {
	out := make([]string, 0, len(s.degraded))
	for t := range s.degraded {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// Complete marks startup as finished: the registry stops reporting
// core.kernel:starting. The kernel calls it once the last Fatal step has
// succeeded and the listeners are up.
func (s *Startup) Complete() {
	if s.reg == nil {
		return
	}
	s.reg.setStartup(false, s.Degraded())
}

// CoreStartupSteps is the core rows of the startup table (the steps core runs
// before any module): the secrets location, the key guard, encryption, the
// stored config, its validation, the operation registry gate and the listener.
// Module rows (a books step such as the search index) arrive through
// Registrar.StartupSteps and Startup.Add.
func CoreStartupSteps() []StartupStep {
	return []StartupStep{
		{Name: "EnsureSecureStateDir", Class: Fatal,
			Reason: "the secrets location must be sane before a key is read or written"},
		{Name: "guardAgainstKeyRegeneration", Class: Fatal,
			Reason: "regenerating the key would orphan every encrypted setting"},
		{Name: "initEncryption", Class: Fatal,
			Reason: "without the key, secrets are unreadable and a save would write them under the wrong key"},
		{Name: "loadConfigFromDB", Class: Fatal,
			Reason: "the stored config is persisted whole, so a server running on defaults overwrites it on its first settings save"},
		{Name: "config.Validate", Class: Fatal,
			Reason: "bad config must not serve"},
		{Name: "opRegistrationGate", Class: Fatal,
			Reason: "a hole in the operations registry makes ops vanish silently"},
		{Name: "http", Class: Fatal,
			Reason: "nothing to serve; a bind failure inside the listener goroutine exits the process"},
	}
}

// Notifier is the supervisor's side of the systemd notify protocol. The
// default is SystemdNotifier; tests pass a recording one.
type Notifier interface {
	Ready() error
	Status(msg string) error
	Stopping() error
	Extend(d time.Duration) error
	Watchdog() error
	// WatchdogInterval is the unit's WatchdogSec, zero when it set none.
	WatchdogInterval() time.Duration
	// Active reports whether a supervisor is listening (NOTIFY_SOCKET set).
	Active() bool
}

// SystemdNotifier is the Notifier backed by internal/sdnotify: every call is a
// no-op without NOTIFY_SOCKET.
func SystemdNotifier() Notifier { return systemdNotifier{} }

type systemdNotifier struct{}

func (systemdNotifier) Ready() error                    { return sdnotify.Ready() }
func (systemdNotifier) Status(m string) error           { return sdnotify.Status(m) }
func (systemdNotifier) Stopping() error                 { return sdnotify.Stopping() }
func (systemdNotifier) Extend(d time.Duration) error    { return sdnotify.ExtendTimeout(d) }
func (systemdNotifier) Watchdog() error                 { return sdnotify.Watchdog() }
func (systemdNotifier) WatchdogInterval() time.Duration { return sdnotify.WatchdogInterval() }
func (systemdNotifier) Active() bool                    { return sdnotify.Active() }

// Supervisor is the kernel's supervisor-signal handle, one per App (R5). It
// holds no goroutine and no timer: extension is per step (ExtendStart,
// ExtendStop), and the watchdog ping rides the heartbeat (Registry.StartHeartbeat
// calls WatchdogTick), so a wedged process stops pinging.
type Supervisor struct {
	log *slog.Logger
	n   Notifier

	mu          sync.Mutex
	lastPing    time.Time
	stopGranted bool
	stopMax     func() time.Duration
}

// NewSupervisor returns a Supervisor over n (nil means SystemdNotifier). log
// receives the failures of a notify write; nil logs nothing.
func NewSupervisor(log *slog.Logger, n Notifier) *Supervisor {
	if n == nil {
		n = SystemdNotifier()
	}
	return &Supervisor{log: log, n: n}
}

// SetStopExtendMax installs the reader of core.tasks.shutdown_extend_max, the
// cap on ExtendStop. Without one ExtendStop grants nothing.
func (s *Supervisor) SetStopExtendMax(f func() time.Duration) {
	s.mu.Lock()
	s.stopMax = f
	s.mu.Unlock()
}

// warn logs a failed notify write; it is never returned, a missing supervisor
// must not fail startup or shutdown.
func (s *Supervisor) warn(what string, err error) {
	if err != nil && s.log != nil {
		s.log.Warn("health: supervisor notification failed", "what", what, "err", err)
	}
}

// Ready sends READY=1: listeners are up and startup completed (not after
// module warm-up).
func (s *Supervisor) Ready() {
	if s != nil {
		s.warn("ready", s.n.Ready())
	}
}

// Status sets the STATUS= line. Callers pass fixed tokens only.
func (s *Supervisor) Status(msg string) {
	if s != nil {
		s.warn("status", s.n.Status(msg))
	}
}

// Stopping sends STOPPING=1 at shutdown start.
func (s *Supervisor) Stopping() {
	if s != nil {
		s.warn("stopping", s.n.Stopping())
	}
}

// ExtendStart is startup use (1) of EXTEND_TIMEOUT_USEC: right before a step
// that makes real progress it extends the start deadline to at least
// StartupStepBudget from now. It is called per step, never from a timer, so a
// step that hangs is still killed when its own budget runs out. No-op without
// NOTIFY_SOCKET; a failure is logged and never fails startup.
func (s *Supervisor) ExtendStart(step string) {
	if s == nil || !s.n.Active() {
		return
	}
	if err := s.n.Extend(StartupStepBudget); err != nil && s.log != nil {
		s.log.Warn("health: could not extend the start timeout", "step", step, "err", err)
	}
}

// ExtendStop is stop use (2): the tasks executor's one bounded extension for an
// in-flight Uninterruptible run. It is granted at most once per shutdown,
// capped by core.tasks.shutdown_extend_max, and only under a supervisor; false
// when refused. d is the extension asked for.
func (s *Supervisor) ExtendStop(d time.Duration) bool {
	if s == nil || d <= 0 || !s.n.Active() {
		return false
	}
	s.mu.Lock()
	max := time.Duration(0)
	if s.stopMax != nil {
		max = s.stopMax()
	}
	if s.stopGranted || max <= 0 {
		s.mu.Unlock()
		return false
	}
	s.stopGranted = true
	s.mu.Unlock()
	if d > max {
		d = max
	}
	if err := s.n.Extend(d); err != nil {
		if s.log != nil {
			s.log.Warn("health: could not extend the stop timeout", "err", err)
		}
		return false
	}
	return true
}

// watchdogEvery is the ping period, half the unit's WatchdogSec, or zero when
// it set none.
func (s *Supervisor) watchdogEvery() time.Duration {
	if s == nil {
		return 0
	}
	return s.n.WatchdogInterval() / 2
}

// WatchdogTick sends WATCHDOG=1 when one is due: the unit set WatchdogSec and
// half of it has passed since the last ping. The heartbeat goroutine calls it
// after each recorded beat; nothing else may, which is what makes a wedged
// heartbeat stop the pings.
func (s *Supervisor) WatchdogTick(now time.Time) {
	every := s.watchdogEvery()
	if every <= 0 {
		return
	}
	s.mu.Lock()
	due := s.lastPing.IsZero() || now.Sub(s.lastPing) >= every
	if due {
		s.lastPing = now
	}
	s.mu.Unlock()
	if due {
		s.warn("watchdog", s.n.Watchdog())
	}
}
