// file: internal/server/handlers/system/config_env_locks_test.go
// version: 1.2.1
// guid: 2e6b0d47-9a13-4c85-bf20-5d81c7a4e396
// last-edited: 2026-10-19

package system_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
)

// getConfigBody issues GET /config against the handler and returns the decoded
// config object, which is a map rather than a config.Config because the
// interesting keys are computed annotations that exist only on the wire.
func getConfigBody(t *testing.T, h *system.Handler) map[string]any {
	t.Helper()
	w := run(http.MethodGet, "/config", "/config", nil, func(r *gin.Engine) {
		r.GET("/config", h.GetConfig)
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Config map[string]any `json:"config"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.Config)
	return resp.Data.Config
}

// TestGetConfig_ReportsEnvLockedSettings is the guard for the Settings page telling
// the truth about what the operator can change.
//
// Production pins DATABASE_PATH in its systemd unit. Without this annotation the
// path control renders as an ordinary editable field, accepts a value, saves it —
// and then applyEnvAuthoritativeConfig overwrites it from the environment on the
// next boot. The edit appears to take and silently does not, which is precisely the
// failure this whole field was added to make visible.
func TestGetConfig_ReportsEnvLockedSettings(t *testing.T) {
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("DATABASE_PATH", "/var/lib/audiobook-organizer/audiobooks.pebble")

	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{})

	cfg := getConfigBody(t, h)

	locked, ok := cfg["env_locked"].([]any)
	require.True(t, ok, "env_locked missing from the config object; got %T", cfg["env_locked"])
	assert.Contains(t, locked, "database_path")
	assert.NotContains(t, locked, "activity_backend",
		"locking the database path must not also lock an unrelated setting")
}

// TestGetConfig_EnvLockedIsEmptyWithoutOverrides covers the ordinary deployment: no
// environment pins, so every control stays editable. An always-populated list would
// disable the UI everywhere and be just as wrong as an always-empty one.
func TestGetConfig_EnvLockedIsEmptyWithoutOverrides(t *testing.T) {
	for _, v := range []string{"DATABASE_PATH", "ACTIVITY_BACKEND"} {
		t.Setenv(v, "")
	}

	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{})

	cfg := getConfigBody(t, h)

	locked, ok := cfg["env_locked"].([]any)
	require.True(t, ok, "env_locked must always be present, even when empty")
	assert.Empty(t, locked)
}

// TestGetConfig_StillCarriesTheOrdinaryConfigFields guards the grafting itself. The
// annotations are added by re-marshalling the config into a map, so a mistake there
// could drop every real setting while leaving both new keys present and both tests
// above green.
func TestGetConfig_StillCarriesTheOrdinaryConfigFields(t *testing.T) {
	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{
		RootDir:         "/library",
		DatabasePath:    "/srv/audiobooks.pebble",
		ActivityBackend: "pebble",
	})

	cfg := getConfigBody(t, h)

	assert.Equal(t, "/library", cfg["root_dir"])
	assert.Equal(t, "/srv/audiobooks.pebble", cfg["database_path"])
	assert.Equal(t, "pebble", cfg["activity_backend"])
	// Removed with the SQLite activity backend (01-P75): neither the settings
	// nor the resolved-path annotation may reappear in the response.
	for _, k := range []string{"activity_db_path", "activity_db_move_on_change", "activity_db_resolved_path"} {
		assert.NotContains(t, cfg, k)
	}
}

// TestGetConfig_PreservesLargeIntegersExactly guards the decode mode, not the
// annotation.
//
// Grafting the annotation means decoding the whole config into a map and encoding
// it again. A plain json.Unmarshal makes every number a float64, which carries 53
// bits of mantissa — so an int64 above 2^53 comes back changed, and the config the
// client receives is not the config the server holds. Config's int64 fields are
// byte counts today, so no realistic value is affected, but this runs over EVERY
// field on the primary read path and would silently mangle a nanosecond timestamp
// or a numeric ID added later.
//
// The assertion is against the raw response bytes on purpose: decoding the body
// into map[string]any to check it would apply the very float64 widening under test
// and pass either way.
func TestGetConfig_PreservesLargeIntegersExactly(t *testing.T) {
	// 2^53 + 1 — the smallest integer a float64 cannot represent. It round-trips
	// through a float64 as 9007199254740992.
	const exact = 9007199254740993

	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{
		BackupMaxTotalBytes: exact,
	})

	w := run(http.MethodGet, "/config", "/config", nil, func(r *gin.Engine) {
		r.GET("/config", h.GetConfig)
	})
	require.Equal(t, http.StatusOK, w.Code)

	assert.Contains(t, w.Body.String(), `"backup_max_total_bytes":9007199254740993`,
		"the config was re-encoded through float64 and lost precision")
}

// TestGetConfig_ReportsDelugeLocks covers the five Deluge connection settings, which
// the environment locks since 2026-10-11. The annotation names the variable (never
// its value) so the Deluge tab can tell the operator where the setting comes from;
// the password's value must not appear anywhere in the response.
func TestGetConfig_ReportsDelugeLocks(t *testing.T) {
	const secret = "dlw-handler-secret-5519"
	t.Setenv("DELUGE_WEB_URL", "http://192.0.2.10:8112")
	t.Setenv("DELUGE_WEB_PASSWORD", secret)
	t.Setenv("DELUGE_DISCOVERY_LABEL", "")
	t.Setenv("DELUGE_DISCOVERY_ENABLED", "")
	t.Setenv("DELUGE_MOVE_ENABLED", "")

	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{DelugeWebPassword: "dlw****5519"})

	w := run(http.MethodGet, "/config", "/config", nil, func(r *gin.Engine) {
		r.GET("/config", h.GetConfig)
	})
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), secret, "the response must never carry the password value")

	cfg := getConfigBody(t, h)
	locked, ok := cfg["env_locked"].([]any)
	require.True(t, ok)
	assert.Contains(t, locked, "deluge_web_url")
	assert.Contains(t, locked, "deluge_web_password")
	assert.NotContains(t, locked, "deluge_discovery_label", "an unset variable must not lock its field")

	locks, ok := cfg["setting_locks"].(map[string]any)
	require.True(t, ok, "setting_locks missing; got %T", cfg["setting_locks"])
	assert.Equal(t, "DELUGE_WEB_URL", locks["deluge_web_url"])
	assert.Equal(t, "DELUGE_WEB_PASSWORD", locks["deluge_web_password"])
	assert.NotContains(t, locks, "deluge_move_enabled")
}
