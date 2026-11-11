// file: core/auth/password/password_guard_test.go
// version: 3.0.0
// guid: b81d3e64-2a97-4c05-8f1e-d7a09c5e3b72
// last-edited: 2026-11-10

package password

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

// Outside a test binary New refuses every cost-lowering field, and with none
// set it builds a production Hasher.
func TestNewRefusesLoweredCostOutsideATestBinary(t *testing.T) {
	notTest := func() bool { return false }
	for name, o := range map[string]Options{
		"time":    {Time: 1},
		"memory":  {MemoryKiB: 8},
		"compute": {Compute: func([]byte, []byte, uint32, uint32, uint8, uint32) []byte { return nil }},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: New lowered the cost outside a test binary", name)
				}
			}()
			newHasher(o, notTest)
		}()
	}
	if h := newHasher(Options{Permits: 2}, notTest); h.time != argonTime || h.memory != argonMemory || h.Permits() != 2 {
		t.Fatalf("production Hasher: time=%d memory=%d permits=%d", h.time, h.memory, h.Permits())
	}
}
