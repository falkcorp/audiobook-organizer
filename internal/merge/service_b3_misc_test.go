// file: internal/merge/service_b3_misc_test.go
// version: 1.0.0
// guid: 2f8b6d1c-5e9a-4c3f-8b7d-0a1e2c4f6d8b
// last-edited: 2026-07-18

package merge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestB3_SetWriteBackBatcher_Wires verifies the setter actually assigns the
// unexported field (exercised indirectly via a merge that has a PID to
// enqueue — the batcher-not-nil branch is separately covered by
// TestB3_MergeBooks_ITunesPIDCollectionAndITLEnqueue, so here we only check
// that a nil batcher is a legal no-op set, matching the "itunes-disabled"
// PostInit path documented on lifecycle.go).
func TestB3_SetWriteBackBatcher_NilIsLegalNoOp(t *testing.T) {
	ms := NewService(nil)
	assert.NotPanics(t, func() { ms.SetWriteBackBatcher(nil) })
}

// TestB3_LockUnlockMergeRMW_RoundTrips exercises the exported
// LockMergeRMW/UnlockMergeRMW pair used by dedup.MergeBooks to share the
// package-level merge serialization lock. A bare lock/unlock round trip
// must not deadlock or panic, and must leave the lock available for the
// next acquirer (proven by acquiring it again afterward).
func TestB3_LockUnlockMergeRMW_RoundTrips(t *testing.T) {
	LockMergeRMW()
	UnlockMergeRMW()

	// If Unlock didn't actually release it, this second Lock would hang
	// forever and the test would time out rather than fail cleanly — that's
	// an acceptable signal for a mutex round-trip test.
	LockMergeRMW()
	UnlockMergeRMW()
}

// TestB3_QuickHash_ComputesStableHash covers collision.go's QuickHash: same
// content hashes the same, different content hashes differently.
func TestB3_QuickHash_ComputesStableHash(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.bin")
	pathB := filepath.Join(dir, "b.bin")
	require.NoError(t, os.WriteFile(pathA, []byte("b3 same content"), 0o644))
	require.NoError(t, os.WriteFile(pathB, []byte("b3 different content"), 0o644))

	hashA1 := QuickHash(pathA)
	hashA2 := QuickHash(pathA)
	hashB := QuickHash(pathB)

	assert.NotEmpty(t, hashA1)
	assert.Equal(t, hashA1, hashA2, "hashing the same file twice must be stable")
	assert.NotEqual(t, hashA1, hashB, "different content must hash differently")
}

// TestB3_QuickHash_MissingFile_ReturnsEmpty covers the os.Open error branch.
func TestB3_QuickHash_MissingFile_ReturnsEmpty(t *testing.T) {
	assert.Empty(t, QuickHash(filepath.Join(t.TempDir(), "does-not-exist.bin")))
}
