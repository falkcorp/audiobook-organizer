// file: internal/server/handlers/opaccess/user_data_results.go
// version: 1.3.0
// guid: 3c7f2a91-5d84-4e06-9b1a-e2d4f6a80c73
// last-edited: 2026-10-26

package opaccess

import (
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
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

// mayHold is held for a permission outside the Repairs lane, false for an
// unwired check (nil reader, no access, no holds function).
func (r *Reader) mayHold(p auth.Permission) bool {
	if r == nil || r.a == nil || r.a.holds == nil {
		return false
	}
	return r.held(p)
}

// ServedResult is the stored result of row as the result route may serve it to
// this caller. It is row.ResultData unchanged for every operation but two:
//
//   - a diagnostics export: zip_path is removed unless the caller holds
//     settings.manage, the permission of the download route. The path says where
//     on the server's disk the archive sits, which a library.view caller has no
//     use for and no right to; narrowed_to and the rest of the record stay.
//   - a Repairs apply (repairs.ApplyOpID): unless the caller may read other
//     users' own data (users.manage), the free text a fixer wrote into it
//     (follow_up_error, aborted and each row's error) is read through UserText,
//     as the run's own error is. That is the protection. The owner_user_id on
//     the result and on each rows[] entry is also removed, but that hides
//     nothing from this caller: it is the operation's own actor id, which every
//     operation read still serves (actor_user_id on the v2 row and list, and on
//     the row an export writes to operations.json; started_by on a v3 run).
//     TODO-V1SHAPE-OP-ACTOR-ID tracks withholding actor ids from a caller
//     without users.manage; until it lands, do not read this removal as
//     keeping the account's id from them.
//
// It fails closed: a result of either kind that cannot be parsed is withheld
// whole (nil), and an unwired check holds nothing. Call it after RequireResult,
// which has already refused the results of user-data operations.
func (r *Reader) ServedResult(row *database.OperationV2Row) *string {
	if row == nil || row.ResultData == nil {
		return nil
	}
	switch row.DefID {
	case diagnostics.ExportOpID:
		if r.mayHold(auth.PermSettingsManage) {
			return row.ResultData
		}
		return redactResult(*row.ResultData, func(rec map[string]json.RawMessage) bool {
			delete(rec, "zip_path")
			return true
		})
	case repairs.ApplyOpID:
		if r.MayReadUserData() {
			return row.ResultData
		}
		return redactResult(*row.ResultData, r.redactApplyResult)
	}
	return row.ResultData
}

// redactResult parses a stored result object, lets edit rewrite it and
// re-encodes it; nil when it does not parse, edit refuses it or it cannot be
// encoded.
func redactResult(data string, edit func(map[string]json.RawMessage) bool) *string {
	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &rec); err != nil || rec == nil {
		return nil
	}
	if !edit(rec) {
		return nil
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil
	}
	out := string(b)
	return &out
}

// redactApplyResult removes the owner's user id (which the op's actor id still
// serves; see ServedResult) and passes the free text of a repairs.apply result
// through UserText. It reports false when a field it has to
// rewrite is not the shape it expects, which withholds the whole result.
func (r *Reader) redactApplyResult(rec map[string]json.RawMessage) bool {
	delete(rec, "owner_user_id")
	for _, key := range []string{"follow_up_error", "aborted"} {
		if !r.redactText(rec, key) {
			return false
		}
	}
	rawRows, ok := rec["rows"]
	if !ok {
		return true
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(rawRows, &rows); err != nil {
		return false
	}
	for _, rowRec := range rows {
		delete(rowRec, "owner_user_id")
		if !r.redactText(rowRec, "error") {
			return false
		}
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return false
	}
	rec["rows"] = b
	return true
}

// redactText rewrites rec[key], when present, through UserText. false when it
// is present and not a string.
func (r *Reader) redactText(rec map[string]json.RawMessage, key string) bool {
	raw, ok := rec[key]
	if !ok {
		return true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	b, err := json.Marshal(r.UserText(s))
	if err != nil {
		return false
	}
	rec[key] = b
	return true
}

// WithoutResult returns row as a route that serves rows in bulk (the diagnostics
// export) may carry it to this caller: without the stored result and params when
// the caller may not read them (MayReadResult), and otherwise with the result as
// ServedResult serves it (a Repairs apply's free text, a
// diagnostics export's archive path). row is not modified: it is passed by value
// and a changed result is a new string.
func (r *Reader) WithoutResult(row database.OperationV2Row) database.OperationV2Row {
	if !r.MayReadResult(&row) {
		row.ResultData, row.Params = nil, ""
		return row
	}
	row.ResultData = r.ServedResult(&row)
	return row
}
