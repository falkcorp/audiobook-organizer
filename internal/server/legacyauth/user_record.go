// file: internal/server/legacyauth/user_record.go
// version: 1.0.0
// guid: a171c7c0-b4c4-4e60-bbf2-3da97b4c89d5
// last-edited: 2026-11-28

package legacyauth

import (
	"context"
	"errors"
	"iter"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// LegacyUserFromCore is a core account in HEAD's words: the mapping of a core
// user into HEAD's user record that the store's account list (UserDirectory)
// and the Users page's list share, so those two agree on every word. It is not
// every account list under core identity: the guest-key code's list
// (coreGuestUsers.ListUsers, server/guest_key_users.go) and user-merge's
// account summaries (usermerge summarizeCore) keep core's words ("disabled",
// roles never nil).
//
// Roles keep nil as nil (HEAD's system user had none, shown as null). Core's
// "disabled" is HEAD's "inactive", HEAD's canonical word for it: M3 maps every
// non-active HEAD status to disabled and reports each word other than
// "inactive" in its import log (SEC-11), so the original of such a row (HEAD's
// DeactivateUser wrote "locked") is in that log, not in core's record. Every
// other status is the same word in both. The hash, the epoch and the version
// are core's and never leave it.
func LegacyUserFromCore(u *coreauth.User) database.User {
	var roles []string
	if u.Roles != nil {
		roles = make([]string, len(u.Roles))
		for i, r := range u.Roles {
			roles[i] = string(r)
		}
	}
	status := string(u.Status)
	if u.Status == coreauth.StatusDisabled {
		status = "inactive"
	}
	return database.User{
		ID: string(u.ID), Username: u.Username, Email: u.Email, Roles: roles, Status: status,
		MergedIntoUserID: string(u.MergedInto), CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

// ErrNoUserDirectory is UserDirectory's refusal when it was given no
// account lister to read.
var ErrNoUserDirectory = errors.New("legacyauth: no core identities to list the accounts from")

// AccountLister is the one read UserDirectory makes of core: its account
// list. coreauth.Identities is one (the serve path, core's auth service), and
// the offline open is another (app.Offline.Users), so a diagnostic run with
// the service stopped lists the accounts through the same mapping.
type AccountLister interface {
	Users(ctx context.Context, f coreauth.UserFilter) iter.Seq2[*coreauth.User, error]
}

// UserDirectory is the store's account list under core identity
// (database.PebbleStore.SetUserDirectory): every account core holds, every
// status and the system user included, as HEAD's ListUsers listed every u: row,
// each in HEAD's words (LegacyUserFromCore). Any read error fails the whole
// list: a list that silently left an account out would read, to a "keep the
// book if anyone has state on it" gate, as that account having no state.
func UserDirectory(ids AccountLister) database.UserDirectory {
	return func() ([]database.User, error) {
		if ids == nil {
			return nil, ErrNoUserDirectory
		}
		var out []database.User
		for u, err := range ids.Users(context.Background(), coreauth.UserFilter{}) {
			if err != nil {
				return nil, err
			}
			if u != nil {
				out = append(out, LegacyUserFromCore(u))
			}
		}
		return out, nil
	}
}
