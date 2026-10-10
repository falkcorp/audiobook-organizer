// file: internal/operations/registry/export_test.go
// version: 1.3.0
// guid: 5c45952c-3393-4f33-9784-efd565ec8c49
// last-edited: 2026-10-10

package registry

import (
	"context"
	"sort"
	"time"
)

// WatchdogCycleForTest runs one watchdog inspection pass synchronously, so an
// external test can step the watchdog against Options.LivenessNow instead of
// waiting on its ticker. Compiled into test binaries only.
func (r *Registry) WatchdogCycleForTest() { r.watchdogCycle() }

// SetReporterClockForTest replaces a DB reporter's clock. Call before the first UpdateProgress.
func SetReporterClockForTest(rep Reporter, now func() time.Time) {
	r, ok := rep.(*dbReporter)
	if !ok {
		panic("SetReporterClockForTest: reporter is not a *dbReporter")
	}
	r.progressMu.Lock()
	r.nowFn = now
	r.progressMu.Unlock()
}

// ScanStandDownDefIDForTest is the library.scan def id (and ConcurrencyKey)
// the registry quiesces, so external tests use the real constant rather than
// a copied string.
const ScanStandDownDefIDForTest = scanStandDownDefID

// DispatchCycleForTest runs one dispatcher pass synchronously.
func (r *Registry) DispatchCycleForTest(ctx context.Context) { r.dispatchCycle(ctx) }

// HeldConcurrencyKeysForTest lists the ConcurrencyKeys currently held, sorted.
func (r *Registry) HeldConcurrencyKeysForTest() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.concurrencyKeys))
	for k := range r.concurrencyKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
