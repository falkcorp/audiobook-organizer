// file: internal/server/handlers/opaccess/repairs_test.go
// version: 1.1.0
// guid: 7e3a9d15-48bc-4f02-b6a1-0d9c5f8e2b74
// last-edited: 2026-10-24

package opaccess

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// scopedFixer is a repairs.Fixer that asks for its own permissions
// (repairs.PermissionScoped); only RequiredPermissions is ever called.
type scopedFixer struct {
	repairs.Fixer
	perms []auth.Permission
}

func (f scopedFixer) RequiredPermissions() []auth.Permission { return f.perms }

type fixers map[string]repairs.Fixer

func (m fixers) Get(id string) (repairs.Fixer, bool) { f, ok := m[id]; return f, ok }

func ptr(s string) *string { return &s }

func ctxHolding() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	return c, w
}

func holdsOnly(held ...auth.Permission) func(*gin.Context, auth.Permission) bool {
	return func(_ *gin.Context, p auth.Permission) bool {
		for _, h := range held {
			if h == p {
				return true
			}
		}
		return false
	}
}

func TestReader_MayRead(t *testing.T) {
	reg := fixers{
		"plain":  scopedFixer{}, // default: library.edit_metadata
		"scoped": scopedFixer{perms: []auth.Permission{auth.PermSettingsManage, auth.PermLibraryEditMetadata}},
	}
	row := func(def, params string, result *string) *database.OperationV2Row {
		return &database.OperationV2Row{ID: "op", DefID: def, Params: params, ResultData: result}
	}
	view := []auth.Permission{auth.PermLibraryView}
	edit := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata}
	admin := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage, auth.PermUsersManage}
	settingsOnly := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage}

	for name, tc := range map[string]struct {
		row  *database.OperationV2Row
		held []auth.Permission
		want bool
	}{
		"non-repairs op, viewer":         {row("library.scan", `{}`, nil), view, true},
		"no row":                         {nil, view, true},
		"plan, fixer in params, viewer":  {row(repairs.PlanOpID, `{"fixer_id":"plain"}`, nil), view, false},
		"plan, fixer in params, editor":  {row(repairs.PlanOpID, `{"fixer_id":"plain"}`, nil), edit, true},
		"apply, fixer in result only":    {row(repairs.ApplyOpID, `{}`, ptr(`{"fixer_id":"plain"}`)), edit, true},
		"apply, fixer in result, viewer": {row(repairs.ApplyOpID, ``, ptr(`{"fixer_id":"plain"}`)), view, false},
		"scoped fixer, editor":           {row(repairs.ApplyOpID, `{"fixer_id":"scoped"}`, nil), edit, false},
		"scoped fixer, admin":            {row(repairs.ApplyOpID, `{"fixer_id":"scoped"}`, nil), admin, true},
		"params win over result":         {row(repairs.ApplyOpID, `{"fixer_id":"plain"}`, ptr(`{"fixer_id":"scoped"}`)), edit, true},
		"no fixer anywhere, admin":       {row(repairs.ApplyOpID, `{}`, ptr(`{}`)), admin, false},
		"unparseable params and result":  {row(repairs.ApplyOpID, `not json`, ptr(`also not`)), admin, false},
		"unknown fixer, editor":          {row(repairs.ApplyOpID, `{"fixer_id":"gone"}`, nil), edit, false},
		"unknown fixer, admin":           {row(repairs.ApplyOpID, `{"fixer_id":"gone"}`, nil), admin, true},
		// A removed fixer could have been the users.manage one (the two
		// read-status imports), so settings.manage alone is not enough.
		"unknown fixer, settings admin": {row(repairs.ApplyOpID, `{"fixer_id":"gone"}`, nil), settingsOnly, false},
	} {
		c, _ := ctxHolding()
		got := NewRepairs(reg, holdsOnly(tc.held...)).For(c).MayRead(tc.row)
		require.Equal(t, tc.want, got, name)
	}
}

// A check the server did not wire, or one with no fixer registry, refuses every
// repairs op and leaves every other op alone.
func TestReader_FailsClosedWhenUnwired(t *testing.T) {
	apply := &database.OperationV2Row{ID: "op", DefID: repairs.ApplyOpID, Params: `{"fixer_id":"plain"}`}
	scan := &database.OperationV2Row{ID: "op", DefID: "library.scan"}
	all := holdsOnly(auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage)
	c, w := ctxHolding()
	for name, a := range map[string]*Repairs{
		"nil check":    nil,
		"nil fixers":   NewRepairs(nil, all),
		"nil holds":    NewRepairs(fixers{"plain": scopedFixer{}}, nil),
		"zero checker": {},
	} {
		require.False(t, a.For(c).MayRead(apply), name)
		require.True(t, a.For(c).MayRead(scan), name)
	}
	require.False(t, (*Repairs)(nil).For(c).Require(apply))
	require.Equal(t, http.StatusForbidden, w.Code)
}

// The verdict for a fixer is asked once per request, not once per row.
func TestReader_AsksOncePerFixer(t *testing.T) {
	asked := 0
	a := NewRepairs(fixers{"plain": scopedFixer{}}, func(*gin.Context, auth.Permission) bool { asked++; return true })
	c, _ := ctxHolding()
	r := a.For(c)
	row := &database.OperationV2Row{DefID: repairs.PlanOpID, Params: `{"fixer_id":"plain"}`}
	for range 50 {
		require.True(t, r.MayRead(row))
	}
	require.Equal(t, 1, asked)
}

// The v3 run record states its fixer the way a v2 row does (params, else
// result), and the two surfaces give one verdict.
func TestReader_MayReadRunAgreesWithRows(t *testing.T) {
	reg := fixers{"scoped": scopedFixer{perms: []auth.Permission{auth.PermSettingsManage}}}
	edit := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata}
	admin := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage}
	for name, tc := range map[string]struct {
		rec  *database.RunRecord
		held []auth.Permission
		want bool
	}{
		"nil":                       {nil, edit, true},
		"non-repairs":               {&database.RunRecord{DefID: "library.scan"}, nil, true},
		"params, editor":            {&database.RunRecord{DefID: repairs.PlanOpID, Params: []byte(`{"fixer_id":"scoped"}`)}, edit, false},
		"params, admin":             {&database.RunRecord{DefID: repairs.PlanOpID, Params: []byte(`{"fixer_id":"scoped"}`)}, admin, true},
		"result only, admin":        {&database.RunRecord{DefID: repairs.ApplyOpID, Result: []byte(`{"fixer_id":"scoped"}`)}, admin, true},
		"no fixer, admin":           {&database.RunRecord{DefID: repairs.ApplyOpID, Params: []byte(`{}`)}, admin, false},
		"unparseable params, admin": {&database.RunRecord{DefID: repairs.ApplyOpID, Params: []byte(`nope`)}, admin, false},
	} {
		c, _ := ctxHolding()
		require.Equal(t, tc.want, NewRepairs(reg, holdsOnly(tc.held...)).For(c).MayReadRun(tc.rec), name)
	}
}

// The fixer-level verdict the event broker carries on a message: a fixer id of
// a non-repairs def is never asked about, and an empty fixer of a repairs def is
// refused.
func TestReader_MayReadFixer(t *testing.T) {
	a := NewRepairs(fixers{"plain": scopedFixer{}}, holdsOnly(auth.PermLibraryEditMetadata))
	c, _ := ctxHolding()
	r := a.For(c)
	require.True(t, r.MayReadFixer("library.scan", ""))
	require.False(t, r.MayReadFixer(repairs.ApplyOpID, ""))
	require.True(t, r.MayReadFixer(repairs.ApplyOpID, "plain"))
	require.False(t, r.MayReadFixer(repairs.PlanOpID, "gone"))
}
