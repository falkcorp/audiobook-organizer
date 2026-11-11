// file: core/auth/password/password_archtest_test.go
// version: 1.0.0
// guid: af65463a-3a1d-4d4a-9717-949d95dec2cc
// last-edited: 2026-11-10

// The module-wide password guards run from an external test package: archtest
// reaches httpapi, which imports auth, which imports this package, so an
// in-package test importing archtest would be an import cycle. Moved out of
// password_guard_test.go at F4-02 when auth began building its own Hasher.
package password_test

import (
	"os"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// TestNoDirectBcrypt fails when production code in the core module calls
// bcrypt.CompareHashAndPassword or bcrypt.GenerateFromPassword outside
// password.go: every stored hash is hashed and verified through the Hasher,
// which rehashes bcrypt to argon2id. ABO runs the same check over its own
// module (internal/arch).
func TestNoDirectBcrypt(t *testing.T) {
	// os.Getwd, not runtime.Caller: CI builds with -trimpath.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	archtest.NoDirectBcrypt(t, root, []string{"auth/password/password.go"})
	archtest.NoLoweredHashCost(t, root, archtest.CoreTestSupport)
}
