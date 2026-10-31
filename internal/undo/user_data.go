// file: internal/undo/user_data.go
// version: 1.5.1
// guid: b7545a0f-8cbd-4cda-b35c-382ea405714b
// last-edited: 2026-10-30

package undo

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The journal's change-type classification table lives in internal/database
// (change_type_class.go), so every journal writer, internal/merge included,
// can reach the one check (database.RecordOperationChange). Every ChangeType
// constant this package declares must have an entry there, true or false;
// database's TestUserDataChangeTypes_EveryConstantIsClassified fails on one
// that does not.

// IsUserDataChangeType reports whether a journal row of changeType carries
// one user's own data that other users may not read. Readers that serve journal
// rows to other users (the operation and per-book journal routes,
// opaccess.Reader.MayReadChange) keep such rows to callers who may manage
// users. A type that is not classified reports false.
func IsUserDataChangeType(changeType string) bool {
	return database.IsUserDataChangeType(changeType)
}

// NamesUser reports whether s, a free-text note a journal carries (a combine or
// sibling-move journal's persisted warnings and last error), mentions a user.
// The merge writes a user's id only into its progress warnings ("user <id>
// progress on ...", "user=<id>"), and the refusals on a user's state name the
// account, so a journal note that says "user" is withheld from a caller who may
// not manage users. It is a deny-list on purpose: it errs towards withholding,
// and a note that says "user" for another reason goes too.
//
// It is only for text that has no structured field to read: the journals store
// their warnings as bare strings. A merge undo's own warnings (merge.Warning)
// and the operation revert's report (audiobooks.RevertNote) carry the accounts
// they name as a field, set where the note is produced, and are withheld by
// it, never by this function.
func NamesUser(s string) bool {
	return strings.Contains(strings.ToLower(s), "user")
}
