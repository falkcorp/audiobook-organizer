// file: internal/server/main_test.go
// version: 1.0.0
// guid: 2d9f6b3e-7a41-4c58-9e02-b6c1f8a35d74
// last-edited: 2026-10-29

package server

import (
	"os"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
)

// TestMain lowers the argon2id cost every password in this package's tests is
// hashed at, and so the cost of the dummy hash a failed login for an unknown
// name verifies against. At the production cost (64 MiB, t=3) one derivation
// takes 1-2 s under -race, and the login-throttle tests drive a throttle to its
// 15-30 failure budget: seven of them took ~26 s each, ~180 s of the package's
// ~20 minutes in `make test-fixtures`, which overran its 25 minute timeout
// (Woodpecker pipeline 1823, 2026-10-29).
//
// None of those tests asserts on the cost. The derivation still runs, with the
// parameters stored in each hash, so a hash verifies and fails exactly as it
// does in production; absauth's own tests, whose binary does not do this, pin
// the production parameters and the one-derivation-per-login property.
func TestMain(m *testing.M) {
	absauth.SetHashCostForTest(1, 64)
	os.Exit(m.Run())
}
