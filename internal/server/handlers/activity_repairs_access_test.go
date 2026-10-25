// file: internal/server/handlers/activity_repairs_access_test.go
// version: 1.0.0
// guid: c7d2f9a1-3b84-4e60-91a5-0d6e8f2b4c37
// last-edited: 2026-10-25

package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// How the activity handler decides what a caller may read of a Repairs
// operation when the records it normally resolves the operation from are
// missing or only one of them exists. The server-level tests
// (internal/server/repairs_op_visibility_followup_test.go) drive the same
// handler through the wired router with both records present.

type activityFixer struct{ repairs.Fixer }

func (activityFixer) RequiredPermissions() []auth.Permission {
	return []auth.Permission{auth.PermUsersManage}
}

type activityFixers map[string]repairs.Fixer

func (m activityFixers) Get(id string) (repairs.Fixer, bool) { f, ok := m[id]; return f, ok }

// fakeActivityLog applies the parts of a filter the handler sets.
type fakeActivityLog struct{ entries []database.ActivityEntry }

func (f *fakeActivityLog) Query(_ context.Context, flt database.ActivityFilter) ([]database.ActivityEntry, int, error) {
	var out []database.ActivityEntry
	for _, e := range f.entries {
		if flt.OperationID != "" && !database.EntryOperationIDs(e, func(id string) bool { return id == flt.OperationID }) {
			continue
		}
		if flt.ExcludeEntry != nil && flt.ExcludeEntry(e) {
			continue
		}
		out = append(out, e)
	}
	return out, len(out), nil
}

func (f *fakeActivityLog) GetDistinctSources(context.Context, database.ActivityFilter) ([]database.SourceCount, error) {
	return nil, nil
}

type v2Rows map[string]*database.OperationV2Row

func (m v2Rows) GetOperationV2(id string) (*database.OperationV2Row, error) { return m[id], nil }

type v3Runs map[string]*database.RunRecord

func (m v3Runs) GetRunV3(id string) (*database.RunRecord, error) { return m[id], nil }

const (
	marker         = "REPAIRS-LOG-LINE"
	plainMarker    = "PLAIN-LOG-LINE"
	corruptOp      = "corrupt"
	corruptPlain   = "CORRUPT-OP-PLAIN-LINE"
	corruptRepairs = "CORRUPT-OP-REPAIRS-LINE"
)

// failingRows is v2Rows whose read of corruptOp fails (an undecodable record),
// counting how often it was asked.
type failingRows struct {
	v2Rows
	asked int
}

func (m *failingRows) GetOperationV2(id string) (*database.OperationV2Row, error) {
	if id == corruptOp {
		m.asked++
		return nil, errors.New("operations_v2 row cannot be decoded")
	}
	return m.v2Rows[id], nil
}

func activityRouter(rows ActivityOperationReader, runs ActivityRunReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := &fakeActivityLog{entries: []database.ActivityEntry{
		// Reporter rows of a repairs op nothing is recorded for: the entry's own
		// type and def: tag are all there is to go on.
		{Summary: marker + " gone", Type: repairs.ApplyOpID, OperationID: "gone", Tags: []string{"def:" + repairs.ApplyOpID}},
		{Summary: plainMarker + " gone", Type: "library.scan", OperationID: "gone", Tags: []string{"def:library.scan"}},
		// Operations that have one record.
		{Summary: marker + " v3only", Type: repairs.PlanOpID, OperationID: "v3only", Tags: []string{"def:" + repairs.PlanOpID}},
		{Summary: marker + " v2only", Type: repairs.PlanOpID, OperationID: "v2only", Tags: []string{"def:" + repairs.PlanOpID}},
		{Summary: plainMarker + " scan", Type: "library.scan", OperationID: "scan", Tags: []string{"def:library.scan"}},
		// A repairs-looking row that names no operation at all.
		{Summary: marker + " orphan", Type: repairs.ApplyOpID, Tags: []string{"def:" + repairs.ApplyOpID}},
		// Two rows of an operation whose record cannot be read (corruptOp): one
		// of a plain definition, one that says repairs.
		{Summary: corruptPlain, Type: "library.scan", OperationID: corruptOp, Tags: []string{"def:library.scan"}},
		{Summary: corruptRepairs, Type: repairs.ApplyOpID, OperationID: corruptOp, Tags: []string{"def:" + repairs.ApplyOpID}},
	}}
	h := NewActivityHandler(log, nil, WithActivityRepairsAccess(
		opaccess.NewRepairs(activityFixers{"needs.users": activityFixer{}}, func(c *gin.Context, p auth.Permission) bool {
			return auth.Can(c.Request.Context(), p)
		}), rows, runs))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if ps := c.GetHeader("X-Perms"); ps != "" {
			c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), strings.Split(ps, ",")))
		}
		c.Next()
	})
	r.GET("/activity", h.ListActivity)
	r.GET("/operations/:id/activity", h.ListOperationActivity)
	r.POST("/operations/activity/merged", h.ListMergedOperationActivity)
	return r
}

func activityGet(r http.Handler, method, path, perms, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perms", perms)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const (
	viewerPerms = auth.PermLibraryView
	adminPerms  = auth.PermLibraryView + "," + auth.PermUsersManage
)

func fixerParams() string { return `{"fixer_id":"needs.users"}` }

// An operation whose v2 row is gone is not an unknown operation if its v3 run
// remains: that record says which fixer it belongs to.
func TestActivityRepairsAccess_V3RunDecidesWhenTheV2RowIsMissing(t *testing.T) {
	r := activityRouter(v2Rows{}, v3Runs{"v3only": {ID: "v3only", DefID: repairs.PlanOpID, Params: []byte(fixerParams())}})
	if w := activityGet(r, http.MethodGet, "/operations/v3only/activity", viewerPerms, ""); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), marker) {
		t.Fatalf("viewer reading an op known only by its v3 run = %d %s, want 403", w.Code, w.Body.String())
	}
	if w := activityGet(r, http.MethodGet, "/operations/v3only/activity", adminPerms, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), marker) {
		t.Fatalf("permitted caller = %d %s, want the entries", w.Code, w.Body.String())
	}
	if w := activityGet(r, http.MethodGet, "/activity", viewerPerms, ""); strings.Contains(w.Body.String(), "v3only") {
		t.Fatalf("viewer's /activity names the op known only by its v3 run: %s", w.Body.String())
	}
	if w := activityGet(r, http.MethodPost, "/operations/activity/merged", viewerPerms, `{"ids":["v3only"]}`); w.Code != http.StatusForbidden {
		t.Fatalf("merged read of only that op = %d, want 403", w.Code)
	}
}

// An operation nothing is recorded for is not a plain operation: its entries are
// judged by their own type and def: tag, and one that says repairs is refused,
// to everyone (nothing says whose fixer it was).
func TestActivityRepairsAccess_UnknownOperationFailsClosedForRepairsEntries(t *testing.T) {
	r := activityRouter(v2Rows{}, v3Runs{})
	for _, perms := range []string{viewerPerms, adminPerms} {
		w := activityGet(r, http.MethodGet, "/operations/gone/activity", perms, "")
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), marker) || !strings.Contains(w.Body.String(), plainMarker) {
			t.Fatalf("%s: /operations/gone/activity = %d %s, want the plain entry and not the repairs one", perms, w.Code, w.Body.String())
		}
		w = activityGet(r, http.MethodPost, "/operations/activity/merged", perms, `{"ids":["gone","scan"]}`)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), marker) || !strings.Contains(w.Body.String(), plainMarker) {
			t.Fatalf("%s: merged = %d %s, want no repairs entry", perms, w.Code, w.Body.String())
		}
		w = activityGet(r, http.MethodGet, "/activity", perms, "")
		if strings.Contains(w.Body.String(), marker) || !strings.Contains(w.Body.String(), plainMarker) {
			t.Fatalf("%s: /activity = %s, want the plain entries only (the orphan repairs row included in the refusal)", perms, w.Body.String())
		}
	}
}

// Operations that are not Repairs ones are unchanged, with or without records.
func TestActivityRepairsAccess_OtherOperationsUnchanged(t *testing.T) {
	r := activityRouter(v2Rows{"scan": {ID: "scan", DefID: "library.scan"}}, v3Runs{})
	w := activityGet(r, http.MethodGet, "/operations/scan/activity", viewerPerms, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), plainMarker+" scan") {
		t.Fatalf("plain op = %d %s", w.Code, w.Body.String())
	}
}

// An operation whose record cannot be read must not take every row of an
// unrelated operation out of the log, for admins included, and must not stay
// silent: its rows are judged by their own type and def: tag (the Repairs-looking
// one is hidden, the plain one shown), the store is asked once for it however many
// rows it has, and the failure is logged once. The by-id read still fails closed.
func TestActivityRepairsAccess_UnreadableRecordFallsBackToTheEntryAndLogsOnce(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(prev)

	rows := &failingRows{v2Rows: v2Rows{}}
	r := activityRouter(rows, v3Runs{})
	for _, perms := range []string{viewerPerms, adminPerms} {
		rows.asked = 0
		logged.Reset()
		w := activityGet(r, http.MethodGet, "/activity", perms, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), corruptPlain) || strings.Contains(w.Body.String(), corruptRepairs) {
			t.Fatalf("%s: /activity = %d %s, want the plain row of the unreadable operation and not its repairs row", perms, w.Code, w.Body.String())
		}
		if rows.asked != 1 {
			t.Fatalf("%s: the unreadable record was read %d times in one request, want once", perms, rows.asked)
		}
		if n := strings.Count(logged.String(), "operation lookup failed"); n != 1 || !strings.Contains(logged.String(), corruptOp) {
			t.Fatalf("%s: the failure was logged %d times (%q), want once, naming the operation", perms, n, logged.String())
		}
	}
	if w := activityGet(r, http.MethodGet, "/operations/"+corruptOp+"/activity", adminPerms, ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("by-id read of the unreadable operation = %d, want a 500 (fails closed)", w.Code)
	}
}
