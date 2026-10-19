// file: internal/database/settings.go
// version: 1.6.2
// guid: 8a7b6c5d-4e3f-2a1b-0c9d-8e7f6a5b4c3d
// last-edited: 2026-10-19

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
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2"
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

// GetAllSettings returns every row under the "setting:" prefix.
//
// It fails rather than returning a partial list. Callers act on what is
// ABSENT from the result: config.LoadConfigFromDatabase falls back to
// defaults (and then the legacy per-key path) when config_blob is missing,
// and SaveConfigToDatabase writes the whole config struct back, so a row that
// silently drops out of this list is overwritten on the next settings save.
// cmd's guardAgainstKeyRegeneration uses the list to decide whether any
// encrypted secret exists before generating a new key, so a dropped secret
// row there means a new key and the loss of every stored secret.
//
// Every writer under the prefix (SetSetting, EmbeddingStore's index flag,
// cmd/pebble-inject-skip) stores the JSON Setting envelope, so a row that does
// not unmarshal is corruption, not another format. All such keys are reported
// together as an *UnreadableSettingsError; the error names keys only, never
// stored values (a value may be a secret). An iterator or close error is
// returned wrapped, not as that type.
func (s *PebbleStore) GetAllSettings() ([]Setting, error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(settingKeyPrefix),
		UpperBound: []byte(settingKeyPrefix + "\xff"),
	})
	if err != nil {
		return nil, err
	}
	return collectSettings(iter)
}

// settingKeyPrefix is the Pebble key prefix of every settings row.
const settingKeyPrefix = "setting:"

// settingsIter is the subset of *GatedIter collectSettings uses; it is
// an interface so a test can inject an iterator error, which a real Pebble
// store does not produce on demand.
type settingsIter interface {
	First() bool
	Valid() bool
	Next() bool
	Key() []byte
	Value() []byte
	Error() error
	Close() error
}

// collectSettings drains iter and closes it. It returns an error if any row
// fails to unmarshal (naming every such key), if the iterator reports an
// error, or if Close does.
func collectSettings(iter settingsIter) (settings []Setting, err error) {
	defer func() {
		if cerr := iter.Close(); cerr != nil && err == nil {
			settings, err = nil, fmt.Errorf("close settings iterator: %w", cerr)
		}
	}()

	var unreadable UnreadableSettingsError
	for iter.First(); iter.Valid(); iter.Next() {
		var setting Setting
		if uerr := json.Unmarshal(iter.Value(), &setting); uerr != nil {
			unreadable.Keys = append(unreadable.Keys, strings.TrimPrefix(string(iter.Key()), settingKeyPrefix))
			// %T, not uerr: a json.SyntaxError's text can quote the stored
			// bytes, and the stored bytes may be an encrypted secret.
			unreadable.causes = append(unreadable.causes, fmt.Sprintf("%T", uerr))
			continue
		}
		settings = append(settings, setting)
	}
	// Checked before the unreadable rows: an iterator error means the list of
	// unreadable keys may itself be incomplete, so it is not reported as one.
	if ierr := iter.Error(); ierr != nil {
		return nil, fmt.Errorf("iterate settings: %w", ierr)
	}
	if len(unreadable.Keys) > 0 {
		return nil, &unreadable
	}
	return settings, nil
}

// UnreadableSettingsError is GetAllSettings' error when one or more rows under
// the settings prefix hold bytes that are not a Setting record. It is distinct
// from an iterator or close error (which wraps the Pebble error instead)
// because the remedy is different: the store itself is readable, and the named
// rows have to be repaired or removed. No startup override helps, because every
// startup path that reads settings (cmd's key-regeneration guard and
// config.LoadConfigFromDatabase) fails on the same rows. Use errors.As.
//
// Keys are the setting names without the "setting:" prefix, in key order. The
// error never carries stored bytes; a value may be an encrypted secret.
type UnreadableSettingsError struct {
	Keys   []string
	causes []string // per-key decode error type (%T), parallel to Keys
}

func (e *UnreadableSettingsError) Error() string {
	parts := make([]string, len(e.Keys))
	for i, k := range e.Keys {
		cause := "unknown"
		if i < len(e.causes) {
			cause = e.causes[i]
		}
		parts[i] = fmt.Sprintf("setting %q: stored row is not a valid setting record (%s)", k, cause)
	}
	return fmt.Sprintf("%d unreadable settings row(s): %s", len(e.Keys), strings.Join(parts, "; "))
}

// QuotedKeys renders Keys for an operator message: each name quoted, comma
// separated.
func (e *UnreadableSettingsError) QuotedKeys() string {
	quoted := make([]string, len(e.Keys))
	for i, k := range e.Keys {
		quoted[i] = strconv.Quote(k)
	}
	return strings.Join(quoted, ", ")
}

// Remedy is the operator instruction shared by every startup path that
// surfaces this error, so the key-regeneration guard and the config loader
// cannot drift into giving different advice for the same rows.
func (e *UnreadableSettingsError) Remedy() string {
	return "startup stops until each named row (Pebble key \"" + settingKeyPrefix +
		"<name>\") is repaired or removed"
}

func (s *PebbleStore) DeleteSetting(key string) error {
	return s.db.Delete([]byte("setting:"+key), nil)
}
