// file: internal/aidispatch/dispatch_testseams_test.go
// version: 1.0.0
// guid: 984597f9-28dc-470c-8bd3-e84b42a5a713
// last-edited: 2026-10-11

package aidispatch

// Test-only helpers. Both had no production caller (deadcode-unreachable) and
// moved here from capabilities.go and dispatch.go.

// SharedFSFeature returns the feature string for one library root.
func SharedFSFeature(rootID string) string { return FeatureSharedFSPrefix + rootID }

// WithTotalCap caps simultaneous requests across all endpoints for one kind.
// A Limit < 1 means unlimited.
func WithTotalCap(k Kind, total TotalCap) Option {
	return func(d *Dispatcher) { d.totals[k] = total }
}
