// file: internal/server/handlers/operations_v2_timeline_test.go
// version: 1.2.0
// guid: 7f3c1a94-2e6b-4d58-9a71-c0d4e8b52f36
// last-edited: 2026-09-09

// Behaviour tests for GET /api/v1/operations/timeline's def_id and limit
// parameters, and for the scope fields the response reports about itself.
//
// These test the LIVE handler. Until 2026-08-24 a second, byte-similar
// implementation of this endpoint sat in package server
// (operations_v2_handlers.go) with its own tests registering their own router;
// no route ever referenced it, so a strict-behaviour test added there passed
// green while production was unchanged. That twin has been deleted. If this
// endpoint ever grows a second implementation again, these tests are the ones
// that must move.

package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// timelineBody decodes the {"data":{…}} envelope this endpoint answers in.
//
// The nesting is load-bearing to test correctly: a reader that takes top-level
// "operations" with a len() fallback silently reports 1, which is how a
// 148-row window was recorded as a single unrelated op in production.
func timelineBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &env))
	require.NotNil(t, env.Data, "response had no data envelope: %s", string(raw))
	return env.Data
}

// timelineRows builds n rows for defID, ids prefixed to stay distinguishable.
func timelineRows(defID string, n int) []database.OperationV2Row {
	rows := make([]database.OperationV2Row, 0, n)
	for i := range n {
		rows = append(rows, database.OperationV2Row{
			ID:     fmt.Sprintf("%s-%d", defID, i),
			DefID:  defID,
			Status: "queued",
		})
	}
	return rows
}

func timelineHandler(t *testing.T, rows []database.OperationV2Row) *handlers.OperationsV2Handler {
	t.Helper()
	store := databasemocks.NewMockOpsV2Store(t)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	store.EXPECT().ListOperationsV2Since(mock.Anything, 5000).Return(rows, nil)
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{}).Maybe()
	return handlers.NewOperationsV2Handler(store, registry, nil, false)
}

// The load-bearing test. def_id must be applied to the WHOLE window, never to a
// page the store already truncated.
//
// The mutant is caught by the mocked store argument: `ListOperationsV2Since(_,
// 5000)` fails on mismatch the moment anyone pushes the caller's limit down into
// the store. That expectation is the pin — say so plainly, because this fixture
// returns all 300 rows unconditionally and does NOT itself simulate the store's
// truncation. A future editor who relaxes that argument to mock.Anything would
// remove the only thing catching this, and a comment claiming the row positions
// do the work would tell them they were still covered.
//
// The positions still document WHY it matters: in the real store the trim happens
// after a StartedAt DESC NULLS LAST sort, so queued rows are dropped FIRST and a
// just-enqueued op is the first thing to vanish from a view whose job is to show
// it.
func TestGetOperationTimeline_DefIDFiltersTheWholeWindowNotJustTheFirstPage(t *testing.T) {
	rows := timelineRows("other.op", 250)
	rows = append(rows, timelineRows("target.op", 3)...)
	rows = append(rows, timelineRows("other.op-tail", 47)...)
	require.Len(t, rows, 300)

	h := timelineHandler(t, rows)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=168h&def_id=target.op", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())

	ops, ok := data["operations"].([]any)
	require.True(t, ok, "operations was not a list: %v", data["operations"])
	assert.Len(t, ops, 3, "all three target.op rows must survive despite sitting past row 200")
	assert.Equal(t, float64(3), data["matched"])
	assert.Equal(t, false, data["truncated"])
	assert.Equal(t, "target.op", data["def_id"])
}

// An empty def_id must mean "every def", not "no def" — the inverted-predicate
// mutant returns nothing here.
func TestGetOperationTimeline_EmptyDefIDReturnsEveryDef(t *testing.T) {
	rows := append(timelineRows("a.op", 2), timelineRows("b.op", 3)...)

	h := timelineHandler(t, rows)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())
	assert.Len(t, data["operations"].([]any), 5)
	assert.Equal(t, float64(5), data["matched"])
	assert.Equal(t, "", data["def_id"])
}

// limit bounds the returned rows, and `matched` still reports the true total so
// the caller can tell a bounded answer from a complete one.
//
// This is the half a len(rows)==limit heuristic cannot express: it cannot
// distinguish "exactly limit existed" from "there were more".
func TestGetOperationTimeline_LimitBoundsRowsAndTruncatedIsAFactNotAGuess(t *testing.T) {
	h := timelineHandler(t, timelineRows("a.op", 7))
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&limit=3", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())

	assert.Len(t, data["operations"].([]any), 3, "limit must bound the rows returned")
	assert.Equal(t, float64(7), data["matched"], "matched must be the pre-limit total")
	assert.Equal(t, true, data["truncated"])
	assert.Equal(t, float64(3), data["limit"])
}

// The boundary the heuristic gets wrong: exactly `limit` rows exist.
// len(rows)==limit would claim truncation; matched>len(resp) correctly does not.
func TestGetOperationTimeline_ExactlyLimitRowsIsNotTruncated(t *testing.T) {
	h := timelineHandler(t, timelineRows("a.op", 3))
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&limit=3", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())
	assert.Len(t, data["operations"].([]any), 3)
	assert.Equal(t, float64(3), data["matched"])
	assert.Equal(t, false, data["truncated"], "exactly limit rows is a complete answer, not a truncated one")
}

// A limit the server cannot honour is rejected, not silently replaced by the
// default. Answering 200 rows to ?limit=abc is the same class of bug as ignoring
// the parameter outright: the caller asked for something and cannot tell it did
// not happen.
func TestGetOperationTimeline_RejectsUnusableLimitRatherThanDefaulting(t *testing.T) {
	for _, raw := range []string{"abc", "0", "-5", "1.5"} {
		t.Run(raw, func(t *testing.T) {
			h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
			c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&limit="+raw, "", nil)
			h.GetOperationTimeline(c)
			assert.Equal(t, http.StatusBadRequest, w.Code, "limit=%s must be rejected", raw)
		})
	}
}

// A negative since puts the window boundary in the future, which answers a
// near-empty list that reads as "nothing happened".
func TestGetOperationTimeline_RejectsNegativeSince(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=-24h", "", nil)
	h.GetOperationTimeline(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// The response states the window it actually measured. The endpoint's real
// failure was never an error — it was a confident undercount from a default
// nobody could see. An answer carrying its own scope cannot be read as a census.
func TestGetOperationTimeline_ReportsTheWindowItMeasured(t *testing.T) {
	h := timelineHandler(t, timelineRows("a.op", 1))
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=168h", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())

	assert.Equal(t, "168h", data["since"])
	assert.NotEmpty(t, data["window_start"], "window_start must name the boundary in absolute time")
	assert.Equal(t, float64(200), data["limit"], "the default limit must be stated, not implied")
}

// Operations still in flight are returned regardless of age, so the window does
// not explain why they are present. Reporting them inside a bare `matched` is the
// same confident-wrong-answer this endpoint is being fixed for, wearing an
// authoritative window: a scan queued three weeks ago and never completed answers
// ?since=1h with matched=1, which reads as "it ran once in the last hour".
func TestGetOperationTimeline_CountsInFlightRowsThatPredateTheWindow(t *testing.T) {
	old := time.Now().UTC().Add(-21 * 24 * time.Hour)
	done := time.Now().UTC().Add(-30 * time.Minute)
	rows := []database.OperationV2Row{
		// Queued three weeks ago, never completed: admitted by the store no matter
		// what `since` says.
		{ID: "stale-running", DefID: "library.scan", Status: "running", QueuedAt: old},
		// Genuinely inside the window.
		{ID: "recent-done", DefID: "library.scan", Status: "completed", QueuedAt: done, CompletedAt: &done},
	}

	store := databasemocks.NewMockOpsV2Store(t)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	store.EXPECT().ListOperationsV2Since(mock.Anything, 5000).Return(rows, nil)
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{}).Maybe()
	registry.EXPECT().GetCurrentItem(mock.Anything).Return("").Maybe()
	h := handlers.NewOperationsV2Handler(store, registry, nil, false)

	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&def_id=library.scan", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())

	assert.Equal(t, float64(2), data["matched"])
	assert.Equal(t, float64(1), data["in_flight_before_window"],
		"a row still running from before window_start must be counted separately, or the "+
			"stated window is false for it and matched reads as activity inside the window")
}

// The counter must not fire for rows that genuinely fall inside the window —
// otherwise it is decorative and every answer looks partly out-of-scope.
func TestGetOperationTimeline_DoesNotCountInWindowRowsAsPredatingIt(t *testing.T) {
	recent := time.Now().UTC().Add(-10 * time.Minute)
	rows := []database.OperationV2Row{
		{ID: "running-now", DefID: "a.op", Status: "running", QueuedAt: recent},
	}

	store := databasemocks.NewMockOpsV2Store(t)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	store.EXPECT().ListOperationsV2Since(mock.Anything, 5000).Return(rows, nil)
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{}).Maybe()
	registry.EXPECT().GetCurrentItem(mock.Anything).Return("").Maybe()
	h := handlers.NewOperationsV2Handler(store, registry, nil, false)

	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())
	assert.Equal(t, float64(1), data["matched"])
	assert.Equal(t, float64(0), data["in_flight_before_window"])
}

// The nil-store path answers the same self-describing shape. A caller that reads
// `matched` must not get a missing key from one branch and a number from another.
func TestGetOperationTimeline_NilStoreStillDescribesItsScope(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&def_id=a.op", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())

	assert.Empty(t, data["operations"])
	assert.Equal(t, float64(0), data["matched"])
	assert.Equal(t, false, data["truncated"])
	assert.Equal(t, "a.op", data["def_id"])
	assert.Equal(t, "1h", data["since"])
}

// A full scan means `matched` is a floor, not a total, so no "it never happened
// before X" claim can rest on it. The flag is absent when the scan did not fill,
// so its presence is meaningful rather than decorative.
func TestGetOperationTimeline_FlagsAFilledScanAsAFloor(t *testing.T) {
	h := timelineHandler(t, timelineRows("a.op", 5000))
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=8760h&limit=10", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())

	assert.Equal(t, true, data["scan_capped"], "a scan that hit the bound must say the total is a floor")
	assert.Equal(t, float64(5000), data["matched"])
	assert.Len(t, data["operations"].([]any), 10)
}

func TestGetOperationTimeline_DoesNotFlagAnUnfilledScan(t *testing.T) {
	h := timelineHandler(t, timelineRows("a.op", 4))
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())
	// Always present, never omitted. Two booleans in one object with two different
	// presence conventions invites a reader to treat a missing key as unknown
	// rather than false — and this object exists to be read by someone who might
	// misread it.
	assert.Equal(t, false, data["scan_capped"], "scan_capped must be present and false when the scan did not fill")
}

// timelineRowsWithStatus builds n rows for defID carrying the given status.
func timelineRowsWithStatus(defID, status string, n int) []database.OperationV2Row {
	rows := timelineRows(defID, n)
	for i := range rows {
		rows[i].Status = status
		rows[i].ID = fmt.Sprintf("%s-%s-%d", defID, status, i)
	}
	return rows
}

// The regression test for the incident that motivated the filter, written in the
// shape that actually cost something.
//
// `?status=canceled` used to return the unfiltered rows, so a caller reading
// `matched` to size DELETE /operations/history?status=canceled was told "1 row"
// and removed 70. The assertion that matters is not that the LIST is filtered —
// it is that `matched` counts canceled rows over the whole window rather than
// the whole window. A fix that filtered the returned page but left `matched`
// counting everything would look right in the UI and re-cause the same deletion.
func TestGetOperationTimeline_StatusCountsMatchedOverTheWholeWindow(t *testing.T) {
	rows := timelineRowsWithStatus("a.op", "completed", 120)
	rows = append(rows, timelineRowsWithStatus("a.op", "canceled", 70)...)
	rows = append(rows, timelineRowsWithStatus("b.op", "failed", 10)...)
	require.Len(t, rows, 200)

	h := timelineHandler(t, rows)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=168h&status=canceled&limit=5", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := timelineBody(t, w.Body.Bytes())

	// 70, not 200: the number a delete would have been sized against.
	assert.Equal(t, float64(70), data["matched"],
		"matched must count canceled rows across the window, not every row in it")
	assert.Len(t, data["operations"].([]any), 5, "limit still bounds the page")
	assert.Equal(t, true, data["truncated"], "70 matched behind a limit of 5 is truncated")
	assert.Equal(t, false, data["scan_capped"], "200 rows is nowhere near the scan bound")

	for _, op := range data["operations"].([]any) {
		assert.Equal(t, "canceled", op.(map[string]any)["status"],
			"a returned row must actually carry the requested status")
	}
}

// The direct anti-regression for "accepted then dropped": filtering must CHANGE
// the answer. This is the assertion whose absence let the bug ship — the old
// behaviour returned byte-identical rows with and without the parameter, and
// every existing test passed because none of them compared the two.
func TestGetOperationTimeline_StatusFilterChangesTheAnswer(t *testing.T) {
	rows := timelineRowsWithStatus("a.op", "completed", 8)
	rows = append(rows, timelineRowsWithStatus("a.op", "canceled", 3)...)

	unfiltered := timelineHandler(t, rows)
	c1, w1 := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h", "", nil)
	unfiltered.GetOperationTimeline(c1)
	all := timelineBody(t, w1.Body.Bytes())

	filtered := timelineHandler(t, rows)
	c2, w2 := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&status=canceled", "", nil)
	filtered.GetOperationTimeline(c2)
	only := timelineBody(t, w2.Body.Bytes())

	require.Equal(t, float64(11), all["matched"])
	require.Equal(t, float64(3), only["matched"])
	assert.NotEqual(t, all["matched"], only["matched"],
		"a status filter that leaves the answer unchanged is the bug this fixes")
}

// An unknown status answers 0 AND says what it did see. A bare matched=0 cannot
// distinguish "no canceled ops in this window" from "this store spells it
// canceled and you typed cancelled", and those need opposite next actions.
//
// Deliberately not a 400: the store bounds its window on CompletedAt rather than
// a status list precisely because status vocabularies grow, and a whitelist here
// would make a newly added status unqueryable until someone edited this file.
func TestGetOperationTimeline_UnknownStatusReportsWhatIsPresent(t *testing.T) {
	rows := timelineRowsWithStatus("a.op", "completed", 4)
	rows = append(rows, timelineRowsWithStatus("a.op", "canceled", 2)...)

	h := timelineHandler(t, rows)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&status=cancelled", "", nil)
	h.GetOperationTimeline(c)

	require.Equal(t, http.StatusOK, w.Code, "an unrecognised status is answered, not rejected")
	data := timelineBody(t, w.Body.Bytes())
	assert.Equal(t, float64(0), data["matched"])
	assert.Equal(t, "cancelled", data["status"], "scope must echo what was asked for")

	present, ok := data["statuses_present"].([]any)
	require.True(t, ok, "an empty filtered answer must say which statuses it did see: %v", data)
	assert.Equal(t, []any{"canceled", "completed"}, present, "sorted, so the field is stable")
}

// The noise guard: statuses_present exists to explain an empty answer, so it must
// NOT appear on an ordinary successful one.
func TestGetOperationTimeline_StatusesPresentOnlyWhenNothingMatched(t *testing.T) {
	rows := timelineRowsWithStatus("a.op", "completed", 4)

	h := timelineHandler(t, rows)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&status=completed", "", nil)
	h.GetOperationTimeline(c)

	data := timelineBody(t, w.Body.Bytes())
	require.Equal(t, float64(4), data["matched"])
	_, exists := data["statuses_present"]
	assert.False(t, exists, "statuses_present must not ride along on a successful filter")
}

// An absent status must mean "every status", not "no status" — the inverted
// predicate mutant returns nothing here, the same pin EmptyDefIDReturnsEveryDef
// provides for def_id.
func TestGetOperationTimeline_EmptyStatusReturnsEveryStatus(t *testing.T) {
	// Deliberately no "running" row: the shared fixture does not stub
	// GetCurrentItem, which the handler calls only for running ops. Two terminal
	// statuses exercise the same predicate without dragging in that dependency.
	rows := timelineRowsWithStatus("a.op", "completed", 3)
	rows = append(rows, timelineRowsWithStatus("a.op", "failed", 2)...)

	h := timelineHandler(t, rows)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h", "", nil)
	h.GetOperationTimeline(c)

	data := timelineBody(t, w.Body.Bytes())
	assert.Equal(t, float64(5), data["matched"])
	assert.Equal(t, "", data["status"])
}

// status and def_id must AND, not OR. An OR would re-admit the rows the caller
// excluded and quietly restore the overcount this endpoint keeps being fixed for.
func TestGetOperationTimeline_StatusAndDefIDCombine(t *testing.T) {
	rows := timelineRowsWithStatus("a.op", "canceled", 6)
	rows = append(rows, timelineRowsWithStatus("b.op", "canceled", 5)...)
	rows = append(rows, timelineRowsWithStatus("a.op", "completed", 4)...)

	h := timelineHandler(t, rows)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=1h&def_id=a.op&status=canceled", "", nil)
	h.GetOperationTimeline(c)

	data := timelineBody(t, w.Body.Bytes())
	assert.Equal(t, float64(6), data["matched"], "only a.op rows that are also canceled")
	assert.Equal(t, "a.op", data["def_id"])
	assert.Equal(t, "canceled", data["status"])
}

// The boundary the filter does NOT fix, pinned so nobody claims it does.
//
// A filtered `matched` is a census only while the scan was complete. When the
// store returns timelineScanBound rows the window overflowed, the filter ran over
// the newest 5000 only, and `matched` is a FLOOR — scan_capped is the one signal
// saying so. Asserting it here keeps "status is honored" from being read as
// "status counts are always total".
func TestGetOperationTimeline_FilteredCountIsAFloorWhenTheScanCapped(t *testing.T) {
	rows := timelineRowsWithStatus("a.op", "canceled", 5000)

	h := timelineHandler(t, rows)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=168h&status=canceled&limit=10", "", nil)
	h.GetOperationTimeline(c)

	data := timelineBody(t, w.Body.Bytes())
	assert.Equal(t, true, data["scan_capped"],
		"a full scan must be declared, or a filtered count reads as a total")
	assert.Equal(t, float64(5000), data["matched"])
}
