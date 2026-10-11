// file: internal/config/log_retention.go
// version: 1.0.0
// guid: 6f2c9e14-7b3a-4d58-a0e1-3c8b5d7f2a96
// last-edited: 2026-10-11

package config

import (
	"encoding/json"
	"strings"
)

// DefaultLogRetentionDays is the shipped value of log_retention_days: the
// purge_old_logs task deletes operation logs, operation changes and system
// activity log rows older than this many days. viper's default, the load and
// Validate normalization and every consumer's fallback read it from here.
const DefaultLogRetentionDays = 90

// LogRetentionKeepForever is the documented value that turns the log purge
// off. Any negative value means the same; -1 is the spelling to write.
const LogRetentionKeepForever = -1

// ResolveLogRetentionDays returns the effective purge window for a configured
// log_retention_days: v when positive, DefaultLogRetentionDays when 0, and 0
// ("do not purge") when negative.
//
// 0 is NOT "keep forever" any more, for the same reason
// chapter_consolidation_threshold_min lost its zero off-switch: 0 is Go's
// zero value. Until 2026-10-11 InitConfig never read log_retention_days, so
// every install ran with 0, the purge_old_logs task was disabled everywhere,
// and the first settings save wrote that 0 into the stored config blob, where
// it looked exactly like an operator's choice. An off-switch that the zero
// value reaches by accident cannot be told apart from one an operator set, so
// the off-switch is now -1 (LogRetentionKeepForever) and 0 means the default.
//
// Consumers call this rather than reading the field, so a 0 that reaches the
// live config by any path (a PUT, ResetToDefaults, a test) still purges at the
// default window.
func ResolveLogRetentionDays(v int) int {
	switch {
	case v < 0:
		return 0
	case v == 0:
		return DefaultLogRetentionDays
	default:
		return v
	}
}

// normalizeLogRetentionDays rewrites a 0 log_retention_days to the default and
// says so at Warn, naming where the value was found. It reports whether it
// changed anything. It is the visible half of ResolveLogRetentionDays: GET
// /config must not show a 0 that behaves like 90, and a 0 that reached a
// stored blob is healed on the next save.
func normalizeLogRetentionDays(c *Config, where string) bool {
	if c == nil || c.LogRetentionDays != 0 {
		return false
	}
	configLog.Warn(
		"config: log_retention_days=0 in %s means the default; using %d. "+
			"0 no longer disables the log purge (it was the value every install ran with while "+
			"the setting was never read); set %d to keep logs forever",
		where, DefaultLogRetentionDays, LogRetentionKeepForever)
	c.LogRetentionDays = DefaultLogRetentionDays
	return true
}

// logRetentionZeroMigratedKey marks that migrateLogRetentionBlob has run on
// this install, so it runs exactly once.
const logRetentionZeroMigratedKey = "log_retention_zero_migrated"

// migrateLogRetentionBlob drops a stored "log_retention_days": 0 from the
// config blob, so the key is absent and the startup value (the default 90, or
// a config-file/environment value) applies. Run exactly once per install; the
// caller gates it on logRetentionZeroMigratedKey and persists the result.
//
// No stored 0 can be an operator choice to keep logs forever: before
// 2026-10-11 InitConfig never read the key, so AppConfig.LogRetentionDays was
// 0 on every boot and every settings save serialised that 0 into the blob. No
// UI ever wrote the key. A stored non-zero value, and an absent key, are left
// alone.
func migrateLogRetentionBlob(blob string) (string, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	v, ok := raw["log_retention_days"]
	if !ok || strings.TrimSpace(string(v)) != "0" {
		return blob, false
	}
	delete(raw, "log_retention_days")
	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}
