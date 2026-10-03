// file: internal/operations/registry/export_test.go
// version: 1.0.0
// guid: 5c45952c-3393-4f33-9784-efd565ec8c49
// last-edited: 2026-10-03

package registry

// WatchdogCycleForTest runs one watchdog inspection pass synchronously, so an
// external test can step the watchdog against Options.LivenessNow instead of
// waiting on its ticker. Compiled into test binaries only.
func (r *Registry) WatchdogCycleForTest() { r.watchdogCycle() }
