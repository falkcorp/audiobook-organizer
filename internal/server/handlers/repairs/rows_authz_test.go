// file: internal/server/handlers/repairs/rows_authz_test.go
// version: 1.0.0
// guid: 5b7e2d94-c1a8-4f63-9e0b-3d6a8f1c4e27
// last-edited: 2026-10-24

package repairs

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// rowsAuthzServer serves GET /repairs and the rows route for two fixers: "plain"
// (the lane's default library.edit_metadata) and "scoped" (settings.manage),
// each with a stored completed plan recorded as its last plan. The plan rows'
// titles are marked so a test can tell whether any row data reached the body.
func rowsAuthzServer(t *testing.T, user *database.User, perms ...auth.Permission) *gin.Engine {
	t.Helper()
	reg := repairs.NewRegistry()
	require.NoError(t, reg.Register(stubFixer{id: "plain"}))
	require.NoError(t, reg.Register(scopedFixer{stubFixer{id: "scoped"}}))
	ops := newFakeOps(map[string]string{
		"repairs_last_plan_op:plain":  "op-plain-plan",
		"repairs_last_plan_op:scoped": "op-scoped-plan",
	})
	ops.addPlan(t, "op-plain-plan", "plain", time.Time{}, planRows(3))
	ops.addPlan(t, "op-scoped-plan", "scoped", time.Time{}, planRows(3))
	h := New(reg, &fakeEnqueuer{}, ops)
	r := gin.New()
	r.Use(callerWith(user, perms...))
	r.GET("/repairs", h.ListFixers)
	r.GET("/repairs/:fixer/plan/:op_id/rows", h.ListPlanRows)
	return r
}

func listFixers(t *testing.T, r *gin.Engine) map[string]FixerInfo {
	t.Helper()
	w := do(r, http.MethodGet, "/repairs", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	out := map[string]FixerInfo{}
	for _, f := range data[struct {
		Fixers []FixerInfo `json:"fixers"`
	}](t, w).Fixers {
		out[f.ID] = f
	}
	require.Len(t, out, 2, "every fixer stays listed")
	return out
}

// A guest holds the viewer role (library.view): reading a plan's rows is a 403
// for every fixer, before any row is read, and GET /repairs lists the fixers
// without the op ids and counts of their last runs.
func TestListPlanRows_ViewerIsRefusedAndListFixersHidesLastRuns(t *testing.T) {
	r := rowsAuthzServer(t, &database.User{ID: "u-guest", Username: "guest"}, auth.PermLibraryView)

	for path, want := range map[string]string{
		"/repairs/scoped/plan/op-scoped-plan/rows": "permission denied: settings.manage",
		"/repairs/plain/plan/op-plain-plan/rows":   "permission denied: library.edit_metadata",
	} {
		w := do(r, http.MethodGet, path, "")
		require.Equal(t, http.StatusForbidden, w.Code, path+": "+w.Body.String())
		require.Contains(t, w.Body.String(), want, path)
		require.NotContains(t, w.Body.String(), "vg-000", path+": no row reached the body")
		require.NotContains(t, w.Body.String(), "Book 0", path)
	}
	// The permission check comes before the request is even validated, so a bad
	// page request or an unknown op id cannot be used to probe.
	require.Equal(t, http.StatusForbidden, do(r, http.MethodGet, "/repairs/plain/plan/op-nope/rows?limit=0", "").Code)

	w := do(r, http.MethodGet, "/repairs", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "op-plain-plan")
	require.NotContains(t, w.Body.String(), "op-scoped-plan")
	require.NotContains(t, w.Body.String(), "last_plan")
	for id, f := range listFixers(t, r) {
		require.False(t, f.Permitted, id)
		require.Nil(t, f.LastPlan, id)
		require.Nil(t, f.LastApply, id)
	}
}

// An editor (library.edit_metadata) reads the rows of a fixer that asks for the
// default only; a fixer that also needs settings.manage stays closed to it, and
// its last-run pointers are left off the list.
func TestListPlanRows_EditorSeesOnlyTheFixersItMayUse(t *testing.T) {
	r := rowsAuthzServer(t, &database.User{ID: "u-editor", Username: "editor"}, auth.PermLibraryEditMetadata)

	w := do(r, http.MethodGet, "/repairs/plain/plan/op-plain-plan/rows", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 3, data[repairs.RowsPage](t, w).Total)

	w = do(r, http.MethodGet, "/repairs/scoped/plan/op-scoped-plan/rows", "")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "permission denied: settings.manage")
	require.NotContains(t, w.Body.String(), "vg-000")

	got := listFixers(t, r)
	require.True(t, got["plain"].Permitted)
	require.NotNil(t, got["plain"].LastPlan)
	require.Equal(t, "op-plain-plan", got["plain"].LastPlan.OperationID)
	require.Equal(t, 3, *got["plain"].LastPlan.Total)
	require.False(t, got["scoped"].Permitted)
	require.Nil(t, got["scoped"].LastPlan)
	require.Nil(t, got["scoped"].LastApply)
	w = do(r, http.MethodGet, "/repairs", "")
	require.NotContains(t, w.Body.String(), "op-scoped-plan")
}

// An admin (every permission) still gets every fixer's rows and last runs.
func TestListPlanRows_AdminStillGetsEverything(t *testing.T) {
	r := rowsAuthzServer(t, &database.User{ID: "u-admin", Username: "admin"},
		auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage)
	for path, op := range map[string]string{
		"/repairs/plain/plan/op-plain-plan/rows":   "op-plain-plan",
		"/repairs/scoped/plan/op-scoped-plan/rows": "op-scoped-plan",
	} {
		w := do(r, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, w.Code, path+": "+w.Body.String())
		require.Equal(t, op, data[repairs.RowsPage](t, w).PlanOpID)
	}
	for id, f := range listFixers(t, r) {
		require.True(t, f.Permitted, id)
		require.NotNil(t, f.LastPlan, id)
	}
}

// With no caller at all the strict default check answers 401 and nothing is read.
func TestListPlanRows_NoCallerIs401(t *testing.T) {
	r := rowsAuthzServer(t, nil)
	w := do(r, http.MethodGet, "/repairs/plain/plan/op-plain-plan/rows", "")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "vg-000")
}

// An op id of another fixer's plan, passed under a fixer the caller may use, is
// refused (400) without a row being read: the permission is the URL fixer's,
// so the plan must belong to that fixer.
func TestListPlanRows_OtherFixersPlanUnderPermittedFixerIsRefused(t *testing.T) {
	// An admin may use both, so the refusal is the mismatch and not the permission.
	for name, perms := range map[string][]auth.Permission{
		"editor": {auth.PermLibraryEditMetadata},
		"admin":  {auth.PermLibraryEditMetadata, auth.PermSettingsManage},
	} {
		r := rowsAuthzServer(t, &database.User{ID: "u-" + name, Username: name}, perms...)
		w := do(r, http.MethodGet, "/repairs/plain/plan/op-scoped-plan/rows", "")
		require.Equal(t, http.StatusBadRequest, w.Code, name+": "+w.Body.String())
		require.NotContains(t, w.Body.String(), "vg-000", name)
		require.NotContains(t, w.Body.String(), "Book 0", name)
	}
}

// The server's own check (WithPermissionCheck) only answers held or not; the
// handler writes the refusal, so the rows route is a 403 naming the permission
// and GET /repairs, which asks the same check, is still one 200 envelope with
// the fixer marked not permitted.
func TestListPlanRows_ServerPermissionCheckIsHonouredAndListStaysOneResponse(t *testing.T) {
	reg := repairs.NewRegistry()
	require.NoError(t, reg.Register(stubFixer{id: "plain"}))
	ops := newFakeOps(map[string]string{"repairs_last_plan_op:plain": "op-plain-plan"})
	ops.addPlan(t, "op-plain-plan", "plain", time.Time{}, planRows(2))
	asked := 0
	h := New(reg, &fakeEnqueuer{}, ops).WithPermissionCheck(func(*gin.Context, auth.Permission) bool {
		asked++
		return false
	})
	r := gin.New()
	r.Use(callerWith(&database.User{ID: "u-guest", Username: "guest"}, auth.PermLibraryView))
	r.GET("/repairs", h.ListFixers)
	r.GET("/repairs/:fixer/plan/:op_id/rows", h.ListPlanRows)

	w := do(r, http.MethodGet, "/repairs/plain/plan/op-plain-plan/rows", "")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "permission denied: library.edit_metadata")
	require.Equal(t, 1, asked)

	got := listOnlyFixer(t, r)
	require.False(t, got.Permitted)
	require.Nil(t, got.LastPlan)
	require.Equal(t, 2, asked, "the list asked the same check once for the one fixer")
}

func listOnlyFixer(t *testing.T, r *gin.Engine) FixerInfo {
	t.Helper()
	w := do(r, http.MethodGet, "/repairs", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	fixers := data[struct {
		Fixers []FixerInfo `json:"fixers"`
	}](t, w).Fixers
	require.Len(t, fixers, 1)
	return fixers[0]
}
