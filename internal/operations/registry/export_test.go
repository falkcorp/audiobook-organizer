// file: internal/operations/registry/export_test.go
// version: 1.4.0
// guid: 5c45952c-3393-4f33-9784-efd565ec8c49
// last-edited: 2026-10-10

package registry

import (
	"context"
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

// HeldConcurrencyKeysForTest returns a copy of the held ConcurrencyKeys,
// mapped to the op id holding each. The holder matters: a key can be released
// by one op and claimed by the next between two reads, so a test that asks
// "does op X still hold a key" must check the holder, not only the key set.
func (r *Registry) HeldConcurrencyKeysForTest() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	held := make(map[string]string, len(r.concurrencyKeys))
	for k, holder := range r.concurrencyKeys {
		held[k] = holder
	}
	return held
}

// BoundToForTest reports whether f was built bound to done: the channel its
// Check watches for the run context ending. It compares channels, not
// behavior, because a behavioral probe (Check() right after ctx.Done()) races
// executeRun's own revoke in its select and passes either way.
func (f Fence) BoundToForTest(done <-chan struct{}) bool {
	return f.st != nil && f.st.done != nil && f.st.done == done
}
