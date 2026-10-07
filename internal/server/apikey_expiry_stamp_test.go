// file: internal/server/apikey_expiry_stamp_test.go
// version: 1.0.0
// guid: 6a1d9e38-2f7c-4b05-9c84-e3b0a7d2f169
// last-edited: 2026-10-07

package server

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// fakeKeyStampStore is an in-memory apiKeyExpiryStampStore.
type fakeKeyStampStore struct {
	*fakeSettingsStore
	keys    []database.APIKey
	listErr error
	setErr  error
	writes  int
}

func (f *fakeKeyStampStore) ListAllAPIKeys() ([]database.APIKey, error) {
	out := make([]database.APIKey, len(f.keys))
	copy(out, f.keys)
	return out, f.listErr
}

func (f *fakeKeyStampStore) SetAPIKeyExpiry(id string, at time.Time) error {
	if f.setErr != nil {
		return f.setErr
	}
	for i := range f.keys {
		if f.keys[i].ID == id {
			t := at
			f.keys[i].ExpiresAt = &t
			f.writes++
			return nil
		}
	}
	return errors.New("no such key")
}

func stampFixture() *fakeKeyStampStore {
	later := time.Now().Add(48 * time.Hour)
	revokedAt := time.Now().Add(-time.Hour)
	return &fakeKeyStampStore{
		fakeSettingsStore: newFakeSettingsStore(),
		keys: []database.APIKey{
			{ID: "never-active", Name: "scraper", UserID: "u1", Status: "active", CreatedAt: time.Now().Add(-400 * 24 * time.Hour)},
			{ID: "never-inactive", Name: "old worker", UserID: "u1", Status: "inactive"},
			{ID: "never-revoked", Name: "gone", UserID: "u1", Status: "revoked", RevokedAt: &revokedAt},
			{ID: "has-expiry", Name: "fine", UserID: "u2", Status: "active", ExpiresAt: &later},
		},
	}
}

func expiryOf(t *testing.T, f *fakeKeyStampStore, id string) *time.Time {
	t.Helper()
	for _, k := range f.keys {
		if k.ID == id {
			return k.ExpiresAt
		}
	}
	t.Fatalf("no key %s", id)
	return nil
}

func TestStampNeverExpiringAPIKeys_RunsOnce(t *testing.T) {
	f := stampFixture()
	now := time.Now()

	res, err := stampNeverExpiringAPIKeys(f, now)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Found, "active and inactive keys without expiry; revoked skipped")
	assert.Equal(t, 2, res.Stamped)
	assert.True(t, res.FlagSet)
	want := now.Add(handlers.DefaultAPIKeyTTL)
	for _, id := range []string{"never-active", "never-inactive"} {
		got := expiryOf(t, f, id)
		require.NotNil(t, got, id)
		assert.WithinDuration(t, want, *got, time.Second, id)
	}
	assert.Nil(t, expiryOf(t, f, "never-revoked"), "a revoked key is left alone")
	assert.WithinDuration(t, now.Add(48*time.Hour), *expiryOf(t, f, "has-expiry"), time.Minute, "an existing expiry is untouched")

	// Second start: the flag is set, nothing is written, even if a
	// never-expiring key appears (say, from a restored database).
	f.keys = append(f.keys, database.APIKey{ID: "restored", Status: "active"})
	writes := f.writes
	res, err = stampNeverExpiringAPIKeys(f, now.Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, res.AlreadyDone)
	assert.Equal(t, 1, res.Found)
	assert.Equal(t, 0, res.Stamped)
	assert.Equal(t, writes, f.writes, "the stamp runs once")
	assert.Nil(t, expiryOf(t, f, "restored"))
}

// A partial listing (an unreadable key record) stamps what it read but does
// not set the flag, so the next start retries the keys it could not see.
func TestStampNeverExpiringAPIKeys_PartialListingRetries(t *testing.T) {
	f := stampFixture()
	f.listErr = &database.UnreadableMembersError{Op: "ListAllAPIKeys", IDs: []string{"bad"}, Errs: []error{errors.New("decode")}}

	res, err := stampNeverExpiringAPIKeys(f, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 2, res.Stamped)
	assert.False(t, res.FlagSet)
	s, _ := f.GetSetting(apiKeyExpiryStampDoneKey)
	assert.True(t, s == nil || s.Value == "", "flag must stay unset after a partial listing")
}

func TestStampNeverExpiringAPIKeys_WriteFailureRetries(t *testing.T) {
	f := stampFixture()
	f.setErr = errors.New("disk full")

	res, err := stampNeverExpiringAPIKeys(f, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, res.Stamped)
	assert.False(t, res.FlagSet)
}

func TestStampNeverExpiringAPIKeys_HardListErrorFails(t *testing.T) {
	f := stampFixture()
	f.listErr = errors.New("index scan failed")
	_, err := stampNeverExpiringAPIKeys(f, time.Now())
	require.Error(t, err)
	s, _ := f.GetSetting(apiKeyExpiryStampDoneKey)
	assert.True(t, s == nil || s.Value == "")
}
