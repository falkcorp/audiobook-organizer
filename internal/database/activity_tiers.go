// file: internal/database/activity_tiers.go
// version: 1.0.0
// guid: 7b1e5c2a-4d3f-4a86-9e0b-2f6c8d1a5e47
// last-edited: 2026-10-09

package database

import (
	"slices"
	"strings"
)

// actTiers is the full list of tier buckets used for queries, sources, wipe,
// and compaction. digest must be last (it is excluded from compaction but
// included in queries). Add new tiers here when introducing them.
var actTiers = []string{"change", "debug", "audit", "info", "batch", "system", "digest"}

// actCompactableTiers returns all tiers eligible for compaction (everything
// except digest, which is the compaction output and must not be re-compacted).
func actCompactableTiers() []string { return actTiers[:len(actTiers)-1] }

// matchesFilter returns true if entry e satisfies all non-time fields in f.
// Time filtering is handled by scanTier's range scan.
//
// THAT SECOND SENTENCE IS ONLY TRUE OF THE TIER SCAN. The two secondary-index
// callers in pebble_activity_store.go (queryByIndexPrefixFull and
// queryByIndexPrefixPaged) apply NO time bounds at all, so
// GET /api/v1/activity?operation_id=X&since=... silently ignores `since`. That
// is a known unfixed defect, and this is the function a fixer reaches for.
//
// Before adding Since/Until handling HERE, read pactPushdownDecidable in
// pebble_activity_store.go: Since and Until sit on its allow-list only because
// this function ignores them. Teaching it to honour them fixes the full path
// and leaves the PAGED path's `total` wrong — that count is computed before
// this function runs, and only rows inside the page window are ever decoded.
// Remove Since/Until from that allow-list in the same change.
func matchesFilter(e ActivityEntry, f ActivityFilter) bool {
	if f.Tier != "" && e.Tier != f.Tier {
		return false
	}
	if !f.acceptsType(e.Type) {
		return false
	}
	if f.Level != "" && e.Level != f.Level {
		return false
	}
	if f.Source != "" && e.Source != f.Source {
		return false
	}
	if f.OperationID != "" && !entryHasOperation(e, f.OperationID) {
		return false
	}
	if f.BookID != "" && e.BookID != f.BookID {
		return false
	}
	if f.Search != "" && !strings.Contains(e.Summary, f.Search) {
		return false
	}
	// Every required tag, each satisfied by any of its aliases (TagAliases).
	if !f.acceptsTags(e.Tags) {
		return false
	}
	if slices.Contains(f.ExcludeSources, e.Source) {
		return false
	}
	if slices.Contains(f.ExcludeTiers, e.Tier) {
		return false
	}
	// Any excluded tag, or any of its aliases (ExcludeTagAliases), hides it.
	return !f.rejectsTags(e.Tags)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
