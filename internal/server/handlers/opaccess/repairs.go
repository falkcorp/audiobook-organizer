// file: internal/server/handlers/opaccess/repairs.go
// version: 1.3.0
// guid: 5d0e8a47-2b9c-4f63-a1d5-7c4e9b3f2a86
// last-edited: 2026-10-25

// Package opaccess holds the read-access checks the generic operations routes
// apply to operations whose data is scoped to something narrower than
// library.view. Today that is the Repairs lane's operations (Repairs).
package opaccess

import (
	"encoding/json"
	"fmt"

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
//   - MayReadChange takes one change-journal row. The row names its fixer
//     itself (OperationChange.Source, stamped by repairs.Writer since
//     2026-10-03); a row journaled before then names none, and its op row,
//     while it exists, names the fixer instead. The journal outlives the op
//     row: registry.Discard deletes the op row and keeps its opchange rows.
//
// Journal rows that carry one user's own data (undo.IsUserDataChangeType:
// listening state and positions, private playlist membership, an account's
// identity) are other users' data, which only a caller who may manage users
// reads (owner decision, 2026-10-24), on top of the fixer's permissions.
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
	// opVerdict is unstampedOpVerdict per operation id.
	opVerdict map[string]opScope
	// userData caches mayReadUserData: nil until asked.
	userData *bool
	// lookupErr is the first op-row read that failed (unstampedOpVerdict).
	lookupErr error
}

// For binds the check to one request.
func (a *Repairs) For(c *gin.Context) *Reader {
	return &Reader{a: a, c: c, verdict: map[string]bool{}, opVerdict: map[string]opScope{}}
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
// It holds after the row's operation has been discarded (registry.Discard
// keeps the journal).
//
// A row is readable when the caller may use the fixer it belongs to
// (MayUndoChange) and, for a row that carries one user's own data
// (undo.IsUserDataChangeType), also holds users.manage: other users'
// listening state, playlists and identity are admin data whoever ran the
// fixer, so an editor who may run fragment-consolidation still does not read
// its user_state_follow rows.
//
// Unwired (nil *Repairs, or no fixer registry, op lookup or permission
// check), user-data rows are refused and so is every row with a Source, since
// which Sources are fixers is unknown.
func (r *Reader) MayReadChange(ch *database.OperationChange) bool {
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
	return r.MayUndoChange(ch)
}

// MayUndoChange reports whether the caller may use the fixer a journal row
// belongs to, which is what the operation's undo report needs: the report
// carries a row's id, book, change type and refusal reason, never its values,
// so a user-data row in it is no reason to refuse a caller who may run (and
// so undo) the fixer. MayReadChange adds the user-data leg.
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

// mayReadUserData reports, once per request, whether the caller may read
// other users' own data: users.manage, the permission that already shows an
// admin every account.
func (r *Reader) mayReadUserData() bool {
	if r.userData == nil {
		v := r.a.holds(r.c, auth.PermUsersManage)
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
