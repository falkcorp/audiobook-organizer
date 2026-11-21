// file: internal/server/handlers/abs/main_test.go
// version: 1.1.1
// guid: fc5d9b1a-46ac-4001-84ff-5f6f759647af
// last-edited: 2026-11-10

package abs_test

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	authpw "github.com/falkcorp/mediacore/auth/password"
)

// TestMain lowers the argon2id cost every password in this package's tests is
// hashed at. Each test that seeds a password user (seedPasswordUser) or logs
// one in pays a full-cost derivation (64 MiB, t=3: 1-2 s under -race), and a
// failed login for an unknown name pays one on the dummy hash too. None of the
// tests asserts on the cost: the derivation still runs with the parameters
// stored in each hash, so a hash verifies and fails exactly as in production.
// absauth's own tests pin the production parameters. Every login verification
// also runs one bcrypt comparison (against a dummy unless the account is a
// legacy bcrypt row), and BcryptCost lowers that dummy from DefaultCost the
// same way.
//
// SetHashCostForTest panics outside a test binary and once the dummy hash is
// built, so it must be the first thing here, before any test code runs.
func TestMain(m *testing.M) {
	absauth.Passwords = authpw.New(authpw.Options{Time: 1, MemoryKiB: 8192, BcryptCost: bcrypt.MinCost})
	os.Exit(m.Run())
}

// TestMainLoweredTheHashCost proves TestMain's call took effect: the speed-up
// is otherwise silent, and losing it would show up only as a slower package.
func TestMainLoweredTheHashCost(t *testing.T) {
	_, hash, err := absauth.Passwords.Hash("Gotham")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hash, "m=8192,t=1,") {
		t.Fatalf("hash %q was written at the production cost; TestMain's SetHashCostForTest did not apply", hash)
	}
}
