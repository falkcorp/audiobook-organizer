// file: internal/config/unread_settings_removed_test.go
// version: 1.0.0
// guid: 5d0f7a3e-2b84-4c19-9e6a-71c3b8d4f052
// last-edited: 2026-10-17

package config

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unreadSettingKeys are the eleven settings 01-P80 removed. Each was loaded and
// persisted but never read, so an old config file or settings export can still
// carry them and must keep loading.
var unreadSettingKeys = []string{
	"metadata_review_default_view",
	"default_user_quota_gb",
	"memory_limit_type",
	"cache_size",
	"memory_limit_percent",
	"memory_limit_mb",
	"log_format",
	"enable_json_logging",
	"auth_rate_limit_per_minute",
	"language",
	"api_keys",
}

// oldConfigYAML is a config file written before the removal: every retired key
// is present next to live ones, including catalog.language, which is a
// different setting and must survive.
const oldConfigYAML = `
root_dir: /srv/library
metadata_review_default_view: compact
default_user_quota_gb: 100
memory_limit_type: items
cache_size: 1000
memory_limit_percent: 25
memory_limit_mb: 512
log_format: text
enable_json_logging: false
auth_rate_limit_per_minute: 10
language: en
api_keys: {}
catalog:
  language: german
`

// A config file that still carries the eleven keys loads without error; the
// live keys beside them are applied and the retired ones are ignored.
func TestInitConfig_RemovedUnreadSettingsInOldFileAreIgnored(t *testing.T) {
	logs := captureRemovedKeyLogs(t)
	fileInitConfig(t, oldConfigYAML, nil)

	snap := Snapshot()
	assert.Equal(t, "/srv/library", snap.RootDir)
	assert.Equal(t, "german", snap.Catalog.Language, "catalog.language is a different, live setting")
	assert.NoError(t, snap.Validate())

	out := logs.String()
	for _, k := range unreadSettingKeys {
		if k == "language" {
			continue // fileOnly: the viper/environment scan skips it on purpose
		}
		assert.Contains(t, out, "key="+k+" ", "no removed-setting warning for %s", k)
	}
	assert.NotContains(t, out, "key=language ", "LANGUAGE is a common environment variable; viper must not warn for it")
}

// The config.yaml next to the database is loaded by LoadConfigFromFile; it warns
// for each retired key, including language, and loads the live keys.
func TestLoadConfigFromFile_RemovedUnreadSettingsWarnAndLoad(t *testing.T) {
	restoreAppConfig(t)
	logs := captureRemovedKeyLogs(t)

	dir := t.TempDir()
	Mutate(func(c *Config) {
		c.DatabasePath = dir + "/audiobooks.pebble"
		c.RootDir = ""
	})
	require.NoError(t, os.WriteFile(dir+"/config.yaml", []byte(oldConfigYAML), 0o600))

	require.NoError(t, LoadConfigFromFile())
	assert.Equal(t, "/srv/library", Snapshot().RootDir)
	for _, k := range unreadSettingKeys {
		assert.Contains(t, logs.String(), "key="+k+" ", "no removed-setting warning for %s", k)
	}
}

// Saving the config scrubs the retired keys from config.yaml so a stale value
// does not live on, and keeps catalog.language.
func TestSaveConfigToFile_ScrubsRemovedUnreadSettings(t *testing.T) {
	path := saveFileFixture(t, oldConfigYAML)
	require.NoError(t, SaveConfigToFile())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, line := range strings.Split(string(data), "\n") {
		for _, k := range unreadSettingKeys {
			assert.False(t, strings.HasPrefix(line, k+":"), "retired key %s survived the save: %q", k, line)
		}
	}
	assert.Contains(t, string(data), "german", "catalog.language was lost")
}

// A stored config blob written before the removal still carries the keys in its
// JSON; decoding it must not fail.
func TestConfigBlob_RemovedUnreadSettingsDecode(t *testing.T) {
	restoreAppConfig(t)
	blob := `{"root_dir":"/srv/library","metadata_review_default_view":"compact","default_user_quota_gb":100,` +
		`"memory_limit_type":"items","cache_size":1000,"memory_limit_percent":25,"memory_limit_mb":512,` +
		`"log_format":"text","enable_json_logging":false,"auth_rate_limit_per_minute":10,"language":"en","api_keys":{}}`
	var c Config
	require.NoError(t, json.Unmarshal([]byte(blob), &c))
	assert.Equal(t, "/srv/library", c.RootDir)
}

// A PUT that still carries a retired key is refused with the removal message
// (the Settings page and its import no longer send them), and applies nothing.
func TestUpdateConfig_RejectsRemovedUnreadSettings(t *testing.T) {
	for _, k := range unreadSettingKeys {
		t.Run(k, func(t *testing.T) {
			restoreAppConfig(t)
			ms, blobs := ladderTestStore(t)
			svc := NewUpdateService(ms)

			status, resp := svc.UpdateConfig(context.Background(), map[string]any{k: 1, "root_dir": "/lib"})
			require.Equal(t, http.StatusBadRequest, status, "resp = %v", resp)
			msg, _ := resp["error"].(string)
			assert.Contains(t, msg, k)
			assert.Contains(t, msg, "was removed")
			assert.Empty(t, *blobs, "a rejected PUT persisted a blob")
			assert.NotEqual(t, "/lib", Snapshot().RootDir)
		})
	}
}
