// file: internal/server/handlers/abs/export_oidc_test.go
// version: 1.0.0
// guid: bf005499-fccb-4779-9b2d-bb67250b11e1
// last-edited: 2026-10-19

package abs

import "testing"

// IsolateOIDCCodesForTest gives the calling test its own empty authorization
// code store and restores the previous one when the test ends.
//
// The store is package-level (the web OAuth deep-link flow mints into it
// without a Handler), so without this every test in the binary shares one
// store: per-user caps and the global cap carry over between tests, and one
// test's codes can evict another's. Not safe with t.Parallel — the swap is a
// plain assignment — which is why no test that mints calls t.Parallel.
func IsolateOIDCCodesForTest(t testing.TB) {
	t.Helper()
	prev := oidcCodes
	oidcCodes = newOIDCTTLStore[oidcPendingCode](oidcStoreCap)
	t.Cleanup(func() { oidcCodes = prev })
}
