// file: internal/operations/registry/touch_liveness_internal_test.go
// version: 1.0.0
// guid: bb461090-e1ae-4b69-9097-d0fbc692c976
// last-edited: 2026-09-13

package registry

import (
	"sync/atomic"
	"testing"
)

// TouchLiveness stamps the watchdog's clock (touchProgressFn -> the runHandle's
// lastProgressAt, worker.go) and nothing else: no progress numbers change, so
// RunItems keeps sole ownership of current/total.
func TestTouchLiveness_StampsClockOnly(t *testing.T) {
	var touched atomic.Int64
	r := &dbReporter{touchProgressFn: func() { touched.Add(1) }}
	TouchLiveness(r)
	TouchLiveness(r)
	if touched.Load() != 2 {
		t.Fatalf("touches = %d, want 2", touched.Load())
	}
	if r.progressGen.Load() != 0 || r.progressCurrent != 0 || r.progressTotal != 0 || r.lastProgressMessage != "" {
		t.Fatalf("TouchLiveness changed progress state: gen=%d cur=%d total=%d msg=%q",
			r.progressGen.Load(), r.progressCurrent, r.progressTotal, r.lastProgressMessage)
	}
}

// A reporter that cannot touch (a fake, an adapter, nil) is a no-op, not a panic.
func TestTouchLiveness_NoToucherIsNoop(t *testing.T) {
	TouchLiveness(nil)
	TouchLiveness(struct{ Reporter }{})
}

// The stand-down wrapper must forward the stamp or ops under a hold would
// heartbeat into nothing. hold is nil: forwarding must not touch the lease.
func TestTouchLiveness_StandDownReporterForwards(t *testing.T) {
	var touched atomic.Int64
	s := &standDownReporter{Reporter: &dbReporter{touchProgressFn: func() { touched.Add(1) }}}
	TouchLiveness(s)
	if touched.Load() != 1 {
		t.Fatalf("touches through standDownReporter = %d, want 1", touched.Load())
	}
}
