// file: core/secrets/record_test.go
// version: 1.0.0
// guid: 0569972f-8a31-42a7-b98b-ce2f204433c3
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The known-answer vector in testdata/kat.keyring and testdata/kat.record.
// Both were written by a stand-alone program using only the standard library
// from 05-config §2.3's constants, never by this package: k1 is the bytes
// 0x00..0x1f, d1 is 0x20..0x3f, the record's nonce is 0x40..0x4b, the data
// subkey is HKDF-SHA256(k1, no salt, "mediacore/secrets/aes-256-gcm/v1"), the
// AAD is "mediacore/secrets/v1\x00gotham\x00core.http.basic_gate.password",
// the fingerprint is the first 8 hex of HMAC-SHA256(HKDF-SHA256(d1, no salt,
// "mediacore/secrets/fingerprint/v1"), value), and DeriveKey(info) is
// HKDF-SHA256(d1, no salt, info).
const (
	katFingerprint = "dc4041a4"
	katDeriveInfo  = "core.auth/state/v1"
	katDerivedHex  = "ae7b1f7fd580c98b03cecc46ccaa6e0100c3e32f6c89aef2f1c452dabaf84285"
)

// TestKnownAnswer_KeyringAndRecord (a test name coined by F3-03): the
// package opens the fixed keyring, decrypts the fixed record to the known
// value, and gives the known fingerprint and derived key. It pins the HKDF
// info strings, the AAD layout (separators included) and the record format
// against an implementation that is not this package's.
func TestKnownAnswer_KeyringAndRecord(t *testing.T) {
	e := newEnv(t)
	kr, err := os.ReadFile(filepath.Join("testdata", "kat.keyring"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.keyringPath(), kr, 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := os.ReadFile(filepath.Join("testdata", "kat.record"))
	if err != nil {
		t.Fatal(err)
	}
	rawSet(t, e.ks, recordKey(nameGate), bytes.TrimSpace(rec))
	s := e.open(t, nil)
	v, m, err := s.Get(context.Background(), nameGate)
	if err != nil {
		t.Fatalf("Get of the known-answer record = %v", err)
	}
	if v.Reveal() != valueVengeance {
		t.Fatal("the known-answer record decrypted to another value")
	}
	if m.Fingerprint != katFingerprint || m.KeyID != "k1" {
		t.Fatalf("Meta = fingerprint %q kid %q, want %q k1", m.Fingerprint, m.KeyID, katFingerprint)
	}
	d, err := s.DeriveKey(katDeriveInfo)
	if err != nil || hex.EncodeToString(d) != katDerivedHex {
		t.Fatalf("DeriveKey(%q) = %x, %v; want the known answer", katDeriveInfo, d, err)
	}
}

// TestSeal_FreshNonceEveryTime (a test name coined by F3-03): sealing the
// same value under the same name twice gives two random nonces and two
// ciphertexts; a constant nonce would reuse the GCM keystream.
func TestSeal_FreshNonceEveryTime(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	r1, err := s.ring.seal(testAppID, nameGate, []byte(valueVengeance), e.clock.now(), byAlfred)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.ring.seal(testAppID, nameGate, []byte(valueVengeance), e.clock.now(), byAlfred)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(r1.Nonce, r2.Nonce) || bytes.Equal(r1.CT, r2.CT) {
		t.Fatal("two seals of one value share a nonce or a ciphertext")
	}
	if bytes.Equal(r1.Nonce, make([]byte, len(r1.Nonce))) {
		t.Fatal("a seal used the all-zero nonce")
	}
	for _, r := range []*record{r1, r2} {
		pt, err := s.ring.open(testAppID, nameGate, r)
		if err != nil || string(pt) != valueVengeance {
			t.Fatalf("a sealed record does not open: %v", err)
		}
	}
}

// TestOpen_BadNonceLengthIsCorrupt (a test name coined by F3-03): a stored
// record whose nonce is shorter or longer than GCM's reads as ErrCorrupt
// (Get's error and Meta.Err); it never reaches the cipher, which panics on
// a wrong nonce length.
func TestOpen_BadNonceLengthIsCorrupt(t *testing.T) {
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	raw, err := e.ks.Get(context.Background(), recordKey(nameGate))
	if err != nil {
		t.Fatal(err)
	}
	good, err := decodeRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	for name, nonce := range map[string][]byte{
		"short": good.Nonce[:4],
		"long":  append(append([]byte(nil), good.Nonce...), 0xb4, 0xb4),
	} {
		bad := *good
		bad.Nonce = nonce
		enc, err := encodeRecord(&bad)
		if err != nil {
			t.Fatal(err)
		}
		rawSet(t, e.ks, recordKey(nameGate), enc)
		_, m, err := s.Get(context.Background(), nameGate)
		if !errors.Is(err, ErrCorrupt) || !errors.Is(m.Err, ErrCorrupt) {
			t.Errorf("%s nonce: Get = %v (Meta.Err %v), want ErrCorrupt", name, err, m.Err)
		}
	}
}
