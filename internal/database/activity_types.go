// file: internal/database/activity_types.go
// version: 1.15.3
// guid: b8c9d0e1-f2a3-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-10-30

// Package database — activity log types and helpers shared by the Pebble
// activity store, activity.Service and their callers. Extracted from the
// legacy activity_store.go in fable5 TASK-022.

package database

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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

// activitySummaryMax bounds the stored summary. A summary is a one-line
// headline; nothing legitimate needs more than this.
//
// Not a theoretical limit. Measured on prod 2026-09-07: the SQLite activity
// database (since removed) was 5.3 GB, of which the summary column alone held 5.48 GB of data. The
// details column — the one that IS zstd-compressed — held 351 MB, about 6% of
// the file, so the compression work had been aimed at the wrong column. 208,103
// `system` rows averaged 27 KB of summary and the single largest was 9,558,930
// bytes: an iTunes write-back failure whose ContractVerdict.Error() formatted
// every one of ~100k mhoh violations into one string.
//
// That root cause is fixed in itl_safety_contract.go. This clamp exists because
// the storage layer must not depend on every caller being well behaved — the
// next unbounded string will come from somewhere else.
const activitySummaryMax = 8 << 10 // 8 KiB

// clampActivitySummary truncates an oversized summary on a rune boundary and
// says how much was dropped, so a truncated row is never mistaken for the whole
// message.
//
// The RESULT is bounded by activitySummaryMax, marker included — not the
// truncated prefix alone. That makes the function idempotent: an entry that
// passes through it twice (a re-recorded row, a future second layer) is not
// truncated again and does not gain a second marker. Reserving no room for the
// marker made the output overshoot the cap, which is how a second pass used to
// append a second marker.
//
// The Pebble store applies it in normalizeActivityEntry, the one step both
// Record and RecordBatch run. Until 2026-10-19 only the SQLite store clamped,
// so Pebble rows were unbounded; the clamp moved here when that store was
// removed rather than leaving the storage layer with no bound at all.
func clampActivitySummary(s string) string {
	if len(s) <= activitySummaryMax {
		return s
	}
	// Render the marker first: its length depends on the sizes it reports, so
	// the budget for the prefix cannot be known until it exists.
	marker := fmt.Sprintf("... [summary truncated: %d bytes total]", len(s))
	cut := max(activitySummaryMax-len(marker), 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// ActivityFilter controls which entries Query returns.
type ActivityFilter struct {
	Limit  int
	Offset int
	Type   string
	// TypeAliases are further type values that match exactly as Type does:
	// the former IDs of a renamed operation, whose activity rows keep the
	// spelling they were written under. Without them a ?type= filter splits a
	// renamed op's history in two -- the old spelling finds only rows from
	// before the rename, the new one only rows from after it. Ignored when Type
	// is empty. Stores read the set through TypeValues / acceptsType, never
	// Type alone.
	TypeAliases []string
	Tier        string
	Level       string
	OperationID string
	BookID      string
	Since       *time.Time
	Until       *time.Time
	Tags        []string
	// TagAliases makes one Tags entry an any-of group: an entry satisfies the
	// AND term for tag T when it carries T or any of TagAliases[T]. It exists
	// for def:<id> tags, which reporter_db.go writes with the op ID the run
	// used and never rewrites, so a renamed op's history carries two
	// spellings. A key that is not also in Tags is ignored. Stores read Tags
	// through TagTerms / acceptsTags, never Tags alone.
	TagAliases     map[string][]string
	Search         string   // LIKE %search% on summary
	Source         string   // show only this source
	ExcludeSources []string // hide these sources
	ExcludeTiers   []string // hide these tiers
	ExcludeTags    []string // hide entries that carry any of these tags
	// ExcludeTagAliases widens ExcludeTags the way TagAliases widens Tags: an
	// entry is hidden when it carries an ExcludeTags entry T or any of
	// ExcludeTagAliases[T]. It exists for def:<id> tags, so hiding a renamed
	// op hides both halves of its history. A key that is not also in
	// ExcludeTags is ignored. Stores read the set through ExcludeTagValues /
	// rejectsTags, never ExcludeTags alone.
	ExcludeTagAliases map[string][]string
	// ExcludeEntry, when set, hides every entry it returns true for. It is how a
	// read decides per caller what it may show without the page, offset and total
	// being counted before the hiding (the Repairs lane's operations are readable
	// only to callers who may use the fixer). It sees every field a filter reads
	// (tier, type, level, source, operation id, book id, summary, tags), so a
	// caller can refuse a row whose operation it cannot resolve but whose type or
	// tags say what it is; EntryOperationIDs lists the operations an entry belongs
	// to. It does NOT reliably see Details: the filter-index path decodes only the
	// fields it filters on and hands the predicate an entry whose Details is nil,
	// while the tier scan hands it the whole row. A predicate must therefore never
	// depend on Details, and a read that has to look inside Details (a daily
	// digest's items) does so on the page after the query, not here. The function
	// is called from the scan, so it must be cheap (memoise a lookup) and safe to
	// call from one goroutine at a time. Query, QueryWithPartial and GetDistinctSources
	// honour it; GetDistinctSources skips its result cache for a filter that
	// carries one (a function cannot be keyed). A non-nil value is a live predicate, so it refuses the id-index
	// pushdown (see pactPushdownDecidable): it needs the decoded row, and the
	// pushdown counts total from the index. A read on operation_id or book_id
	// alone still never walks a tier: queryByIndexPrefixExcluding decodes and
	// judges the rows of that one id as a stream, newest first, up to
	// activityQueryScanBudget decodes plus the page probe; an id with more rows
	// than that is answered from its newest rows and QueryWithPartial reports it
	// (ActivityQueryResult.Partial).
	// Every other read applies it to every candidate row of the scan.
	ExcludeEntry func(e ActivityEntry) bool
}

// TypeValues returns every type value f accepts: Type followed by TypeAliases,
// without empty strings or duplicates. Nil means f has no type predicate.
func (f ActivityFilter) TypeValues() []string {
	if f.Type == "" {
		return nil
	}
	out := []string{f.Type}
	for _, a := range f.TypeAliases {
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// acceptsType reports whether an entry of type t passes f's type predicate.
// Same set as TypeValues, without allocating per row.
func (f ActivityFilter) acceptsType(t string) bool {
	if f.Type == "" || t == f.Type {
		return true
	}
	return t != "" && slices.Contains(f.TypeAliases, t)
}

// TagTerms returns f's required-tag predicate as AND-ed any-of groups: one
// group per Tags entry, holding that tag followed by its TagAliases, without
// empty aliases or duplicates. Group order follows Tags, so the result is
// deterministic. Nil means f requires no tag.
func (f ActivityFilter) TagTerms() [][]string {
	if len(f.Tags) == 0 {
		return nil
	}
	out := make([][]string, 0, len(f.Tags))
	for _, tag := range f.Tags {
		group := []string{tag}
		for _, a := range f.TagAliases[tag] {
			if a != "" && !slices.Contains(group, a) {
				group = append(group, a)
			}
		}
		out = append(out, group)
	}
	return out
}

// acceptsTags reports whether an entry carrying tags passes f's required-tag
// predicate: for every Tags entry, the entry carries it or one of its aliases.
// Same predicate as TagTerms, without allocating per row.
func (f ActivityFilter) acceptsTags(tags []string) bool {
	for _, tag := range f.Tags {
		if slices.Contains(tags, tag) {
			continue
		}
		if !slices.ContainsFunc(f.TagAliases[tag], func(a string) bool {
			return a != "" && slices.Contains(tags, a)
		}) {
			return false
		}
	}
	return true
}

// ExcludeTagValues returns every tag value f's exclusion hides: each
// ExcludeTags entry followed by its ExcludeTagAliases, without empty aliases
// or duplicates. Order follows ExcludeTags, so the result is deterministic.
// Exclusion is one any-of set (a row carrying ANY value is hidden), so the
// flat list is the whole predicate. Nil means f excludes no tag.
func (f ActivityFilter) ExcludeTagValues() []string {
	if len(f.ExcludeTags) == 0 {
		return nil
	}
	out := make([]string, 0, len(f.ExcludeTags))
	for _, tag := range f.ExcludeTags {
		// The literal tag is kept even when empty, as ExcludeTags always
		// was; only empty ALIASES are dropped.
		if !slices.Contains(out, tag) {
			out = append(out, tag)
		}
		for _, a := range f.ExcludeTagAliases[tag] {
			if a != "" && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}

// rejectsTags reports whether an entry carrying tags is hidden by f's
// exclusion: it carries some ExcludeTags entry or one of that entry's aliases.
// Same predicate as ExcludeTagValues, without allocating per row.
func (f ActivityFilter) rejectsTags(tags []string) bool {
	for _, tag := range f.ExcludeTags {
		if slices.Contains(tags, tag) {
			return true
		}
		if slices.ContainsFunc(f.ExcludeTagAliases[tag], func(a string) bool {
			return a != "" && slices.Contains(tags, a)
		}) {
			return true
		}
	}
	return false
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

// DigestTier and DailyDigestType are the tier and type of the row CompactByDay
// writes for each day it folds. The row names no operation of its own; what it
// folded sits in Details["items"] (see DigestItem).
const (
	DigestTier      = "digest"
	DailyDigestType = "daily_digest"
)

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

	// AggregatesVersion is DigestAggregatesVersion for a digest whose every row
	// is accounted for in OpAggregates and NoOpAggregate, and 0 for one that is
	// not: a digest compacted before 2026-10-30, one an older build added rows
	// to, or one whose day named more operations than maxDigestOpAggregates. The
	// aggregates above (Counts, OriginalCount, TagCounts) cover every row of the
	// day, so a reader who may not read some of those rows can only subtract
	// their share when the share is known: at version 0 it is not.
	AggregatesVersion int `json:"aggregates_version,omitempty"`
	// OpAggregates is each operation's share of the aggregates above, by
	// operation id; NoOpAggregate is the share of rows that name no operation.
	// They sum to Counts, OriginalCount and TagCounts exactly. They are for the
	// server's own use (activity redactDigest) and are never sent to a client.
	OpAggregates  map[string]*DigestOpAggregate `json:"op_aggregates,omitempty"`
	NoOpAggregate *DigestOpAggregate            `json:"no_op_aggregate,omitempty"`
	// AggregatesWithheld is set on the copy served to a caller who may not read
	// every row the day folded and for whom the share of the rows they may not
	// read is unknown (AggregatesVersion 0): Counts, OriginalCount and TagCounts
	// are then omitted rather than served with hidden rows in them. Never stored.
	AggregatesWithheld bool `json:"aggregates_withheld,omitempty"`
}

// DigestAggregatesVersion is the AggregatesVersion of a digest that carries the
// per-operation aggregates for every row it folded.
const DigestAggregatesVersion = 1

// maxDigestOpAggregates caps how many distinct operations one digest keeps an
// aggregate for. Each is a few hundred bytes of JSON in a row read for every
// page that includes the digest, so a day that names more operations than this
// stores none (AggregatesVersion 0) rather than an unbounded row.
const maxDigestOpAggregates = 1000

// DigestAggregate is a count of folded rows: in total, per entry type, and per
// tag value in the action: and source: namespaces (the same shape as
// DigestDetails.Counts and DigestDetails.TagCounts).
type DigestAggregate struct {
	Count     int                       `json:"count"`
	Counts    map[string]int            `json:"counts,omitempty"`
	TagCounts map[string]map[string]int `json:"tag_counts,omitempty"`
}

// DigestOpAggregate is the share of a digest's aggregates that came from the
// rows of one operation (or, for NoOpAggregate, from rows that name none).
// RepairsLike is the part of it from rows whose own type, or whose def: tag,
// names a Repairs definition (EntryLooksRepairs): a reader decides an
// operation it cannot find a record for by those, so they can be hidden when
// the operation as a whole is not.
type DigestOpAggregate struct {
	DigestAggregate
	RepairsLike *DigestAggregate `json:"repairs_like,omitempty"`
}

// repairsDefIDs are the definitions of the Repairs lane: repairs.plan and
// repairs.apply (internal/repairs owns the names; handlers.TestEntryLooksRepairs_MatchesTheHandlersJudgement
// pins these to them, since this package cannot import it).
var repairsDefIDs = [...]string{"repairs.plan", "repairs.apply"}

// digestDefTagPrefix is the tag every operation activity row carries to name its
// definition (reporter_db.go).
const digestDefTagPrefix = "def:"

// EntryLooksRepairs reports whether an entry says it belongs to the Repairs
// lane: its type, or its def: tag, names repairs.plan or repairs.apply. It is
// all that is left to go on for an operation no record exists for.
func EntryLooksRepairs(e ActivityEntry) bool {
	if slices.Contains(repairsDefIDs[:], e.Type) {
		return true
	}
	for _, t := range e.Tags {
		if def, ok := strings.CutPrefix(t, digestDefTagPrefix); ok && slices.Contains(repairsDefIDs[:], def) {
			return true
		}
	}
	return false
}

// digestTagNamespaces are the tag namespaces TagCounts aggregates.
var digestTagNamespaces = [...]string{"action", "source"}

// addTagCounts adds one entry's (or item's) action: and source: tags to into.
func addTagCounts(into map[string]map[string]int, tags []string) {
	for _, tag := range tags {
		ns, val, ok := strings.Cut(tag, ":")
		if !ok || ns == "" || !slices.Contains(digestTagNamespaces[:], ns) {
			continue
		}
		if into[ns] == nil {
			into[ns] = make(map[string]int)
		}
		into[ns][val]++
	}
}

// add folds one entry into the aggregate.
func (a *DigestAggregate) add(e ActivityEntry) {
	a.Count++
	if a.Counts == nil {
		a.Counts = make(map[string]int)
	}
	a.Counts[e.Type]++
	if a.TagCounts == nil {
		a.TagCounts = make(map[string]map[string]int)
	}
	addTagCounts(a.TagCounts, e.Tags)
}

// Merge adds o into a.
func (a *DigestAggregate) Merge(o *DigestAggregate) {
	if o == nil {
		return
	}
	a.Count += o.Count
	if a.Counts == nil && len(o.Counts) > 0 {
		a.Counts = make(map[string]int, len(o.Counts))
	}
	for k, v := range o.Counts {
		a.Counts[k] += v
	}
	if a.TagCounts == nil && len(o.TagCounts) > 0 {
		a.TagCounts = make(map[string]map[string]int, len(o.TagCounts))
	}
	for ns, m := range o.TagCounts {
		if a.TagCounts[ns] == nil {
			a.TagCounts[ns] = make(map[string]int, len(m))
		}
		for k, v := range m {
			a.TagCounts[ns][k] += v
		}
	}
}

// add folds one entry into the operation's aggregate.
func (a *DigestOpAggregate) add(e ActivityEntry) {
	a.DigestAggregate.add(e)
	if EntryLooksRepairs(e) {
		if a.RepairsLike == nil {
			a.RepairsLike = &DigestAggregate{}
		}
		a.RepairsLike.add(e)
	}
}

// Merge adds o into a.
func (a *DigestOpAggregate) merge(o *DigestOpAggregate) {
	if o == nil {
		return
	}
	a.DigestAggregate.Merge(&o.DigestAggregate)
	if o.RepairsLike != nil {
		if a.RepairsLike == nil {
			a.RepairsLike = &DigestAggregate{}
		}
		a.RepairsLike.Merge(o.RepairsLike)
	}
}

// Subtract removes o's share from a: counts that reach zero drop out, and a
// count that would go below zero stops at zero (a digest whose aggregates were
// edited after compaction cannot make a reader's numbers negative).
func (a *DigestAggregate) Subtract(o *DigestAggregate) {
	if o == nil {
		return
	}
	a.Count = max(0, a.Count-o.Count)
	for k, v := range o.Counts {
		if left := a.Counts[k] - v; left > 0 {
			a.Counts[k] = left
		} else {
			delete(a.Counts, k)
		}
	}
	for ns, m := range o.TagCounts {
		for k, v := range m {
			if left := a.TagCounts[ns][k] - v; left > 0 {
				a.TagCounts[ns][k] = left
			} else if a.TagCounts[ns] != nil {
				delete(a.TagCounts[ns], k)
			}
		}
		if len(a.TagCounts[ns]) == 0 {
			delete(a.TagCounts, ns)
		}
	}
}

// aggregateScratch is the per-operation accounting of one digest being built.
type aggregateScratch struct {
	ops  map[string]*DigestOpAggregate
	noOp *DigestOpAggregate
	tags map[string]map[string]int
	// unattributable: the rows cannot all be filed under one operation each, so
	// no per-operation aggregate is stored (the digest stays at AggregatesVersion
	// 0). Either a row belongs to several operations (a Summarize row, which a
	// reader judges hidden when any operation it names is) or the day named more
	// than maxDigestOpAggregates operations.
	unattributable bool
}

func newAggregateScratch() *aggregateScratch {
	return &aggregateScratch{ops: map[string]*DigestOpAggregate{}, tags: map[string]map[string]int{}}
}

// add files e under the operation it belongs to, by EntryOperationIDs, the same
// membership the activity reader uses to decide whether a row is hidden: a
// Summarize row names its operations in op: tags, not in OperationID, so
// filing by OperationID alone would put a row a reader hides under "no
// operation" and leave its share in the totals served to that reader.
func (as *aggregateScratch) add(e ActivityEntry) {
	addTagCounts(as.tags, e.Tags)
	if as.unattributable {
		return
	}
	var id string
	n := 0
	EntryOperationIDs(e, func(x string) bool {
		id = x
		n++
		return false
	})
	switch {
	case n > 1:
		as.unattributable = true
		return
	case n == 0:
		if as.noOp == nil {
			as.noOp = &DigestOpAggregate{}
		}
		as.noOp.add(e)
		return
	}
	a := as.ops[id]
	if a == nil {
		if len(as.ops) >= maxDigestOpAggregates {
			as.unattributable = true
			return
		}
		a = &DigestOpAggregate{}
		as.ops[id] = a
	}
	a.add(e)
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

// rederiveLegacyItem re-derives a legacy item's type, tier and tags from its
// message (what enrichment would have given the row). The message cannot say
// which operation the row belonged to, or that it belonged to the Repairs lane,
// so what the item already states of that survives: a repairs.plan /
// repairs.apply type is kept, and the item's existing def: and op: tags are kept
// beside the derived tags. Without that, an item of an operation whose record is
// gone would stop looking like a Repairs row, and a Summarize row would stop
// naming the operations it spans; no reader could tell either from any other.
func rederiveLegacyItem(item DigestItem) DigestItem {
	derivedType, derivedTier := deriveTypeFromMessage(item.Summary, "")
	derivedTags := enrichLegacyLogTags(item.Summary, "", "info")
	if slices.Contains(repairsDefIDs[:], item.Type) {
		derivedType = item.Type
	}
	for _, t := range item.Tags {
		if (strings.HasPrefix(t, digestDefTagPrefix) || strings.HasPrefix(t, "op:")) && !slices.Contains(derivedTags, t) {
			derivedTags = append(derivedTags, t)
		}
	}
	item.Type, item.Tier, item.Tags = derivedType, derivedTier, derivedTags
	return item
}

// DigestItemSource is the Source a digest item's row had, as far as operation
// membership goes. An item keeps no Source, but only Summarize rows carry op:
// tags, so an item with such a tag is the Summarize row it came from, whose
// operations are the ones those tags name (EntryOperationIDs). The writer files
// aggregates by it and the activity reader judges items by it, so the two agree.
func DigestItemSource(tags []string) string {
	if slices.ContainsFunc(tags, func(t string) bool { return strings.HasPrefix(t, "op:") }) {
		return summarizeSource
	}
	return ""
}

// digestItemAsEntry is the activity row a digest item stood for, as far as
// attribution goes.
func digestItemAsEntry(item DigestItem) ActivityEntry {
	return ActivityEntry{Type: item.Type, OperationID: item.OperationID, Tags: item.Tags, Source: DigestItemSource(item.Tags)}
}

// summarizeOpSampleMax caps how many operation ids a Summarize row keeps.
//
// Summarize groups by (day, type, source) and NEVER by operation_id. An
// operation id is unique per run, so grouping by it made the pass write one
// summary row per operation: a day with thousands of operations became
// thousands of rows, which is the opposite of summarizing. The ids are still
// worth keeping for tracing, so each summary records how many distinct
// operations it covers plus the lowest summarizeOpSampleMax of them (sorted, so
// the sample is deterministic), and says when the sample is cut.
const summarizeOpSampleMax = 20

// summarizeGroupKey is the Summarize group key.
type summarizeGroupKey struct{ day, typ, source string }

// summarizeDetails renders the Details map of one Summarize row. opIDs is the
// group's set of distinct non-empty operation ids.
//
// Keys:
//   - summarized_source: the Source the folded entries had (the row's own
//     Source is "summarize", which is what marks it as a summary).
//   - distinct_ops:      how many distinct operations the group covered.
//   - op_ids_sample:     at most summarizeOpSampleMax ids, lowest first.
//   - op_ids_truncated:  true when distinct_ops exceeds the sample.
//
// Summary rows written before 2026-09-19 have no Details and one OperationID;
// nothing reads these keys as required, so both shapes stay readable.
//
// It also returns the "op:<id>" tags for the same sample (summarizeOpTags).
func summarizeDetails(source string, opIDs map[string]struct{}) (map[string]any, []string) {
	ids := make([]string, 0, len(opIDs))
	for id := range opIDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	sample := ids
	if len(sample) > summarizeOpSampleMax {
		sample = sample[:summarizeOpSampleMax]
	}
	details := map[string]any{
		"summarized_source": source,
		"distinct_ops":      len(ids),
		"op_ids_sample":     append([]string{}, sample...),
		"op_ids_truncated":  len(ids) > len(sample),
	}
	return details, summarizeOpTags(ids)
}

// summarizeTestHooks lets tests stop a Summarize pass at its commit
// boundaries. Nil in production.
//
//   - afterChunk runs after each committed delete chunk (the first one carries
//     the summary row), so a test can cancel ctx between chunks.
var summarizeTestHooks struct {
	afterChunk func()
}

// summarizeOpTags renders a summary's sampled operation ids as "op:<id>" tags,
// the same tag EnrichTags puts on every row that has an operation id. That is
// what makes a summarized operation findable: the op: tag chip filter matches
// it directly, and every backend's ?operation_id= filter accepts a summary row
// carrying the tag (see entryHasOperation).
func summarizeOpTags(sortedIDs []string) []string {
	if len(sortedIDs) > summarizeOpSampleMax {
		sortedIDs = sortedIDs[:summarizeOpSampleMax]
	}
	if len(sortedIDs) == 0 {
		return nil
	}
	tags := make([]string, len(sortedIDs))
	for i, id := range sortedIDs {
		tags[i] = "op:" + id
	}
	return tags
}

// summarizeSource marks a Summarize row.
const summarizeSource = "summarize"

// EntryOperationIDs calls visit with each operation e belongs to until it
// returns true: e's own OperationID, then, for a Summarize row only (which spans
// many operations), the id of each op:<id> tag. It reports whether visit
// returned true. Same membership as entryHasOperation.
func EntryOperationIDs(e ActivityEntry, visit func(id string) bool) bool {
	if e.OperationID != "" && visit(e.OperationID) {
		return true
	}
	if e.Source != summarizeSource {
		return false
	}
	for _, t := range e.Tags {
		if id, ok := strings.CutPrefix(t, "op:"); ok && id != "" && id != e.OperationID && visit(id) {
			return true
		}
	}
	return false
}

// rejectsEntry reports whether f's ExcludeEntry hides e.
func (f ActivityFilter) rejectsEntry(e ActivityEntry) bool {
	return f.ExcludeEntry != nil && f.ExcludeEntry(e)
}

// entryHasOperation reports whether e answers an operation_id filter for id:
// its own OperationID, or — for a Summarize row only, which spans many
// operations and has none of its own — an "op:<id>" tag from its sample.
func entryHasOperation(e ActivityEntry, id string) bool {
	if e.OperationID == id {
		return true
	}
	if e.Source != summarizeSource {
		return false
	}
	want := "op:" + id
	return slices.Contains(e.Tags, want)
}

// mergeAggregates folds the aggregates of the digest already stored for the day
// (existing) into dd, the digest of the rows being added to it. A stored digest
// without per-operation aggregates (AggregatesVersion 0) cannot say which of its
// rows belong to which operation, so the merged digest does not either. Counts,
// OriginalCount and the items are merged by the caller.
func (dd *DigestDetails) mergeAggregates(existing DigestDetails) {
	if existing.AggregatesVersion == 0 || dd.AggregatesVersion == 0 {
		dd.AggregatesVersion = 0
		dd.OpAggregates = nil
		dd.NoOpAggregate = nil
	} else {
		if dd.OpAggregates == nil {
			dd.OpAggregates = map[string]*DigestOpAggregate{}
		}
		for id, a := range existing.OpAggregates {
			into := dd.OpAggregates[id]
			if into == nil {
				if len(dd.OpAggregates) >= maxDigestOpAggregates {
					dd.AggregatesVersion = 0
					dd.OpAggregates = nil
					dd.NoOpAggregate = nil
					break
				}
				into = &DigestOpAggregate{}
				dd.OpAggregates[id] = into
			}
			into.merge(a)
		}
		if dd.AggregatesVersion != 0 && existing.NoOpAggregate != nil {
			if dd.NoOpAggregate == nil {
				dd.NoOpAggregate = &DigestOpAggregate{}
			}
			dd.NoOpAggregate.merge(existing.NoOpAggregate)
		}
		if len(dd.OpAggregates) == 0 {
			dd.OpAggregates = nil
		}
	}
	if len(existing.TagCounts) > 0 {
		if dd.TagCounts == nil {
			dd.TagCounts = map[string]map[string]int{}
		}
		(&DigestAggregate{TagCounts: dd.TagCounts}).Merge(&DigestAggregate{TagCounts: existing.TagCounts})
	}
}
