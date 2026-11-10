// file: core/health/registry.go
// version: 1.0.0
// guid: 268f470f-94c4-4d33-93bd-af65944395ec
// last-edited: 2026-11-10

package health

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// The readiness core of 05-health §5H.3.1: Status, ComponentID, Result,
// Check, Registry, Snapshot. It is the F2 slice (05-health §5H.10): components
// are updated by a push-only Reporter, plus one synchronous call of a Check's
// Probe at Register. The probe loop, /livez, /readyz and the HTTP snapshot
// are F8's.
//
// The state of a Registry is one mutex-guarded map. Wait is signalled, not
// polled: every Register and Report closes the registry's changed channel
// and installs a fresh one, so each blocked Wait wakes exactly when
// something it might be waiting for happened, and no goroutine exists beyond
// the callers'.

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

// Snapshot is the registry's state at one instant.
type Snapshot struct {
	Ready      bool
	Live       bool
	Components []ComponentState // sorted by ID
	Startup    []string         // degraded startup tokens
}

// ComponentState is one component in a Snapshot.
type ComponentState struct {
	ID       ComponentID
	Result   Result
	Critical bool
	Duration time.Duration // last check duration
}

type component struct {
	check    Check
	result   Result
	duration time.Duration
}

// Registry is one per App (R5). Modules register Checks via Registrar.Health and
// push through module.Deps.Health (a Reporter scoped to the module's IDs).
// It is goroutine-safe; build one with NewRegistry.
type Registry struct {
	now func() time.Time

	mu    sync.Mutex
	comps map[ComponentID]*component
	// changed is closed, and replaced, by every Register and Report: the
	// broadcast Wait blocks on.
	changed chan struct{}
}

// NewRegistry returns an empty registry. A kernel builds one per App.
func NewRegistry() *Registry {
	return &Registry{
		now:     time.Now,
		comps:   map[ComponentID]*component{},
		changed: make(chan struct{}),
	}
}

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
// Unknown, unless its Check has a Probe: the Probe is then called once, here
// and synchronously, under Check.Timeout (default 5s), and its Result is the
// component's first. The Probe is never called again: scheduling is F8's, so
// until then a probed component changes only by Report. A Probe that panics
// yields Down with the Reason probe_panicked. A Probe that ignores its
// context holds Register for as long as it runs.
func (r *Registry) Register(c Check) (Reporter, error) {
	if !validID(c.ID) {
		return nil, fmt.Errorf("health: component ID %q is not <module>.<name> of [a-z0-9_-] segments", c.ID)
	}
	r.mu.Lock()
	_, dup := r.comps[c.ID]
	r.mu.Unlock()
	if dup {
		return nil, fmt.Errorf("health: component %s is already registered", c.ID)
	}
	comp := &component{check: c}
	if c.Probe != nil {
		// Outside the lock: a Probe may be slow and may call the registry.
		comp.result, comp.duration = r.runProbe(c)
		comp.result = r.clamp(comp.result)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.comps[c.ID]; dup { // registered meanwhile
		return nil, fmt.Errorf("health: component %s is already registered", c.ID)
	}
	r.comps[c.ID] = comp
	r.broadcastLocked()
	return componentReporter{reg: r, id: c.ID}, nil
}

// runProbe calls c.Probe once under its timeout, containing a panic.
func (r *Registry) runProbe(c Check) (res Result, took time.Duration) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	defer func() {
		took = time.Since(start)
		if p := recover(); p != nil {
			res = Result{Status: StatusDown, Reason: "probe_panicked", Detail: fmt.Sprintf("probe panicked: %v", p)}
		}
	}()
	return c.Probe(ctx), 0
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
	defer r.mu.Unlock()
	if comp, ok := r.comps[id]; ok {
		comp.result = res
		r.broadcastLocked()
	}
}

// broadcastLocked wakes every blocked Wait. r.mu is held.
func (r *Registry) broadcastLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// notReadyLocked is the readiness tokens, "<component>:<reason>", sorted by
// component ID: every Readiness component that is not Up or Degraded, and
// every Critical component that is Down. A result with no Reason has the
// fixed one, "unknown" or "down". r.mu is held.
func (r *Registry) notReadyLocked() []string {
	ids := make([]ComponentID, 0, len(r.comps))
	for id := range r.comps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var tokens []string
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

// Live reports whether the process is alive. There is no heartbeat before
// F8's, so a registry is live for as long as it exists.
func (r *Registry) Live() bool { return r != nil }

// Snapshot is the registry's state: Ready, Live and every component sorted by
// ID. Startup is F8's and stays empty.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{Ready: len(r.notReadyLocked()) == 0, Live: true, Components: make([]ComponentState, 0, len(r.comps))}
	for id, comp := range r.comps {
		s.Components = append(s.Components, ComponentState{ID: id, Result: comp.result, Critical: comp.check.Critical, Duration: comp.duration})
	}
	slices.SortFunc(s.Components, func(a, b ComponentState) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return s
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
