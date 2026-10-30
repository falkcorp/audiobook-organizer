// file: internal/server/handlers/abs/main_test.go
// version: 1.1.0
// guid: fc5d9b1a-46ac-4001-84ff-5f6f759647af
// last-edited: 2026-10-30

package abs_test

import (
	"os"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
)

// TestMain lowers the argon2id cost every password in this package's tests is
// hashed at. Each test that seeds a password user (seedPasswordUser) or logs
// one in pays a full-cost derivation (64 MiB, t=3: 1-2 s under -race), and a
// failed login for an unknown name pays one on the dummy hash too. None of the
// tests asserts on the cost: the derivation still runs with the parameters
// stored in each hash, so a hash verifies and fails exactly as in production.
// absauth's own tests pin the production parameters.
//
// SetHashCostForTest panics outside a test binary and once the dummy hash is
// built, so it must be the first thing here, before any test code runs.
func TestMain(m *testing.M) {
	absauth.SetHashCostForTest(1, 8192)
	os.Exit(m.Run())
}

// TestMainLoweredTheHashCost proves TestMain's call took effect: the speed-up
// is otherwise silent, and losing it would show up only as a slower package.
func TestMainLoweredTheHashCost(t *testing.T) {
	_, hash, err := absauth.HashPassword("Gotham")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hash, "m=8192,t=1,") {
		t.Fatalf("hash %q was written at the production cost; TestMain's SetHashCostForTest did not apply", hash)
	}
}
