// file: internal/database/activity_types.go
// version: 1.1.0
// guid: b8c9d0e1-f2a3-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-08-29

// Package database — activity log types and helpers previously defined in
// activity_store.go (the legacy SQLite backend). Extracted here in fable5
// TASK-022 so NutsActivityStore, activity.Service, and their callers continue
// to compile.

package database

import (
	"fmt"
	"strings"
	"time"
)

// ActivityEntry represents a single entry in the unified activity log.
type ActivityEntry struct {
	ID          int64          `json:"id"`
	Timestamp   time.Time      `json:"timestamp"`
	Tier        string         `json:"tier"`
	Type        string         `json:"type"`
	Level       string         `json:"level"`
	Source      string         `json:"source"`
	OperationID string         `json:"operation_id,omitempty"`
	BookID      string         `json:"book_id,omitempty"`
	Summary     string         `json:"summary"`
	Details     map[string]any `json:"details,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	PrunedAt    *time.Time     `json:"pruned_at,omitempty"`
}

// ActivityFilter controls which entries Query returns.
type ActivityFilter struct {
	Limit          int
	Offset         int
	Type           string
	Tier           string
	Level          string
	OperationID    string
	BookID         string
	Since          *time.Time
	Until          *time.Time
	Tags           []string
	Search         string   // LIKE %search% on summary
	Source         string   // show only this source
	ExcludeSources []string // hide these sources
	ExcludeTiers   []string // hide these tiers
	ExcludeTags    []string // hide entries that carry any of these tags
}

// CompactResult holds the outcome of a CompactByDay operation.
type CompactResult struct {
	DaysCompacted  int `json:"days_compacted"`
	EntriesDeleted int `json:"entries_deleted"`
}

// DigestItem represents a single compacted entry within a daily digest.
type DigestItem struct {
	Type        string `json:"type"`
	Tier        string `json:"tier,omitempty"`
	Book        string `json:"book,omitempty"`
	BookID      string `json:"book_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	Summary     string `json:"summary"`
	Details     string `json:"details,omitempty"`
	// Timestamp is the original event time. Zero for digests compacted before
	// 2026-05-20 (when this field was added); omitempty hides it from old JSON.
	Timestamp time.Time `json:"timestamp,omitempty"`
	// Tags carries the enriched tag set from the source row.
	// Empty for digests compacted before 2026-05-20.
	Tags []string `json:"tags,omitempty"`
}

// DigestDetails is the JSON structure stored in a daily digest row's details column.
type DigestDetails struct {
	Date          string         `json:"date"`
	OriginalCount int            `json:"original_count"`
	Counts        map[string]int `json:"counts"`
	// TagCounts aggregates entry counts grouped by tag namespace → tag value.
	// Outer key is a namespace like "action" or "source"; inner key is the
	// tag value (e.g. "metadata-apply", "scan"). Used by the frontend as a
	// fallback breakdown when Counts has only one key (e.g. legacy "system_log"
	// entries whose type was not yet classified at compaction time).
	TagCounts      map[string]map[string]int `json:"tag_counts,omitempty"`
	Items          []DigestItem              `json:"items"`
	Truncated      bool                      `json:"truncated,omitempty"`
	TruncatedCount int                       `json:"truncated_count,omitempty"`
}

// maxDigestItems caps the number of DigestItems stored per daily digest.
const maxDigestItems = 500

// SourceCount holds a source name and the number of activity log entries from it.
type SourceCount struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// ActivityIndexRepairResult holds the outcome of a RepairActivityIndexes run.
//
// Deleted is Orphaned+Malformed for a run that finished; on an error or a
// cancellation it is the number actually committed, never a projection.
type ActivityIndexRepairResult struct {
	// Scanned is how many act:op:/act:bk: index entries were examined.
	Scanned int64 `json:"scanned"`
	// Orphaned is how many pointed at a primary row that no longer exists.
	Orphaned int64 `json:"orphaned"`
	// Malformed is how many carried a reference that cannot be turned back
	// into a primary key at all (so no reader could follow them either).
	Malformed int64 `json:"malformed"`
	// Deleted is how many index entries were actually removed.
	Deleted int64 `json:"deleted"`
}

// RecompactResult holds the outcome of a RecompactDigests operation.
type RecompactResult struct {
	Touched int `json:"touched"`
	Skipped int `json:"skipped"`
}

// CacheStatsSnapshot is one row in the metrics store history table.
// Misses, Invalidations, and Evictions are flattened sums across reasons/scopes.
type CacheStatsSnapshot struct {
	CacheName        string    `json:"cache_name"`
	Timestamp        time.Time `json:"ts"`
	Hits             int64     `json:"hits"`
	Misses           int64     `json:"misses"`
	Sets             int64     `json:"sets"`
	Invalidations    int64     `json:"invalidations"`
	Evictions        int64     `json:"evictions"`
	Size             int64     `json:"size"`
	GetDurationCount int64     `json:"get_duration_count"`
	GetDurationSum   float64   `json:"get_duration_sum"`
}

// extractBookName returns the book title from an ActivityEntry's details.
func extractBookName(e ActivityEntry) string {
	if v, ok := e.Details["book_title"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if v, ok := e.Details["title"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// extractItemSummary builds a short summary string from the entry based on its type.
func extractItemSummary(e ActivityEntry) string {
	switch e.Type {
	case "metadata_applied":
		fields, _ := e.Details["fields"].(string)
		source, _ := e.Details["source"].(string)
		if fields != "" && source != "" {
			return fields + " from " + source
		}
		if fields != "" {
			return fields
		}
	case "tag_written":
		tagCount := detailNumber(e.Details, "tag_count")
		fileCount := detailNumber(e.Details, "file_count")
		return fmt.Sprintf("wrote %d tags to %d files", tagCount, fileCount)
	case "organize_completed":
		if p, ok := e.Details["new_path"].(string); ok {
			return "moved to " + p
		}
	case "config_changed":
		if k, ok := e.Details["key"].(string); ok {
			return k + " changed"
		}
	}
	// Default: truncate summary to 120 chars.
	if len(e.Summary) > 120 {
		return e.Summary[:120]
	}
	return e.Summary
}

// extractErrorDetails joins error-related fields from entry details.
func extractErrorDetails(e ActivityEntry) string {
	var parts []string
	for _, key := range []string{"error", "path", "file_path"} {
		if v, ok := e.Details[key].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

// detailNumber extracts a numeric value from details as int.
func detailNumber(details map[string]any, key string) int {
	v, ok := details[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// isLegacyItem returns true if a DigestItem needs re-derivation.
// An item needs update if its type is the generic "system_log" or "system" fallback
// OR if it has no tags (i.e. was compacted before tag enrichment was added).
func isLegacyItem(item DigestItem) bool {
	return (item.Type == "system_log" || item.Type == "system") || len(item.Tags) == 0
}
