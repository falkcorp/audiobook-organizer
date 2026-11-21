// file: internal/books/identity_flip.go
// version: 0.1.0
// guid: 3f7a5c19-2d84-4e6b-a1c0-9b5e8d27f406
// last-edited: 2026-11-21

package books

import "sync/atomic"

// ABO's identity flip (F4-08 B10) is ONE decision, identityOn, and everything
// the flip changes in this package reads it: the manifest (the books.level-2
// step, M3 in the cutover list, the retired identity claims), books.Options
// (the identity boot and its inputs), Register (the server built under core
// identity, the mount's authentication). They are tied to one predicate on
// purpose. Three of them can come apart, and each of the ways is a way to
// serve with the wrong authority:
//
//   - the identity boot without M3: core identity over an empty user table,
//     every HEAD user locked out;
//   - core identity without the server built under it (server.WithIdentity):
//     HEAD's own middleware keeps judging requests against HEAD's tables, which
//     M3 drained;
//   - the server built under core identity while the mount still says
//     LegacyAuthGin, or on a store that is not at BooksIdentityLevel (the
//     merge permanence rule reads s.identity != nil and must agree with the
//     store's level).
//
// In production the predicate is OFF until B10g, the switch, which turns it on
// for good and deletes the selector below. Until then the only way to turn it on
// is the test-only selector, and an archtest (internal/arch) pins that nothing
// outside a test calls it and nothing outside this file touches the variable.

// identityFlip is the predicate's backing state. It is never written outside
// SetIdentityFlipForTest (internal/arch/identity_flip_guard_test.go).
var identityFlip atomic.Bool

// identityOn reports whether this build runs ABO under core identity. A runtime
// read, not a constant: every consumer reaches it through this one function, so
// the selector below moves them all together and none can keep a private copy.
func identityOn() bool { return identityFlip.Load() }

// SetIdentityFlipForTest turns the identity predicate on or off and returns the
// function that puts the previous value back. It is for tests of the flipped
// wiring (B10c to B10f), which run the real Module under core identity before
// B10g turns the flip on. The value is process-global: a test that sets it must
// not run in parallel with one that reads the manifest, Options or Register,
// and restores it with t.Cleanup(restore).
func SetIdentityFlipForTest(on bool) (restore func()) {
	prev := identityFlip.Swap(on)
	return func() { identityFlip.Store(prev) }
}
