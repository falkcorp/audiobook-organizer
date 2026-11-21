// file: internal/server/handlers/main_test.go
// version: 0.1.0
// guid: d28601c5-8d17-40b8-9e2d-ffb44c06c6d3
// last-edited: 2026-11-21

package handlers_test

import (
	"os"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	authpw "github.com/falkcorp/mediacore/auth/password"
	"golang.org/x/crypto/bcrypt"
)

// TestMain lowers only the bcrypt dummy every login verification compares
// against (a DefaultCost comparison per login added about 96 s to this
// package's login tests under -race -short on 2026-11-21). The argon2id cost
// stays at production here, as before: the login timing tests in this
// package measure it. A legacy bcrypt row still verifies at the cost stored
// in it; only the dummy, used when the account has no bcrypt hash of its own,
// is cheaper.
func TestMain(m *testing.M) {
	absauth.Passwords = authpw.New(authpw.Options{BcryptCost: bcrypt.MinCost})
	os.Exit(m.Run())
}
