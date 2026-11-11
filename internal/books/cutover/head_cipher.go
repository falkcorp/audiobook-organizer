// file: internal/books/cutover/head_cipher.go
// version: 1.0.0
// guid: 0e37e84c-653e-4581-bfaf-468c7dfaf77e
// last-edited: 2026-11-11

// FROZEN COPY of HEAD (748fd787a) settings row cipher (decrypt half and key search); read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// headKeyFileName is the settings encryption key's file name (HEAD:
// encryptionKeyFileName in internal/database, now internal/books/store).
const headKeyFileName = ".encryption_key"

// headKeyPath returns the key file path inside dir (HEAD: EncryptionKeyPath).
func headKeyPath(dir string) string {
	return filepath.Join(dir, headKeyFileName)
}

// headFindKey returns the first of dirs that holds a key file, or "" if none
// does (HEAD: FindEncryptionKey). An unreadable-but-present key counts as
// found: the stat error is not ErrNotExist.
func headFindKey(dirs ...string) string {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(headKeyPath(dir)); err == nil || !errors.Is(err, os.ErrNotExist) {
			return dir
		}
	}
	return ""
}

// headReadKey reads a key file and requires exactly 32 bytes (HEAD:
// readEncryptionKey). A wrong length is an error, never padded or truncated.
func headReadKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != 32 {
		return nil, fmt.Errorf("invalid encryption key length in %q: got %d bytes, want 32", path, len(data))
	}
	return data, nil
}

// headDecrypt decrypts a stored secret value (HEAD: DecryptValue, with the
// key passed in instead of read from a package global): base64 (standard
// alphabet) of nonce || AES-GCM ciphertext, no additional data.
func headDecrypt(key []byte, encrypted string) (string, error) {
	if key == nil {
		return "", fmt.Errorf("encryption key not initialized")
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
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

// headSetting is the stored row's JSON shape (HEAD: Setting). A settings row
// "setting:<key>" holds this JSON; only Value is encrypted, and only when
// IsSecret is true.
type headSetting struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type"`
	IsSecret bool   `json:"is_secret"`
}

// headSecretRows are the four settings rows HEAD stores encrypted
// (SaveConfigToDatabase's secrets list, and the four LoadConfigFromDatabase
// re-encrypts from the config file when a row fails to decrypt).
func headSecretRows() []string {
	return []string{"openai_api_key", "google_books_api_key", "hardcover_api_token", "basic_auth_password"}
}

// headHasEncryptedValue reports whether any row is a secret with a non-empty
// value, the rows that become unrecoverable if the key is lost (HEAD:
// HasEncryptedSettings). It reads only the flag, never the ciphertext.
func headHasEncryptedValue(rows []headSetting) bool {
	for _, s := range rows {
		if s.IsSecret && s.Value != "" {
			return true
		}
	}
	return false
}
