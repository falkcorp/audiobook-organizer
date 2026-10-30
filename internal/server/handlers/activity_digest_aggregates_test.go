// file: internal/server/handlers/activity_digest_aggregates_test.go
// version: 1.0.0
// guid: 9aeeb003-2207-4edc-b631-05185533f65f
// last-edited: 2026-10-30

package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// A daily digest states counts, original_count, tag_counts and a summary that
// total every row of the day, the rows of Repairs runs the caller may not read
// included. These pin what a caller is served: a digest that stored each
// operation's share (aggregates_version 1) is served with the shares of the
// operations the caller may not read subtracted; an older digest, whose shares
// are unknown, has its totals left out and says so unless nothing can be hidden
// from the caller.

type restrictedFixer struct{ repairs.Fixer }

// needs users.manage, so an editor may not read a run of it.
func (restrictedFixer) RequiredPermissions() []auth.Permission {
	return []auth.Permission{auth.PermUsersManage}
}

const (
	aggScanOp       = "op-agg-scan"
	aggPlainOp      = "op-agg-plain-apply"
	aggRestrictedOp = "op-agg-restricted-apply"
	aggDay          = "2026-10-01"
	aggDigestID     = "DIGEST-AGG-ROW"
)

// everyPerm holds every permission there is: the caller MayReadEveryRepairsOp.
var everyPerm = strings.Join(auth.All(), ",")

func aggAgg(n int, counts map[string]int, source map[string]int) *database.DigestAggregate {
	return &database.DigestAggregate{Count: n, Counts: counts, TagCounts: map[string]map[string]int{"source": source}}
}

// versionOneDetails is a compacted day of 14 rows: 3 of a library scan, 4 of a
// Repairs run a library editor may read, 5 of one that needs users.manage, and 2
// naming no operation, one of which looks like the Repairs lane.
func versionOneDetails() database.DigestDetails {
	return database.DigestDetails{
		Date:          aggDay,
		OriginalCount: 14,
		Counts:        map[string]int{"system": 6, repairs.ApplyOpID: 8},
		TagCounts:     map[string]map[string]int{"source": {"scheduler": 3, "engine": 9, "misc": 2}},
		Items: []database.DigestItem{
			{Type: "system", OperationID: aggScanOp, Summary: "AGG-SCAN-ITEM"},
			{Type: repairs.ApplyOpID, OperationID: aggPlainOp, Summary: "AGG-PLAIN-ITEM"},
			{Type: repairs.ApplyOpID, OperationID: aggRestrictedOp, Summary: "AGG-RESTRICTED-ITEM"},
			{Type: "system", Summary: "AGG-NOOP-SYSTEM-ITEM"},
			{Type: repairs.ApplyOpID, Summary: "AGG-NOOP-REPAIRS-ITEM"},
		},
		Truncated:         true,
		TruncatedCount:    9,
		AggregatesVersion: database.DigestAggregatesVersion,
		OpAggregates: map[string]*database.DigestOpAggregate{
			aggScanOp: {DigestAggregate: *aggAgg(3, map[string]int{"system": 3}, map[string]int{"scheduler": 3})},
			aggPlainOp: {
				DigestAggregate: *aggAgg(4, map[string]int{repairs.ApplyOpID: 2, "system": 2}, map[string]int{"engine": 4}),
				RepairsLike:     aggAgg(2, map[string]int{repairs.ApplyOpID: 2}, map[string]int{"engine": 2}),
			},
			aggRestrictedOp: {
				DigestAggregate: *aggAgg(5, map[string]int{repairs.ApplyOpID: 5}, map[string]int{"engine": 5}),
				RepairsLike:     aggAgg(5, map[string]int{repairs.ApplyOpID: 5}, map[string]int{"engine": 5}),
			},
		},
		NoOpAggregate: &database.DigestOpAggregate{
			DigestAggregate: *aggAgg(2, map[string]int{"system": 1, repairs.ApplyOpID: 1}, map[string]int{"misc": 2}),
			RepairsLike:     aggAgg(1, map[string]int{repairs.ApplyOpID: 1}, map[string]int{"misc": 1}),
		},
	}
}

// legacyDetails is the same day compacted before the shares were stored.
func legacyDetails() database.DigestDetails {
	dd := versionOneDetails()
	dd.AggregatesVersion, dd.OpAggregates, dd.NoOpAggregate = 0, nil, nil
	return dd
}

func digestRow(t *testing.T, dd database.DigestDetails) database.ActivityEntry {
	t.Helper()
	b, err := json.Marshal(dd)
	require.NoError(t, err)
	var details map[string]any
	require.NoError(t, json.Unmarshal(b, &details))
	return database.ActivityEntry{
		Tier: database.DigestTier, Type: database.DailyDigestType,
		Summary: "Daily digest for " + dd.Date + " (14 entries)", Details: details,
	}
}

func digestRouter(entries ...database.ActivityEntry) *gin.Engine {
	gin.SetMode(gin.TestMode)
	rows := v2Rows{
		aggScanOp:       {ID: aggScanOp, DefID: "library.scan"},
		aggPlainOp:      {ID: aggPlainOp, DefID: repairs.ApplyOpID, Params: `{"fixer_id":"plain.fixer"}`},
		aggRestrictedOp: {ID: aggRestrictedOp, DefID: repairs.ApplyOpID, Params: `{"fixer_id":"restricted.fixer"}`},
	}
	holds := func(c *gin.Context, p auth.Permission) bool { return auth.Can(c.Request.Context(), p) }
	h := NewActivityHandler(&fakeActivityLog{entries: entries}, opLogs{},
		WithActivityRepairsAccess(opaccess.NewRepairs(activityFixers{
			"plain.fixer": plainFixer{}, "restricted.fixer": restrictedFixer{},
		}, rows, holds), rows, nil),
		WithActivityOthersUserData(func(c *gin.Context) bool { return auth.Can(c.Request.Context(), auth.PermUsersManage) }),
	)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), strings.Split(c.GetHeader("X-Perms"), ",")))
		c.Next()
	})
	r.GET("/activity", h.ListActivity)
	return r
}

type servedDigest struct {
	Summary string `json:"summary"`
	Details struct {
		OriginalCount      int                       `json:"original_count"`
		Counts             map[string]int            `json:"counts"`
		TagCounts          map[string]map[string]int `json:"tag_counts"`
		Truncated          bool                      `json:"truncated"`
		TruncatedCount     int                       `json:"truncated_count"`
		AggregatesWithheld bool                      `json:"aggregates_withheld"`
		Items              []struct {
			Summary string `json:"summary"`
		} `json:"items"`
	} `json:"details"`
}

func readDigest(t *testing.T, r http.Handler, perms string) (servedDigest, string) {
	t.Helper()
	w := userDataDo(r, http.MethodGet, "/activity", perms, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Entries []servedDigest `json:"entries"`
		Data    struct {
			Entries []servedDigest `json:"entries"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	entries := body.Entries
	if len(entries) == 0 {
		entries = body.Data.Entries
	}
	require.Len(t, entries, 1, w.Body.String())
	for _, key := range []string{"op_aggregates", "no_op_aggregate", "aggregates_version"} {
		require.NotContains(t, w.Body.String(), key, "%s as %s: an internal field was sent to the client", key, perms)
	}
	return entries[0], w.Body.String()
}

func itemSummaries(d servedDigest) []string {
	var out []string
	for _, it := range d.Details.Items {
		out = append(out, it.Summary)
	}
	return out
}

func TestActivity_DigestTotalsDescribeOnlyWhatTheCallerMayRead(t *testing.T) {
	r := digestRouter(digestRow(t, versionOneDetails()))

	// admin: reads both runs; only the no-operation row that looks like the
	// Repairs lane is gone (nothing can say whose it is).
	d, _ := readDigest(t, r, adminUsers)
	assert.Equal(t, 13, d.Details.OriginalCount)
	assert.Equal(t, map[string]int{"system": 6, repairs.ApplyOpID: 7}, d.Details.Counts)
	assert.Equal(t, map[string]map[string]int{"source": {"scheduler": 3, "engine": 9, "misc": 1}}, d.Details.TagCounts)
	assert.Equal(t, "Daily digest for "+aggDay+" (13 entries)", d.Summary)
	assert.False(t, d.Details.AggregatesWithheld)
	assert.ElementsMatch(t, []string{"AGG-SCAN-ITEM", "AGG-PLAIN-ITEM", "AGG-RESTRICTED-ITEM", "AGG-NOOP-SYSTEM-ITEM"}, itemSummaries(d))
	assert.True(t, d.Details.Truncated)
	assert.Equal(t, 9, d.Details.TruncatedCount, "13 rows shown, 4 of them as items")

	// editor: may not read the users.manage run.
	d, body := readDigest(t, r, editorPerms)
	assert.Equal(t, 8, d.Details.OriginalCount)
	assert.Equal(t, map[string]int{"system": 6, repairs.ApplyOpID: 2}, d.Details.Counts)
	assert.Equal(t, map[string]map[string]int{"source": {"scheduler": 3, "engine": 4, "misc": 1}}, d.Details.TagCounts)
	assert.Equal(t, "Daily digest for "+aggDay+" (8 entries)", d.Summary)
	assert.ElementsMatch(t, []string{"AGG-SCAN-ITEM", "AGG-PLAIN-ITEM", "AGG-NOOP-SYSTEM-ITEM"}, itemSummaries(d))
	assert.Equal(t, 5, d.Details.TruncatedCount)
	assert.NotContains(t, body, "AGG-RESTRICTED-ITEM")

	// viewer: reads neither run, so no Repairs count is left at all.
	d, _ = readDigest(t, r, viewerPerms)
	assert.Equal(t, 4, d.Details.OriginalCount)
	assert.Equal(t, map[string]int{"system": 4}, d.Details.Counts, "a type with no row left is not listed")
	assert.Equal(t, map[string]map[string]int{"source": {"scheduler": 3, "misc": 1}}, d.Details.TagCounts)
	assert.Equal(t, "Daily digest for "+aggDay+" (4 entries)", d.Summary)
	assert.ElementsMatch(t, []string{"AGG-SCAN-ITEM", "AGG-NOOP-SYSTEM-ITEM"}, itemSummaries(d))
	assert.Equal(t, 2, d.Details.TruncatedCount)
}

// A digest that stored no shares says nothing about its totals to a caller who
// may not read every Repairs run, and everything to one who may.
func TestActivity_LegacyDigestTotalsAreWithheldFromACallerWhoMayNotReadEveryRun(t *testing.T) {
	r := digestRouter(digestRow(t, legacyDetails()))

	for _, perms := range []string{viewerPerms, editorPerms, adminUsers} {
		d, body := readDigest(t, r, perms)
		assert.True(t, d.Details.AggregatesWithheld, perms)
		assert.Zero(t, d.Details.OriginalCount, perms)
		assert.Nil(t, d.Details.Counts, perms)
		assert.Nil(t, d.Details.TagCounts, perms)
		assert.False(t, d.Details.Truncated, perms)
		assert.Zero(t, d.Details.TruncatedCount, perms)
		assert.Equal(t, "Daily digest for "+aggDay, d.Summary, "%s: the summary still names the total", perms)
		assert.NotContains(t, body, "14", "%s: the stored total leaked: %s", perms, body)
	}

	d, _ := readDigest(t, r, everyPerm)
	assert.False(t, d.Details.AggregatesWithheld)
	assert.Equal(t, 14, d.Details.OriginalCount)
	assert.Equal(t, map[string]int{"system": 6, repairs.ApplyOpID: 8}, d.Details.Counts)
	assert.Equal(t, "Daily digest for "+aggDay+" (14 entries)", d.Summary)
}

// An old digest whose items are every row of the day, none of them a Repairs row
// the caller may not read, has nothing to hide: its totals are served as stored.
func TestActivity_LegacyDigestThatIsCompleteAndHidesNothingIsServedWhole(t *testing.T) {
	complete := database.DigestDetails{
		Date: aggDay, OriginalCount: 2, Counts: map[string]int{"system": 2},
		Items: []database.DigestItem{
			{Type: "system", OperationID: aggScanOp, Summary: "AGG-SCAN-ITEM"},
			{Type: "system", Summary: "AGG-NOOP-SYSTEM-ITEM"},
		},
	}
	r := digestRouter(digestRow(t, complete))
	d, _ := readDigest(t, r, viewerPerms)
	assert.False(t, d.Details.AggregatesWithheld)
	assert.Equal(t, 2, d.Details.OriginalCount)
	assert.Equal(t, map[string]int{"system": 2}, d.Details.Counts)

	// The same day with one of its rows belonging to a run the viewer may not read:
	// the total would include it, so it is withheld.
	complete.Items = append(complete.Items, database.DigestItem{Type: repairs.ApplyOpID, OperationID: aggPlainOp, Summary: "AGG-PLAIN-ITEM"})
	complete.OriginalCount = 3
	complete.Counts = map[string]int{"system": 2, repairs.ApplyOpID: 1}
	r = digestRouter(digestRow(t, complete))
	d, body := readDigest(t, r, viewerPerms)
	assert.True(t, d.Details.AggregatesWithheld)
	assert.Zero(t, d.Details.OriginalCount)
	assert.NotContains(t, body, "AGG-PLAIN-ITEM")

	// ...and the editor, who may read that run, is served the whole.
	d, _ = readDigest(t, r, editorPerms)
	assert.False(t, d.Details.AggregatesWithheld)
	assert.Equal(t, 3, d.Details.OriginalCount)
}

// Handler without the Repairs check wired: no operation can be looked up, so a
// version 1 digest drops the share of every row that looks like the Repairs lane
// (its own type or def: tag), for every caller, and keeps the rest.
func TestActivity_DigestWithoutAnOperationReaderDropsTheRepairsLookingShare(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewActivityHandler(&fakeActivityLog{entries: []database.ActivityEntry{digestRow(t, versionOneDetails())}}, opLogs{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), strings.Split(c.GetHeader("X-Perms"), ",")))
		c.Next()
	})
	r.GET("/activity", h.ListActivity)
	d, _ := readDigest(t, r, adminUsers)
	assert.Equal(t, 6, d.Details.OriginalCount, "14 rows less the 1+2+5 that look like Repairs rows")
	assert.Equal(t, map[string]int{"system": 6}, d.Details.Counts)
	assert.Equal(t, map[string]map[string]int{"source": {"scheduler": 3, "engine": 2, "misc": 1}}, d.Details.TagCounts)
	assert.Equal(t, "Daily digest for "+aggDay+" (6 entries)", d.Summary)
}

// The database package cannot import the handlers, so it holds its own copy of
// the Repairs-lane test CompactByDay uses to file a row under RepairsLike. The
// handlers judge a row by looksRepairs; a drift between them would make the
// stored share and the verdict disagree.
func TestEntryLooksRepairs_MatchesTheHandlersJudgement(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    database.ActivityEntry
	}{
		{"plan type", database.ActivityEntry{Type: repairs.PlanOpID}},
		{"apply type", database.ActivityEntry{Type: repairs.ApplyOpID}},
		{"plan def tag", database.ActivityEntry{Type: "system", Tags: []string{"def:" + repairs.PlanOpID}}},
		{"apply def tag", database.ActivityEntry{Type: "system", Tags: []string{"source:engine", "def:" + repairs.ApplyOpID}}},
		{"other def tag", database.ActivityEntry{Type: "system", Tags: []string{"def:library.scan"}}},
		{"similar type", database.ActivityEntry{Type: "repairs.applyx"}},
		{"plain system row", database.ActivityEntry{Type: "system"}},
		{"empty", database.ActivityEntry{}},
		{"def tag with no value", database.ActivityEntry{Tags: []string{"def:"}}},
		{"tag that only contains the id", database.ActivityEntry{Tags: []string{"note:def:" + repairs.ApplyOpID}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, looksRepairs(tc.e), database.EntryLooksRepairs(tc.e))
		})
	}
	assert.True(t, database.EntryLooksRepairs(database.ActivityEntry{Type: repairs.PlanOpID}))
	assert.True(t, database.EntryLooksRepairs(database.ActivityEntry{Type: repairs.ApplyOpID}))
}

// A digest row whose details are missing, or carry no date, still has a stored
// summary that states the day's total. It must not be served as it stands to a
// caller who could be hidden something: the details cannot say which rows were.
func TestActivity_DigestWithoutDetailsOrDateDoesNotServeTheStoredTotal(t *testing.T) {
	stamp := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	noDetails := database.ActivityEntry{
		Tier: database.DigestTier, Type: database.DailyDigestType, Timestamp: stamp,
		Summary: "Daily digest for 2026-10-01 (14 entries)",
	}
	r := digestRouter(noDetails)
	for _, perms := range []string{viewerPerms, editorPerms, adminUsers} {
		d, body := readDigest(t, r, perms)
		assert.Equal(t, "Daily digest for 2026-10-01", d.Summary, perms)
		assert.True(t, d.Details.AggregatesWithheld, perms)
		assert.NotContains(t, body, "14", "%s: the stored total leaked: %s", perms, body)
	}
	d, _ := readDigest(t, r, everyPerm)
	assert.Equal(t, "Daily digest for 2026-10-01 (14 entries)", d.Summary, "a caller who may read every run is served the row whole")
	assert.False(t, d.Details.AggregatesWithheld)

	// Details without a date: the day comes from the timestamp, for the withheld
	// legacy digest and for the rewritten version 1 one.
	for name, dd := range map[string]database.DigestDetails{"legacy": legacyDetails(), "version 1": versionOneDetails()} {
		dd.Date = ""
		row := digestRow(t, dd)
		row.Timestamp = stamp
		r := digestRouter(row)
		d, body := readDigest(t, r, viewerPerms)
		if name == "legacy" {
			assert.Equal(t, "Daily digest for 2026-10-01", d.Summary, name)
			assert.True(t, d.Details.AggregatesWithheld, name)
		} else {
			assert.Equal(t, "Daily digest for 2026-10-01 (4 entries)", d.Summary, name)
		}
		assert.NotContains(t, body, "14", "%s: the stored total leaked: %s", name, body)
	}
}

// A digest item keeps no Source, and a Summarize row names its operations in op:
// tags, not in OperationID. An item of a multi-operation Summarize row that names a
// Repairs run the caller may not read is left out, as the row itself would be, and
// the digest (which the writer leaves without shares for such a row) has its
// totals withheld from that caller. A caller who may read the run sees everything.
func TestActivity_DigestItemOfASummarizeRowNamingAHiddenRunIsLeftOut(t *testing.T) {
	dd := database.DigestDetails{
		Date: aggDay, OriginalCount: 4, Counts: map[string]int{"system": 4},
		Items: []database.DigestItem{
			{Type: "system", OperationID: aggScanOp, Summary: "AGG-SCAN-ITEM"},
			{Type: "system", Summary: "AGG-SUMMARIZE-MULTI-ITEM", Tags: []string{"op:" + aggScanOp, "op:" + aggRestrictedOp}},
			{Type: "system", Summary: "AGG-SUMMARIZE-HIDDEN-ONLY-ITEM", Tags: []string{"op:" + aggRestrictedOp}},
			{Type: "system", Summary: "AGG-SUMMARIZE-READABLE-ITEM", Tags: []string{"op:" + aggScanOp}},
		},
	}
	r := digestRouter(digestRow(t, dd))

	d, body := readDigest(t, r, viewerPerms)
	assert.ElementsMatch(t, []string{"AGG-SCAN-ITEM", "AGG-SUMMARIZE-READABLE-ITEM"}, itemSummaries(d),
		"an item that names a hidden run in an op: tag is shown to a caller who may not read it")
	assert.NotContains(t, body, aggRestrictedOp)
	assert.True(t, d.Details.AggregatesWithheld, "a hidden item means the stored totals include a hidden row")
	assert.Zero(t, d.Details.OriginalCount)

	// The editor may read the plain run but not the restricted one.
	d, _ = readDigest(t, r, editorPerms)
	assert.ElementsMatch(t, []string{"AGG-SCAN-ITEM", "AGG-SUMMARIZE-READABLE-ITEM"}, itemSummaries(d))
	assert.True(t, d.Details.AggregatesWithheld)

	// Who may read the run reads everything, totals included.
	for _, perms := range []string{adminUsers, everyPerm} {
		d, _ = readDigest(t, r, perms)
		assert.Len(t, itemSummaries(d), 4, perms)
		assert.False(t, d.Details.AggregatesWithheld, perms)
		assert.Equal(t, 4, d.Details.OriginalCount, perms)
	}
}
