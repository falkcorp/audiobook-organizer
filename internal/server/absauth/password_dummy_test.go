// file: internal/server/absauth/password_dummy_test.go
// version: 1.0.0
// guid: 5cd17e42-c363-4a36-b03d-c2556ce8cef7
// last-edited: 2026-10-22

package absauth

import (
	"sync/atomic"
	"testing"
)

// countArgon swaps argonCompute for a counting wrapper around the real one.
func countArgon(t *testing.T) *atomic.Int32 {
	t.Helper()
	orig := argonCompute
	var calls atomic.Int32
	argonCompute = func(password, salt []byte, time_, memory uint32, threads uint8, keyLen uint32) []byte {
		calls.Add(1)
		return orig(password, salt, time_, memory, threads, keyLen)
	}
	t.Cleanup(func() { argonCompute = orig })
	return &calls
}

// S3: a login for a name with no usable password credential spends exactly one
// argon2id derivation, the same as a wrong password for a real argon2id account,
// so response time does not tell the two apart.
func TestVerifyLoginPassword_EveryNonSuccessSpendsOneDerivation(t *testing.T) {
	_, real, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	WarmLoginDummy() // built before counting, as the handlers' constructors do
	calls := countArgon(t)

	cases := []struct {
		name       string
		algo, hash string
		plain      string
	}{
		{"wrong password, real argon2id account", AlgoArgon2id, real, "wrong"},
		{"no hash at all", AlgoArgon2id, "", "wrong"},
		{"blank hash", AlgoArgon2id, "   ", "wrong"},
		{"oauth user", AlgoOAuth, real, "wrong"},
		{"unknown algorithm", "scrypt", real, "wrong"},
	}
	for _, tc := range cases {
		calls.Store(0)
		ok, rehash := VerifyLoginPassword(tc.algo, tc.hash, tc.plain)
		if ok || rehash {
			t.Fatalf("%s: ok=%v rehash=%v", tc.name, ok, rehash)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("%s: %d argon2id derivations, want 1", tc.name, got)
		}
	}

	calls.Store(0)
	SpendVerification("anything")
	if got := calls.Load(); got != 1 {
		t.Errorf("SpendVerification: %d derivations, want 1", got)
	}

	// The right password for a real account still succeeds, with one derivation.
	calls.Store(0)
	if ok, _ := VerifyLoginPassword(AlgoArgon2id, real, "correct horse"); !ok || calls.Load() != 1 {
		t.Errorf("real account: ok=%v derivations=%d", ok, calls.Load())
	}
}

// The dummy is built once, with the production parameters.
func TestDummyHashUsesProductionParametersAndIsBuiltOnce(t *testing.T) {
	WarmLoginDummy()
	first := dummyArgon2idHash()
	if first == "" {
		t.Fatal("no dummy hash")
	}
	want := "$argon2id$v=19$m=65536,t=3,p=2$"
	if len(first) < len(want) || first[:len(want)] != want {
		t.Fatalf("dummy hash %q does not carry the production parameters %q", first, want)
	}
	if again := dummyArgon2idHash(); again != first {
		t.Fatal("the dummy hash was rebuilt")
	}
	// It cannot be satisfied by an ordinary password.
	if verifyArgon2id(first, "") || verifyArgon2id(first, "password") {
		t.Fatal("the dummy hash verified a guess")
	}
}

func TestRefreshKey_IsAKeyOfItsOwn(t *testing.T) {
	if RefreshKey("") != "" {
		t.Fatal("an empty source stays empty")
	}
	if k := RefreshKey("192.0.2.1"); k == "192.0.2.1" || k == "" {
		t.Fatalf("refresh key %q must differ from the source key", k)
	}
	th := NewThrottle()
	for range MaxFailuresPerIP {
		th.RecordFailure("", RefreshKey("192.0.2.1"))
	}
	if !th.IPBlocked(RefreshKey("192.0.2.1")) || th.IPBlocked("192.0.2.1") {
		t.Fatal("refresh failures spend the refresh budget only")
	}
}
