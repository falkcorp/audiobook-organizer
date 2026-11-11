// file: internal/books/cutover/head_cipher_test.go
// version: 1.0.0
// guid: 1c211f7b-8f9b-48bb-b51e-9b20ab1e64e3
// last-edited: 2026-11-11

package cutover

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// encryptForTest is HEAD's EncryptValue with the key passed in: base64 (std)
// of nonce || AES-GCM(plaintext), nil additional data.
func encryptForTest(key []byte, plaintext string) string {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), nil))
}

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestHeadDecryptRoundTrip(t *testing.T) {
	key := testKey(t)
	for _, plain := range []string{"alfred-pennyworth-fixture", "", "batcave éè password with spaces"} {
		got, err := headDecrypt(key, encryptForTest(key, plain))
		if err != nil || got != plain {
			t.Fatalf("round trip of %q = %q, %v", plain, got, err)
		}
	}
}

func TestHeadDecryptFailures(t *testing.T) {
	key := testKey(t)
	enc := encryptForTest(key, "joker-fixture")
	if _, err := headDecrypt(testKey(t), enc); err == nil {
		t.Fatal("wrong key decrypted")
	}
	if _, err := headDecrypt(key, base64.StdEncoding.EncodeToString([]byte("short"))); err == nil || err.Error() != "ciphertext too short" {
		t.Fatalf("short ciphertext: %v", err)
	}
	if _, err := headDecrypt(key, "not base64 !!"); err == nil {
		t.Fatal("bad base64 decrypted")
	}
	if _, err := headDecrypt(nil, enc); err == nil {
		t.Fatal("nil key decrypted")
	}
	if _, err := headDecrypt([]byte("short key"), enc); err == nil {
		t.Fatal("invalid AES key length decrypted")
	}
	// A flipped bit fails the GCM tag.
	raw, _ := base64.StdEncoding.DecodeString(enc)
	raw[len(raw)-1] ^= 1
	if _, err := headDecrypt(key, base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
	// The plain (URL-safe or unpadded) alphabets are not HEAD's.
	if _, err := headDecrypt(key, base64.RawStdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("unpadded base64 accepted")
	}
}

func TestHeadReadKey(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	key := testKey(t)
	if err := os.WriteFile(good, key, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := headReadKey(good)
	if err != nil || !slices.Equal(got, key) {
		t.Fatalf("headReadKey = %v, %v", got, err)
	}
	for name, size := range map[string]int{"empty": 0, "short": 31, "long": 33, "hex": 64} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := headReadKey(p); err == nil {
			t.Fatalf("%s key (%d bytes) accepted", name, size)
		}
	}
	if _, err := headReadKey(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing key err = %v", err)
	}
}

func TestHeadKeyPath(t *testing.T) {
	if got := headKeyPath("/gotham"); got != "/gotham/.encryption_key" {
		t.Fatalf("headKeyPath = %q", got)
	}
}

func TestHeadFindKeyOrder(t *testing.T) {
	a, b, empty := t.TempDir(), t.TempDir(), t.TempDir()
	if got := headFindKey(a, b); got != "" {
		t.Fatalf("no key anywhere = %q", got)
	}
	if got := headFindKey("", empty, a); got != "" {
		t.Fatalf("empty dir names are skipped, got %q", got)
	}
	if err := os.WriteFile(headKeyPath(b), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := headFindKey(a, b); got != b {
		t.Fatalf("got %q, want %q", got, b)
	}
	if err := os.WriteFile(headKeyPath(a), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := headFindKey(a, b); got != a {
		t.Fatalf("first dir with a key must win: got %q, want %q", got, a)
	}
	if got := headFindKey(b, a); got != b {
		t.Fatalf("order is the argument order: got %q, want %q", got, b)
	}
}

func TestHeadFindKeyUnreadableButPresentCountsAsFound(t *testing.T) {
	dir := t.TempDir()
	// A directory where the key file should be: it exists but cannot be read.
	if err := os.Mkdir(headKeyPath(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := headFindKey(dir); got != dir {
		t.Fatalf("present-but-unreadable key not found: %q", got)
	}
	if _, err := headReadKey(headKeyPath(dir)); err == nil {
		t.Fatal("a directory read as a key")
	}
	// A stat error that is not ErrNotExist (a path through a regular file)
	// also counts as found.
	file := filepath.Join(t.TempDir(), "plainfile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := headFindKey(file); got != file {
		t.Fatalf("non-ErrNotExist stat error not treated as found: %q", got)
	}
}

func TestHeadHasEncryptedValue(t *testing.T) {
	cases := []struct {
		name string
		rows []headSetting
		want bool
	}{
		{"none", nil, false},
		{"plain only", []headSetting{{Key: "root_dir", Value: "/gotham"}}, false},
		{"empty secret", []headSetting{{Key: "openai_api_key", IsSecret: true}}, false},
		{"secret", []headSetting{{Key: "root_dir", Value: "/gotham"}, {Key: "openai_api_key", Value: "ciphertext", IsSecret: true}}, true},
	}
	for _, c := range cases {
		if got := headHasEncryptedValue(c.rows); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestHeadSecretRows(t *testing.T) {
	want := []string{"openai_api_key", "google_books_api_key", "hardcover_api_token", "basic_auth_password"}
	if got := headSecretRows(); !slices.Equal(got, want) {
		t.Fatalf("headSecretRows = %v", got)
	}
}
