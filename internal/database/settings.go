// file: internal/database/settings.go
// version: 1.3.0
// guid: 8a7b6c5d-4e3f-2a1b-0c9d-8e7f6a5b4c3d

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

// InitEncryption initializes or loads the encryption key
func InitEncryption(dataDir string) error {
	keyPath := filepath.Join(dataDir, ".encryption_key")

	// Try to load existing key
	if data, err := os.ReadFile(keyPath); err == nil {
		encryptionKey = data
		if len(encryptionKey) != 32 {
			return fmt.Errorf("invalid encryption key length: %d", len(encryptionKey))
		}
		return nil
	}

	// Generate new key
	encryptionKey = make([]byte, 32) // AES-256
	if _, err := io.ReadFull(rand.Reader, encryptionKey); err != nil {
		return fmt.Errorf("failed to generate encryption key: %w", err)
	}

	// Save key with restrictive permissions
	if err := os.WriteFile(keyPath, encryptionKey, 0600); err != nil {
		return fmt.Errorf("failed to save encryption key: %w", err)
	}

	return nil
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
