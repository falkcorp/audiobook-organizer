// file: internal/server/handlers/system/config_env_locks_test.go
// version: 1.0.0
// guid: 2e6b0d47-9a13-4c85-bf20-5d81c7a4e396
// last-edited: 2026-09-07

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
// config object, which is a map rather than a config.Config because the two
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
// Production pins ACTIVITY_DB_PATH in its systemd unit. Without this annotation the
// path control renders as an ordinary editable field, accepts a value, saves it —
// and then applyEnvAuthoritativeConfig overwrites it from the environment on the
// next boot. The edit appears to take and silently does not, which is precisely the
// failure this whole field was added to make visible.
func TestGetConfig_ReportsEnvLockedSettings(t *testing.T) {
	t.Setenv("ACTIVITY_DB_MOVE_ON_CHANGE", "")
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("ACTIVITY_DB_PATH", "/var/lib/audiobook-organizer/activity-v2.sqlite")

	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{})

	cfg := getConfigBody(t, h)

	locked, ok := cfg["env_locked"].([]any)
	require.True(t, ok, "env_locked missing from the config object; got %T", cfg["env_locked"])
	assert.Contains(t, locked, "activity_db_path")
	assert.NotContains(t, locked, "activity_db_move_on_change",
		"locking the path must not also freeze the unrelated move toggle")
}

// TestGetConfig_EnvLockedIsEmptyWithoutOverrides covers the ordinary deployment: no
// environment pins, so every control stays editable. An always-populated list would
// disable the UI everywhere and be just as wrong as an always-empty one.
func TestGetConfig_EnvLockedIsEmptyWithoutOverrides(t *testing.T) {
	for _, v := range []string{"ACTIVITY_DB_PATH", "ACTIVITY_DB_MOVE_ON_CHANGE", "ACTIVITY_BACKEND"} {
		t.Setenv(v, "")
	}

	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{})

	cfg := getConfigBody(t, h)

	locked, ok := cfg["env_locked"].([]any)
	require.True(t, ok, "env_locked must always be present, even when empty")
	assert.Empty(t, locked)
}

// TestGetConfig_ReportsTheResolvedActivityDBPath pins the placeholder source.
//
// With activity_db_path empty the UI must still show where the database actually
// is. The value is computed server-side on purpose: ResolveActivityDBPath has three
// branches, and a TypeScript reimplementation would drift from it silently and show
// the operator a location the server does not use.
func TestGetConfig_ReportsTheResolvedActivityDBPath(t *testing.T) {
	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{
		RootDir:      "/mnt/bigdata/books/audiobook-organizer",
		DatabasePath: "/var/lib/audiobook-organizer/db",
		// ActivityDBPath deliberately empty — this is the defaulting path.
	})

	cfg := getConfigBody(t, h)

	assert.Equal(t,
		"/mnt/bigdata/books/audiobook-organizer/.activity/activity.sqlite",
		cfg["activity_db_resolved_path"],
		"an empty activity_db_path must resolve to the dot-directory under the library root")
}

// TestGetConfig_StillCarriesTheOrdinaryConfigFields guards the grafting itself. The
// annotations are added by re-marshalling the config into a map, so a mistake there
// could drop every real setting while leaving both new keys present and both tests
// above green.
func TestGetConfig_StillCarriesTheOrdinaryConfigFields(t *testing.T) {
	h, d := newTestHandler(t)
	d.cfgUpd.EXPECT().MaskSecrets(mock.Anything).Return(config.Config{
		RootDir:                "/library",
		ActivityDBPath:         "/srv/activity.sqlite",
		ActivityDBMoveOnChange: true,
	})

	cfg := getConfigBody(t, h)

	assert.Equal(t, "/library", cfg["root_dir"])
	assert.Equal(t, "/srv/activity.sqlite", cfg["activity_db_path"])
	assert.Equal(t, true, cfg["activity_db_move_on_change"])
}
