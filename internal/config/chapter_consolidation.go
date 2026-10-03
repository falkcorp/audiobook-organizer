// file: internal/config/chapter_consolidation.go
// version: 1.0.0
// guid: 352b4f24-83a2-44dd-83d3-0d58454324bf
// last-edited: 2026-10-03

package config

import "github.com/falkcorp/audiobook-organizer/internal/logger"

// DefaultChapterConsolidationThresholdMin is the shipped value of
// chapter_consolidation_threshold_min. viper's default, ResetToDefaults, the
// load/save normalization below and every consumer's fallback read it from
// here, so the number exists once.
const DefaultChapterConsolidationThresholdMin = 10

// chapterConsolidationLog carries the normalization warning. It goes through
// internal/logger rather than log/slog so the line gets the log-injection
// barrier (TestGuard_NoDirectSlogCalls).
var chapterConsolidationLog = logger.New("config")

// ResolveChapterConsolidationThresholdMin returns the effective import-scanner
// chapter threshold in minutes: the configured value, or
// DefaultChapterConsolidationThresholdMin when it is 0 or negative.
//
// Zero is NOT "disabled". It used to be: the field's doc said "Set to 0 to
// disable consolidation", and the scanner honored it. But zero is also Go's
// zero value, so every partially-populated Config -- a struct literal that
// forgot the field (ResetToDefaults did, until #2729), a blob saved from one, a
// test fixture -- wrote the same 0 an operator would, and nothing could tell
// them apart. Production ran with it for eleven days in 2026-08 and 12,525
// books were imported without book_file rows, behind a green suite and with
// no log line. A setting whose off-switch is the zero value cannot be made
// safe by logging alone, so the off-switch is gone: 0 or less means the
// default, the same rule as repair_chapter_max_min.
//
// Consumers call this instead of reading the field, so a zero that reaches the
// live config by any path (a PUT, a test, a restored backup) still behaves as
// the default even before Validate normalizes it.
func (c *Config) ResolveChapterConsolidationThresholdMin() int {
	if c.ChapterConsolidationThresholdMin <= 0 {
		return DefaultChapterConsolidationThresholdMin
	}
	return c.ChapterConsolidationThresholdMin
}

// normalizeChapterConsolidationThreshold rewrites a 0-or-negative threshold to
// the default and says so at Warn, naming where the value was found. It
// reports whether it changed anything.
//
// It is the visible half of ResolveChapterConsolidationThresholdMin: the
// resolver keeps the scanner correct, this keeps the stored and displayed
// value truthful (GET /config must not show a 0 that behaves like 10) and
// heals a persisted zero on the next save instead of reloading it forever.
func normalizeChapterConsolidationThreshold(c *Config, where string) bool {
	if c == nil || c.ChapterConsolidationThresholdMin > 0 {
		return false
	}
	chapterConsolidationLog.Warn(
		"config: chapter_consolidation_threshold_min=%d in %s is not a usable threshold; using the default %d. "+
			"0 no longer disables chapter consolidation (a stray zero silently stopped multi-file grouping in 2026-08)",
		c.ChapterConsolidationThresholdMin, where, DefaultChapterConsolidationThresholdMin)
	c.ChapterConsolidationThresholdMin = DefaultChapterConsolidationThresholdMin
	return true
}
