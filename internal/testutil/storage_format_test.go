// file: internal/testutil/storage_format_test.go
// version: 1.0.0
// guid: 490e04d3-cb40-4b81-8c85-c7567ef22ee9
// last-edited: 2026-10-03

package testutil

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestSetupIntegration_StoreStampedCurrent checks that the store every
// integration test runs against carries the storage_format stamp at the
// build's format, so integration tests exercise the same guard production
// does.
func TestSetupIntegration_StoreStampedCurrent(t *testing.T) {
	env, cleanup := SetupIntegration(t)
	defer cleanup()

	pref, err := env.Store.GetUserPreference("storage_format")
	require.NoError(t, err)
	require.NotNil(t, pref, "preference:storage_format is missing")
	require.NotNil(t, pref.Value)

	var v database.DatabaseVersion
	require.NoError(t, json.Unmarshal([]byte(*pref.Value), &v))
	require.Equal(t, database.SupportedStorageFormat, v.Version)
}
