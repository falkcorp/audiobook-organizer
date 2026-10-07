// file: internal/server/handlers/repairs/owner_apply_test.go
// version: 1.0.0
// guid: 4c8a1e57-3b29-4d6f-a0e4-6f2d9b7c1a83
// last-edited: 2026-10-06

package repairs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// ownerPlanRows: "own" is owner-applicable, "manual" is a plain manual row,
// "fix" an ordinary applicable row.
func ownerPlanRows() []repairs.Row {
	return []repairs.Row{
		{RowID: "own", BookIDs: []string{"b-own"}, Title: "Owner Book", Skipped: repairs.SkipITunes, SkipReason: "iTunes tracks this file",
			OwnerApplicable: true, OwnerApplyReason: "byte-identical", OwnerWrites: []string{"b-own"}, Fingerprint: "fp"},
		{RowID: "manual", BookIDs: []string{"b-man"}, Title: "Manual", Skipped: repairs.SkipITunes, SkipReason: "iTunes book", Fingerprint: "fp"},
		{RowID: "fix", BookIDs: []string{"b-fix"}, Title: "Fix", Risk: repairs.RiskLow, Fingerprint: "fp"},
	}
}

type ownerCaller struct {
	method auth.Method
	user   *database.User // nil: no user bound
}

// ownerSetup serves owner-apply behind a stand-in for the auth middleware
// that binds caller's user and method, with its own grant store.
func ownerSetup(t *testing.T, caller ownerCaller) (*gin.Engine, *fakeEnqueuer, *repairs.OwnerGrants) {
	t.Helper()
	_, enq, ops := setup(t)
	ops.rows["op-owner-plan"] = &database.OperationV2Row{ID: "op-owner-plan", DefID: repairs.PlanOpID, Status: "completed",
		Params: `{"fixer_id":"version-group-primary-repair"}`, ResultData: storedPlan(t, "version-group-primary-repair", ownerPlanRows())}
	reg := repairs.NewRegistry()
	require.NoError(t, reg.Register(stubFixer{id: "version-group-primary-repair"}))
	h := New(reg, enq, ops)
	h.grants = repairs.NewOwnerGrants()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		if caller.user != nil {
			ctx = auth.WithUser(ctx, caller.user)
		}
		c.Request = c.Request.WithContext(auth.WithMethod(ctx, caller.method))
		c.Next()
	})
	g := r.Group("/api/v1")
	g.POST("/repairs/:fixer/owner-apply", h.OwnerApply)
	g.POST("/repairs/:fixer/apply", h.StartApply)
	return r, enq, h.grants
}

const ownerPath = "/api/v1/repairs/version-group-primary-repair/owner-apply"

func ownerPost(r *gin.Engine, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Host = "books.example.com"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

var (
	ownerUser   = &database.User{ID: "u-owner", Username: "owner", Status: "active", Roles: []string{"admin"}}
	browserHdrs = map[string]string{OwnerApplyHeader: "1", "Origin": "https://books.example.com", "Sec-Fetch-Site": "same-origin"}
)

// The owner's click from the Repairs page: a grant naming exactly this user,
// plan and row is minted, and the enqueued apply carries it; the apply's own
// ResolveOwnerApproval accepts it once.
func TestOwnerApply_InteractiveOwnerEnqueuesGrantedApply(t *testing.T) {
	for _, m := range []auth.Method{auth.MethodSession, auth.MethodCFAccess} {
		t.Run(string(m), func(t *testing.T) {
			r, enq, grants := ownerSetup(t, ownerCaller{method: m, user: ownerUser})
			w := ownerPost(r, ownerPath, `{"plan_op_id":"op-owner-plan","row_id":"own"}`, browserHdrs)
			require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
			require.Len(t, enq.calls, 1)
			require.Equal(t, repairs.ApplyOpID, enq.calls[0].defID)
			var p repairs.ApplyParams
			require.NoError(t, json.Unmarshal(enq.calls[0].params, &p))
			require.Equal(t, []string{"own"}, p.OwnerApplyRowIDs)
			require.Empty(t, p.RowIDs, "no bulk rows ride along")
			require.NotNil(t, p.DryRun)
			require.False(t, *p.DryRun)
			require.NotEmpty(t, p.OwnerGrant)

			a := repairs.ResolveOwnerApproval(grants, p)
			require.Empty(t, a.Refused)
			require.Equal(t, "u-owner", a.UserID)
			require.Equal(t, string(m), a.AuthMethod)
			require.NotEmpty(t, repairs.ResolveOwnerApproval(grants, p).Refused, "single use")
		})
	}
}

// API keys, ABS tokens, temp-login / invite sessions, bootstrap (no method),
// a non-admin and a missing user are all refused, and nothing is enqueued.
func TestOwnerApply_RefusesNonInteractiveOrNonAdmin(t *testing.T) {
	plainUser := &database.User{ID: "u-reader", Status: "active", Roles: []string{"user"}}
	for name, caller := range map[string]ownerCaller{
		"api key":           {auth.MethodAPIKey, ownerUser},
		"abs token":         {auth.MethodABS, ownerUser},
		"delegated session": {auth.MethodSessionDelegated, ownerUser},
		"no method":         {auth.MethodNone, ownerUser},
		"no user":           {auth.MethodSession, nil},
		"not admin":         {auth.MethodSession, plainUser},
	} {
		t.Run(name, func(t *testing.T) {
			r, enq, _ := ownerSetup(t, caller)
			w := ownerPost(r, ownerPath, `{"plan_op_id":"op-owner-plan","row_id":"own"}`, browserHdrs)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Empty(t, enq.calls)
		})
	}
}

// CSRF: a request without the custom header, from another origin, or marked
// cross-site by the browser is refused even for the owner's session.
func TestOwnerApply_RefusesCrossSite(t *testing.T) {
	for name, hdr := range map[string]map[string]string{
		"no header":         {"Origin": "https://books.example.com"},
		"header not 1":      {OwnerApplyHeader: "yes"},
		"foreign origin":    {OwnerApplyHeader: "1", "Origin": "https://evil.example"},
		"lookalike origin":  {OwnerApplyHeader: "1", "Origin": "https://books.example.com.evil.example"},
		"null origin":       {OwnerApplyHeader: "1", "Origin": "null"},
		"cross-site fetch":  {OwnerApplyHeader: "1", "Sec-Fetch-Site": "cross-site"},
		"same-site sibling": {OwnerApplyHeader: "1", "Sec-Fetch-Site": "same-site"},
	} {
		t.Run(name, func(t *testing.T) {
			r, enq, _ := ownerSetup(t, ownerCaller{auth.MethodSession, ownerUser})
			w := ownerPost(r, ownerPath, `{"plan_op_id":"op-owner-plan","row_id":"own"}`, hdr)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Empty(t, enq.calls)
		})
	}
	// A non-browser client with the header and no Origin/Sec-Fetch-Site is
	// still bound by the interactive-session requirement above.
	r, _, _ := ownerSetup(t, ownerCaller{auth.MethodSession, ownerUser})
	require.Equal(t, http.StatusAccepted,
		ownerPost(r, ownerPath, `{"plan_op_id":"op-owner-plan","row_id":"own"}`, map[string]string{OwnerApplyHeader: "1"}).Code)
}

// Only a row the plan marked owner-applicable can be granted.
func TestOwnerApply_RowMustBeOwnerApplicable(t *testing.T) {
	r, enq, _ := ownerSetup(t, ownerCaller{auth.MethodSession, ownerUser})
	for body, want := range map[string]int{
		`{"plan_op_id":"op-owner-plan","row_id":"manual"}`:  http.StatusBadRequest,
		`{"plan_op_id":"op-owner-plan","row_id":"fix"}`:     http.StatusBadRequest,
		`{"plan_op_id":"op-owner-plan","row_id":"missing"}`: http.StatusNotFound,
		`{"plan_op_id":"op-owner-plan"}`:                    http.StatusBadRequest,
		`{"row_id":"own"}`:                                  http.StatusBadRequest,
		`{"plan_op_id":"op-scan","row_id":"own"}`:           http.StatusBadRequest,
	} {
		require.Equal(t, want, ownerPost(r, ownerPath, body, browserHdrs).Code, body)
	}
	require.Empty(t, enq.calls)
}

// The bulk endpoint never carries owner rows: an API key is refused, a
// person is pointed at the per-row endpoint; nothing is enqueued.
func TestStartApply_RefusesOwnerRowIDs(t *testing.T) {
	body := `{"plan_op_id":"op-owner-plan","row_ids":["fix"],"owner_apply_row_ids":["own"],"dry_run":false}`
	path := "/api/v1/repairs/version-group-primary-repair/apply"
	r, enq, _ := ownerSetup(t, ownerCaller{auth.MethodAPIKey, ownerUser})
	require.Equal(t, http.StatusForbidden, ownerPost(r, path, body, nil).Code)
	r2, enq2, _ := ownerSetup(t, ownerCaller{auth.MethodSession, ownerUser})
	require.Equal(t, http.StatusBadRequest, ownerPost(r2, path, body, browserHdrs).Code)
	require.Empty(t, enq.calls)
	require.Empty(t, enq2.calls)
}
