// file: core/auth/password/password_test.go
// version: 1.0.0
// guid: 579aa651-6f40-4fe7-a01e-2758245ff9c6
// last-edited: 2026-11-10

package password

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The password cases of ABO's internal/server/absauth/absauth_test.go, moved
// with the code they test (F4-02).

func cheap() *Hasher { return New(Options{Time: 1, MemoryKiB: 8}) }

func TestPassword_Argon2idRoundTrip(t *testing.T) {
	h := cheap()
	algo, hash, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if algo != AlgoArgon2id || AlgoOf(hash) != AlgoArgon2id {
		t.Fatalf("new passwords must be argon2id, got %q (AlgoOf %q)", algo, AlgoOf(hash))
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash must be PHC-encoded argon2id, got %q", hash)
	}
	ok, rehash := h.Verify(algo, hash, "correct horse battery staple")
	if !ok || rehash {
		t.Fatalf("the correct password: ok=%v rehash=%v, want true, false", ok, rehash)
	}
	if ok, _ := h.Verify(algo, hash, "wrong"); ok {
		t.Fatal("a wrong password must not verify")
	}
}

func TestPassword_Argon2idSaltsDiffer(t *testing.T) {
	h := cheap()
	_, h1, _ := h.Hash("same")
	_, h2, _ := h.Hash("same")
	if h1 == h2 {
		t.Fatal("two hashes of the same password must differ (per-hash salt)")
	}
}

func TestPassword_BcryptVerifiesAndAsksForRehash(t *testing.T) {
	raw, err := bcrypt.GenerateFromPassword([]byte("legacy-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	h := cheap()
	if AlgoOf(string(raw)) != AlgoBcrypt {
		t.Fatalf("AlgoOf(bcrypt) = %q", AlgoOf(string(raw)))
	}
	ok, rehash := h.Verify(AlgoBcrypt, string(raw), "legacy-pw")
	if !ok || !rehash {
		t.Fatalf("an existing bcrypt user: ok=%v rehash=%v, want true, true", ok, rehash)
	}
	if ok, _ := h.Verify(AlgoBcrypt, string(raw), "nope"); ok {
		t.Fatal("wrong password must not verify under bcrypt either")
	}
}

// TestPassword_EmptyAlgoFallsBackToBcrypt covers rows written before HEAD's
// algo column was populated.
func TestPassword_EmptyAlgoFallsBackToBcrypt(t *testing.T) {
	raw, _ := bcrypt.GenerateFromPassword([]byte("legacy-pw"), bcrypt.MinCost)
	ok, rehash := cheap().Verify("", string(raw), "legacy-pw")
	if !ok || !rehash {
		t.Fatalf("empty algo should verify as bcrypt and request rehash (ok=%v rehash=%v)", ok, rehash)
	}
}

// TestPassword_CredentiallessUserCannotLogIn: an account with algo "oauth"
// or an empty hash never authenticates, with any password including "".
func TestPassword_CredentiallessUserCannotLogIn(t *testing.T) {
	h := cheap()
	for _, algo := range []string{AlgoOAuth, "", AlgoArgon2id, AlgoBcrypt} {
		for _, pw := range []string{"", "anything", "$argon2id$"} {
			if ok, _ := h.Verify(algo, "", pw); ok {
				t.Fatalf("an empty password hash must never authenticate (algo=%q pw=%q)", algo, pw)
			}
			if HasCredential(algo, "") {
				t.Fatalf("HasCredential(%q, \"\") = true", algo)
			}
		}
	}
	if ok, _ := h.Verify(AlgoOAuth, "some-non-empty-value", "some-non-empty-value"); ok {
		t.Fatal(`a user whose algo is "oauth" must never authenticate by password`)
	}
	if HasCredential(AlgoOAuth, "x") || HasCredential("scrypt", "x") || !HasCredential(" Argon2id ", "x") || !HasCredential("", "x") {
		t.Fatal("HasCredential classifies oauth/unknown as none and argon2id/bcrypt/empty-algo as a credential")
	}
}

func TestPassword_RejectsMalformedArgonHash(t *testing.T) {
	h := cheap()
	for _, s := range []string{
		"$argon2id$v=19$m=65536,t=3,p=2$notbase64$alsonot",
		"$argon2id$v=19$badparams$c2FsdA$aGFzaA",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=8,t=1,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=0,t=1,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=0,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=0$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=2$$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=2$c2FsdA$",
		"x$argon2id$v=19$m=8,t=1,p=2$c2FsdA$aGFzaA",
		"garbage",
	} {
		if ok, _ := h.Verify(AlgoArgon2id, s, "pw"); ok {
			t.Fatalf("malformed argon2id hash %q must not verify", s)
		}
	}
	if ok, _ := h.Verify("scrypt", "$argon2id$v=19$m=8,t=1,p=2$c2FsdA$aGFzaA", "pw"); ok {
		t.Fatal("an unknown algorithm must never verify")
	}
	if got := AlgoOf("$scrypt$x"); got != "unknown" {
		t.Fatalf("AlgoOf(unknown format) = %q, want unknown", got)
	}
	if got := AlgoOf("  "); got != "" {
		t.Fatalf("AlgoOf(blank) = %q, want empty", got)
	}
}

// A stored hash asking for more than argonMaxMemory is refused before any
// derivation, so one bad row cannot allocate gigabytes on every login.
func TestPassword_RefusesArgonMemoryAboveCap(t *testing.T) {
	var derived int
	h := New(Options{Compute: func(p, s []byte, tm, m uint32, th uint8, k uint32) []byte {
		derived++
		return make([]byte, k)
	}})
	_, stored, err := h.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("m=%d,", argonMemory)
	if !strings.Contains(stored, want) {
		t.Fatalf("hash %q does not carry %q", stored, want)
	}
	derived = 0
	if ok, _ := h.Verify(AlgoArgon2id, strings.Replace(stored, want, fmt.Sprintf("m=%d,", argonMaxMemory+1), 1), "pw"); ok {
		t.Fatal("a hash above the memory cap must not verify")
	}
	if derived != 0 {
		t.Fatalf("a hash above the memory cap must be refused before deriving; derivations = %d", derived)
	}
	if _, _, err := h.Hash("pw"); err != nil || derived != 1 {
		t.Fatalf("the injected Compute is the derivation (derived %d, err %v)", derived, err)
	}
}
