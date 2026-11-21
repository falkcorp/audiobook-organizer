// file: internal/config/unread_settings_removed_test.go
// version: 1.1.1
// guid: 5d0f7a3e-2b84-4c19-9e6a-71c3b8d4f052
// last-edited: 2026-10-20

package config

import (
	"encoding/json"
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
		assert.Contains(t, out, "key="+k+" ", "no removed-setting warning for %s", k)
	}
}

// LANGUAGE is the POSIX locale variable, so the environment alone must never
// draw the removed-setting warning, while the same key in the viper config file
// (--config or ~/.audiobook-organizer.yaml) must.
func TestInitConfig_LanguageWarnsFromFileNotEnvironment(t *testing.T) {
	t.Run("env only", func(t *testing.T) {
		logs := captureRemovedKeyLogs(t)
		fileInitConfig(t, "root_dir: /srv/library\n", func() { t.Setenv("LANGUAGE", "en_US:en") })
		assert.NotContains(t, logs.String(), "key=language ", "env LANGUAGE is a locale, not a stale setting")
	})
	t.Run("viper config file", func(t *testing.T) {
		logs := captureRemovedKeyLogs(t)
		fileInitConfig(t, "root_dir: /srv/library\nlanguage: en\n", nil)
		assert.Contains(t, logs.String(), "key=language ")
	})
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
