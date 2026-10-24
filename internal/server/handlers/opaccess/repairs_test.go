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
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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
	admin := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage}
	everything := auth.All() // the admin role

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
		// A fixer this build does not know needs every permission: a custom
		// role with settings.manage but not users.manage (which
		// master-list-import names) is refused.
		"unknown fixer, settings.manage without users.manage": {row(repairs.ApplyOpID, `{"fixer_id":"gone"}`, nil), admin, false},
		"unknown fixer, admin":                                {row(repairs.ApplyOpID, `{"fixer_id":"gone"}`, nil), everything, true},
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

// MayReadChange decides from the journal row alone: a user-state row is always
// scoped (to its fixer, or to every permission when the fixer is unknown), a
// row whose Source is a registered fixer is scoped to that fixer, and every
// other row, including one whose Source is a v3 op definition id, is readable.
func TestReader_MayReadChange(t *testing.T) {
	reg := fixers{
		"plain":  scopedFixer{}, // default: library.edit_metadata
		"scoped": scopedFixer{perms: []auth.Permission{auth.PermSettingsManage, auth.PermLibraryEditMetadata}},
	}
	change := func(changeType, source string) *database.OperationChange {
		return &database.OperationChange{ID: "c", OperationID: "op-gone", BookID: "b", ChangeType: changeType, Source: source}
	}
	userState := undo.ChangeTypeUserBookStateSet
	view := []auth.Permission{auth.PermLibraryView}
	edit := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata}
	settings := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage}
	everything := auth.All()

	for name, tc := range map[string]struct {
		ch   *database.OperationChange
		held []auth.Permission
		want bool
	}{
		"nil row":                                 {nil, view, true},
		"unstamped metadata row, viewer":          {change("metadata_update", ""), view, true},
		"v3 def id as source, viewer":             {change("metadata_update", "library.scan"), view, true},
		"removed fixer's metadata row, viewer":    {change("metadata_update", "gone"), view, true},
		"fixer's metadata row, viewer":            {change("metadata_update", "plain"), view, false},
		"fixer's metadata row, editor":            {change("metadata_update", "plain"), edit, true},
		"scoped fixer's row, editor":              {change("file_move", "scoped"), edit, false},
		"scoped fixer's row, settings":            {change("file_move", "scoped"), settings, true},
		"user state, fixer, viewer":               {change(userState, "plain"), view, false},
		"user state, fixer, editor":               {change(userState, "plain"), edit, true},
		"user state, unknown fixer, settings":     {change(userState, "gone"), settings, false},
		"user state, unknown fixer, admin":        {change(userState, "gone"), everything, true},
		"user state, no source, settings":         {change(userState, ""), settings, false},
		"user state, no source, admin":            {change(userState, ""), everything, true},
		"user state, v3 def id as source, editor": {change(userState, "library.scan"), edit, false},
	} {
		c, _ := ctxHolding()
		got := NewRepairs(reg, holdsOnly(tc.held...)).For(c).MayReadChange(tc.ch)
		require.Equal(t, tc.want, got, name)
	}
}

// Unwired, a journal row that could be Repairs data (user state, or any
// stamped Source, since which Sources are fixers is unknown) is refused and an
// unstamped row is not.
func TestReader_MayReadChangeFailsClosedWhenUnwired(t *testing.T) {
	all := holdsOnly(auth.All()...)
	c, _ := ctxHolding()
	for name, a := range map[string]*Repairs{
		"nil check":    nil,
		"nil fixers":   NewRepairs(nil, all),
		"nil holds":    NewRepairs(fixers{"plain": scopedFixer{}}, nil),
		"zero checker": {},
	} {
		r := a.For(c)
		require.False(t, r.MayReadChange(&database.OperationChange{ChangeType: undo.ChangeTypeUserBookStateSet, Source: "plain"}), name)
		require.False(t, r.MayReadChange(&database.OperationChange{ChangeType: undo.ChangeTypeUserBookStateSet}), name)
		require.False(t, r.MayReadChange(&database.OperationChange{ChangeType: "metadata_update", Source: "plain"}), name)
		require.True(t, r.MayReadChange(&database.OperationChange{ChangeType: "metadata_update"}), name)
	}
}

// ReadableChanges keeps the readable rows in order and never answers nil.
func TestReader_ReadableChanges(t *testing.T) {
	a := NewRepairs(fixers{"plain": scopedFixer{}}, holdsOnly(auth.PermLibraryView))
	c, _ := ctxHolding()
	r := a.For(c)
	require.NotNil(t, r.ReadableChanges(nil))
	in := []*database.OperationChange{
		{ID: "1", ChangeType: "metadata_update"},
		{ID: "2", ChangeType: undo.ChangeTypeUserBookStateSet, Source: "plain"},
		{ID: "3", ChangeType: "file_move", Source: "library.scan"},
		{ID: "4", ChangeType: "file_move", Source: "plain"},
	}
	var ids []string
	for _, ch := range r.ReadableChanges(in) {
		ids = append(ids, ch.ID)
	}
	require.Equal(t, []string{"1", "3"}, ids)
}
