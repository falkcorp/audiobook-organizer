// file: internal/config/log_retention.go
// version: 1.1.1
// guid: 6f2c9e14-7b3a-4d58-a0e1-3c8b5d7f2a96
// last-edited: 2026-11-06

package config

import (
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
