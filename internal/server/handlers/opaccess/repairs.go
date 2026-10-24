// file: internal/server/handlers/opaccess/repairs.go
// version: 1.0.0
// guid: 5d0e8a47-2b9c-4f63-a1d5-7c4e9b3f2a86
// last-edited: 2026-10-24

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
// reader's operation_id branch, /operations/timeline and /operations/events)
// are library.view reads, and guests hold the viewer role. So for these two
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

// repairsOpFixerID is the fixer a repairs op row belongs to: params.fixer_id,
// else result.fixer_id; "" when neither states one.
func repairsOpFixerID(row *database.OperationV2Row) string {
	var v struct {
		FixerID string `json:"fixer_id"`
	}
	if row.Params != "" {
		if json.Unmarshal([]byte(row.Params), &v) == nil && v.FixerID != "" {
			return v.FixerID
		}
	}
	if row.ResultData != nil {
		v.FixerID = ""
		if json.Unmarshal([]byte(*row.ResultData), &v) == nil && v.FixerID != "" {
			return v.FixerID
		}
	}
	return ""
}

// Reader answers, for one request, whether the caller may read the
// data of an operation row. It remembers the verdict per fixer, because a
// timeline can hold hundreds of repairs rows and each check asks the store.
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
	if r.a == nil || r.a.fixers == nil || r.a.holds == nil {
		return false
	}
	id := repairsOpFixerID(row)
	if id == "" {
		return false
	}
	if v, ok := r.verdict[id]; ok {
		return v
	}
	var perms []auth.Permission
	if f, ok := r.a.fixers.Get(id); ok {
		perms = repairs.RequiredPermissions(f)
	} else {
		// A fixer this build does not know (removed or renamed since the op
		// ran): its scope is unknown, so ask for the lane's permission and the
		// strictest one a fixer names.
		perms = []auth.Permission{repairs.DefaultPermission, auth.PermSettingsManage}
	}
	ok := true
	for _, p := range perms {
		if !r.a.holds(r.c, p) {
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
