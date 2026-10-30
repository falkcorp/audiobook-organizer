// file: internal/server/handlers/activity_digest_aggregates.go
// version: 1.0.0
// guid: 6e1b8a43-9d27-4c05-b3f8-a05c7e2d94b1
// last-edited: 2026-10-30

package handlers

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A daily digest states counts (per entry type), original_count and tag_counts
// over every row of the day, and its summary names the total. Those rows include
// the rows of Repairs operations the caller may not read, so serving the numbers
// as stored would say how much a hidden run did even with its items redacted.
//
// Two shapes of digest, by database.DigestDetails.AggregatesVersion:
//
//   - Version 1 (compacted since 2026-10-30): CompactByDay stored each
//     operation's share of those numbers, so the share of every operation the
//     caller may not read is subtracted and the rest is served exactly. The
//     verdict for an operation is the one a row gets (opVisibility.hidesRepairs):
//     the whole share of an operation whose record the caller may not read, and
//     only the share of rows that look like the Repairs lane (their own type or
//     def: tag) for an operation no record can be read for.
//   - Version 0 (older, or one a day with too many operations spilled): the share
//     of each operation is not known. The numbers are served only to a caller who
//     cannot be hiding anything from (they may read every Repairs operation, or
//     the digest's items are every row of the day and none of them is hidden), and
//     are otherwise left out, with aggregates_withheld set so a page can say so.
//
// Rows hidden only because their text names other users (activity.NamesOthersUserData)
// are not subtracted: that judgement reads each row's text, which an aggregate
// does not keep. They are what the activity feed's other-users rule covers, not
// the Repairs rule.

// digestAggregateKeys are the Details keys that carry the totals.
var digestAggregateKeys = []string{"counts", "original_count", "tag_counts", "truncated", "truncated_count"}

// redactDigestAggregates rewrites details (the served copy of e.Details) so its
// totals and e's summary (rewritten in place) describe only what the caller may read. keptItems is how
// many items the served copy keeps, and hidItem whether any item of the stored
// digest belongs to a Repairs operation the caller may not read.
func (v *opVisibility) redactDigestAggregates(e *database.ActivityEntry, details map[string]any, keptItems int, hidItem bool) {
	dd, decoded := digestDetailsOf(e.Details)
	// Never sent to a client: they exist for this check.
	delete(details, "op_aggregates")
	delete(details, "no_op_aggregate")
	delete(details, "aggregates_version")
	if !decoded {
		v.withholdAggregates(e, details, dd)
		return
	}
	if dd.AggregatesVersion == 0 {
		if !v.mayReadLegacyAggregates(dd, hidItem) {
			v.withholdAggregates(e, details, dd)
		}
		return
	}

	hidden := v.hiddenShare(dd)
	if hidden.Count == 0 {
		return
	}
	shown := &database.DigestAggregate{
		Count:     dd.OriginalCount,
		Counts:    maps.Clone(dd.Counts),
		TagCounts: cloneTagCounts(dd.TagCounts),
	}
	shown.Subtract(hidden)
	details["original_count"] = shown.Count
	details["counts"] = nonNilCounts(shown.Counts)
	if len(shown.TagCounts) > 0 {
		details["tag_counts"] = shown.TagCounts
	} else {
		delete(details, "tag_counts")
	}
	// truncated_count is the rows without an item; stated against the stored total
	// it would give the hidden rows' number away.
	if left := shown.Count - keptItems; left > 0 {
		details["truncated"] = true
		details["truncated_count"] = left
	} else {
		delete(details, "truncated")
		delete(details, "truncated_count")
	}
	e.Summary = digestSummary(*e, dd.Date, shown.Count)
}

// hiddenShare is the part of the digest's totals that came from rows the caller
// may not read.
func (v *opVisibility) hiddenShare(dd database.DigestDetails) *database.DigestAggregate {
	hidden := &database.DigestAggregate{}
	for id, a := range dd.OpAggregates {
		if a == nil {
			continue
		}
		if v.resolve {
			if r, err := v.lookup(id); err == nil && r.known {
				if !r.ok {
					hidden.Merge(&a.DigestAggregate)
				}
				continue
			}
		}
		// No record, a record that cannot be read, or no way to look: the rows'
		// own type and def: tag decide, as for an activity row.
		hidden.Merge(a.RepairsLike)
	}
	if dd.NoOpAggregate != nil {
		hidden.Merge(dd.NoOpAggregate.RepairsLike)
	}
	return hidden
}

// mayReadLegacyAggregates says whether a digest without per-operation aggregates
// may state its totals to this caller.
func (v *opVisibility) mayReadLegacyAggregates(dd database.DigestDetails, hidItem bool) bool {
	if v.reader.MayReadEveryRepairsOp() {
		return true
	}
	complete := !dd.Truncated && dd.OriginalCount == len(dd.Items)
	return complete && !hidItem
}

// withholdAggregates takes the totals out of the served copy and says so.
func (v *opVisibility) withholdAggregates(e *database.ActivityEntry, details map[string]any, dd database.DigestDetails) {
	for _, k := range digestAggregateKeys {
		delete(details, k)
	}
	details["aggregates_withheld"] = true
	e.Summary = digestSummary(*e, dd.Date, -1)
}

// digestSummary is the summary of a digest whose totals were rewritten: it names
// the day (the digest's own date, else the day of its timestamp, so a digest
// whose details are gone or carry no date still has its stored summary replaced)
// and, when count is not negative, the total. The stored summary is never kept:
// it names the unredacted total.
func digestSummary(e database.ActivityEntry, date string, count int) string {
	if date == "" && !e.Timestamp.IsZero() {
		date = e.Timestamp.UTC().Format("2006-01-02")
	}
	out := "Daily digest"
	if date != "" {
		out += " for " + date
	}
	if count >= 0 {
		out += fmt.Sprintf(" (%d entries)", count)
	}
	return out
}

// withholdDigestWithoutDetails is redactDigest for a digest that has no details
// at all: its stored summary still states the day's total, and nothing can say
// which of those rows the caller may read, so the summary is replaced by one
// that states none, unless the caller may read every Repairs operation.
func (v *opVisibility) withholdDigestWithoutDetails(e database.ActivityEntry) database.ActivityEntry {
	if v.reader.MayReadEveryRepairsOp() {
		return e
	}
	e.Summary = digestSummary(e, "", -1)
	e.Details = map[string]any{"aggregates_withheld": true}
	return e
}

// digestDetailsOf reads a digest's details: false when they cannot be read.
func digestDetailsOf(details map[string]any) (database.DigestDetails, bool) {
	var dd database.DigestDetails
	b, err := json.Marshal(details)
	if err != nil {
		return dd, false
	}
	if err := json.Unmarshal(b, &dd); err != nil {
		return dd, false
	}
	return dd, true
}

func cloneTagCounts(in map[string]map[string]int) map[string]map[string]int {
	if in == nil {
		return nil
	}
	out := make(map[string]map[string]int, len(in))
	for ns, m := range in {
		out[ns] = maps.Clone(m)
	}
	return out
}

func nonNilCounts(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}
