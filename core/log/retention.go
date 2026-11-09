// file: core/log/retention.go
// version: 1.0.1
// guid: 74f43bde-ff40-4eae-88dc-907de43dfe92
// last-edited: 2026-11-09

package log

// DefaultLogRetentionDays is the shipped value of log_retention_days: the
// purge_old_logs task deletes system log rows (syslog:) and legacy operation
// log rows (operationlog:) older than this many days. It never deletes undo
// history (opchange: rows). viper's default, the load and Validate
// normalization and every consumer's fallback read it from here.
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
// live config by any path (a PUT, a test) still purges at the default window.
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
