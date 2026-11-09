// file: internal/books/store/legacy_migration_test.go
// version: 1.0.1
// guid: b2c3d4e5-f6a7-2345-bcde-f0123456789a

package store

import (
	"sort"
	"testing"
)

func TestEnrichLegacyLogTags(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		source   string
		level    string
		expected []string
	}{
		{
			name:     "compaction message",
			message:  "Compacted 150 entries",
			source:   "maintenance",
			level:    "info",
			expected: []string{"legacy", "action:compact", "tier:maintenance", "outcome:ok", "source:maintenance"},
		},
		{
			name:     "purge operation",
			message:  "Purged 42 deleted books",
			source:   "cleanup",
			level:    "info",
			expected: []string{"legacy", "action:purge", "outcome:ok", "source:cleanup"},
		},
		{
			name:     "scan operation",
			message:  "Scanned 1200 files",
			source:   "scanner",
			level:    "info",
			expected: []string{"legacy", "action:scan", "outcome:ok", "source:scanner"},
		},
		{
			name:     "metadata enrichment",
			message:  "Applied metadata and ISBN enriched",
			source:   "metadata",
			level:    "info",
			expected: []string{"legacy", "action:metadata-apply", "outcome:ok", "source:metadata"},
		},
		{
			name:     "warning level",
			message:  "Scanned with warnings",
			source:   "scanner",
			level:    "warning",
			expected: []string{"legacy", "action:scan", "outcome:warn", "source:scanner"},
		},
		{
			name:     "error level",
			message:  "Failed to process metadata",
			source:   "metadata",
			level:    "error",
			expected: []string{"legacy", "action:metadata-apply", "outcome:error", "source:metadata"},
		},
		{
			name:     "dedup operation",
			message:  "Dedup merged 5 duplicates",
			source:   "dedup",
			level:    "info",
			expected: []string{"legacy", "action:dedup", "outcome:ok", "source:dedup"},
		},
		{
			name:     "cover update",
			message:  "Cover image updated",
			source:   "covers",
			level:    "info",
			expected: []string{"legacy", "action:cover-update", "outcome:ok", "source:covers"},
		},
		{
			name:     "tag write operation",
			message:  "Tag write operation completed",
			source:   "tagger",
			level:    "info",
			expected: []string{"legacy", "action:tag-write", "outcome:ok", "source:tagger"},
		},
		{
			name:     "rename operation",
			message:  "Renamed 15 files",
			source:   "organizer",
			level:    "info",
			expected: []string{"legacy", "action:organizer", "outcome:ok", "source:organizer"},
		},
		{
			name:     "no source",
			message:  "Compacted entries",
			source:   "",
			level:    "info",
			expected: []string{"legacy", "action:compact", "tier:maintenance", "outcome:ok"},
		},
		{
			name:     "case insensitive",
			message:  "COMPACTED ENTRIES",
			source:   "Maintenance",
			level:    "INFO",
			expected: []string{"legacy", "action:compact", "tier:maintenance", "outcome:ok", "source:maintenance"},
		},
		{
			name:     "empty message",
			message:  "",
			source:   "unknown",
			level:    "info",
			expected: []string{"legacy", "outcome:ok", "source:unknown"},
		},
		{
			name:     "debug level defaults to ok",
			message:  "Debug output",
			source:   "system",
			level:    "debug",
			expected: []string{"legacy", "outcome:ok", "source:system"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := enrichLegacyLogTags(tt.message, tt.source, tt.level)

			// Sort for deterministic comparison
			sort.Strings(result)
			sort.Strings(tt.expected)

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d tags, got %d: %v vs %v", len(tt.expected), len(result), tt.expected, result)
				return
			}

			for i, tag := range result {
				if tag != tt.expected[i] {
					t.Errorf("tag mismatch at index %d: expected %q, got %q", i, tt.expected[i], tag)
				}
			}
		})
	}
}

func TestEnrichLegacyLogTags_NoDuplicates(t *testing.T) {
	// Edge case: message with multiple matching patterns shouldn't duplicate tags
	result := enrichLegacyLogTags("Compacted compaction entries", "maintenance", "info")

	tagCount := make(map[string]int)
	for _, tag := range result {
		tagCount[tag]++
	}

	for tag, count := range tagCount {
		if count > 1 {
			t.Errorf("tag %q appeared %d times (should be unique)", tag, count)
		}
	}
}
