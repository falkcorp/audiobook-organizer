// file: internal/server/handlers/opaccess/repairs.go
// version: 1.1.0
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
// reader's operation_id branch, /operations/timeline, /operations/events, and
// the per-book journal reads GET /audiobooks/:id/changes and
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
//   - MayReadChange takes one change-journal row and needs no operation row,
//     because the journal outlives it: registry.Discard deletes the op row and
//     keeps its opchange rows. The row names its fixer itself
//     (OperationChange.Source, stamped by repairs.Writer).
//
// Every repairs op, and every journal row that is Repairs data, is refused for
// a nil *Repairs: a handler the server did not wire cannot leak.
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

// Reader answers, for one request, whether the caller may read the data of
// an operation row or a change-journal row. It remembers the verdict per
// fixer, because a timeline or a journal can hold hundreds of repairs rows and
// each check asks the store.
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
	if !r.wired() {
		return false
	}
	id := repairsOpFixerID(row)
	if id == "" {
		return false
	}
	return r.fixerPermitted(id)
}

// MayReadChange reports whether the caller may read one change-journal row.
// It decides from the row alone, so it holds after the row's operation has
// been discarded (registry.Discard keeps the journal).
//
// A row is Repairs data when either holds:
//
//   - its ChangeType is undo.ChangeTypeUserBookStateSet: one user's listening
//     state and positions, before and after. Only repairs.Writer.SetUserState
//     writes it, so it is always scoped: to the fixer its Source names, or,
//     when Source names no registered fixer (or none at all), to the strictest
//     set (see fixerPermitted). Every such row names its fixer: Source
//     stamping landed on 2026-10-03 (563a229e9) and the only writer of this
//     change type, the Audible read-status fixer, on 2026-10-05 (7f437d343).
//   - its Source is a registered fixer's id: the fixer's permissions.
//
// Any other row is readable. A non-empty Source alone does not make a row
// Repairs data: the v3 op writer stamps its definition id there too
// (registry's v3 adapter via opswriter.NewBookHistory), and that history is
// library.view data. A removed fixer's rows other than user state therefore
// read as ordinary history here; the operation routes still apply MayRead to
// the op row while it exists.
//
// Unwired (nil *Repairs or no fixer registry), user-state rows are refused and
// so is every row with a Source, since which Sources are fixers is unknown.
func (r *Reader) MayReadChange(ch *database.OperationChange) bool {
	if ch == nil {
		return true
	}
	userState := ch.ChangeType == undo.ChangeTypeUserBookStateSet
	if !r.wired() {
		return !userState && ch.Source == ""
	}
	if !userState {
		if ch.Source == "" {
			return true
		}
		if _, ok := r.a.fixers.Get(ch.Source); !ok {
			return true
		}
	}
	return r.fixerPermitted(ch.Source)
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
	return r.a != nil && r.a.fixers != nil && r.a.holds != nil
}

// fixerPermitted reports whether the caller holds every permission fixer id
// requires, asking once per fixer per request. An id this build does not
// register (a fixer removed or renamed since it ran, or "" for a user-state
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
