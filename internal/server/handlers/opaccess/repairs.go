// file: internal/server/handlers/opaccess/repairs.go
// version: 1.12.0
// guid: 5d0e8a47-2b9c-4f63-a1d5-7c4e9b3f2a86
// last-edited: 2026-10-30

// Package opaccess holds the read-access checks the generic operations routes
// apply to operations whose data is scoped to something narrower than
// library.view. There are two scopes: the Repairs lane's operations (Repairs),
// and the operations whose stored result carries other users' own data
// (ResultUserDataDefs, Reader.MayReadResult).
package opaccess

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Repairs decides who may read the data of a Repairs operation
// (repairs.plan, repairs.apply) through the generic routes.
//
// Those operations carry other users' data: a plan's stored result and an
// apply's per-row results (including owner_user_id), the logs the engine writes
// for an owner apply (the owner's user id), and, for a fixer that writes user
// listening state, the operation's change journal (every user's state and
// positions before and after). The generic routes (GET /operations/:id/result,
// /changes, /undo/preflight, /operations/v2/:id and its logs, the system log
// reader's operation_id branch, /operations/timeline, /operations/events,
// /api/v3/ops runs, counts, timeline and events, GET /operations/pause and
// /operations/stale (through Reader.ReadableOps), the activity log's reads by
// operation, and the per-book journal reads GET /audiobooks/:id/changes and
// /audiobooks/:id/changelog) are library.view reads, and guests hold the
// viewer role. So for these two definitions the caller must hold every
// permission of the op's fixer (repairs.RequiredPermissions, the same set the
// Repairs routes check in fixerPermitted), and every other operation is
// unaffected.
//
// Two checks, because the data lives in two places:
//
//   - MayRead takes an operation row. The fixer is read from the op's params
//     (both ops carry fixer_id) and, when they do not state it, from its
//     stored result. A repairs op whose fixer cannot be resolved is refused
//     (fail closed).
//   - MayReadChange takes one change-journal row. The row names its fixer
//     itself (OperationChange.Source, stamped by repairs.Writer since
//     2026-10-03); a row journaled before then names none, and its op row,
//     while it exists, names the fixer instead. The journal outlives the op
//     row: registry.Discard deletes the op row and keeps its opchange rows.
//
// Journal rows that carry one user's own data (undo.IsUserDataChangeType:
// listening state and positions, private playlist membership, an account's
// identity) are other users' data, which only a caller who may manage users
// reads and undoes (owner decision, 2026-10-24), on top of the fixer's
// permissions.
//
// Unwired (a nil *Repairs, or one missing its fixer registry, op lookup or
// permission check), every repairs op is refused, and so is every journal row
// that carries user data or a Source. An unstamped library row still reads:
// with no op lookup there is no way to tell a pre-2026-10-03 Repairs row from
// any other op's history, and refusing them all would hide most of every
// book's history from admins too.
type Repairs struct {
	fixers FixerLookup
	ops    OpLookup
	holds  func(c *gin.Context, p auth.Permission) bool
	// live, when set, answers holds again from the account's current state, for
	// a reader that outlives its request (WithLiveHolds, ForStream).
	live func(c *gin.Context, p auth.Permission) bool
	// alive, when set, answers whether the stream's credential still stands: its
	// account exists and is active, and the API key it came in on is still
	// usable (WithLiveSession, Reader.StreamOpen).
	alive func(c *gin.Context) bool
	// recheck is the shortest time between two live questions a stream reader
	// puts to the store (WithLiveRecheck); 0 asks at every Refresh.
	recheck time.Duration
	// now is the clock recheck is measured on; time.Now when nil.
	now func() time.Time
}

// FixerLookup is the part of *repairs.Registry the access check needs.
type FixerLookup interface {
	Get(id string) (repairs.Fixer, bool)
}

// OpLookup reads an operation row: (nil, nil) when there is none. It is how
// MayReadChange scopes a journal row that names no fixer.
type OpLookup interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
}

// NewRepairs builds the check. holds reports whether the request's
// caller holds a permission and writes nothing (server.permHolds: every
// permission is held with auth disabled and on a first run). A nil fixers,
// ops or holds leaves the check unwired, which fails closed.
func NewRepairs(fixers FixerLookup, ops OpLookup, holds func(c *gin.Context, p auth.Permission) bool) *Repairs {
	return &Repairs{fixers: fixers, ops: ops, holds: holds}
}

// IsRepairsOp reports whether defID is one of the Repairs lane's operations.
func IsRepairsOp(defID string) bool {
	return defID == repairs.PlanOpID || defID == repairs.ApplyOpID
}

// fixerIDOf is the fixer a repairs op belongs to: params.fixer_id, else
// result.fixer_id; "" when neither states one. The one reader of that rule, so
// an operations_v2 row and an operations v3 run record cannot disagree.
func fixerIDOf(params string, result *string) string {
	var v struct {
		FixerID string `json:"fixer_id"`
	}
	if params != "" {
		if json.Unmarshal([]byte(params), &v) == nil && v.FixerID != "" {
			return v.FixerID
		}
	}
	if result != nil {
		v.FixerID = ""
		if json.Unmarshal([]byte(*result), &v) == nil && v.FixerID != "" {
			return v.FixerID
		}
	}
	return ""
}

// RowFixerID is the fixer a repairs op row belongs to; "" when it states none.
func RowFixerID(row *database.OperationV2Row) string {
	return fixerIDOf(row.Params, row.ResultData)
}

// RunFixerID is RowFixerID for an operations v3 run record.
func RunFixerID(rec *database.RunRecord) string {
	var result *string
	if len(rec.Result) > 0 {
		r := string(rec.Result)
		result = &r
	}
	return fixerIDOf(string(rec.Params), result)
}

// Reader answers, for one request, whether the caller may read the data of
// an operation row or a change-journal row. It remembers the verdict per
// fixer, because a timeline or a journal can hold hundreds of repairs rows and
// each check asks the store.
type Reader struct {
	a       *Repairs
	c       *gin.Context
	verdict map[string]bool
	// opVerdict is unstampedOpVerdict per operation id.
	opVerdict map[string]opScope
	// userData caches mayReadUserData: nil until asked.
	userData *bool
	// lookupErr is the first op-row read that failed (unstampedOpVerdict).
	lookupErr error
	// live makes the reader ask a.live instead of a.holds (ForStream).
	live bool
	// stream marks a reader made by ForStream.
	stream bool
	// refreshedAt is when Refresh last forgot the cached verdicts
	// (WithLiveRecheck).
	refreshedAt time.Time
	// heldNow caches the live answer per permission until the next refresh.
	heldNow map[auth.Permission]bool
	// standing caches StreamOpen's answer until the next refresh.
	standing *bool
}

// For binds the check to one request.
func (a *Repairs) For(c *gin.Context) *Reader {
	return &Reader{a: a, c: c, verdict: map[string]bool{}, opVerdict: map[string]opScope{}}
}

// WithLiveHolds sets the way a stream asks "does the caller hold p now": the
// connect-time answer (holds) narrowed by what the account holds at the moment
// of asking, so a caller whose role was lowered or whose account was disabled
// after the stream opened stops being served (server.permHoldsLive). It returns
// a.
func (a *Repairs) WithLiveHolds(live func(c *gin.Context, p auth.Permission) bool) *Repairs {
	a.live = live
	return a
}

// WithLiveSession sets the way a stream asks "does the caller's credential
// still stand": its account exists and is active, and the API key it came in on
// is neither revoked, inactive nor expired (server.sessionStillStands). A stream
// that outlives its credential ends (Reader.StreamOpen). It returns a.
func (a *Repairs) WithLiveSession(alive func(c *gin.Context) bool) *Repairs {
	a.alive = alive
	return a
}

// WithLiveRecheck bounds how often a stream reader puts a live question to the
// store: it asks again at a Refresh only when every has passed since it last
// did, measured on now (time.Now when nil). Every event of a stream refreshes
// the reader, and an operation's progress is an event per update, so without a
// bound each of them costs several store reads per open stream. The bound is
// also how long a lowered role or a disabled account keeps being served: the
// check can only narrow, and narrows within every. 0 asks at every Refresh. It
// returns a.
func (a *Repairs) WithLiveRecheck(every time.Duration, now func() time.Time) *Repairs {
	a.recheck = every
	a.now = now
	return a
}

// ForStream is For for a reader that lives as long as an event stream: its
// questions go to the live check when one is set (WithLiveHolds), and the
// caller calls Refresh before each verdict, so a stream cannot keep serving what
// the account lost the right to read after it connected.
func (a *Repairs) ForStream(c *gin.Context) *Reader {
	r := a.For(c)
	r.live = a != nil && a.live != nil
	r.stream = a != nil
	return r
}

// Refresh forgets every verdict the reader has cached (per fixer, per
// operation row, users.manage), so the next question is asked again. A stream
// calls it before each event it decides on; a reader of one request never
// needs it. On a stream reader whose check has a recheck bound
// (WithLiveRecheck), a Refresh within the bound of the last one that forgot
// anything does nothing: the cached verdicts stand until it passes.
func (r *Reader) Refresh() {
	if r == nil {
		return
	}
	if r.stream && r.a.recheck > 0 {
		now := r.clock()
		if !r.refreshedAt.IsZero() && now.Sub(r.refreshedAt) < r.a.recheck {
			return
		}
		r.refreshedAt = now
	}
	r.verdict = map[string]bool{}
	r.opVerdict = map[string]opScope{}
	r.userData = nil
	r.lookupErr = nil
	r.heldNow = nil
	r.standing = nil
}

func (r *Reader) clock() time.Time {
	if r.a != nil && r.a.now != nil {
		return r.a.now()
	}
	return time.Now()
}

// StreamOpen reports whether a stream's credential still stands, so the stream
// may go on: false once its account is disabled or removed or its API key is
// revoked, deactivated or expired, and the stream ends. It refreshes the reader
// (bounded by WithLiveRecheck) and caches the answer until the next refresh that
// forgets. True when no session check is set, and for a reader that is not a
// stream's.
func (r *Reader) StreamOpen() bool {
	if r == nil || !r.stream || r.a.alive == nil {
		return true
	}
	r.Refresh()
	if r.standing == nil {
		v := r.a.alive(r.c)
		r.standing = &v
	}
	return *r.standing
}

// held asks whether the caller holds p: the live check for a stream reader (once
// per permission until the next refresh that forgets), else the connect-time
// one.
func (r *Reader) held(p auth.Permission) bool {
	if !r.live {
		return r.a.holds(r.c, p)
	}
	if v, ok := r.heldNow[p]; ok {
		return v
	}
	v := r.a.live(r.c, p)
	if r.heldNow == nil {
		r.heldNow = map[auth.Permission]bool{}
	}
	r.heldNow[p] = v
	return v
}

// MayRead reports whether the caller may read row's data: always for an
// operation that is not a Repairs one.
func (r *Reader) MayRead(row *database.OperationV2Row) bool {
	if row == nil || !IsRepairsOp(row.DefID) {
		return true
	}
	return r.MayReadFixer(row.DefID, RowFixerID(row))
}

// ReadableOps is rows without the operations the caller may not read (MayRead),
// in order. It never returns nil. It is the one filter every route that lists
// operation rows, or reports something derived from them (a definition id, a
// count), applies, so that a new list route has one call to make rather than a
// check of its own to get right; TestOperationsOverviewRoutes_... in package
// server walks the wired routes and fails on a list route it has no driver for.
func (r *Reader) ReadableOps(rows []database.OperationV2Row) []database.OperationV2Row {
	out := make([]database.OperationV2Row, 0, len(rows))
	for i := range rows {
		if r.MayRead(&rows[i]) {
			out = append(out, rows[i])
		}
	}
	return out
}

// MayReadLegacy is MayRead for a row of the retired v1 operations table, which
// states no params: a Repairs row's fixer comes from its stored result alone, and
// one that names none is refused (fail closed), as MayReadFixer does.
func (r *Reader) MayReadLegacy(op *database.Operation) bool {
	if op == nil || !IsRepairsOp(op.Type) {
		return true
	}
	return r.MayReadFixer(op.Type, fixerIDOf("", op.ResultData))
}

// ReadableLegacyOps is ReadableOps for rows of the retired v1 operations table
// (GET /operations/stale). It never returns nil.
func (r *Reader) ReadableLegacyOps(ops []database.Operation) []database.Operation {
	out := make([]database.Operation, 0, len(ops))
	for i := range ops {
		if r.MayReadLegacy(&ops[i]) {
			out = append(out, ops[i])
		}
	}
	return out
}

// MayReadRun is MayRead for an operations v3 run record.
func (r *Reader) MayReadRun(rec *database.RunRecord) bool {
	if rec == nil || !IsRepairsOp(rec.DefID) {
		return true
	}
	return r.MayReadFixer(rec.DefID, RunFixerID(rec))
}

// MayReadFixer is the verdict for an operation of definition defID that belongs
// to fixerID ("" when it states none): always true for an operation that is not
// a Repairs one, and false for a Repairs one with no fixer, when the check is
// unwired, or whose fixer's permissions the caller does not all hold. A fixer
// this build does not register needs every permission (fixerPermitted).
func (r *Reader) MayReadFixer(defID, fixerID string) bool {
	if !IsRepairsOp(defID) {
		return true
	}
	if !r.wired() || fixerID == "" {
		return false
	}
	return r.fixerPermitted(fixerID)
}

// MayReadChange reports whether the caller may read one change-journal row.
// It holds after the row's operation has been discarded (registry.Discard
// keeps the journal).
//
// A row is readable exactly when it may be undone (MayUndoChange): the caller
// uses the fixer it belongs to and, for a row that carries one user's own data
// (undo.IsUserDataChangeType), also holds users.manage. Other users' listening
// state, playlists and identity are admin data whoever ran the fixer, so an
// editor who may run fragment-consolidation does not read its
// user_state_follow rows and cannot undo them either.
//
// Unwired (nil *Repairs, or no fixer registry, op lookup or permission
// check), user-data rows are refused and so is every row with a Source, since
// which Sources are fixers is unknown.
func (r *Reader) MayReadChange(ch *database.OperationChange) bool {
	if ch == nil {
		return true
	}
	return r.MayUndoChange(ch)
}

// MayUndoChange reports whether the caller may undo one change-journal row:
// the operation's undo report and its revert accept a row only when this
// holds. The caller must use the fixer the row belongs to and, for a row that
// carries one user's own data (undo.IsUserDataChangeType), also hold
// users.manage: undoing such a row writes another user's listening state or
// playlist position back, which no caller may do to an account they could not
// read (owner decision, 2026-10-24). An editor who may run fragment
// consolidation therefore gets neither the undo report nor the revert of an
// operation whose journal holds user_state_follow or user_content_follow
// rows.
//
// What this does NOT cover, on purpose (R8, security re-review of 2026-10-26):
// the undo of a book combine or a sibling move. Those go through the same
// POST /operations/:id/revert (library.organize) but their journal rows are
// ChangeTypeMergeCombineJournal and ChangeTypeMergeSiblingJournal, which are
// not user data (undo.IsUserDataChangeType): the pointer row says that a merge
// happened and how to put the library's structure back, and replaying it
// restores the absorbed book and, with it, the listening state the merge folded
// into the survivor (merge.CombineUserProgress). An editor who may organize the
// library can therefore undo a combine whose journal holds other users'
// progress, though they could not undo a Repairs follow row. That is the
// intended reading: a combine undo restores the library's structure, which
// library.organize governs, and is not one user's listening state being written
// by an editor who may not manage users. Gating it like the follow rows would
// cost every editor the Library combine-undo toast whenever any user had
// progress on an absorbed book. The undo report of such an operation is still
// stripped of other users' rows, so nothing is read through it. Do not add the
// merge journal types to undo.IsUserDataChangeType without making that call.
//
// The fixer a row belongs to:
//
//   - its Source, when that is a registered fixer's id: the fixer's
//     permissions.
//   - its Source, when that names no registered fixer: a v3 op stamps its
//     definition id there too (registry's v3 adapter via
//     opswriter.NewBookHistory), and that history is library.view data, so
//     such a row is readable -- unless it carries user data, which only
//     repairs.Writer writes: then the fixer's scope is unknown and every
//     permission is needed (fixerPermitted). A removed fixer's rows other
//     than user data therefore read as ordinary history.
//   - no Source (a row journaled before Source stamping landed on 2026-10-03,
//     563a229e9, or by a writer that does not stamp): the row's op row, while
//     it exists, decides (MayRead: a Repairs op needs its fixer's
//     permissions). With no op row left, or one that is not a Repairs op, the
//     row is readable unless it carries user data, which then needs every
//     permission. A store error refuses the row.
//
// Unwired, as MayReadChange.
func (r *Reader) MayUndoChange(ch *database.OperationChange) bool {
	if ch == nil {
		return true
	}
	userData := undo.IsUserDataChangeType(ch.ChangeType)
	if !r.wired() {
		return !userData && ch.Source == ""
	}
	if userData && !r.mayReadUserData() {
		return false
	}
	if ch.Source != "" {
		if _, ok := r.a.fixers.Get(ch.Source); ok || userData {
			return r.fixerPermitted(ch.Source)
		}
		return true
	}
	switch r.unstampedOpVerdict(ch.OperationID) {
	case opRepairsReadable:
		return true
	case opRepairsRefused, opUnreadable:
		return false
	}
	// No op row, or one that is not a Repairs op.
	if userData {
		return r.fixerPermitted("")
	}
	return true
}

// opScope is what a journal row's op row says about a row that names no
// fixer.
type opScope int

const (
	// opNotRepairs: no op row, or one that is not a Repairs op.
	opNotRepairs opScope = iota
	// opRepairsReadable: a Repairs op the caller may read (MayRead).
	opRepairsReadable
	// opRepairsRefused: a Repairs op the caller may not read.
	opRepairsRefused
	// opUnreadable: the op row could not be read (fail closed).
	opUnreadable
)

// unstampedOpVerdict reads operation id's row once per request and scopes it.
func (r *Reader) unstampedOpVerdict(id string) opScope {
	if id == "" {
		return opNotRepairs
	}
	if v, ok := r.opVerdict[id]; ok {
		return v
	}
	v := opNotRepairs
	row, err := r.a.ops.GetOperationV2(id)
	switch {
	case err != nil:
		v = opUnreadable
		if r.lookupErr == nil {
			r.lookupErr = fmt.Errorf("read operation %s: %w", id, err)
		}
	case row != nil && IsRepairsOp(row.DefID):
		v = opRepairsRefused
		if r.MayRead(row) {
			v = opRepairsReadable
		}
	}
	r.opVerdict[id] = v
	return v
}

// LookupError is the first op-row read that failed while checking rows with
// no Source, or nil. Such a row is refused (fail closed); a route that refuses
// a whole response on one refused row checks this first, so a store failure
// answers 500 rather than a 403 that blames the caller's permissions.
func (r *Reader) LookupError() error { return r.lookupErr }

// MayReadUserData is mayReadUserData for a route outside this package that
// serves free text a user's own data can sit in (the revert report's merge
// warnings). An unwired check holds nothing.
func (r *Reader) MayReadUserData() bool {
	if r == nil || r.a == nil || r.a.holds == nil {
		return false
	}
	return r.mayReadUserData()
}

// UserText is s as the caller may read it: unchanged for a caller who holds
// users.manage, and activity.WithheldUserText in its place when it names other
// users' own data. It is for the free-text fields of an operation row (its
// error and progress message) that the row serves to every caller who may read
// the row. An unwired check holds nothing, so such text is withheld.
func (r *Reader) UserText(s string) string {
	if s == "" || r.MayReadUserData() {
		return s
	}
	return activity.WithoutOthersUserText(s)
}

// mayReadUserData reports, once per request, whether the caller may read
// other users' own data: users.manage, the permission that already shows an
// admin every account.
func (r *Reader) mayReadUserData() bool {
	if r.userData == nil {
		v := r.held(auth.PermUsersManage)
		r.userData = &v
	}
	return *r.userData
}

// ReadableChanges is changes without the rows the caller may not read
// (MayReadChange), in order. It never returns nil.
func (r *Reader) ReadableChanges(changes []*database.OperationChange) []*database.OperationChange {
	out := make([]*database.OperationChange, 0, len(changes))
	for _, ch := range changes {
		if r.MayReadChange(ch) {
			out = append(out, ch)
		}
	}
	return out
}

func (r *Reader) wired() bool {
	return r.a != nil && r.a.fixers != nil && r.a.ops != nil && r.a.holds != nil
}

// fixerPermitted reports whether the caller holds every permission fixer id
// requires, asking once per fixer per request. An id this build does not
// register (a fixer removed or renamed since it ran, or "" for a user-data
// row that names none) has an unknown scope, so it needs every permission
// there is (auth.All(), the admin role's set): nothing a fixer could name,
// such as master-list-import's users.manage, is left out.
func (r *Reader) fixerPermitted(id string) bool {
	if v, ok := r.verdict[id]; ok {
		return v
	}
	var perms []auth.Permission
	if f, ok := r.a.fixers.Get(id); ok && id != "" {
		perms = repairs.RequiredPermissions(f)
	} else {
		perms = auth.All()
	}
	ok := true
	for _, p := range perms {
		if !r.held(p) {
			ok = false
			break
		}
	}
	r.verdict[id] = ok
	return ok
}

// Require is MayRead that answers 403 itself when the caller may not read.
func (r *Reader) Require(row *database.OperationV2Row) bool {
	if r.MayRead(row) {
		return true
	}
	httputil.RespondWithForbidden(r.c, "permission denied: this operation belongs to a Repairs fixer you may not use")
	return false
}

// StartRefusal is why a generic start route will not queue a run of a Repairs
// definition. It is what the Repairs routes answer for the same request, so the
// two ways in cannot disagree: a 404 for a fixer this build does not register, a
// 400 for params that name none, and the 401/403 of the first permission the
// fixer requires that the caller lacks.
type StartRefusal struct {
	Status  int             // 400 or 404, or 403 for an unwired check; 0 when Missing is set
	Message string          // for Status
	Missing auth.Permission // set when the caller lacks this permission
}

// Respond writes the refusal.
func (f *StartRefusal) Respond(c *gin.Context) {
	switch {
	case f.Missing != "":
		servermiddleware.RespondPermissionDenied(c, f.Missing)
	case f.Status == 404:
		httputil.RespondWithNotFound(c, "repair fixer", f.Message)
	case f.Status == 400:
		httputil.RespondWithBadRequest(c, f.Message)
	default:
		httputil.RespondWithForbidden(c, f.Message)
	}
}

// CheckStart decides whether the caller may queue a run of definition defID with
// params (the JSON the run would carry): nil for a definition that is not a
// Repairs one, and for a Repairs one when the caller holds every permission its
// fixer requires (repairs.RequiredPermissions, as the Repairs routes' own check
// does). The generic start routes (POST /operations/v2, a retry, POST
// /api/v3/ops/runs) ask only the definition's own permissions, which for these
// two definitions is a bare settings.manage, so without this a role that holds
// settings.manage but not the fixer's users.manage could start the Audible
// read-status fixer from them. Params that name no fixer are refused too: such a
// run could never be read by anyone (MayReadFixer), and cannot do anything.
// An unwired check refuses every Repairs start (fail closed).
func (r *Reader) CheckStart(defID string, params []byte) *StartRefusal {
	if !IsRepairsOp(defID) {
		return nil
	}
	if !r.wired() {
		return &StartRefusal{Status: 403, Message: "permission denied: this operation belongs to a Repairs fixer you may not use"}
	}
	id := fixerIDOf(string(params), nil)
	if id == "" {
		return &StartRefusal{Status: 400, Message: "params.fixer_id is required to start a Repairs operation"}
	}
	f, ok := r.a.fixers.Get(id)
	if !ok {
		return &StartRefusal{Status: 404, Message: id}
	}
	for _, p := range repairs.RequiredPermissions(f) {
		if !r.held(p) {
			return &StartRefusal{Missing: p}
		}
	}
	return nil
}
