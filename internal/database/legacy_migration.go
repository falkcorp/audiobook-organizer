// file: internal/database/legacy_migration.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-1234-abcd-ef0123456789

package database

import (
	"strings"
)

// deriveTypeFromMessage inspects the message (and optionally source) of a legacy
// system_activity_log row and returns a structured (typ, tier) pair suitable for
// the unified activity_log schema. The function is used by both
// MigrateSystemActivityLogs (to set the Type/Tier fields on new rows) and
// enrichLegacyLogTags (to stay in sync with the action: tags it produces).
//
// Priority order: most-specific patterns first, "system_log" / "system" as
// ultimate fallback.
func deriveTypeFromMessage(msg, source string) (typ, tier string) {
	m := strings.ToLower(msg)

	// ── metadata / tag write ───────────────────────────────────────────────
	// Match on "metadata" alone (broader) so that messages like
	// "Failed to process metadata" are still classified here.
	if strings.Contains(m, "metadata") || strings.Contains(m, "metadata_apply") ||
		strings.Contains(m, "isbn") || strings.Contains(m, "enriched") {
		return "metadata_apply", "change"
	}
	if (strings.Contains(m, "tag") && strings.Contains(m, "write")) ||
		strings.Contains(m, "tag_write") ||
		strings.Contains(m, "wrote tag") ||
		strings.Contains(m, "tags written") {
		return "tag_write", "change"
	}

	// ── scan ───────────────────────────────────────────────────────────────
	if strings.Contains(m, "scan completed") || strings.Contains(m, "scan complete") ||
		strings.Contains(m, "scan finished") || strings.Contains(m, "finished scanning") ||
		strings.Contains(m, "scanned") {
		return "scan_completed", "audit"
	}
	if strings.Contains(m, "scan started") || strings.Contains(m, "starting scan") ||
		strings.Contains(m, "begin scan") {
		return "scan_started", "audit"
	}
	// generic scan (no start/end qualifier)
	if strings.Contains(m, "scan") {
		return "scan_completed", "audit"
	}

	// ── operation lifecycle ────────────────────────────────────────────────
	if strings.Contains(m, "operation completed") || strings.Contains(m, "op completed") ||
		strings.Contains(m, "operation finished") {
		return "operation_completed", "audit"
	}
	if strings.Contains(m, "operation started") || strings.Contains(m, "op started") ||
		strings.Contains(m, "op id") || strings.Contains(m, "operation id") {
		return "operation_started", "audit"
	}

	// ── fingerprint ────────────────────────────────────────────────────────
	if strings.Contains(m, "fingerprint") {
		return "fingerprint", "change"
	}

	// ── dedup ──────────────────────────────────────────────────────────────
	if strings.Contains(m, "dedup") || strings.Contains(m, "duplicate") {
		return "dedup", "change"
	}

	// ── itunes ─────────────────────────────────────────────────────────────
	if strings.Contains(m, "itunes") || strings.Contains(m, "itl") {
		return "itunes_sync", "change"
	}

	// ── cover ──────────────────────────────────────────────────────────────
	if strings.Contains(m, "cover") {
		return "cover_update", "change"
	}

	// ── organize / rename ──────────────────────────────────────────────────
	if strings.Contains(m, "rename") || strings.Contains(m, "organize") {
		return "organize", "change"
	}

	// ── server / boot / shutdown ────────────────────────────────────────────
	if strings.Contains(m, "server") || strings.Contains(m, "listening") ||
		strings.Contains(m, "startup") || strings.Contains(m, "shutdown") ||
		strings.Contains(m, "starting") || strings.Contains(m, "http") {
		return "server_log", "system"
	}
	// source-based heuristic: if the source looks like an HTTP or server module
	if strings.Contains(strings.ToLower(source), "server") ||
		strings.Contains(strings.ToLower(source), "http") {
		return "server_log", "system"
	}

	// ── fallback ──────────────────────────────────────────────────────────
	return "system_log", "system"
}

// enrichLegacyLogTags derives intelligent tags from legacy system_activity_log entries.
// Analyzes message content and level to infer action, outcome, source, and tier tags.
// Returns a de-duplicated list of tags that describe what the log entry represents.
// The action: tag is kept in sync with deriveTypeFromMessage so digest aggregation
// and tag-based filtering agree on the classification.
func enrichLegacyLogTags(message, source, level string) []string {
	seen := make(map[string]bool)
	var tags []string

	addTag := func(tag string) {
		if !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}

	// Always include legacy marker
	addTag("legacy")

	msgLower := strings.ToLower(message)

	// Apply legacy secondary action patterns first (backward-compat).
	// These are more granular than deriveTypeFromMessage for certain cases.
	actionAdded := false

	// Compaction and maintenance operations
	if strings.Contains(msgLower, "compacted") || strings.Contains(msgLower, "compaction") {
		addTag("action:compact")
		addTag("tier:maintenance")
		actionAdded = true
	}

	// Purge operations
	if strings.Contains(msgLower, "purged") || strings.Contains(msgLower, "purge") {
		addTag("action:purge")
		actionAdded = true
	}

	// If no action tag was produced by the legacy patterns, derive one from
	// deriveTypeFromMessage so that digest TagCounts.action has meaningful values.
	// Skip the "system_log" fallback to avoid adding a generic action:system tag
	// to messages that simply have no recognised pattern.
	if !actionAdded {
		typ, _ := deriveTypeFromMessage(message, source)
		switch typ {
		case "metadata_apply":
			addTag("action:metadata-apply")
		case "tag_write":
			addTag("action:tag-write")
		case "scan_started", "scan_completed":
			addTag("action:scan")
		case "operation_started", "operation_completed":
			addTag("action:operation")
		case "fingerprint":
			addTag("action:fingerprint")
		case "dedup":
			addTag("action:dedup")
		case "itunes_sync":
			addTag("action:itunes-sync")
		case "cover_update":
			addTag("action:cover-update")
		case "organize":
			addTag("action:organizer")
		case "server_log":
			addTag("action:server")
			// "system_log" fallback: don't add a tag — mirrors the pre-existing
			// behaviour where unknown messages had no action: tag at all.
		}
	}

	// Derive outcome from level
	switch strings.ToLower(level) {
	case "error":
		addTag("outcome:error")
	case "warning":
		addTag("outcome:warn")
	case "info", "debug":
		addTag("outcome:ok")
	}

	// Add source tag if present
	if source != "" {
		addTag("source:" + strings.ToLower(source))
	}

	return tags
}
