// file: internal/server/handlers/opaccess/repairs_test.go
// version: 1.9.0
// guid: 7e3a9d15-48bc-4f02-b6a1-0d9c5f8e2b74
// last-edited: 2026-10-30

package opaccess

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// opRows is an OpLookup over a fixed set of op rows; an id in fail errors.
type opRows struct {
	rows  map[string]*database.OperationV2Row
	fail  map[string]bool
	reads map[string]int
}

func (o *opRows) GetOperationV2(id string) (*database.OperationV2Row, error) {
	if o.reads == nil {
		o.reads = map[string]int{}
	}
	o.reads[id]++
	if o.fail[id] {
		return nil, errors.New("invented store failure")
	}
	return o.rows[id], nil
}

// noOps is an OpLookup with no op rows at all.
var noOps = &opRows{}

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
		got := NewRepairs(reg, noOps, holdsOnly(tc.held...)).For(c).MayRead(tc.row)
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
		"nil fixers":   NewRepairs(nil, noOps, all),
		"nil ops":      NewRepairs(fixers{"plain": scopedFixer{}}, nil, all),
		"nil holds":    NewRepairs(fixers{"plain": scopedFixer{}}, noOps, nil),
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
	a := NewRepairs(fixers{"plain": scopedFixer{}}, noOps, func(*gin.Context, auth.Permission) bool { asked++; return true })
	c, _ := ctxHolding()
	r := a.For(c)
	row := &database.OperationV2Row{DefID: repairs.PlanOpID, Params: `{"fixer_id":"plain"}`}
	for range 50 {
		require.True(t, r.MayRead(row))
	}
	require.Equal(t, 1, asked)
}

// MayReadChange: a row whose Source is a registered fixer is scoped to that
// fixer; a row with no Source is scoped by its op row while that exists; a
// user-data row (undo.IsUserDataChangeType) also needs users.manage, and
// every permission when its fixer is unknown or unnamed; every other row,
// including one whose Source is a v3 op definition id, is readable.
func TestReader_MayReadChange(t *testing.T) {
	reg := fixers{
		"plain":  scopedFixer{}, // default: library.edit_metadata
		"scoped": scopedFixer{perms: []auth.Permission{auth.PermSettingsManage, auth.PermLibraryEditMetadata}},
	}
	ops := &opRows{
		rows: map[string]*database.OperationV2Row{
			"op-plain-apply": {ID: "op-plain-apply", DefID: repairs.ApplyOpID, Params: `{"fixer_id":"plain"}`},
			"op-nofixer":     {ID: "op-nofixer", DefID: repairs.ApplyOpID, Params: `{}`},
			"op-scan":        {ID: "op-scan", DefID: "library.scan"},
		},
		fail: map[string]bool{"op-unreadable": true},
	}
	change := func(changeType, source string) *database.OperationChange {
		return &database.OperationChange{ID: "c", OperationID: "op-gone", BookID: "b", ChangeType: changeType, Source: source}
	}
	under := func(changeType, opID string) *database.OperationChange {
		return &database.OperationChange{ID: "c", OperationID: opID, BookID: "b", ChangeType: changeType}
	}
	userState := undo.ChangeTypeUserBookStateSet
	follow := undo.ChangeTypeUserStateFollow
	content := undo.ChangeTypeUserContentFollow
	view := []auth.Permission{auth.PermLibraryView}
	edit := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata}
	settings := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage}
	// A custom role that may manage users and edit metadata, nothing else.
	editUsers := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermUsersManage}
	everything := auth.All()

	for name, tc := range map[string]struct {
		ch   *database.OperationChange
		held []auth.Permission
		want bool
	}{
		"nil row":                              {nil, view, true},
		"unstamped metadata row, viewer":       {change("metadata_update", ""), view, true},
		"v3 def id as source, viewer":          {change("metadata_update", "library.scan"), view, true},
		"removed fixer's metadata row, viewer": {change("metadata_update", "gone"), view, true},
		"fixer's metadata row, viewer":         {change("metadata_update", "plain"), view, false},
		"fixer's metadata row, editor":         {change("metadata_update", "plain"), edit, true},
		"scoped fixer's row, editor":           {change("file_move", "scoped"), edit, false},
		"scoped fixer's row, settings":         {change("file_move", "scoped"), settings, true},

		// User data is admin data: users.manage on top of the fixer's
		// permissions, whoever may run the fixer.
		"user state, fixer, viewer":                       {change(userState, "plain"), view, false},
		"user state, fixer, editor":                       {change(userState, "plain"), edit, false},
		"user state, fixer, editor with users.manage":     {change(userState, "plain"), editUsers, true},
		"user state, fixer, admin":                        {change(userState, "plain"), everything, true},
		"user state, unknown fixer, settings":             {change(userState, "gone"), settings, false},
		"user state, unknown fixer, users.manage":         {change(userState, "gone"), editUsers, false},
		"user state, unknown fixer, admin":                {change(userState, "gone"), everything, true},
		"user state, no source, settings":                 {change(userState, ""), settings, false},
		"user state, no source, admin":                    {change(userState, ""), everything, true},
		"user state, v3 def id as source, editor":         {change(userState, "library.scan"), edit, false},
		"follow, no source, viewer":                       {change(follow, ""), view, false},
		"follow, no source, editor":                       {change(follow, ""), edit, false},
		"follow, no source, users.manage without all":     {change(follow, ""), editUsers, false},
		"follow, no source, admin":                        {change(follow, ""), everything, true},
		"follow, removed fixer, viewer":                   {change(follow, "gone"), view, false},
		"follow, removed fixer, editor":                   {change(follow, "gone"), edit, false},
		"follow, removed fixer, admin":                    {change(follow, "gone"), everything, true},
		"follow, registered fixer, viewer":                {change(follow, "plain"), view, false},
		"follow, registered fixer, editor":                {change(follow, "plain"), edit, false},
		"follow, registered fixer, editor with users.mgr": {change(follow, "plain"), editUsers, true},
		"follow, registered fixer, admin":                 {change(follow, "plain"), everything, true},
		"content follow, no source, viewer":               {change(content, ""), view, false},
		"content follow, registered fixer, editor":        {change(content, "plain"), edit, false},
		"content follow, registered fixer, admin":         {change(content, "plain"), everything, true},
		"owner apply note, registered fixer, editor":      {change(undo.ChangeTypeRepairOwnerApply, "plain"), edit, false},

		// A row with no Source is scoped by its op row while that exists.
		"unstamped row of a repairs op, viewer":           {under("metadata_update", "op-plain-apply"), view, false},
		"unstamped row of a repairs op, editor":           {under("metadata_update", "op-plain-apply"), edit, true},
		"unstamped row of a fixerless repairs op, admin":  {under("metadata_update", "op-nofixer"), everything, false},
		"unstamped row of a plain op, viewer":             {under("metadata_update", "op-scan"), view, true},
		"unstamped row, op row unreadable, admin":         {under("metadata_update", "op-unreadable"), everything, false},
		"unstamped row, no op id, viewer":                 {under("metadata_update", ""), view, true},
		"unstamped follow of a repairs op, editor":        {under(follow, "op-plain-apply"), edit, false},
		"unstamped follow of a repairs op, editor+users":  {under(follow, "op-plain-apply"), editUsers, true},
		"unstamped follow of a plain op, editor+users":    {under(follow, "op-scan"), editUsers, false},
		"unstamped follow of a plain op, admin":           {under(follow, "op-scan"), everything, true},
		"unstamped follow, op row unreadable, admin":      {under(follow, "op-unreadable"), everything, false},
		"unstamped follow of a fixerless repairs op, adm": {under(follow, "op-nofixer"), everything, false},
	} {
		c, _ := ctxHolding()
		got := NewRepairs(reg, ops, holdsOnly(tc.held...)).For(c).MayReadChange(tc.ch)
		require.Equal(t, tc.want, got, name)
	}
}

// MayUndoChange is the predicate the undo report and the revert both apply, and
// MayReadChange is the same one: a row that carries one user's own data needs
// users.manage on top of the fixer's permissions, because undoing it writes
// another user's listening state back. An editor who may run the fixer is
// refused its user-data rows (the revert used to let him undo them).
func TestReader_MayUndoChange(t *testing.T) {
	reg := fixers{"plain": scopedFixer{}}
	ops := &opRows{rows: map[string]*database.OperationV2Row{
		"op-plain-apply": {ID: "op-plain-apply", DefID: repairs.ApplyOpID, Params: `{"fixer_id":"plain"}`},
	}}
	view := []auth.Permission{auth.PermLibraryView}
	edit := []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata}
	editUsers := append([]auth.Permission{auth.PermUsersManage}, edit...)
	for name, tc := range map[string]struct {
		ch   *database.OperationChange
		held []auth.Permission
		want bool
	}{
		"stamped follow, editor":            {&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow, Source: "plain"}, edit, false},
		"stamped follow, editor+users":      {&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow, Source: "plain"}, editUsers, true},
		"stamped content follow, editor":    {&database.OperationChange{ChangeType: undo.ChangeTypeUserContentFollow, Source: "plain"}, edit, false},
		"stamped file move, editor":         {&database.OperationChange{ChangeType: undo.ChangeTypeBookFileMove, Source: "plain"}, edit, true},
		"stamped follow, viewer":            {&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow, Source: "plain"}, view, false},
		"unstamped follow of op, editor":    {&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow, OperationID: "op-plain-apply"}, edit, false},
		"unstamped follow of op, ed+users":  {&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow, OperationID: "op-plain-apply"}, editUsers, true},
		"unstamped follow of op, viewer":    {&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow, OperationID: "op-plain-apply"}, view, false},
		"unstamped follow, no op, editor":   {&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow, OperationID: "op-gone"}, edit, false},
		"removed fixer's follow, editor":    {&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow, Source: "gone"}, edit, false},
		"unstamped metadata, no op, viewer": {&database.OperationChange{ChangeType: "metadata_update", OperationID: "op-gone"}, view, true},
	} {
		c, _ := ctxHolding()
		r := NewRepairs(reg, ops, holdsOnly(tc.held...)).For(c)
		require.Equal(t, tc.want, r.MayUndoChange(tc.ch), name)
	}
}

// An op row is read once per request however many of its rows are checked,
// and users.manage is asked once.
func TestReader_MayReadChangeReadsEachOpOnce(t *testing.T) {
	ops := &opRows{rows: map[string]*database.OperationV2Row{
		"op-plain-apply": {ID: "op-plain-apply", DefID: repairs.ApplyOpID, Params: `{"fixer_id":"plain"}`},
	}}
	askedUsers := 0
	holds := func(_ *gin.Context, p auth.Permission) bool {
		if p == auth.PermUsersManage {
			askedUsers++
		}
		return true
	}
	c, _ := ctxHolding()
	r := NewRepairs(fixers{"plain": scopedFixer{}}, ops, holds).For(c)
	for range 40 {
		require.True(t, r.MayReadChange(&database.OperationChange{OperationID: "op-plain-apply", ChangeType: "metadata_update"}))
		require.True(t, r.MayReadChange(&database.OperationChange{OperationID: "op-plain-apply", ChangeType: undo.ChangeTypeUserStateFollow}))
	}
	require.Equal(t, 1, ops.reads["op-plain-apply"])
	require.Equal(t, 1, askedUsers)
}

// Unwired, a journal row that could be Repairs data (user data, or any
// stamped Source, since which Sources are fixers is unknown) is refused and an
// unstamped row is not.
func TestReader_MayReadChangeFailsClosedWhenUnwired(t *testing.T) {
	all := holdsOnly(auth.All()...)
	c, _ := ctxHolding()
	for name, a := range map[string]*Repairs{
		"nil check":    nil,
		"nil fixers":   NewRepairs(nil, noOps, all),
		"nil ops":      NewRepairs(fixers{"plain": scopedFixer{}}, nil, all),
		"nil holds":    NewRepairs(fixers{"plain": scopedFixer{}}, noOps, nil),
		"zero checker": {},
	} {
		r := a.For(c)
		require.False(t, r.MayReadChange(&database.OperationChange{ChangeType: undo.ChangeTypeUserBookStateSet, Source: "plain"}), name)
		require.False(t, r.MayReadChange(&database.OperationChange{ChangeType: undo.ChangeTypeUserBookStateSet}), name)
		require.False(t, r.MayReadChange(&database.OperationChange{ChangeType: "metadata_update", Source: "plain"}), name)
		require.False(t, r.MayReadChange(&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow}), name)
		require.False(t, r.MayUndoChange(&database.OperationChange{ChangeType: undo.ChangeTypeUserStateFollow}), name)
		require.True(t, r.MayReadChange(&database.OperationChange{ChangeType: "metadata_update"}), name)
	}
}

// ReadableChanges keeps the readable rows in order and never answers nil.
func TestReader_ReadableChanges(t *testing.T) {
	a := NewRepairs(fixers{"plain": scopedFixer{}}, noOps, holdsOnly(auth.PermLibraryView))
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

// A row refused because its op row could not be read leaves the error on the
// reader (LookupError), so a route can answer 500 rather than blame the
// caller's permissions. A refusal for permissions leaves none.
func TestReader_LookupError(t *testing.T) {
	ops := &opRows{
		rows: map[string]*database.OperationV2Row{
			"op-plain-apply": {ID: "op-plain-apply", DefID: repairs.ApplyOpID, Params: `{"fixer_id":"plain"}`},
		},
		fail: map[string]bool{"op-unreadable": true},
	}
	c, _ := ctxHolding()
	r := NewRepairs(fixers{"plain": scopedFixer{}}, ops, holdsOnly(auth.PermLibraryView)).For(c)
	require.False(t, r.MayUndoChange(&database.OperationChange{OperationID: "op-plain-apply", ChangeType: "metadata_update"}))
	require.NoError(t, r.LookupError(), "a permission refusal is not a lookup error")
	require.False(t, r.MayUndoChange(&database.OperationChange{OperationID: "op-unreadable", ChangeType: "metadata_update"}))
	require.False(t, r.MayReadChange(&database.OperationChange{OperationID: "op-unreadable", ChangeType: "metadata_update"}))
	require.ErrorContains(t, r.LookupError(), "invented store failure")
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
		require.Equal(t, tc.want, NewRepairs(reg, noOps, holdsOnly(tc.held...)).For(c).MayReadRun(tc.rec), name)
	}
}

// The fixer-level verdict the event broker carries on a message: a fixer id of
// a non-repairs def is never asked about, and an empty fixer of a repairs def is
// refused.
func TestReader_MayReadFixer(t *testing.T) {
	a := NewRepairs(fixers{"plain": scopedFixer{}}, noOps, holdsOnly(auth.PermLibraryEditMetadata))
	c, _ := ctxHolding()
	r := a.For(c)
	require.True(t, r.MayReadFixer("library.scan", ""))
	require.False(t, r.MayReadFixer(repairs.ApplyOpID, ""))
	require.True(t, r.MayReadFixer(repairs.ApplyOpID, "plain"))
	require.False(t, r.MayReadFixer(repairs.PlanOpID, "gone"))
}

// A stream reader asks the live check, not the connect-time one, and Refresh
// makes it ask again: a permission lost after the reader was made is lost for
// the next verdict. A request reader (For) keeps what it was given.
func TestReader_ForStreamAsksTheLiveCheckAgainAfterRefresh(t *testing.T) {
	reg := fixers{"plain": scopedFixer{}}
	ops := &opRows{rows: map[string]*database.OperationV2Row{}}
	connected := func(*gin.Context, auth.Permission) bool { return true }
	lost := false
	live := func(_ *gin.Context, p auth.Permission) bool { return !lost }
	access := NewRepairs(reg, ops, connected).WithLiveHolds(live)
	c, _ := ctxHolding()

	stream := access.ForStream(c)
	request := access.For(c)
	require.True(t, stream.MayReadFixer(repairs.ApplyOpID, "plain"))
	require.True(t, stream.MayReadUserData())
	require.True(t, request.MayReadFixer(repairs.ApplyOpID, "plain"))

	lost = true
	require.True(t, stream.MayReadFixer(repairs.ApplyOpID, "plain"), "the verdict is cached until Refresh")
	stream.Refresh()
	require.False(t, stream.MayReadFixer(repairs.ApplyOpID, "plain"), "the live check says the permission is gone")
	require.False(t, stream.MayReadUserData())
	require.True(t, request.MayReadFixer(repairs.ApplyOpID, "plain"), "a request reader keeps its connect-time answer")

	// With no live check set, a stream reader falls back to the one it has.
	plain := NewRepairs(reg, ops, connected).ForStream(c)
	plain.Refresh()
	require.True(t, plain.MayReadFixer(repairs.ApplyOpID, "plain"))
}

// fakeClock is a clock a test moves by hand.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

// A stream's progress events each refresh the reader, and each refresh used to
// cost a live question to the store per permission. With a recheck bound the
// questions are asked once per bound, not once per event, and what was last
// answered stands in between: a role lowered inside the window is served until
// it passes, and not a refresh longer.
func TestReader_StreamRecheckBoundsTheLiveQuestions(t *testing.T) {
	reg := fixers{"plain": scopedFixer{}}
	ops := &opRows{rows: map[string]*database.OperationV2Row{}}
	connected := func(*gin.Context, auth.Permission) bool { return true }
	asked := 0
	lost := false
	live := func(_ *gin.Context, _ auth.Permission) bool { asked++; return !lost }
	clock := &fakeClock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	access := NewRepairs(reg, ops, connected).WithLiveHolds(live).WithLiveRecheck(5*time.Second, clock.now)
	c, _ := ctxHolding()
	r := access.ForStream(c)

	r.Refresh()
	require.True(t, r.MayReadFixer(repairs.ApplyOpID, "plain"))
	require.True(t, r.MayReadUserData())
	first := asked
	require.Positive(t, first, "the first verdicts are asked live")

	// A burst of events inside the window: no new questions, verdicts stand.
	lost = true
	for i := 0; i < 50; i++ {
		clock.advance(50 * time.Millisecond)
		r.Refresh()
		require.True(t, r.MayReadFixer(repairs.ApplyOpID, "plain"), "event %d is inside the window", i)
		require.True(t, r.MayReadUserData())
	}
	require.Equal(t, first, asked, "50 events inside the window put no question to the store")

	// Past the window the next refresh asks again and the demotion lands.
	clock.advance(5 * time.Second)
	r.Refresh()
	require.False(t, r.MayReadFixer(repairs.ApplyOpID, "plain"))
	require.False(t, r.MayReadUserData())
	require.Greater(t, asked, first)
	afterDemotion := asked

	// And it is bounded again from there.
	r.Refresh()
	require.False(t, r.MayReadFixer(repairs.ApplyOpID, "plain"))
	require.Equal(t, afterDemotion, asked)
}

// A verdict is asked live once per permission between refreshes, however many
// fixers ask for it (each fixer's own verdict is cached, but two fixers that
// need the same permission are two verdicts).
func TestReader_StreamAsksEachPermissionOncePerRefresh(t *testing.T) {
	need := []auth.Permission{auth.PermLibraryEditMetadata}
	reg := fixers{"a": scopedFixer{perms: need}, "b": scopedFixer{perms: need}}
	ops := &opRows{rows: map[string]*database.OperationV2Row{}}
	connected := func(*gin.Context, auth.Permission) bool { return true }
	asked := map[auth.Permission]int{}
	live := func(_ *gin.Context, p auth.Permission) bool { asked[p]++; return true }
	access := NewRepairs(reg, ops, connected).WithLiveHolds(live)
	c, _ := ctxHolding()
	r := access.ForStream(c)

	require.True(t, r.MayReadFixer(repairs.ApplyOpID, "a"))
	require.True(t, r.MayReadFixer(repairs.ApplyOpID, "b"))
	require.Equal(t, 1, asked[auth.PermLibraryEditMetadata], "two fixers needing one permission ask it once")
	r.Refresh()
	require.True(t, r.MayReadFixer(repairs.ApplyOpID, "b"))
	require.Equal(t, 2, asked[auth.PermLibraryEditMetadata], "and again after a refresh")
}

// StreamOpen is the stream's question "does my caller still exist": false once
// the session check says no, asked at the recheck bound like the permissions, and
// always true for a reader with no session check or one that is not a stream's.
func TestReader_StreamOpenEndsAStreamWhoseCredentialIsGone(t *testing.T) {
	reg := fixers{}
	ops := &opRows{rows: map[string]*database.OperationV2Row{}}
	connected := func(*gin.Context, auth.Permission) bool { return true }
	standing := true
	asked := 0
	alive := func(*gin.Context) bool { asked++; return standing }
	clock := &fakeClock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	access := NewRepairs(reg, ops, connected).WithLiveSession(alive).WithLiveRecheck(5*time.Second, clock.now)
	c, _ := ctxHolding()
	stream := access.ForStream(c)

	require.True(t, stream.StreamOpen())
	standing = false
	for i := 0; i < 20; i++ {
		clock.advance(100 * time.Millisecond)
		require.True(t, stream.StreamOpen(), "inside the window the answer stands")
	}
	require.Equal(t, 1, asked, "20 events inside the window ask once")
	clock.advance(5 * time.Second)
	require.False(t, stream.StreamOpen(), "past the window the credential is asked again and the stream ends")
	require.False(t, stream.StreamOpen(), "and it stays ended")

	// Nothing to ask: open. A request reader is never ended by it.
	require.True(t, NewRepairs(reg, ops, connected).ForStream(c).StreamOpen(), "no session check set")
	require.True(t, access.For(c).StreamOpen(), "a request reader is not a stream")
	var none *Reader
	require.True(t, none.StreamOpen(), "an unwired reader does not end a stream")
}

// TestReader_ReadableOps: the shared list filter keeps every operation that is
// not a Repairs one, keeps a Repairs one only when MayRead holds, preserves
// order, and never returns nil.
func TestReader_ReadableOps(t *testing.T) {
	reg := fixers{"plain": scopedFixer{}, "scoped": scopedFixer{perms: []auth.Permission{auth.PermSettingsManage}}}
	rows := []database.OperationV2Row{
		{ID: "scan", DefID: "library.scan"},
		{ID: "plain", DefID: repairs.ApplyOpID, Params: `{"fixer_id":"plain"}`},
		{ID: "scoped", DefID: repairs.PlanOpID, Params: `{"fixer_id":"scoped"}`},
		{ID: "nofixer", DefID: repairs.ApplyOpID, Params: `{}`},
	}
	ids := func(in []database.OperationV2Row) []string {
		out := []string{}
		for _, r := range in {
			out = append(out, r.ID)
		}
		return out
	}
	c, _ := ctxHolding()
	edit := NewRepairs(reg, noOps, holdsOnly(auth.PermLibraryView, auth.PermLibraryEditMetadata)).For(c)
	require.Equal(t, []string{"scan", "plain"}, ids(edit.ReadableOps(rows)))
	all := NewRepairs(reg, noOps, holdsOnly(auth.All()...)).For(c)
	require.Equal(t, []string{"scan", "plain", "scoped"}, ids(all.ReadableOps(rows)), "an op that states no fixer is hidden from everyone")
	var unwired *Repairs
	require.Equal(t, []string{"scan"}, ids(unwired.For(c).ReadableOps(rows)), "unwired fails closed")
	require.NotNil(t, edit.ReadableOps(nil))
}

// TestReader_ReadableLegacyOps: a row of the retired v1 table states no params,
// so a Repairs row's fixer comes from its stored result alone.
func TestReader_ReadableLegacyOps(t *testing.T) {
	reg := fixers{"plain": scopedFixer{}, "scoped": scopedFixer{perms: []auth.Permission{auth.PermSettingsManage}}}
	ops := []database.Operation{
		{ID: "scan", Type: "library.scan"},
		{ID: "plain", Type: repairs.ApplyOpID, ResultData: ptr(`{"fixer_id":"plain"}`)},
		{ID: "scoped", Type: repairs.PlanOpID, ResultData: ptr(`{"fixer_id":"scoped"}`)},
		{ID: "nofixer", Type: repairs.ApplyOpID},
		{ID: "garbled", Type: repairs.ApplyOpID, ResultData: ptr(`not json`)},
	}
	ids := func(in []database.Operation) []string {
		out := []string{}
		for _, o := range in {
			out = append(out, o.ID)
		}
		return out
	}
	c, _ := ctxHolding()
	viewer := NewRepairs(reg, noOps, holdsOnly(auth.PermLibraryView)).For(c)
	require.Equal(t, []string{"scan"}, ids(viewer.ReadableLegacyOps(ops)))
	edit := NewRepairs(reg, noOps, holdsOnly(auth.PermLibraryView, auth.PermLibraryEditMetadata)).For(c)
	require.Equal(t, []string{"scan", "plain"}, ids(edit.ReadableLegacyOps(ops)))
	all := NewRepairs(reg, noOps, holdsOnly(auth.All()...)).For(c)
	require.Equal(t, []string{"scan", "plain", "scoped"}, ids(all.ReadableLegacyOps(ops)))
	require.NotNil(t, viewer.ReadableLegacyOps(nil))
}

// TestReader_MayReadEveryRepairsOp: only a caller holding every permission there
// is (what an unregistered fixer needs) may read every Repairs operation, and an
// unwired check fails closed. A narrower caller is refused even when they hold
// every permission of the fixers that happen to be registered.
func TestReader_MayReadEveryRepairsOp(t *testing.T) {
	reg := fixers{"plain": scopedFixer{}, "scoped": scopedFixer{perms: []auth.Permission{auth.PermSettingsManage}}}
	c, _ := ctxHolding()
	require.True(t, NewRepairs(reg, noOps, holdsOnly(auth.All()...)).For(c).MayReadEveryRepairsOp())
	require.False(t, NewRepairs(reg, noOps, holdsOnly(auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermSettingsManage)).For(c).MayReadEveryRepairsOp())
	require.False(t, NewRepairs(reg, noOps, holdsOnly(auth.PermLibraryView)).For(c).MayReadEveryRepairsOp())
	var unwired *Repairs
	require.False(t, unwired.For(c).MayReadEveryRepairsOp())
	var nilReader *Reader
	require.False(t, nilReader.MayReadEveryRepairsOp())
}
