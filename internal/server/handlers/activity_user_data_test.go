// file: internal/server/handlers/activity_user_data_test.go
// version: 1.2.1
// guid: 2e8a4c71-9d35-4b60-a1f4-6c0b7d9e2a58
// last-edited: 2026-10-30

package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// The server's own log is mirrored into the activity log, and GET /activity is a
// library.view read. These pin what a caller without users.manage is not served:
// log lines that name a user (the abs progress handler, the merge undo, the owner
// guard), whether they were stored before the writer tagged them or after, the
// same lines folded into a daily digest, the engine's owner-apply line under its
// Repairs operation, and the op-log fallback that reads the same lines.

const (
	absLine       = "USERDATA-ABS-LINE progress patch: user=01INVENTEDUSER0000000000A currentTime=3600.5"
	taggedLine    = "USERDATA-TAGGED-LINE something with no recognisable shape"
	guardLine     = "USERDATA-GUARD-LINE owner action allowed: user=01INVENTEDUSER0000000000A access_email=bruce.wayne@example.invalid"
	mergeLine     = "USERDATA-MERGE-LINE survivor reconciled"
	ownerApply    = "USERDATA-OWNER-APPLY-LINE repairs.apply plain.fixer: owner apply by user 01INVENTEDUSER0000000000A (cookie): 1 of 1 owner row(s) written"
	plainLine     = "PLAIN-SCHEDULER-LINE Next sync in 28m"
	opLogFallbck  = "USERDATA-OPLOG-LINE owner apply by user 01INVENTEDUSER0000000000A (cookie)"
	opLogPlain    = "PLAIN-OPLOG-LINE row ran"
	opLogAttrLine = "ATTR-OPLOG-LINE clean message"
	attrUserID    = "01INVENTEDATTRUSER000000A"
	ownerApplyOp  = "op-owner-apply"
	fallbackOp    = "op-fallback"
)

type opLogs map[string][]database.OpLogV2Row

func (m opLogs) GetOpLogsV2(id string, _ int) ([]database.OpLogV2Row, error) { return m[id], nil }

type plainFixer struct{ repairs.Fixer }

// needs only the lane's default permission, as a library fixer does: an editor
// may run it, and still does not manage users.
func (plainFixer) RequiredPermissions() []auth.Permission {
	return []auth.Permission{auth.PermLibraryEditMetadata}
}

func userDataRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	digest := database.ActivityEntry{
		Tier: database.DigestTier, Type: database.DailyDigestType, Summary: "DIGEST-ROW",
		Details: map[string]any{"items": []any{
			map[string]any{"type": "system", "summary": absLine, "tags": []any{"source:abs"}},
			map[string]any{"type": "system", "summary": plainLine, "tags": []any{"source:scheduler"}},
			map[string]any{"type": "system", "summary": "digest item with an error", "details": "failed for user=01INVENTEDUSER0000000000A"},
		}},
	}
	log := &fakeActivityLog{entries: []database.ActivityEntry{
		{Type: "system", Source: "abs", Summary: absLine, Tags: []string{"source:abs"}},
		{Type: "system", Source: "scheduler", Summary: taggedLine, Tags: []string{activity.TagUserData}},
		{Type: "system", Source: "http", Summary: guardLine},
		{Type: "system", Source: "merge", Summary: mergeLine},
		{Type: "system", Source: "scheduler", Summary: plainLine},
		{Type: repairs.ApplyOpID, Summary: ownerApply, OperationID: ownerApplyOp, Tags: []string{"def:" + repairs.ApplyOpID}},
		{Type: "system", Source: "abs", Summary: absLine, OperationID: "op-abs"},
		digest,
	}}
	rows := v2Rows{
		ownerApplyOp: {ID: ownerApplyOp, DefID: repairs.ApplyOpID, Params: `{"fixer_id":"plain.fixer"}`},
		fallbackOp:   {ID: fallbackOp, DefID: "library.scan"},
		"op-abs":     {ID: "op-abs", DefID: "library.scan"},
	}
	holds := func(c *gin.Context, p auth.Permission) bool { return auth.Can(c.Request.Context(), p) }
	h := NewActivityHandler(log,
		opLogs{fallbackOp: {
			{OperationID: fallbackOp, Level: "info", Message: opLogFallbck, CreatedAt: time.Now()},
			{OperationID: fallbackOp, Level: "info", Message: opLogPlain, CreatedAt: time.Now()},
			{OperationID: fallbackOp, Level: "info", Message: opLogAttrLine, Attrs: `{"user_id":"` + attrUserID + `","book":"Hush"}`, CreatedAt: time.Now()},
		}},
		WithActivityRepairsAccess(opaccess.NewRepairs(activityFixers{"plain.fixer": plainFixer{}}, rows, holds), rows, nil),
		WithActivityOthersUserData(func(c *gin.Context) bool { return auth.Can(c.Request.Context(), auth.PermUsersManage) }),
	)
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

func userDataDo(r http.Handler, method, path, perms, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perms", perms)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const (
	editorPerms = auth.PermLibraryView + "," + auth.PermLibraryEditMetadata
	adminUsers  = auth.PermLibraryView + "," + auth.PermLibraryEditMetadata + "," + auth.PermUsersManage
)

const ownerApplyOpRead = "owner-apply op"

var userDataLines = []string{absLine, taggedLine, guardLine, mergeLine, ownerApply, opLogFallbck}

func TestActivity_LinesThatNameAUserNeedUsersManage(t *testing.T) {
	r := userDataRouter()
	reads := map[string]func(perms string) *httptest.ResponseRecorder{
		"GET /activity": func(p string) *httptest.ResponseRecorder { return userDataDo(r, http.MethodGet, "/activity", p, "") },
		"GET /activity?operation_id": func(p string) *httptest.ResponseRecorder {
			return userDataDo(r, http.MethodGet, "/activity?operation_id="+ownerApplyOp, p, "")
		},
		"GET /operations/:id/activity (" + ownerApplyOpRead + ")": func(p string) *httptest.ResponseRecorder {
			return userDataDo(r, http.MethodGet, "/operations/"+ownerApplyOp+"/activity", p, "")
		},
		"GET /operations/:id (abs op)": func(p string) *httptest.ResponseRecorder {
			return userDataDo(r, http.MethodGet, "/operations/op-abs/activity", p, "")
		},
		"GET /operations/:id (fallbck)": func(p string) *httptest.ResponseRecorder {
			return userDataDo(r, http.MethodGet, "/operations/"+fallbackOp+"/activity", p, "")
		},
		"POST merged": func(p string) *httptest.ResponseRecorder {
			return userDataDo(r, http.MethodPost, "/operations/activity/merged", p, `{"ids":["`+ownerApplyOp+`","op-abs","`+fallbackOp+`"]}`)
		},
	}
	for name, read := range reads {
		for _, perms := range []string{viewerPerms, editorPerms} {
			w := read(perms)
			// A viewer may not use the fixer, so the owner-apply op itself is a 403
			// (the Repairs check); the user-data rule is what stops the editor.
			if perms == viewerPerms && strings.Contains(name, ownerApplyOpRead) {
				require.Equal(t, http.StatusForbidden, w.Code, "%s as %s: %s", name, perms, w.Body.String())
			} else {
				require.Equal(t, http.StatusOK, w.Code, "%s as %s: %s", name, perms, w.Body.String())
			}
			for _, line := range userDataLines {
				assert.NotContains(t, w.Body.String(), strings.Fields(line)[0], "%s as %s serves a line that names a user", name, perms)
			}
			assert.NotContains(t, w.Body.String(), "01INVENTEDUSER0000000000A", "%s as %s", name, perms)
			assert.NotContains(t, w.Body.String(), attrUserID, "%s as %s serves an attribute that names a user", name, perms)
		}
	}

	// The caller who may manage users reads them; this is the oracle that the
	// routes above are wired, not just empty.
	w := reads["GET /activity"](adminUsers)
	for _, line := range []string{absLine, taggedLine, guardLine, mergeLine, ownerApply} {
		assert.Contains(t, w.Body.String(), strings.Fields(line)[0], "the admin reads %q", line)
	}
	assert.Contains(t, reads["GET /operations/:id (fallbck)"](adminUsers).Body.String(), "USERDATA-OPLOG-LINE")
	assert.Contains(t, reads["POST merged"](adminUsers).Body.String(), "USERDATA-OPLOG-LINE")
	assert.Contains(t, reads["GET /operations/:id (fallbck)"](adminUsers).Body.String(), attrUserID, "the admin reads the attributes whole")
	assert.Contains(t, reads["GET /operations/:id (fallbck)"](editorPerms).Body.String(), "ATTR-OPLOG-LINE", "the clean line stays for the editor")
	assert.Contains(t, reads["GET /operations/:id/activity ("+ownerApplyOpRead+")"](adminUsers).Body.String(), "USERDATA-OWNER-APPLY-LINE")

	// Ordinary lines are untouched for everyone.
	assert.Contains(t, reads["GET /activity"](viewerPerms).Body.String(), "PLAIN-SCHEDULER-LINE")
	assert.Contains(t, reads["GET /operations/:id (fallbck)"](viewerPerms).Body.String(), "PLAIN-OPLOG-LINE")
}

// The total and the page are counted without the withheld lines (the store's
// predicate), and a daily digest keeps its row but loses the items that name a
// user.
func TestActivity_UserDataLeavesTheCountAndRedactsDigests(t *testing.T) {
	r := userDataRouter()
	w := userDataDo(r, http.MethodGet, "/activity", viewerPerms, "")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"total":2`, "only the plain scheduler line and the digest row remain: %s", body)
	// The row stays; its stored summary, which would state the day's total, does
	// not: this digest has no per-operation shares and no date (activity_digest_aggregates.go).
	assert.Contains(t, body, `"type":"daily_digest"`)
	assert.NotContains(t, body, "DIGEST-ROW")
	assert.Contains(t, body, "PLAIN-SCHEDULER-LINE", "the digest's ordinary item stays")
	assert.NotContains(t, body, "01INVENTEDUSER0000000000A")
	assert.NotContains(t, body, "USERDATA-ABS-LINE")

	w = userDataDo(r, http.MethodGet, "/activity", adminUsers, "")
	assert.Contains(t, w.Body.String(), "01INVENTEDUSER0000000000A", "the admin's digest keeps every item")
}

// Lines the scanner batches (activity.LogBatch) reach the log as one folded
// entry whose items are the lines. A batch of lines that name a user is tagged
// when it is flushed, so a caller without users.manage is not served it while
// the batch of ordinary lines is. Driven through the real writer and a real
// store so the tag and the read predicate are the production ones.
func TestActivity_BatchedLinesThatNameAUserNeedUsersManage(t *testing.T) {
	db, err := pebble.Open("batched.pebble", &pebble.Options{FS: vfs.NewMem()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := database.NewPebbleActivityStore(database.NewGatedDB(db))

	w := activity.NewWriter(store, 64)
	activity.LogBatch(w, "op-b", "tag-scan", "scanner", activity.BatchItem{Name: "BATCHED-USER-ITEM progress user=01INVENTEDUSER0000000000A", Count: 1})
	activity.LogBatch(w, "op-b", "tag-scan", "scanner", activity.BatchItem{Name: "BATCHED-PLAIN-ITEM /lib/Gotham/year-one.m4b", Count: 1})
	activity.FlushOperation(w, "op-b")
	w.Flush()

	h := NewActivityHandler(store, nil,
		WithActivityOthersUserData(func(c *gin.Context) bool { return auth.Can(c.Request.Context(), auth.PermUsersManage) }))
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), strings.Split(c.GetHeader("X-Perms"), ",")))
		c.Next()
	})
	r.GET("/activity", h.ListActivity)

	for _, perms := range []string{viewerPerms, editorPerms} {
		w := userDataDo(r, http.MethodGet, "/activity?tier=batch", perms, "")
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", perms, w.Body.String())
		assert.NotContains(t, w.Body.String(), "BATCHED-USER-ITEM", "%s is served a batch of lines that name a user", perms)
		assert.NotContains(t, w.Body.String(), "01INVENTEDUSER0000000000A", perms)
		assert.Contains(t, w.Body.String(), "BATCHED-PLAIN-ITEM", "%s: the batch of ordinary lines still folds and shows", perms)
	}
	body := userDataDo(r, http.MethodGet, "/activity?tier=batch", adminUsers, "").Body.String()
	assert.Contains(t, body, "BATCHED-USER-ITEM", "the users.manage caller reads it")
	assert.Contains(t, body, "BATCHED-PLAIN-ITEM")
}
