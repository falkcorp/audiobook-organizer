// file: internal/operations/registry/inflight_internal_test.go
// version: 1.1.0
// guid: 7e3b9d14-5c2a-4f81-a6e0-2d9c8b4f1a37
// last-edited: 2026-10-10

package registry

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
)

// TestInflightByDef_StubHandleHasNoAge: a dispatcher stub handle (claimed, not
// yet picked up by a worker) counts as in flight but contributes no progress
// age. It has neither stamp, and reading it as now - 0 would report a
// decades-long stall for every run queued behind a saturated pool.
func TestInflightByDef_StubHandleHasNoAge(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &Registry{running: map[string]*runHandle{}, livenessNow: func() time.Time { return now }}

	stub := &runHandle{id: "stub", defID: "d", plugin: "p"}
	started := &runHandle{id: "started", defID: "d", plugin: "p"}
	started.attemptStartedAt.Store(now.Add(-3 * time.Minute).UnixNano())
	reported := &runHandle{id: "reported", defID: "d", plugin: "p"}
	reported.attemptStartedAt.Store(now.Add(-2 * time.Hour).UnixNano())
	reported.lastProgressAt.Store(now.Add(-time.Minute).UnixNano())
	r.running["stub"], r.running["started"], r.running["reported"] = stub, started, reported

	got := r.inflightByDef()[opsmetrics.InflightKey{DefID: "d", Plugin: "p"}]
	if got.Runs != 3 {
		t.Errorf("Runs = %d, want 3 (the stub holds a slot)", got.Runs)
	}
	// started has never reported: its age runs from the attempt start (3m).
	// reported uses its last report (1m), not its attempt start (2h).
	if got.ProgressAge != 3*time.Minute {
		t.Errorf("ProgressAge = %v, want 3m", got.ProgressAge)
	}

	only := &Registry{running: map[string]*runHandle{"stub": stub}, livenessNow: func() time.Time { return now }}
	if st := only.inflightByDef()[opsmetrics.InflightKey{DefID: "d", Plugin: "p"}]; st.Runs != 1 || st.ProgressAge != 0 {
		t.Errorf("stub only: %+v, want Runs 1 and ProgressAge 0", st)
	}
}

// TestInflightByDef_ProgressTimeout: each key reports its def's effective
// watchdog budget (explicit, or the default when the def sets 0, or when the
// def no longer resolves).
func TestInflightByDef_ProgressTimeout(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &Registry{
		running:     map[string]*runHandle{},
		livenessNow: func() time.Time { return now },
		defs: map[string]OperationDef{
			"long": {ID: "long", Liveness: LivenessNone, ProgressTimeout: 2 * time.Hour},
			"dflt": {ID: "dflt"},
		},
	}
	r.running["a"] = &runHandle{id: "a", defID: "long", plugin: "p"}
	r.running["b"] = &runHandle{id: "b", defID: "dflt", plugin: "p"}
	r.running["c"] = &runHandle{id: "c", defID: "gone", plugin: "p"}

	got := r.inflightByDef()
	for def, want := range map[string]time.Duration{"long": 2 * time.Hour, "dflt": defaultProgressTimeout, "gone": defaultProgressTimeout} {
		if st := got[opsmetrics.InflightKey{DefID: def, Plugin: "p"}]; st.ProgressTimeout != want {
			t.Errorf("%s: ProgressTimeout = %v, want %v", def, st.ProgressTimeout, want)
		}
	}
}
