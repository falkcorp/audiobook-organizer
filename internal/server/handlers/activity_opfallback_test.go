// file: internal/server/handlers/activity_opfallback_test.go
// version: 1.1.0
// guid: 6a1f3c85-2d9e-4b47-8c06-e5b2d7a9f134
// last-edited: 2026-10-26

package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

const (
	hiddenOp       = "op-year-one-hidden"   // activity rows exist, every one is hidden; no record
	logsOnlyOp     = "op-hush-logs-only"    // no activity rows; op-log lines only; no record
	deniedOp       = "op-knightfall-denied" // a record the caller may not read
	repairsLogLine = "REPAIRS-OPLOG-LINE the long halloween"
	plainLogLine   = "PLAIN-OPLOG-LINE no man's land"
	nodefLogLine   = "NODEF-OPLOG-LINE a line that names no definition"

	// A plain operation the caller may read, whose only activity rows are tagged
	// user data: the filter drops every row, and its own op-log line is harmless.
	taggedOp       = "op-hush-tagged-rows"
	taggedOpLine   = "TAGGED-OP-OPLOG-LINE the batch ran"
	taggedOpTagRow = "TAGGED-OP-ACTIVITY-ROW"
)

func opFallbackRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	now := time.Now()
	log := &fakeActivityLog{entries: []database.ActivityEntry{
		// Rows of an operation with no record that say repairs: the visibility
		// filter hides every one of them.
		{Summary: "REPAIRS-ACTIVITY-ROW one", Type: repairs.ApplyOpID, OperationID: hiddenOp, Tags: []string{"def:" + repairs.ApplyOpID}},
		{Summary: taggedOpTagRow, Type: "system", OperationID: taggedOp, Tags: []string{activity.TagUserData}},
		{Summary: "REPAIRS-ACTIVITY-ROW two", Type: repairs.ApplyOpID, OperationID: hiddenOp, Tags: []string{"def:" + repairs.ApplyOpID}},
	}}
	repairsAttrs := `{"def_id":"` + repairs.ApplyOpID + `"}`
	scanAttrs := `{"def_id":"library.scan"}`
	opLog := opLogs{
		hiddenOp: {{OperationID: hiddenOp, Level: "info", Message: repairsLogLine, Attrs: repairsAttrs, CreatedAt: now}},
		logsOnlyOp: {
			{OperationID: logsOnlyOp, Level: "info", Message: repairsLogLine, Attrs: repairsAttrs, CreatedAt: now},
			{OperationID: logsOnlyOp, Level: "info", Message: plainLogLine, Attrs: scanAttrs, CreatedAt: now},
			{OperationID: logsOnlyOp, Level: "info", Message: nodefLogLine, CreatedAt: now},
		},
		taggedOp: {{OperationID: taggedOp, Level: "info", Message: taggedOpLine, Attrs: scanAttrs, CreatedAt: now}},
		deniedOp: {{OperationID: deniedOp, Level: "info", Message: repairsLogLine, Attrs: repairsAttrs, CreatedAt: now}},
	}
	rows := v2Rows{taggedOp: {ID: taggedOp, DefID: "library.scan"}, deniedOp: {ID: deniedOp, DefID: repairs.ApplyOpID, Params: fixerParams()}}
	holds := func(c *gin.Context, p auth.Permission) bool { return auth.Can(c.Request.Context(), p) }
	h := NewActivityHandler(log, opLog,
		WithActivityRepairsAccess(opaccess.NewRepairs(activityFixers{"needs.users": activityFixer{}}, rows, holds), rows, nil),
		WithActivityOthersUserData(func(c *gin.Context) bool { return auth.Can(c.Request.Context(), auth.PermUsersManage) }))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if ps := c.GetHeader("X-Perms"); ps != "" {
			c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), strings.Split(ps, ",")))
		}
		c.Next()
	})
	r.GET("/operations/:id/activity", h.ListOperationActivity)
	r.POST("/operations/activity/merged", h.ListMergedOperationActivity)
	return r
}

// TestOperationActivity_HiddenRowsDoNotFallBackToTheOpLog: when the activity
// log holds rows for an operation and the visibility filter drops every one, the
// caller gets nothing from either path (the op-log fallback is only for an
// operation the log holds nothing for, and it applies the same verdict to its own
// lines: a line of a Repairs definition whose operation has no readable record is
// withheld). A library.view caller on an operation they may not read is refused.
func TestOperationActivity_HiddenRowsDoNotFallBackToTheOpLog(t *testing.T) {
	r := opFallbackRouter()
	for _, perms := range []string{viewerPerms, adminPerms} {
		// Every activity row is hidden: no fallback to the op-log lines.
		w := activityGet(r, http.MethodGet, "/operations/"+hiddenOp+"/activity", perms, "")
		require.Equal(t, http.StatusOK, w.Code, perms+": "+w.Body.String())
		assert.NotContains(t, w.Body.String(), "REPAIRS-ACTIVITY-ROW", perms)
		assert.NotContains(t, w.Body.String(), "REPAIRS-OPLOG-LINE", perms+": the filter dropped every row and the op-log lines came back")
		assert.Contains(t, w.Body.String(), `"entries":[]`, perms)

		w = activityGet(r, http.MethodPost, "/operations/activity/merged", perms, `{"ids":["`+hiddenOp+`"]}`)
		assert.NotContains(t, w.Body.String(), "REPAIRS-OPLOG-LINE", perms+" merged")
		assert.NotContains(t, w.Body.String(), "REPAIRS-ACTIVITY-ROW", perms+" merged")

		// A readable plain operation whose rows are all tagged user data: the
		// filter hides them for a caller without users.manage, and the op-log
		// fallback must not hand back the operation's lines instead.
		w = activityGet(r, http.MethodGet, "/operations/"+taggedOp+"/activity", perms, "")
		require.Equal(t, http.StatusOK, w.Code, perms+": "+w.Body.String())
		if perms == viewerPerms {
			assert.NotContains(t, w.Body.String(), taggedOpTagRow, perms)
			assert.NotContains(t, w.Body.String(), taggedOpLine, perms+": every row was filtered and the op-log lines came back")
		}
		w = activityGet(r, http.MethodPost, "/operations/activity/merged", perms, `{"ids":["`+taggedOp+`"]}`)
		if perms == viewerPerms {
			assert.NotContains(t, w.Body.String(), taggedOpLine, perms+" merged")
		}

		// No activity rows at all: the fallback runs, and drops the Repairs line of
		// an operation with no record while keeping the others.
		w = activityGet(r, http.MethodGet, "/operations/"+logsOnlyOp+"/activity", perms, "")
		require.Equal(t, http.StatusOK, w.Code, perms+": "+w.Body.String())
		assert.NotContains(t, w.Body.String(), "REPAIRS-OPLOG-LINE", perms)
		assert.Contains(t, w.Body.String(), "PLAIN-OPLOG-LINE", perms)
		assert.Contains(t, w.Body.String(), "NODEF-OPLOG-LINE", perms)
		w = activityGet(r, http.MethodPost, "/operations/activity/merged", perms, `{"ids":["`+logsOnlyOp+`"]}`)
		assert.NotContains(t, w.Body.String(), "REPAIRS-OPLOG-LINE", perms+" merged")
		assert.Contains(t, w.Body.String(), "PLAIN-OPLOG-LINE", perms+" merged")
	}

	// The oracle for the tagged-rows case: the caller who manages users reads the
	// rows themselves.
	w2 := activityGet(r, http.MethodGet, "/operations/"+taggedOp+"/activity", adminPerms, "")
	assert.Contains(t, w2.Body.String(), taggedOpTagRow)

	// An operation whose record the viewer may not read is a 403 on both routes,
	// and the admin (who holds users.manage) reads its op-log line: the oracle
	// that the fixture's line is served when it may be.
	w := activityGet(r, http.MethodGet, "/operations/"+deniedOp+"/activity", viewerPerms, "")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "REPAIRS-OPLOG-LINE")
	w = activityGet(r, http.MethodPost, "/operations/activity/merged", viewerPerms, `{"ids":["`+deniedOp+`"]}`)
	assert.NotContains(t, w.Body.String(), "REPAIRS-OPLOG-LINE")
	w = activityGet(r, http.MethodGet, "/operations/"+deniedOp+"/activity", adminPerms, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "REPAIRS-OPLOG-LINE")
}

// TestOperationActivity_UnwiredHandlerFailsClosedOnOpLogLines: a handler built
// without WithActivityRepairsAccess cannot resolve any operation, so an op-log
// line is judged by the definition it names alone: the line that names a Repairs
// definition is withheld (for an admin as well, as for a pruned record), while a
// plain line and one that names no definition are still served.
func TestOperationActivity_UnwiredHandlerFailsClosedOnOpLogLines(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now()
	repairsAttrs := `{"def_id":"` + repairs.ApplyOpID + `"}`
	scanAttrs := `{"def_id":"library.scan"}`
	h := NewActivityHandler(&fakeActivityLog{}, opLogs{
		logsOnlyOp: {
			{OperationID: logsOnlyOp, Level: "info", Message: repairsLogLine, Attrs: repairsAttrs, CreatedAt: now},
			{OperationID: logsOnlyOp, Level: "info", Message: plainLogLine, Attrs: scanAttrs, CreatedAt: now},
			{OperationID: logsOnlyOp, Level: "info", Message: nodefLogLine, CreatedAt: now},
		},
	})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if ps := c.GetHeader("X-Perms"); ps != "" {
			c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), strings.Split(ps, ",")))
		}
		c.Next()
	})
	r.GET("/operations/:id/activity", h.ListOperationActivity)
	r.POST("/operations/activity/merged", h.ListMergedOperationActivity)

	for _, perms := range []string{viewerPerms, adminPerms} {
		w := activityGet(r, http.MethodGet, "/operations/"+logsOnlyOp+"/activity", perms, "")
		require.Equal(t, http.StatusOK, w.Code, perms+": "+w.Body.String())
		assert.NotContains(t, w.Body.String(), "REPAIRS-OPLOG-LINE", perms)
		assert.Contains(t, w.Body.String(), "PLAIN-OPLOG-LINE", perms)
		assert.Contains(t, w.Body.String(), "NODEF-OPLOG-LINE", perms)

		w = activityGet(r, http.MethodPost, "/operations/activity/merged", perms, `{"ids":["`+logsOnlyOp+`"]}`)
		require.Equal(t, http.StatusOK, w.Code, perms+" merged: "+w.Body.String())
		assert.NotContains(t, w.Body.String(), "REPAIRS-OPLOG-LINE", perms+" merged")
		assert.Contains(t, w.Body.String(), "PLAIN-OPLOG-LINE", perms+" merged")
	}
}
