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

// 01-P80 removed eleven settings that were loaded and persisted but never
// read. An old config.yaml carrying them is now the cutover's input, and M2's
// handling of them is pinned in internal/books/cutover (headRemovedKeys,
// head_gapfill_test.go).

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
