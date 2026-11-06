// file: internal/config/log_retention.go
// version: 1.1.1
// guid: 6f2c9e14-7b3a-4d58-a0e1-3c8b5d7f2a96
// last-edited: 2026-11-06

package config

import (
	"encoding/json"
	"strings"

	logging "github.com/falkcorp/mediacore/log"
)

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
		where, logging.DefaultLogRetentionDays, logging.LogRetentionKeepForever)
	c.LogRetentionDays = logging.DefaultLogRetentionDays
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
// A stored 0 is almost certainly not an operator's choice to keep logs
// forever: before 2026-10-11 InitConfig never read the key, so
// AppConfig.LogRetentionDays was 0 on every boot and every settings save
// serialised that 0 into the blob, and no UI ever wrote the key. It cannot be
// ruled out, though: from 2026-08-22 (1f66acfc7) the scheduler admin API's
// purge_old_logs hint told operators "set that key via PUT /config, 0 disables
// the purge", so an operator could have PUT a deliberate 0, which looks
// exactly like the accidental one. The owner-approved intent is that logs
// purge at the default window, so the 0 is dropped anyway, and the caller logs
// it at Warn saying what 0 used to mean and that -1 now keeps logs forever.
// A stored non-zero value, and an absent key, are left alone.
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
