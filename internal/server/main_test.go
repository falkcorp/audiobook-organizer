// file: internal/server/main_test.go
// version: 1.1.0
// guid: 2d9f6b3e-7a41-4c58-9e02-b6c1f8a35d74
// last-edited: 2026-10-30

package server

import (
	"os"
	"strings"
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
// does in production. absauth's own tests do not lower the cost (apart from the
// ones proving the setter's guards, which panic before changing anything): they
// pin the production parameters and the one-derivation-per-login property.
//
// SetHashCostForTest panics outside a test binary and once the dummy hash is
// built, so it must be the first thing here, before any test code runs.
func TestMain(m *testing.M) {
	absauth.SetHashCostForTest(1, 64)
	os.Exit(m.Run())
}

// TestMainLoweredTheHashCost is the proof that TestMain's call took effect: the
// speed-up this package's login-throttle tests depend on is otherwise silent,
// and losing it would show up only as a package that runs 4x longer.
func TestMainLoweredTheHashCost(t *testing.T) {
	_, hash, err := absauth.HashPassword("Gotham")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hash, "m=64,t=1,") {
		t.Fatalf("hash %q was written at the production cost; TestMain's SetHashCostForTest did not apply", hash)
	}
}
