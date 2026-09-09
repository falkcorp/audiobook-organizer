// file: internal/database/settings.go
// version: 1.4.0
// guid: 8a7b6c5d-4e3f-2a1b-0c9d-8e7f6a5b4c3d
// last-edited: 2026-09-09

package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/crypto/argon2"
)

// ErrSettingNotFound is returned (wrapped) by GetSetting when the requested key
// does not exist. Callers that must distinguish "missing key" from a real
// backend error should use errors.Is(err, ErrSettingNotFound) rather than the
// error string. Pen-test finding HIGH-4a: a missing key previously surfaced as a
// generic error, causing the bootstrap exchange to return 500 instead of 401
// once the one-time token had been consumed.
var ErrSettingNotFound = errors.New("setting not found")

// Setting represents a stored configuration setting
type Setting struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type"`      // "string", "int", "bool", "json"
	IsSecret bool   `json:"is_secret"` // If true, value is encrypted
}

// Encryption key derivation and storage
var encryptionKey []byte

// encryptionKeyFileName is the one place this filename is spelled. It is shared
// by InitEncryption and FindEncryptionKey so the loader and the "are we about to
// generate?" probe can never disagree about what they are looking for.
const encryptionKeyFileName = ".encryption_key"

// EncryptionKeyPath returns the key file path inside dir.
func EncryptionKeyPath(dir string) string {
	return filepath.Join(dir, encryptionKeyFileName)
}

// FindEncryptionKey returns the first of dirs that holds a key file, or "" if
// none does. It is a plain existence probe for callers that need to know
// whether InitEncryption is about to GENERATE rather than load -- generating is
// the destructive case, and the caller is the only one holding the store needed
// to check whether there are secrets to destroy.
//
// An unreadable-but-present key counts as found here, deliberately: the point is
// "do not let a new key be generated", and InitEncryption will refuse that path
// with a precise error of its own.
func FindEncryptionKey(dirs ...string) string {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(EncryptionKeyPath(dir)); err == nil || !errors.Is(err, os.ErrNotExist) {
			return dir
		}
	}
	return ""
}

// InitEncryption loads the settings encryption key from dataDir, falling back
// to legacyDirs, and generates a new one only when no key exists anywhere.
//
// GENERATING A KEY IS DESTRUCTIVE when encrypted settings already exist.
// LoadConfigFromDatabase tries to decrypt every IsSecret row; on failure it
// re-encrypts the four secrets it can recover from the config file
// (openai_api_key, google_books_api_key, hardcover_api_token,
// basic_auth_password) and calls DeleteSetting on every other one. So a key
// that silently regenerates does not surface as an error -- it surfaces weeks
// later as "why do I have to re-enter that credential again".
//
// Three branches, and the two that are NOT the happy path are the point:
//
//   - A read error that is not "does not exist" -- EIO, a key file that is
//     writable but not readable, a directory where the file should be -- is
//     FATAL. The previous code treated every read error identically and fell
//     through to generate, which ends in os.WriteFile, and os.WriteFile
//     TRUNCATES. Whether that actually destroyed the key depended on the
//     failure mode: EISDIR or a file we could not write failed a second time
//     and surfaced as "failed to save encryption key", but a key that was
//     writable-and-unreadable (mode 0200, or a transient EIO) was silently
//     replaced, and THAT is unrecoverable. Distinguishing the two is not worth
//     it -- never write over a key we failed to read, whatever the reason.
//   - legacyDirs exist because the credential directory became a constant on
//     2026-09-09 while production's key was still sitting next to the database
//     at <root_dir>/.appdata/.encryption_key. Reading it there keeps that
//     install working across the upgrade instead of quietly rotating its
//     secrets. The key is NOT copied to dataDir: two files holding the same
//     secret is a second thing to leak and a divergence waiting to happen, and
//     a copy made here would be made by whatever user the service runs as, at
//     whatever umask. The log line tells the operator to move it.
//
// The key is validated BEFORE it is installed in the package global, so a
// truncated or padded file fails loudly rather than half-loading.
func InitEncryption(dataDir string, legacyDirs ...string) error {
	keyPath := EncryptionKeyPath(dataDir)

	switch key, err := readEncryptionKey(keyPath); {
	case err == nil:
		encryptionKey = key
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("encryption key %q exists but could not be read (refusing to "+
			"generate a replacement, which would overwrite it and make every "+
			"encrypted setting unrecoverable): %w", keyPath, err)
	}

	for _, dir := range legacyDirs {
		if dir == "" || filepath.Clean(dir) == filepath.Clean(dataDir) {
			continue
		}
		legacyPath := EncryptionKeyPath(dir)
		switch key, err := readEncryptionKey(legacyPath); {
		case err == nil:
			encryptionKey = key
			slog.Warn("settings encryption key found at its OLD location — move it",
				"found_at", legacyPath, "expected_at", keyPath,
				"action", "mv the file, then restart; until then this install depends on the old path")
			return nil
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("legacy encryption key %q could not be read (refusing to "+
				"continue, because generating a new key would discard the secrets it "+
				"protects): %w", legacyPath, err)
		}
	}

	// No key anywhere. Either a first run, or a key that has gone missing --
	// and those two look identical from here, which is why the caller is
	// expected to have checked for existing encrypted settings first.
	key := make([]byte, 32) // AES-256
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("failed to generate encryption key: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return fmt.Errorf("failed to save encryption key: %w", err)
	}
	encryptionKey = key
	slog.Info("generated a new settings encryption key", "key_file", keyPath)
	return nil
}

// readEncryptionKey reads and validates a key file without touching global
// state. A wrong length is reported as a real error rather than being padded
// or truncated into something AES will silently accept.
func readEncryptionKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != 32 {
		return nil, fmt.Errorf("invalid encryption key length in %q: got %d bytes, want 32", path, len(data))
	}
	return data, nil
}

// HasEncryptedSettings reports whether the store holds any secret whose value
// would become unrecoverable if the encryption key were replaced.
//
// It reads only the IsSecret flag, never the ciphertext, so it works before a
// key is available: SetSetting marshals the whole Setting struct to JSON and
// encrypts ONLY the Value field, leaving IsSecret legible in plaintext. If that
// ever changes to whole-record encryption this check becomes circular and must
// be replaced with a sentinel, not deleted.
func HasEncryptedSettings(settings []Setting) bool {
	for _, s := range settings {
		if s.IsSecret && s.Value != "" {
			return true
		}
	}
	return false
}

// argon2idParams holds the Argon2id tuning parameters used by DeriveKeyFromPassword.
// These values follow the OWASP recommendation for interactive login scenarios
// (time=1, memory=64MiB, threads=4) while producing a 32-byte key suitable
// for AES-256.
//
// Security note: DeriveKeyFromPassword should only be used as a fallback KDF
// when a pre-generated random key is not available. The recommended path is
// InitEncryption with a random key stored in a file with mode 0600.
const (
	argon2idTime    = 1
	argon2idMemory  = 64 * 1024 // 64 MiB
	argon2idThreads = 4
	argon2idKeyLen  = 32 // AES-256
	argon2idSaltLen = 16
)

// appKDFSalt is a fixed, application-specific salt for DeriveKeyFromPassword.
// Fixed so the same password always derives the same AES-256 key, enabling
// decrypt of previously encrypted data. Still prevents cross-application
// rainbow tables. Use DeriveKeyFromPasswordWithSalt for per-credential salts.
var appKDFSalt = []byte("audiobook-org-v1") // 16 bytes = argon2idSaltLen

// DeriveKeyFromPassword derives a 32-byte AES key from a password using
// Argon2id, which is resistant to brute-force and side-channel attacks.
//
// Uses a fixed application salt so the derivation is deterministic: the same
// password always produces the same AES-256 key, enabling decryption of
// previously encrypted data. This prevents cross-application rainbow tables
// while remaining reproducible across process restarts.
//
// Use DeriveKeyFromPasswordWithSalt when a per-credential random salt is
// needed (e.g. storing hashed credentials where you control the salt storage).
//
// Replaces the previous implementation that used plain SHA-256 (no salt,
// no work factor) — see security alert #132.
func DeriveKeyFromPassword(password string) []byte {
	return argon2.IDKey([]byte(password), appKDFSalt, argon2idTime, argon2idMemory, argon2idThreads, argon2idKeyLen)
}

// DeriveKeyFromPasswordWithSalt derives a 32-byte AES key from a password and
// a caller-supplied salt using Argon2id. The salt must be exactly argon2idSaltLen
// bytes. This function is deterministic: the same (password, salt) pair always
// produces the same key, enabling decryption of data encrypted with a previous
// call to DeriveKeyFromPassword.
//
// Usage pattern:
//
//	salt, key := GenerateArgon2Salt(), DeriveKeyFromPasswordWithSalt(password, salt)
//	// persist hex.EncodeToString(salt) alongside the encrypted payload
func DeriveKeyFromPasswordWithSalt(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, argon2idTime, argon2idMemory, argon2idThreads, argon2idKeyLen)
}

// GenerateArgon2Salt returns a cryptographically random salt for use with
// DeriveKeyFromPasswordWithSalt. Returns an error only when the OS RNG fails.
func GenerateArgon2Salt() ([]byte, error) {
	salt := make([]byte, argon2idSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate argon2 salt: %w", err)
	}
	return salt, nil
}

// EncryptValue encrypts a plaintext value
func EncryptValue(plaintext string) (string, error) {
	if encryptionKey == nil {
		return "", fmt.Errorf("encryption key not initialized")
	}

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptValue decrypts an encrypted value
func DecryptValue(encrypted string) (string, error) {
	if encryptionKey == nil {
		return "", fmt.Errorf("encryption key not initialized")
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// MaskSecret returns a masked version of a secret (for display)
func MaskSecret(secret string) string {
	if len(secret) < 8 {
		return "****"
	}
	return secret[:3] + "****" + secret[len(secret)-4:]
}

// PebbleDB implementation
func (s *PebbleStore) GetSetting(key string) (*Setting, error) {
	data, closer, err := s.db.Get([]byte("setting:" + key))
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, fmt.Errorf("setting not found: %s: %w", key, ErrSettingNotFound)
		}
		return nil, err
	}
	defer closer.Close()

	var setting Setting
	if err := json.Unmarshal(data, &setting); err != nil {
		return nil, err
	}

	return &setting, nil
}

func (s *PebbleStore) SetSetting(key, value, typ string, isSecret bool) error {
	// Encrypt if secret
	storedValue := value
	if isSecret && value != "" {
		encrypted, err := EncryptValue(value)
		if err != nil {
			return fmt.Errorf("encryption failed: %w", err)
		}
		storedValue = encrypted
	}

	setting := Setting{
		Key:      key,
		Value:    storedValue,
		Type:     typ,
		IsSecret: isSecret,
	}

	data, err := json.Marshal(setting)
	if err != nil {
		return err
	}

	return s.db.Set([]byte("setting:"+key), data, nil)
}

func (s *PebbleStore) GetAllSettings() ([]Setting, error) {
	var settings []Setting

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("setting:"),
		UpperBound: []byte("setting:\xff"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var setting Setting
		if err := json.Unmarshal(iter.Value(), &setting); err != nil {
			continue
		}

		settings = append(settings, setting)
	}

	return settings, nil
}

func (s *PebbleStore) DeleteSetting(key string) error {
	return s.db.Delete([]byte("setting:"+key), nil)
}

// SQLite implementation

// Helper functions to get decrypted setting value from stores
func GetDecryptedSetting(store Store, key string) (string, error) {
	setting, err := store.GetSetting(key)
	if err != nil {
		return "", err
	}

	if !setting.IsSecret {
		return setting.Value, nil
	}

	return DecryptValue(setting.Value)
}
