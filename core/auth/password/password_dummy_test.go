// file: core/auth/password/password_dummy_test.go
// version: 2.1.0
// guid: 5cd17e42-c363-4a36-b03d-c2556ce8cef7
// last-edited: 2026-11-10

package password

import (
	"fmt"
	"strings"
	"testing"
)

// S3 (§3.2 equal timing): a login for a name with no usable password
// credential spends exactly one argon2id derivation of the same cost as a
// wrong password for a real account, so response time does not tell the two
// apart.
func TestVerifyLogin_EveryNonSuccessSpendsOneDerivation(t *testing.T) {
	h := New(Options{Time: 1, MemoryKiB: 16})
	_, real, err := h.Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	work := func(f func()) (n, cost uint64) {
		n0, c0 := h.Derivations()
		f()
		n1, c1 := h.Derivations()
		return n1 - n0, c1 - c0
	}
	wantN, wantCost := work(func() { h.VerifyLogin(AlgoArgon2id, real, "wrong") })
	if wantN != 1 || wantCost == 0 {
		t.Fatalf("a wrong password costs %d derivations (%d KiB-passes), want 1", wantN, wantCost)
	}
	for _, tc := range []struct {
		name, algo, hash string
	}{
		{"no hash at all", AlgoArgon2id, ""},
		{"blank hash", AlgoArgon2id, "   "},
		{"oauth user", AlgoOAuth, real},
		{"unknown algorithm", "scrypt", real},
	} {
		var ok, rehash bool
		n, cost := work(func() { ok, rehash = h.VerifyLogin(tc.algo, tc.hash, "wrong") })
		if ok || rehash {
			t.Fatalf("%s: ok=%v rehash=%v", tc.name, ok, rehash)
		}
		if n != wantN || cost != wantCost {
			t.Errorf("%s: %d derivations (%d KiB-passes), want %d (%d)", tc.name, n, cost, wantN, wantCost)
		}
	}
	if n, cost := work(func() { h.SpendVerification("anything") }); n != 1 || cost != wantCost {
		t.Errorf("SpendVerification: %d derivations (%d KiB-passes), want 1 (%d)", n, cost, wantCost)
	}
	var ok bool
	if n, _ := work(func() { ok, _ = h.VerifyLogin(AlgoArgon2id, real, "correct horse") }); !ok || n != 1 {
		t.Errorf("real account: ok=%v derivations=%d", ok, n)
	}
}

// The dummy carries the cost this Hasher writes, is fixed for its life, and
// no guess satisfies it.
func TestDummyHashCarriesTheWriteCostAndIsFixed(t *testing.T) {
	for _, o := range []Options{{}, {Time: 2, MemoryKiB: 32}} {
		h := New(o)
		want := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=2$", h.memory, h.time)
		if !strings.HasPrefix(h.dummy, want) {
			t.Fatalf("dummy hash %q does not carry the write cost %q", h.dummy, want)
		}
	}
	h := New(Options{Time: 1, MemoryKiB: 8})
	first := h.dummy
	for _, guess := range []string{"", "password"} {
		if ok, _ := h.Verify(AlgoArgon2id, first, guess); ok {
			t.Fatalf("the dummy hash verified %q", guess)
		}
	}
	h.SpendVerification("x")
	if h.dummy != first {
		t.Fatal("the dummy hash changed")
	}
	if New(Options{Time: 1, MemoryKiB: 8}).dummy == first {
		t.Fatal("two Hashers share a dummy: its salt and key are not random")
	}
}

// The first login with no usable credential on a fresh Hasher, with nothing
// warmed, costs exactly one derivation at the write cost: building the dummy
// derives nothing, so no first caller pays for it.
func TestFirstLoginWithoutACredentialCostsOneDerivation(t *testing.T) {
	h := New(Options{Time: 1, MemoryKiB: 16})
	if n, _ := h.Derivations(); n != 0 {
		t.Fatalf("New ran %d derivations, want 0", n)
	}
	if ok, _ := h.VerifyLogin(AlgoOAuth, "", "anything"); ok {
		t.Fatal("a login with no credential authenticated")
	}
	if n, cost := h.Derivations(); n != 1 || cost != 16 {
		t.Fatalf("first no-credential login: %d derivations (%d KiB-passes), want 1 (16)", n, cost)
	}
}
