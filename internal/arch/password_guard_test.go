// file: internal/arch/password_guard_test.go
// version: 1.0.0
// guid: ae36d58c-ed3f-448e-b9a3-0bcf100805e7
// last-edited: 2026-11-10

package arch

import (
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// TestPasswordHashingStaysInCore: ABO hashes and verifies every password
// through core/auth/password (F4-02). No non-test file in the module calls
// bcrypt directly (a direct compare rejects every user the sign-in rehashed
// to argon2id), and none builds a password.Options that lowers the hash cost
// (Time, MemoryKiB and Compute are for test binaries; password.New also
// panics on them outside one). ABO has no exemptions: its absauth package
// only builds the production Hasher. These replace absauth's
// TestNoDirectBcrypt and this package's TestHashCostSeamStaysInTests, whose
// seam (absauth.SetHashCostForTest) became those Options fields.
func TestPasswordHashingStaysInCore(t *testing.T) {
	root := repoRoot(t)
	archtest.NoDirectBcrypt(t, root, nil)
	archtest.NoLoweredHashCost(t, root, nil)
}
