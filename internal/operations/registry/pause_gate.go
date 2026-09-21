// file: internal/operations/registry/pause_gate.go
// version: 1.0.0
// guid: 5e81c3f7-92a4-4d16-8b0e-6c7f2a91d340
// last-edited: 2026-09-20

package registry

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Operator pause: drain in-flight items, hold before dispatching new ones.
//
// WHAT IT IS NOT. This is not cancel, and it is not the scan stand-down.
// Cancel ends a run. The scan stand-down is an INTERNAL gate an apply op takes
// so library.scan cannot rewrite files underneath it; it names one def, carries
// a lease, and releases itself. This gate is an OPERATOR control over every op
// that dispatches items, it names no def, and nothing releases it but a resume.
//
// SEMANTICS (owner, 2026-09-20): "allow current tasks in the operation to
// complete and hold before assigning any new ones." An item already inside its
// callback runs to completion. The next item parks at the gate. Scope is items
// within running ops — the dispatcher still starts new operations and the
// scheduler still fires, which the owner chose deliberately.
//
// WHY IT STAMPS LIVENESS WHILE PARKED. watchdog.go's defaultProgressTimeout is
// 5 minutes: an op that reports nothing for that long is reaped as stuck. A
// gate that merely blocked would therefore turn "pause" into "cancel after five
// minutes" — the precise opposite of the request. Wait keeps the op's liveness
// clock stamped and its current-item label honest ("paused — holding before
// item N") for as long as it is held.
//
// WHERE IT IS CALLED FROM. run_items.go's runOne, BEFORE the per-item
// context.WithTimeout. Gating after that timeout starts would make every item
// resumed from a pause longer than PerItemTimeout (3m on the batch apply) fail
// with DeadlineExceeded.
//
// COVERAGE, HONESTLY. Only ops that dispatch through RunItems can park here —
// 60 files, including acoustid.window-backfill and metadata.batch-apply-cached.
// An op with a single long-running body and no item loop (library.scan above
// all) has no safe generic seam and does NOT pause; the API says so rather than
// implying otherwise.

// pauseSettingKey is the SettingsStore key holding the persisted marker. The
// pause survives a restart on purpose (owner choice): a reboot or a deploy must
// not silently resume work someone stopped. The guard against a forgotten pause
// is visibility, not a timeout — there is no holder process here whose death
// could clear it, so a lease like the stand-down's would fight the feature.
const pauseSettingKey = "registry.operations_paused"

// pauseMarker is the persisted form.
type pauseMarker struct {
	Paused   bool   `json:"paused"`
	Reason   string `json:"reason,omitempty"`
	By       string `json:"by,omitempty"`
	SinceUTC int64  `json:"since_unix_nano,omitempty"`
}

// pauseGate is the process-wide gate. The zero value is usable and unpaused.
type pauseGate struct {
	mu sync.Mutex
	// ch is non-nil exactly while paused. Waiters block on receive; Resume
	// closes it, releasing every waiter at once. A fresh channel is made on
	// each Pause so a resumed-then-repaused gate never reuses a closed one.
	ch     chan struct{}
	marker pauseMarker
}

// PauseState is the read model the API returns.
type PauseState struct {
	Paused bool      `json:"paused"`
	Reason string    `json:"reason,omitempty"`
	By     string    `json:"by,omitempty"`
	Since  time.Time `json:"since,omitempty"`
	// Waiting is how many item dispatches are parked right now. Zero while
	// in-flight items are still draining, which is the normal first seconds of
	// a pause and NOT a sign the pause failed to take.
	Waiting int `json:"waiting"`
}

// waiting counts parked dispatches for the read model.
var pauseWaiting = struct {
	mu sync.Mutex
	n  int
}{}

// Pause holds item dispatch. Idempotent: pausing an already-paused gate updates
// the reason without disturbing existing waiters.
func (g *pauseGate) Pause(reason, by string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.marker = pauseMarker{Paused: true, Reason: reason, By: by, SinceUTC: time.Now().UnixNano()}
	if g.ch == nil {
		g.ch = make(chan struct{})
	}
}

// Resume releases every parked dispatch. Idempotent.
func (g *pauseGate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch != nil {
		close(g.ch)
		g.ch = nil
	}
	g.marker = pauseMarker{}
}

// IsPaused is the cheap check on the dispatch hot path.
func (g *pauseGate) IsPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ch != nil
}

// State returns the read model.
func (g *pauseGate) State() PauseState {
	g.mu.Lock()
	m := g.marker
	paused := g.ch != nil
	g.mu.Unlock()
	pauseWaiting.mu.Lock()
	n := pauseWaiting.n
	pauseWaiting.mu.Unlock()
	st := PauseState{Paused: paused, Reason: m.Reason, By: m.By, Waiting: n}
	if m.SinceUTC > 0 {
		st.Since = time.Unix(0, m.SinceUTC).UTC()
	}
	return st
}

// pauseLivenessInterval is how often a parked dispatch stamps the op's liveness
// clock. Comfortably inside watchdog.go's 5-minute defaultProgressTimeout, and
// cheap: it is one stamp per parked item per interval, not a busy loop.
const pauseLivenessInterval = 30 * time.Second

// Wait parks until the gate is released or ctx ends.
//
// It returns ctx.Err() on cancellation, so a paused op stays cancellable — an
// operator who changes their mind does not have to resume before cancelling.
// It returns nil when the gate is open or was released.
//
// rep may be nil (tests); when present, Wait stamps liveness and labels the op
// so the UI shows a deliberate hold rather than a stall.
func (g *pauseGate) Wait(ctx context.Context, rep Reporter, label string) error {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	if ch == nil {
		return nil // open: the common path allocates nothing and does not block
	}

	pauseWaiting.mu.Lock()
	pauseWaiting.n++
	pauseWaiting.mu.Unlock()
	defer func() {
		pauseWaiting.mu.Lock()
		pauseWaiting.n--
		pauseWaiting.mu.Unlock()
	}()

	if rep != nil {
		rep.SetCurrentItem("paused — holding before " + label)
		TouchLiveness(rep)
	}

	tick := time.NewTicker(pauseLivenessInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
			return nil
		case <-tick.C:
			// The whole reason this loop exists. Without it the watchdog reaps
			// the op as stuck and the pause becomes a delayed cancel.
			if rep != nil {
				TouchLiveness(rep)
			}
			// Re-read: a Resume followed by a new Pause swaps the channel, and
			// a waiter parked on the old closed one would spin.
			g.mu.Lock()
			cur := g.ch
			g.mu.Unlock()
			if cur == nil {
				return nil
			}
			ch = cur
		}
	}
}

// globalPauseGate is the process-wide instance. Package-level because RunItems
// is a free function reached from 60 call sites that hold no Registry pointer.
var globalPauseGate = &pauseGate{}

// PauseOperations holds item dispatch across every op that uses RunItems and
// persists the marker. The returned error is the PERSIST result only: the hold
// is always in effect when this returns.
func PauseOperations(reason, by string) error {
	globalPauseGate.Pause(reason, by)
	return persistPause()
}

// ResumeOperations releases the hold and clears the marker. The returned error
// is the persist result only; the hold is always released when this returns.
func ResumeOperations() error {
	globalPauseGate.Resume()
	return persistPause()
}

// OperationsPaused reports whether item dispatch is held.
func OperationsPaused() bool { return globalPauseGate.IsPaused() }

// OperationsPauseState returns the read model for the API.
func OperationsPauseState() PauseState { return globalPauseGate.State() }

// pauseStore is the persister, set once at wire time. Package-level for the
// same reason globalPauseGate is: the gate is reached from RunItems, a free
// function, and from an HTTP handler that holds no Registry pointer.
//
// Persistence is INSIDE Pause/Resume rather than a step the caller remembers.
// A pause that took effect in memory but was never written would resume itself
// at the next deploy with nobody the wiser, and this session deploys often.
var pauseStore struct {
	mu sync.Mutex
	ss standDownPersister
}

// SetPauseStore wires persistence. Safe to call with nil (tests).
func SetPauseStore(ss standDownPersister) {
	pauseStore.mu.Lock()
	pauseStore.ss = ss
	pauseStore.mu.Unlock()
}

// persistPause writes the marker. Errors are returned for logging but never
// block the in-memory state change: the operator asked for a hold, and they get
// one even if the marker cannot be written.
func persistPause() error {
	pauseStore.mu.Lock()
	ss := pauseStore.ss
	pauseStore.mu.Unlock()
	if ss == nil {
		return nil
	}
	st := globalPauseGate.State()
	m := pauseMarker{Paused: st.Paused, Reason: st.Reason, By: st.By}
	if !st.Since.IsZero() {
		m.SinceUTC = st.Since.UnixNano()
	}
	blob, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return ss.SetSetting(pauseSettingKey, string(blob), "json", false)
}

// RestorePauseState re-applies a persisted pause at boot.
//
// A pause MUST survive a restart. Prod is deployed often, and a deploy that
// silently resumed everything an operator had stopped would be a trap: they
// would have no reason to re-check, and the work would be running again.
func RestorePauseState(ss standDownPersister) (bool, error) {
	if ss == nil {
		return false, nil
	}
	s, err := ss.GetSetting(pauseSettingKey)
	if err != nil || s == nil || s.Value == "" {
		return false, err
	}
	var m pauseMarker
	if err := json.Unmarshal([]byte(s.Value), &m); err != nil {
		return false, err
	}
	if !m.Paused {
		return false, nil
	}
	globalPauseGate.mu.Lock()
	globalPauseGate.marker = m
	if globalPauseGate.ch == nil {
		globalPauseGate.ch = make(chan struct{})
	}
	globalPauseGate.mu.Unlock()
	return true, nil
}
