// file: internal/server/absauth/hash_cost_guard_test.go
// version: 2.0.0
// guid: 6e0b7d24-91c3-4f8a-b5d6-3a28c4f1e907
// last-edited: 2026-10-30

package absauth

import (
	"strings"
	"testing"
)

// The static guard that keeps non-test code from naming SetHashCostForTest
// lives in internal/arch (TestHashCostSeamStaysInTests), which loads the whole
// module, the root main package included. These tests cover the function's own
// runtime guards and the cost it writes.

func requirePanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one containing %q", want)
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Fatalf("panic %v, want one containing %q", r, want)
		}
	}()
	f()
}

func TestSetHashCostForTestPanicsOutsideATestBinary(t *testing.T) {
	oldFn, oldT, oldM := isTestBinary, hashTime, hashMemory
	t.Cleanup(func() { isTestBinary, hashTime, hashMemory = oldFn, oldT, oldM })

	isTestBinary = func() bool { return false }
	requirePanic(t, "outside a test binary", func() { SetHashCostForTest(1, 64) })
	if hashTime != oldT || hashMemory != oldM {
		t.Fatalf("the refused call changed the cost to t=%d m=%d", hashTime, hashMemory)
	}
}

func TestSetHashCostForTestPanicsOnceTheDummyHashIsBuilt(t *testing.T) {
	oldT, oldM := hashTime, hashMemory
	t.Cleanup(func() { hashTime, hashMemory = oldT, oldM })

	WarmLoginDummy() // builds it at whatever cost is current; sticks for the process
	if !LoginDummyReadyForTest() {
		t.Fatal("the dummy hash was not built")
	}
	requirePanic(t, "after the dummy hash was built", func() { SetHashCostForTest(1, 64) })
	if hashTime != oldT || hashMemory != oldM {
		t.Fatalf("the refused call changed the cost to t=%d m=%d", hashTime, hashMemory)
	}
}

func TestLoweredHashCostChangesWhatIsWrittenAndStillVerifies(t *testing.T) {
	oldT, oldM := hashTime, hashMemory
	t.Cleanup(func() { hashTime, hashMemory = oldT, oldM })

	// Not through SetHashCostForTest: another test in this binary may already
	// have built the dummy hash, which that function refuses.
	hashTime, hashMemory = 1, 64
	algo, hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if algo != AlgoArgon2id || !strings.Contains(hash, "m=64,t=1,p=2$") {
		t.Fatalf("hash %q not written at the lowered cost", hash)
	}
	if ok, _ := VerifyPassword(algo, hash, "correct horse"); !ok {
		t.Fatal("a hash written at the lowered cost must verify")
	}
	if ok, _ := VerifyPassword(algo, hash, "wrong horse"); ok {
		t.Fatal("a wrong password must still fail at the lowered cost")
	}
}
