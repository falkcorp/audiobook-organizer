// file: internal/server/handlers/opaccess/repairs.go
// version: 1.2.0
// guid: 5d0e8a47-2b9c-4f63-a1d5-7c4e9b3f2a86
// last-edited: 2026-10-25

// Package opaccess holds the read-access checks the generic operations routes
// apply to operations whose data is scoped to something narrower than
// library.view. Today that is the Repairs lane's operations (Repairs).
package opaccess

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Repairs decides who may read the data of a Repairs operation
// (repairs.plan, repairs.apply) through the generic operations routes.
//
// Those operations carry other users' data: a plan's stored result and an
// apply's per-row results (including owner_user_id), the logs the engine writes
// for an owner apply (the owner's user id), and, for a fixer that writes user
// listening state, the operation's change journal (every user's state and
// positions before and after). The generic routes (GET /operations/:id/result,
// /changes, /undo/preflight, /operations/v2/:id and its logs, the system log
// reader's operation_id branch, /operations/timeline and /operations/events;
// /api/v3/ops runs, counts, timeline and events; the activity log's reads by
// operation) are library.view reads, and guests hold the viewer role. So for these two
// definitions the caller must hold every permission of the op's fixer
// (repairs.RequiredPermissions, the same set the Repairs routes check in
// fixerPermitted), and every other operation is unaffected.
//
// The fixer is read from the op's params (both ops carry fixer_id) and, when
// they do not state it, from its stored result. A repairs op whose fixer cannot
// be resolved is refused (fail closed), and so is every repairs op for a nil
// *Repairs: a handler the server did not wire cannot leak.
type Repairs struct {
	fixers FixerLookup
	holds  func(c *gin.Context, p auth.Permission) bool
}

// FixerLookup is the part of *repairs.Registry the access check needs.
type FixerLookup interface {
	Get(id string) (repairs.Fixer, bool)
}

// NewRepairs builds the check. holds reports whether the request's
// caller holds a permission and writes nothing (server.permHolds: every
// permission is held with auth disabled and on a first run).
func NewRepairs(fixers FixerLookup, holds func(c *gin.Context, p auth.Permission) bool) *Repairs {
	return &Repairs{fixers: fixers, holds: holds}
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

// Reader answers, for one request, whether the caller may read the
// data of an operation. It remembers the verdict per fixer, because a
// timeline can hold hundreds of repairs rows and each check asks the store.
// It is not safe for concurrent use: one request goroutine owns it.
type Reader struct {
	a       *Repairs
	c       *gin.Context
	verdict map[string]bool
}

// For binds the check to one request.
func (a *Repairs) For(c *gin.Context) *Reader {
	return &Reader{a: a, c: c, verdict: map[string]bool{}}
}

// MayRead reports whether the caller may read row's data: always for an
// operation that is not a Repairs one.
func (r *Reader) MayRead(row *database.OperationV2Row) bool {
	if row == nil || !IsRepairsOp(row.DefID) {
		return true
	}
	return r.MayReadFixer(row.DefID, RowFixerID(row))
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
// a Repairs one, and false for a Repairs one with no fixer, with a fixer no
// check can resolve, or whose permissions the caller does not all hold.
func (r *Reader) MayReadFixer(defID, fixerID string) bool {
	if !IsRepairsOp(defID) {
		return true
	}
	if r.a == nil || r.a.fixers == nil || r.a.holds == nil {
		return false
	}
	if fixerID == "" {
		return false
	}
	if v, ok := r.verdict[fixerID]; ok {
		return v
	}
	var perms []auth.Permission
	if f, ok := r.a.fixers.Get(fixerID); ok {
		perms = repairs.RequiredPermissions(f)
	} else {
		// A fixer this build does not know (removed or renamed since the op
		// ran): its scope is unknown, so ask for the lane's permission and
		// every stricter one a fixer names (UnknownFixerPermissions).
		perms = UnknownFixerPermissions
	}
	ok := true
	for _, p := range perms {
		if !r.a.holds(r.c, p) {
			ok = false
			break
		}
	}
	r.verdict[fixerID] = ok
	return ok
}

// UnknownFixerPermissions is what reading an operation of a fixer this build
// does not register requires: the lane's default permission and each stricter
// permission any registered fixer names (settings.manage for the Missing files
// and reconcile fixers, users.manage for the two read-status imports). A test
// walks the registry and fails when a fixer names a permission this list lacks.
var UnknownFixerPermissions = []auth.Permission{
	repairs.DefaultPermission, auth.PermSettingsManage, auth.PermUsersManage,
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
	if r.a == nil || r.a.fixers == nil || r.a.holds == nil {
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
		if !r.a.holds(r.c, p) {
			return &StartRefusal{Missing: p}
		}
	}
	return nil
}
