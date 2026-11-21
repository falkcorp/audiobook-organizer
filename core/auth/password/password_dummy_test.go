// file: core/auth/password/password_dummy_test.go
// version: 2.1.0
// guid: 5cd17e42-c363-4a36-b03d-c2556ce8cef7
// last-edited: 2026-11-10

package password

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// loginWork is the password work one call spent: argon2id derivations and
// their KiB-passes, bcrypt comparisons and their rounds.
type loginWork struct{ derivations, kibPasses, compares, rounds uint64 }

func workOf(h *Hasher, f func()) loginWork {
	n0, c0 := h.Derivations()
	b0, r0 := h.BcryptCompares()
	f()
	n1, c1 := h.Derivations()
	b1, r1 := h.BcryptCompares()
	return loginWork{n1 - n0, c1 - c0, b1 - b0, r1 - r0}
}

// S3 (§3.2 equal timing): every login verification spends exactly one
// argon2id derivation at the write cost and one bcrypt comparison at the
// dummy's cost, whatever the account holds and whether or not the password
// is right: a real argon2id account, a legacy bcrypt account, a name with no
// usable credential, and a stored hash that does not parse (which would
// otherwise answer in microseconds) all cost the same, so response time does
// not tell them apart.
func TestVerifyLogin_EveryShapeSpendsTheSameWork(t *testing.T) {
	h := New(Options{Time: 1, MemoryKiB: 16, BcryptCost: bcrypt.MinCost})
	_, real, err := h.Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	want := loginWork{derivations: 1, kibPasses: 16, compares: 1, rounds: 1 << bcrypt.MinCost}
	for _, tc := range []struct {
		name, algo, hash, plain string
		ok, rehash              bool
	}{
		{"argon2id, wrong password", AlgoArgon2id, real, "wrong", false, false},
		{"argon2id, right password", AlgoArgon2id, real, "correct horse", true, false},
		{"bcrypt, wrong password", AlgoBcrypt, string(legacy), "wrong", false, false},
		{"bcrypt, right password", AlgoBcrypt, string(legacy), "correct horse", true, true},
		{"empty algo (bcrypt), wrong password", "", string(legacy), "wrong", false, false},
		{"no hash at all", AlgoArgon2id, "", "wrong", false, false},
		{"blank hash", AlgoArgon2id, "   ", "wrong", false, false},
		{"oauth user", AlgoOAuth, real, "wrong", false, false},
		{"unknown algorithm", "scrypt", real, "wrong", false, false},
		{"argon2id that does not parse", AlgoArgon2id, "$argon2id$v=19$m=16,t=1,p=2$!!!$!!!", "wrong", false, false},
		{"argon2id above the memory cap", AlgoArgon2id, strings.Replace(real, "m=16,", "m=999999999,", 1), "correct horse", false, false},
		{"argon2id label on a bcrypt hash", AlgoArgon2id, string(legacy), "correct horse", false, false},
		{"bcrypt label on an argon2id hash", AlgoBcrypt, real, "correct horse", false, false},
		{"bcrypt too short", AlgoBcrypt, string(legacy[:40]), "correct horse", false, false},
		{"bcrypt salt outside the alphabet", AlgoBcrypt, string(legacy[:7]) + "!!" + string(legacy[9:]), "correct horse", false, false},
		{"bcrypt cost out of range", AlgoBcrypt, "$2a$99$" + string(legacy[7:]), "correct horse", false, false},
	} {
		var ok, rehash bool
		got := workOf(h, func() { ok, rehash = h.VerifyLogin(tc.algo, tc.hash, tc.plain) })
		if ok != tc.ok || rehash != tc.rehash {
			t.Errorf("%s: ok=%v rehash=%v, want %v %v", tc.name, ok, rehash, tc.ok, tc.rehash)
		}
		if got != want {
			t.Errorf("%s: spent %+v, want %+v", tc.name, got, want)
		}
	}
	if got := workOf(h, func() { h.SpendVerification("anything") }); got != want {
		t.Errorf("SpendVerification: spent %+v, want %+v", got, want)
	}
}

// The bcrypt dummy is the shape x/crypto writes, at the configured cost, and
// comparing against it runs the full key schedule: x/crypto answers
// ErrMismatchedHashAndPassword only after the work, and returns a parse error
// (no work) for anything it cannot use.
func TestDummyBcryptIsAFullCostComparison(t *testing.T) {
	for _, cost := range []int{0, bcrypt.MinCost, 6} {
		h := New(Options{Time: 1, MemoryKiB: 8, BcryptCost: cost})
		wantCost := cost
		if cost == 0 {
			wantCost = bcrypt.DefaultCost
		}
		if !bcryptUsable(h.dummyBcrypt) {
			t.Fatalf("cost %d: dummy %q is not a usable bcrypt hash", cost, h.dummyBcrypt)
		}
		if got, err := bcrypt.Cost([]byte(h.dummyBcrypt)); err != nil || got != wantCost {
			t.Fatalf("cost %d: dummy cost = %d, %v; want %d", cost, got, err, wantCost)
		}
		if cost == 0 {
			continue // a DefaultCost comparison is slow; its shape is what matters
		}
		for _, guess := range []string{"", "password"} {
			if err := bcrypt.CompareHashAndPassword([]byte(h.dummyBcrypt), []byte(guess)); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
				t.Fatalf("cost %d: comparing %q against the dummy = %v, want a mismatch after the full work", cost, guess, err)
			}
		}
	}
	a := New(Options{Time: 1, MemoryKiB: 8, BcryptCost: bcrypt.MinCost})
	b := New(Options{Time: 1, MemoryKiB: 8, BcryptCost: bcrypt.MinCost})
	if a.dummyBcrypt == b.dummyBcrypt {
		t.Fatal("two Hashers share a bcrypt dummy: it is not random")
	}
	for _, bad := range []int{-1, 3, 32} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("BcryptCost %d: New accepted a cost bcrypt cannot run", bad)
				}
			}()
			New(Options{BcryptCost: bad})
		}()
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
	h := New(Options{Time: 1, MemoryKiB: 16, BcryptCost: bcrypt.MinCost})
	if n, _ := h.Derivations(); n != 0 {
		t.Fatalf("New ran %d derivations, want 0", n)
	}
	if ok, _ := h.VerifyLogin(AlgoOAuth, "", "anything"); ok {
		t.Fatal("a login with no credential authenticated")
	}
	if n, cost := h.Derivations(); n != 1 || cost != 16 {
		t.Fatalf("first no-credential login: %d derivations (%d KiB-passes), want 1 (16)", n, cost)
	}
	if n, rounds := h.BcryptCompares(); n != 1 || rounds != 1<<bcrypt.MinCost {
		t.Fatalf("first no-credential login: %d bcrypt comparisons (%d rounds), want 1 (%d)", n, rounds, 1<<bcrypt.MinCost)
	}
}
