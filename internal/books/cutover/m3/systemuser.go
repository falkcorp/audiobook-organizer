// file: internal/books/cutover/m3/systemuser.go
// version: 0.1.0
// guid: 5b0f8c3e-4d1a-4e6b-9a27-3c8e1f7d2a64
// last-edited: 2026-11-22

package m3

import (
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// HEAD's _system pseudo-user (internal/auth/seed.go SeedSystemUser) is created
// through CreateUser, so its row has a ULID like any account: username "_system",
// status "system". Every HEAD writer that attributes to it uses the literal
// "_system" as the user ID (the organizer's checkpoints and failure records, the
// maintenance ops' preferences: pref:_system:...), and core's own system user is
// auth.SystemUserID, the ID "_system", which BootWrites looks up by ID. A row
// imported under its ULID therefore leaves core without a "_system" user and
// BootWrites unable to seed one (its username is taken): the boot refuses to
// serve (P4d step 4).
//
// So the system row imports under auth.SystemUserID, with status system (core
// refuses to authenticate it and to let an administrator change it by that
// status, never by the ID), and every reference M3 imports to its ULID is
// rewritten: an API key's or an OAuth identity's owner, another user's merged-into
// marker. A key or identity that names it is pulled forward into the chunk that
// imports the user (pullSystemOwned), so no row that still names the ULID is left
// for a later chunk, which could not know the ULID: the row it was read from is
// gone by then.
//
// Rows that make "the system user" ambiguous are cutover blockers (D13) and are
// never guessed at.

// systemUsername is HEAD's system user's username.
const systemUsername = string(auth.SystemUserID)

// userEntry is a decoded HEAD user row with its key.
type userEntry struct {
	key string
	h   *headUser
}

// isSystemStatus is HEAD's rule for a system account: the status, trimmed and
// folded, is "system".
func isSystemStatus(h *headUser) bool {
	return strings.ToLower(strings.TrimSpace(h.Status)) == string(auth.StatusSystem)
}

// isSystemName reports the username HEAD's seed gives the system user.
func isSystemName(h *headUser) bool { return auth.FoldUsername(h.Username) == systemUsername }

// Fixed classes of a system-user blocker: a blocker names a row, never a value.
const (
	reasonSeveralSystem = "more than one user has status system, so the system user is ambiguous"
	reasonSystemRename  = "a user with status system is not named _system"
	reasonSystemName    = "a user that does not have status system is named _system"
)

// systemUsers is what one pass makes of the HEAD user rows that concern the
// system user. The zero value is "HEAD has none".
type systemUsers struct {
	// from is the HEAD ID of the system row; "" when there is none or the rows
	// are ambiguous (refusals then names them).
	from string
	// refusals is the reason, by user row key, of each row that makes the system
	// user ambiguous.
	refusals map[string]string
}

// classifySystem finds the system user among the decoded user rows. Exactly one
// row with status system, named _system, is the system user. Anything else that
// touches either property is refused: several rows with the status, a status
// row with another name, a row with the name and another status.
func classifySystem(entries []userEntry) systemUsers {
	var su systemUsers
	refuse := func(key, reason string) {
		if su.refusals == nil {
			su.refusals = map[string]string{}
		}
		if _, held := su.refusals[key]; !held {
			su.refusals[key] = reason
		}
	}
	var system []userEntry
	for _, e := range entries {
		if isSystemStatus(e.h) {
			system = append(system, e)
		}
	}
	for _, e := range system {
		switch {
		case len(system) > 1:
			refuse(e.key, reasonSeveralSystem)
		case !isSystemName(e.h):
			refuse(e.key, reasonSystemRename)
		}
	}
	for _, e := range entries {
		if isSystemName(e.h) && !isSystemStatus(e.h) {
			refuse(e.key, reasonSystemName)
		}
	}
	if len(su.refusals) == 0 && len(system) == 1 {
		su.from = system[0].h.ID
	}
	return su
}

// ref is the native ID of the user a HEAD row names.
func (s systemUsers) ref(id string) auth.UserID {
	if s.from != "" && id == s.from {
		return auth.SystemUserID
	}
	return auth.UserID(id)
}

// mapUser is mapUser with the system row under its native ID and status, and
// the merged-into marker of any user rewritten like every other reference. The
// reported status is the odd one mapUser names (the system row's is not odd).
func (s systemUsers) mapUser(h *headUser, roles []rbac.RoleName) (auth.User, string, string) {
	u, hash, reported := mapUser(h, roles)
	if s.from != "" && h.ID == s.from {
		u.ID = auth.SystemUserID
		u.Status = auth.StatusSystem
		reported = ""
	}
	if u.MergedInto != "" {
		u.MergedInto = s.ref(string(u.MergedInto))
	}
	return u, hash, reported
}
