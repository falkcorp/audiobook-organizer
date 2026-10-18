// file: internal/merge/service_b3_misc_test.go
// version: 1.1.3
// guid: 2f8b6d1c-5e9a-4c3f-8b7d-0a1e2c4f6d8b
// last-edited: 2026-10-18

package merge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestB3_AsExternalIDReassigner_Nil covers the s == nil branch: passing a
// literal nil (not a typed-nil store) must return nil, not panic on the
// type assertion.
func TestB3_AsExternalIDReassigner_Nil(t *testing.T) {
	assert.Nil(t, AsExternalIDReassigner(nil))
}

// b3NotAReassigner is any concrete type that does NOT implement
// ExternalIDReassigner, to exercise AsExternalIDReassigner's failed
// type-assertion branch.
type b3NotAReassigner struct{}

// TestB3_AsExternalIDReassigner_NotImplementing covers the case where the
// argument is non-nil but does not implement ReassignExternalIDs.
func TestB3_AsExternalIDReassigner_NotImplementing(t *testing.T) {
	assert.Nil(t, AsExternalIDReassigner(&b3NotAReassigner{}))
	assert.Nil(t, AsExternalIDReassigner("just a string"))
}

// TestB3_LockUnlockMergeRMW_RoundTrips exercises the exported
// LockMergeRMW/UnlockMergeRMW pair used by callers outside this package (e.g.
// dedup's split-book merge) to share the package-level merge serialization
// lock. A bare lock/unlock round trip must not deadlock or panic, and must
// leave the lock available for the next acquirer (proven by acquiring it
// again afterward).
func TestB3_LockUnlockMergeRMW_RoundTrips(t *testing.T) {
	LockMergeRMW()
	UnlockMergeRMW()

	// If Unlock didn't actually release it, this second Lock would hang
	// forever and the test would time out rather than fail cleanly — that's
	// an acceptable signal for a mutex round-trip test.
	LockMergeRMW()
	UnlockMergeRMW()
}
