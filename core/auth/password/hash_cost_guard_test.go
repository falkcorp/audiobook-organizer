// file: core/auth/password/hash_cost_guard_test.go
// version: 3.0.0
// guid: 6e0b7d24-91c3-4f8a-b5d6-3a28c4f1e907
// last-edited: 2026-11-10

package password

import (
	"fmt"
	"strings"
	"testing"
)

// TestProductionCostIsTheZeroValue: Options{} writes HEAD's production
// argon2id parameters (m=64 MiB, t=3, p=2; §4.5), so only a caller that sets
// Time or MemoryKiB lowers them.
func TestProductionCostIsTheZeroValue(t *testing.T) {
	_, h, err := New(Options{}).Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if want := "$argon2id$v=19$m=65536,t=3,p=2$"; !strings.HasPrefix(h, want) {
		t.Fatalf("hash %q does not carry the production parameters %q", h, want)
	}
}

// TestLoweredHashCostChangesWhatIsWrittenAndStillVerifies: a hash written at
// a lowered cost carries that cost and verifies under any Hasher, because
// verification reads the parameters stored in the hash.
func TestLoweredHashCostChangesWhatIsWrittenAndStillVerifies(t *testing.T) {
	cheap := New(Options{Time: 1, MemoryKiB: 8})
	algo, h, err := cheap.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("m=%d,t=%d,", 8, 1); !strings.Contains(h, want) {
		t.Fatalf("hash %q does not carry the lowered cost %q", h, want)
	}
	for _, v := range []*Hasher{cheap, New(Options{})} {
		if ok, rehash := v.Verify(algo, h, "pw"); !ok || rehash {
			t.Fatalf("Verify = %v, %v; want true, false", ok, rehash)
		}
	}
	if _, _, err := cheap.Hash(""); err != ErrEmpty {
		t.Fatalf("Hash(\"\") = %v, want ErrEmpty", err)
	}
}
