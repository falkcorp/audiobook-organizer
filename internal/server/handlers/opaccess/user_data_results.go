// file: internal/server/handlers/opaccess/user_data_results.go
// version: 1.0.0
// guid: 3c7f2a91-5d84-4e06-9b1a-e2d4f6a80c73
// last-edited: 2026-10-26

package opaccess

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// ResultUserDataDefs is the single list of operation definitions whose stored
// result (and params) carry other users' own data, so only a caller who may
// manage users reads them (owner decision D24, 2026-10-26). It is the second
// scope this package decides, next to the Repairs lane: the operation row stays
// a library.view read (its state, progress, timing and error are not user data),
// but its result is withheld.
//
// A definition opts in by being listed here. The generic routes that serve a
// stored result ask ResultCarriesUserData / Reader.MayReadResult, so a new op
// whose result names users needs one line here and no route change:
//
//   - merge.UserStateRepairOpID: the result's items[] hold, per user, the books
//     they have listening state on, how many positions and bookmarks, and where
//     the state was moved.
//   - usermerge.OpDefID: the plan and journal summaries are redacted (no names
//     or emails) but still carry the survivor and absorbed account ids and, per
//     family, how many rows the absorbed account holds.
//
// TestResultUserDataDefs_AreRegisteredDefs pins that every id here is a
// definition the server registers, so a rename cannot drop the gate.
var ResultUserDataDefs = map[string]struct{}{
	merge.UserStateRepairOpID: {},
	usermerge.OpDefID:         {},
}

// ResultCarriesUserData reports whether defID's stored result carries other
// users' own data (ResultUserDataDefs).
func ResultCarriesUserData(defID string) bool {
	_, ok := ResultUserDataDefs[defID]
	return ok
}

// MayReadResult reports whether the caller may read row's stored result and
// params: always for an operation whose result carries no user data, and for
// one that does (ResultUserDataDefs) only with users.manage. An unwired check
// holds nothing, so such a result is refused.
func (r *Reader) MayReadResult(row *database.OperationV2Row) bool {
	if row == nil || !ResultCarriesUserData(row.DefID) {
		return true
	}
	return r.MayReadUserData()
}

// RequireResult is Require (the Repairs read check) followed by MayReadResult,
// answering 403 itself when either refuses. It is the check of a route that
// serves row's stored result.
func (r *Reader) RequireResult(row *database.OperationV2Row) bool {
	if !r.Require(row) {
		return false
	}
	if r.MayReadResult(row) {
		return true
	}
	servermiddleware.RespondPermissionDenied(r.c, auth.PermUsersManage)
	return false
}

// WithoutResult returns row without the stored result and params when the caller
// may not read them (MayReadResult), for a route that serves rows in bulk (the
// diagnostics export). row is not modified: a refused row is copied first.
func (r *Reader) WithoutResult(row database.OperationV2Row) database.OperationV2Row {
	if r.MayReadResult(&row) {
		return row
	}
	row.ResultData, row.Params = nil, ""
	return row
}
