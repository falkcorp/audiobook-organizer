// file: internal/books/deferred_settings_test.go
// version: 1.1.0
// guid: 9a4d2c71-6e3b-4f58-b1a0-7c5e8d2f9b36
// last-edited: 2026-11-08

package books

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
)

// memSettings is a settings store in memory that counts the writes it
// takes, the way PebbleStore stores rows: a secret's value encrypted.
type memSettings struct {
	rows    map[string]database.Setting
	writes  int
	failKey string // a SetSetting of this key fails
	allErr  error  // GetAllSettings fails with it
}

func newMemSettings(rows ...database.Setting) *memSettings {
	m := &memSettings{rows: map[string]database.Setting{}}
	for _, r := range rows {
		m.rows[r.Key] = r
	}
	return m
}

func (m *memSettings) GetSetting(key string) (*database.Setting, error) {
	r, ok := m.rows[key]
	if !ok {
		return nil, fmt.Errorf("setting not found: %s: %w", key, database.ErrSettingNotFound)
	}
	return &r, nil
}

func (m *memSettings) SetSetting(key, value, typ string, isSecret bool) error {
	if key == m.failKey {
		return errors.New("Bane broke the write")
	}
	m.writes++
	if isSecret && value != "" {
		enc, err := database.EncryptValue(value)
		if err != nil {
			return err
		}
		value = enc
	}
	m.rows[key] = database.Setting{Key: key, Value: value, Type: typ, IsSecret: isSecret}
	return nil
}

func (m *memSettings) DeleteSetting(key string) error {
	m.writes++
	delete(m.rows, key)
	return nil
}

func (m *memSettings) GetAllSettings() ([]database.Setting, error) {
	if m.allErr != nil {
		return nil, m.allErr
	}
	var out []database.Setting
	for _, r := range m.rows {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b database.Setting) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

func keysOf(rows []database.Setting) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Key)
	}
	return out
}

// The settings load reads its own held writes back, as it would read them
// from the store: a set key returns the new row, a deleted key is not
// found, and the census merges both over the stored rows in key order. The
// store takes no write until apply.
func TestDeferredSettings_ReadsItsOwnWrites(t *testing.T) {
	store := newMemSettings(
		database.Setting{Key: "alfred", Value: "butler", Type: "string"},
		database.Setting{Key: "gordon", Value: "commissioner", Type: "string"},
	)
	d := newDeferredSettings(store)

	require.NoError(t, d.SetSetting("alfred", "pennyworth", "string", false))
	require.NoError(t, d.SetSetting("batgirl", "oracle", "string", false))
	require.NoError(t, d.DeleteSetting("gordon"))

	got, err := d.GetSetting("alfred")
	require.NoError(t, err)
	assert.Equal(t, "pennyworth", got.Value)
	_, err = d.GetSetting("gordon")
	assert.ErrorIs(t, err, database.ErrSettingNotFound)
	got, err = d.GetSetting("batgirl")
	require.NoError(t, err)
	assert.Equal(t, "oracle", got.Value)

	all, err := d.GetAllSettings()
	require.NoError(t, err)
	assert.Equal(t, []string{"alfred", "batgirl"}, keysOf(all))
	assert.Equal(t, "pennyworth", all[0].Value)

	assert.Zero(t, store.writes, "the store took a write before apply")
	assert.Equal(t, 3, d.pending())
}

// A returned row is a copy: a caller editing it does not change what the
// next read returns.
func TestDeferredSettings_GetReturnsACopy(t *testing.T) {
	d := newDeferredSettings(newMemSettings())
	require.NoError(t, d.SetSetting("robin", "dick", "string", false))
	got, err := d.GetSetting("robin")
	require.NoError(t, err)
	got.Value = "jason"
	again, err := d.GetSetting("robin")
	require.NoError(t, err)
	assert.Equal(t, "dick", again.Value)
}

// A held secret reads back as the store would hold it, encrypted with the
// loaded key, and commits as the plaintext SetSetting encrypts.
func TestDeferredSettings_SecretReadsBackEncrypted(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(database.EncryptionKeyPath(dir), bytes.Repeat([]byte{9}, 32), 0o600))
	require.NoError(t, database.InitEncryption(dir))

	store := newMemSettings()
	d := newDeferredSettings(store)
	require.NoError(t, d.SetSetting("lucius_key", "wayne-enterprises", "string", true))

	held, err := d.GetSetting("lucius_key")
	require.NoError(t, err)
	assert.True(t, held.IsSecret)
	assert.NotEqual(t, "wayne-enterprises", held.Value, "a held secret reads back in plaintext")
	plain, err := database.DecryptValue(held.Value)
	require.NoError(t, err)
	assert.Equal(t, "wayne-enterprises", plain)
	all, err := d.GetAllSettings()
	require.NoError(t, err)
	assert.True(t, database.HasEncryptedSettings(all))

	require.NoError(t, d.apply())
	stored, err := store.GetSetting("lucius_key")
	require.NoError(t, err)
	plain, err = database.DecryptValue(stored.Value)
	require.NoError(t, err)
	assert.Equal(t, "wayne-enterprises", plain)
}

// apply replays the held writes in order (a set then a delete of one key
// leaves it deleted), once: a second call commits nothing.
func TestDeferredSettings_ApplyInOrderOnce(t *testing.T) {
	store := newMemSettings(database.Setting{Key: "joker", Value: "arkham", Type: "string"})
	d := newDeferredSettings(store)
	require.NoError(t, d.SetSetting("joker", "blackgate", "string", false))
	require.NoError(t, d.DeleteSetting("joker"))
	require.NoError(t, d.SetSetting("harley", "quinn", "string", false))

	require.NoError(t, d.apply())
	assert.Equal(t, 3, store.writes)
	_, err := store.GetSetting("joker")
	assert.ErrorIs(t, err, database.ErrSettingNotFound)
	got, err := store.GetSetting("harley")
	require.NoError(t, err)
	assert.Equal(t, "quinn", got.Value)

	require.NoError(t, d.apply())
	assert.Equal(t, 3, store.writes, "a second apply committed again")
	assert.Zero(t, d.pending())
}

// apply stops at the first failed write and returns it naming the key; the
// writes after it are not committed (the next boot's load stages them
// again).
func TestDeferredSettings_ApplyStopsAtTheFirstFailure(t *testing.T) {
	store := newMemSettings()
	store.failKey = "scarecrow"
	d := newDeferredSettings(store)
	require.NoError(t, d.SetSetting("penguin", "iceberg", "string", false))
	require.NoError(t, d.SetSetting("scarecrow", "fear", "string", false))
	require.NoError(t, d.SetSetting("riddler", "enigma", "string", false))

	err := d.apply()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"scarecrow"`)
	assert.Equal(t, 1, store.writes)
	_, err = store.GetSetting("riddler")
	assert.ErrorIs(t, err, database.ErrSettingNotFound, "a write after the failure committed")
}

// A census error is the store's own error, unwrapped by the overlay, so the
// load's unreadable-row handling (errors.AsType) still sees it.
func TestDeferredSettings_CensusErrorPassesThrough(t *testing.T) {
	unreadable := &database.UnreadableSettingsError{Keys: []string{"two_face"}}
	store := newMemSettings()
	store.allErr = unreadable
	d := newDeferredSettings(store)
	require.NoError(t, d.SetSetting("bruce", "wayne", "string", false))
	_, err := d.GetAllSettings()
	got, ok := errors.AsType[*database.UnreadableSettingsError](err)
	require.True(t, ok, "the census error lost its type: %v", err)
	assert.Same(t, unreadable, got)
}

// TestRepairsHook_SettingsWriteFailureDegrades (R1-23 review S2): a failed
// commit of the settings load's held writes is the Degraded startup step
// settingsWrites: logged naming the key, settings_writes recorded for
// /health, and the hook goes on to the boot repairs with the store open.
func TestRepairsHook_SettingsWriteFailureDegrades(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(tempDir(t), "gcpd.pebble"))
	require.NoError(t, err)
	m := &booksModule{}
	m.store.Store(store)
	t.Cleanup(func() { _ = m.closeStore() })

	mem := newMemSettings()
	mem.failKey = "scarecrow"
	writes := newDeferredSettings(mem)
	require.NoError(t, writes.SetSetting("penguin", "iceberg", "string", false))
	require.NoError(t, writes.SetSetting("scarecrow", "fear", "string", false))
	var logged []string
	steps := startup.New().SetErrorLog(func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })

	require.NoError(t, m.startRepairs(context.Background(), steps, writes, store), "a failed settings commit failed the boot")
	assert.Equal(t, []string{startup.TokenSettingsWrites}, steps.Tokens())
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "scarecrow")
	assert.Equal(t, 1, mem.writes, "the write before the failure was not committed")
	// The boot repairs ran and the store is still open.
	require.NoError(t, store.SetSetting("riddler", "enigma", "string", false))
}
