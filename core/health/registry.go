// file: core/health/registry.go
// version: 1.1.2
// guid: 268f470f-94c4-4d33-93bd-af65944395ec
// last-edited: 2026-11-13

package health

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The component registry of 05-health §5H.3.1: Status, ComponentID, Result,
// Check, Registry. F2 landed the readiness core (components updated by a push
// Reporter, plus one call of a Check's Probe at Register); F8 completes it
// (snapshot.go, probes.go, startup.go): the probe loop (StartChecks), the
// heartbeat that /livez and the watchdog follow (StartHeartbeat), the startup
// and shutdown readiness conditions, and the status feed.
//
// A Registry starts no goroutine and reads no clock of its own until one of
// the Start functions is called, so an unstarted registry behaves as it did at
// F2: always live, ready exactly when no Readiness component is not Up or
// Degraded and no Critical one is Down.
//
// The state of a Registry is one mutex-guarded map. Wait is signalled, not
// polled: every Register and Report closes the registry's changed channel
// and installs a fresh one, so each blocked Wait wakes exactly when
// something it might be waiting for happened.

// Status is one component's state.
type Status uint8

const (
	StatusUnknown Status = iota
	StatusUp
	StatusDegraded
	StatusDown
)

// String is the status's lower-case name.
func (s Status) String() string {
	switch s {
	case StatusUp:
		return "up"
	case StatusDegraded:
		return "degraded"
	case StatusDown:
		return "down"
	default:
		return "unknown"
	}
}

// ComponentID is "<moduleID>.<name>", e.g. "core.storage", "core.outbox",
// "core.integrations.download.deluge-main", "books.memdb". Register refuses
// an ID that is not two or more dot-separated segments of [a-z0-9_-] that
// start with [a-z0-9].
type ComponentID string

// Result is one observation. Reason is a FIXED machine token
// ([a-z0-9_]{1,40}); it is the only thing unauthenticated probes ever show.
// Detail is free text shown only on the authenticated endpoint, after
// redaction (05-logging). A Reason may be empty (nothing to say); a
// non-empty one outside the grammar is clamped to InvalidReason when it is
// stored, and its text is dropped, never copied to Detail.
type Result struct {
	Status Status
	Reason string
	Detail string
	At     time.Time
}

// InvalidReason replaces a Reason outside [a-z0-9_]{1,40}. The grammar is
// enforced by clamping, not refusal, because Reporter.Report has no error
// return and a component must still be able to say Down.
const InvalidReason = "invalid_reason"

// maxReasonLen is the longest Reason.
const maxReasonLen = 40

// defaultProbeTimeout bounds a Probe called at Register when Check.Timeout is
// zero. 05-health §5H.7's core.health.check_timeout key belongs to F8.
const defaultProbeTimeout = 5 * time.Second

// Timings are the three hot-reloadable settings of 05-health §5H.7. A zero
// field takes the key's default.
type Timings struct {
	CheckInterval time.Duration // core.health.check_interval, 30s
	CheckTimeout  time.Duration // core.health.check_timeout, 5s
	LiveStall     time.Duration // core.health.live_stall, 60s
}

// withDefaults fills the zero fields of t with the keys' defaults.
func (t Timings) withDefaults() Timings {
	if t.CheckInterval <= 0 {
		t.CheckInterval = 30 * time.Second
	}
	if t.CheckTimeout <= 0 {
		t.CheckTimeout = defaultProbeTimeout
	}
	if t.LiveStall <= 0 {
		t.LiveStall = 60 * time.Second
	}
	return t
}

// Options configure a Registry built with NewRegistryWith.
type Options struct {
	// Timings reads the live core.health.* values on every use, so a reload
	// takes effect on the next tick. nil means the defaults.
	Timings func() Timings
	// Metrics receives the health series (§5H.8). nil records nothing.
	Metrics *Metrics
	// Log receives a check's panic and an abandoned check. nil logs nothing.
	Log *slog.Logger
	// Now is the clock. nil means time.Now.
	Now func() time.Time
}

// Check declares one component (module.Registrar.Health takes these, 06 §6.4.3).
type Check struct {
	ID        ComponentID
	Title     string
	Critical  bool                             // Down => process not ready
	Readiness bool                             // must be Up or Degraded for readiness
	Probe     func(ctx context.Context) Result // nil = push-only (Reporter)
	Interval  time.Duration                    // 0 = core.health.check_interval
	Timeout   time.Duration                    // 0 = core.health.check_timeout
}

type component struct {
	check    Check
	result   Result
	duration time.Duration
	// running is set while a probe goroutine of this component is alive, so a
	// check that outlives its timeout is never joined by a second call of
	// itself (§5H.6: never concurrently with itself).
	running atomic.Bool
}

// Registry is one per App (R5). Modules register Checks via Registrar.Health and
// push through module.Deps.Health (a Reporter scoped to the module's IDs).
// It is goroutine-safe; build one with NewRegistry or NewRegistryWith.
type Registry struct {
	now     func() time.Time
	timings func() Timings
	metrics *Metrics
	log     *slog.Logger

	mu    sync.Mutex
	comps map[ComponentID]*component
	// changed is closed, and replaced, by every Register and Report: the
	// broadcast Wait blocks on.
	changed chan struct{}

	// The readiness conditions beyond the components (§5H.3.3): startup has
	// not completed (set by a Startup attached to this registry), shutdown has
	// begun. Both are false on a registry nothing attached to.
	startupPending bool
	shuttingDown   bool
	startupTokens  []string // degraded startup tokens, sorted
	noJitter       bool     // tests: probe loops tick exactly on the interval

	// checks: the probe loops (checks.go). runCtx is non-nil while they run;
	// a component registered meanwhile gets its loop at once.
	runCtx context.Context
	wg     sync.WaitGroup

	// The heartbeat (/livez, the watchdog). beating is set while StartHeartbeat's
	// goroutine runs; lastBeat is its last tick in unix nanoseconds. A registry
	// with no heartbeat is always live.
	beating  atomic.Bool
	lastBeat atomic.Int64

	// The status feed (probes.go): pubMu serialises publication so events
	// leave in the order the state changed; sink and last are guarded by it.
	pubMu sync.Mutex
	sink  func(StatusPayload)
	last  *StatusPayload
	// observed is true once a sink or Metrics exists, so afterChange is one
	// atomic load for a registry nobody watches.
	observed atomic.Bool
}

// NewRegistry returns an empty registry with the default timings and no
// metrics. A kernel builds one per App.
func NewRegistry() *Registry { return NewRegistryWith(Options{}) }

// NewRegistryWith returns an empty registry configured by o. It starts no
// goroutine: the probe loop and the heartbeat start only from StartChecks and
// StartHeartbeat.
func NewRegistryWith(o Options) *Registry {
	r := &Registry{
		now:     o.Now,
		timings: o.Timings,
		metrics: o.Metrics,
		log:     o.Log,
		comps:   map[ComponentID]*component{},
		changed: make(chan struct{}),
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.timings == nil {
		r.timings = func() Timings { return Timings{} }
	}
	r.metrics.attach(r)
	r.observed.Store(o.Metrics != nil)
	return r
}

// cfg is the current timings with defaults applied.
func (r *Registry) cfg() Timings { return r.timings().withDefaults() }

// validID reports whether id is "<module>.<name>[.<more>...]".
func validID(id ComponentID) bool {
	segs := strings.Split(string(id), ".")
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs {
		if seg == "" {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			alnum := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
			if !alnum && (i == 0 || c != '_' && c != '-') {
				return false
			}
		}
	}
	return true
}

// validReason reports whether s is a token of [a-z0-9_]{1,40}.
func validReason(s string) bool {
	if s == "" || len(s) > maxReasonLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// Register declares a component and returns its push Reporter. A duplicate
// or malformed ID is an error and registers nothing. A component starts
// Unknown, unless its Check has a Probe: the Probe is then called once, here,
// under Check.Timeout (default core.health.check_timeout, 5s), and its Result
// is the component's first. StartChecks calls it again on its Interval; until
// then a probed component changes only by Report. A Probe that panics yields
// Down with the Reason check_panic, one that outlives its Timeout yields Down
// with check_timeout (the call is abandoned, not waited for). An abandoned
// Register-time call that does answer later, before StartChecks has run and
// before any Report, replaces that Down with its answer (acceptLate): a slow
// first check at boot is not a permanent Down.
func (r *Registry) Register(c Check) (Reporter, error) {
	if !validID(c.ID) {
		return nil, fmt.Errorf("health: component ID %q is not <module>.<name> of [a-z0-9_-] segments", c.ID)
	}
	r.mu.Lock()
	_, dup := r.comps[c.ID]
	r.mu.Unlock()
	if dup {
		return nil, fmt.Errorf("%w: %s", ErrComponentExists, c.ID)
	}
	comp := &component{check: c}
	var late <-chan outcome
	if c.Probe != nil {
		// Outside the lock: a Probe may be slow and may call the registry.
		var res Result
		var took time.Duration
		res, took, _, late = r.runCheck(context.Background(), comp)
		comp.result, comp.duration = r.clamp(res), took
		r.metrics.recordCheck(c.ID, took, comp.result, comp.result.Status == StatusDown)
	}
	r.mu.Lock()
	if _, dup := r.comps[c.ID]; dup { // registered meanwhile
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrComponentExists, c.ID)
	}
	r.comps[c.ID] = comp
	r.broadcastLocked()
	if c.Probe != nil && r.runCtx != nil {
		r.spawnLocked(comp)
	}
	r.mu.Unlock()
	if late != nil {
		go r.acceptLate(comp, late)
	}
	r.afterChange()
	return componentReporter{reg: r, id: c.ID}, nil
}

// The Reasons a check that did not return a Result of its own is given.
const (
	reasonCheckPanic   = "check_panic"
	reasonCheckTimeout = "check_timeout"
)

// outcome is how one Probe call ended: a Result, or the value it panicked with.
type outcome struct {
	res      Result
	panicked any
	// at is when the probe returned. runCheck compares it with the deadline
	// when the answer and the deadline are both ready, so an answer given in
	// time is kept and one given after the deadline is not.
	at time.Time
}

// result is the Result an outcome stands for.
func (o outcome) result() Result {
	if o.panicked != nil {
		return Result{Status: StatusDown, Reason: reasonCheckPanic, Detail: fmt.Sprintf("check panicked: %v", o.panicked)}
	}
	return o.res
}

// runCheck calls comp's Probe once under its timeout and contains what goes
// wrong: a panic is Down check_panic, a Probe still running when its context
// ends is Down check_timeout. The call runs on its own goroutine so a Probe
// that ignores its context cannot hold the caller; such a goroutine is
// abandoned (and logged), and comp.running keeps a second call from starting
// until it returns. ok is false when parent ended first (shutdown): the
// caller records nothing. late is non-nil only after a timeout: it delivers
// the abandoned call's outcome whenever it ends (Register uses it, see there).
//
// When the Probe answers at the instant the timeout fires, the answer wins:
// select picks at random between two ready cases, so the timeout branch looks
// for an answer before it reports one.
func (r *Registry) runCheck(parent context.Context, comp *component) (res Result, took time.Duration, ok bool, late <-chan outcome) {
	c := comp.check
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = r.cfg().CheckTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	start := time.Now()
	done := make(chan outcome, 1)
	comp.running.Store(true)
	go func() {
		defer comp.running.Store(false)
		defer func() {
			if p := recover(); p != nil {
				done <- outcome{panicked: p, at: time.Now()}
			}
		}()
		res := c.Probe(ctx)
		done <- outcome{res: res, at: time.Now()}
	}()
	// inTime reports whether o was given before the deadline.
	inTime := func(o outcome) bool {
		dl, has := ctx.Deadline()
		return !has || o.at.Before(dl)
	}
	// overran is the Result of a check that answered, or is still to answer,
	// after its deadline. An answer already given is handed on as the late one.
	overran := func(late <-chan outcome) (Result, time.Duration, bool, <-chan outcome) {
		return Result{Status: StatusDown, Reason: reasonCheckTimeout, Detail: "the check did not answer within " + timeout.String()}, time.Since(start), true, late
	}
	lateAnswer := func(o outcome) <-chan outcome {
		c := make(chan outcome, 1)
		c <- o
		return c
	}
	select {
	case o := <-done:
		if !inTime(o) {
			return overran(lateAnswer(o))
		}
		return o.result(), time.Since(start), true, nil
	case <-ctx.Done():
		took = time.Since(start)
		if parent.Err() != nil {
			return Result{}, took, false, nil
		}
		// The answer and the deadline can become ready together, and select
		// then picks either. An answer that was given before the deadline is
		// not a timeout, however the tie fell; one given after it is.
		select {
		case o := <-done:
			if !inTime(o) {
				return overran(lateAnswer(o))
			}
			return o.result(), took, true, nil
		default:
		}
		if r.log != nil {
			r.log.Warn("health: check exceeded its timeout and was abandoned", "component", string(c.ID), "timeout", timeout.String())
		}
		return overran(done)
	}
}

// acceptLate gives a Register-time probe that overran its timeout, and then
// answered, the answer it was waiting to give. Until StartChecks runs, nothing
// else will probe the component, so without this a component whose first check
// is slow (core.storage's engine ping at boot) would stay Down, and the tasks
// executor, which waits on it, would never start. It applies only while the
// result is still the timeout this Register recorded: a Report, or the first
// probe of a started loop, has since spoken for the component and is newer.
func (r *Registry) acceptLate(comp *component, late <-chan outcome) {
	res := r.clamp((<-late).result())
	r.mu.Lock()
	if r.runCtx != nil || comp.result.Reason != reasonCheckTimeout || comp.result.Status != StatusDown {
		r.mu.Unlock()
		return
	}
	comp.result = res
	r.broadcastLocked()
	r.mu.Unlock()
	r.metrics.recordCheck(comp.check.ID, 0, res, res.Status == StatusDown)
	r.afterChange()
}

// clamp is the stored form of res: a Reason outside the grammar becomes
// InvalidReason, and a zero At the registry's clock.
func (r *Registry) clamp(res Result) Result {
	if res.Reason != "" && !validReason(res.Reason) {
		res.Reason = InvalidReason
	}
	if res.At.IsZero() {
		res.At = r.now()
	}
	return res
}

// report is componentReporter.Report: replace id's Result.
func (r *Registry) report(id ComponentID, res Result) {
	res = r.clamp(res)
	r.mu.Lock()
	comp, ok := r.comps[id]
	if ok {
		comp.result = res
		r.broadcastLocked()
	}
	r.mu.Unlock()
	if ok {
		r.afterChange()
	}
}

// broadcastLocked wakes every blocked Wait. r.mu is held.
func (r *Registry) broadcastLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// The tokens of the two readiness conditions that are not a component's
// (§5H.3.3: "startup complete and not shutting down"). They have the same
// "<component>:<reason>" shape as every other not-ready token and are fixed
// strings, so /readyz may show them.
const (
	tokenStarting = "core.kernel:starting"
	tokenStopping = "core.kernel:stopping"
)

// notReadyLocked is the readiness tokens, "<component>:<reason>", sorted by
// component ID (the startup and shutdown tokens first): every Readiness component that is not Up or Degraded, and
// every Critical component that is Down. A result with no Reason has the
// fixed one, "unknown" or "down". r.mu is held.
func (r *Registry) notReadyLocked() []string {
	ids := make([]ComponentID, 0, len(r.comps))
	for id := range r.comps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var tokens []string
	if r.startupPending {
		tokens = append(tokens, tokenStarting)
	}
	if r.shuttingDown {
		tokens = append(tokens, tokenStopping)
	}
	for _, id := range ids {
		comp := r.comps[id]
		st := comp.result.Status
		passes := st == StatusUp || st == StatusDegraded
		if !(comp.check.Readiness && !passes || comp.check.Critical && st == StatusDown) {
			continue
		}
		reason := comp.result.Reason
		if reason == "" {
			reason = "down"
			if st != StatusDown {
				reason = "unknown"
			}
		}
		tokens = append(tokens, string(id)+":"+reason)
	}
	return tokens
}

// Ready reports whether the process is ready, with the tokens that say why
// not: "<component>:<reason>", sorted by component. A registry with no
// components is ready.
func (r *Registry) Ready() (ready bool, notReady []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	notReady = r.notReadyLocked()
	return len(notReady) == 0, notReady
}

// Wait blocks until every listed component is Up or Degraded (tasks Ready
// signals), and returns ctx.Err() if ctx ends first. A listed ID that is not
// registered yet counts as not Up, so a later Register can still release the
// Wait. No ids returns nil. A Wait that is already satisfied returns nil
// even on a cancelled ctx.
func (r *Registry) Wait(ctx context.Context, ids ...ComponentID) error {
	for {
		r.mu.Lock()
		waiting := r.pendingLocked(ids)
		changed := r.changed
		r.mu.Unlock()
		if !waiting {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// pendingLocked reports whether any of ids is not registered, or not Up or
// Degraded. r.mu is held.
func (r *Registry) pendingLocked(ids []ComponentID) bool {
	for _, id := range ids {
		comp, ok := r.comps[id]
		if !ok {
			return true
		}
		if st := comp.result.Status; st != StatusUp && st != StatusDegraded {
			return true
		}
	}
	return false
}
