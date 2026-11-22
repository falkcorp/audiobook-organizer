// file: internal/server/invite_accept_identity_test.go
// version: 0.1.0
// guid: d40f7b91-2c58-4e13-8a69-5b13e0c9f7a4
// last-edited: 2026-11-22

package server

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// HEAD's EmailBinding and SSOGrant are decided by planInviteAcceptLocked in four
// branches; the accept row restates them over core's event flags, so each branch is
// pinned here with HEAD's own constants.
func TestInviteAcceptBinding_IsHEADsFourBranches(t *testing.T) {
	for _, tc := range []struct {
		name             string
		email            string
		mailed, isAdmin  bool
		wantBind, wantSS string
	}{
		{"no address, nothing mailed", "", false, true, "", ""},
		{"no address even if the flags are set", "", true, true, "", ""},
		{"an address the invitation never went to", "a@example.test", false, true, database.InviteEmailNotEmailed, ""},
		{"never mailed and the creator lost admin: not mailed wins, as HEAD checked delivery first", "a@example.test", false, false, database.InviteEmailNotEmailed, ""},
		{"mailed, creator no longer admin", "a@example.test", true, false, database.InviteEmailCreatorNotAdmin, database.InviteSSOCreatorNotAdmin},
		{"mailed by an admin: bound and granted", "a@example.test", true, true, database.InviteEmailBound, database.InviteSSOGranted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bind, grant := inviteAcceptBinding(tc.email, tc.mailed, tc.isAdmin)
			if bind != tc.wantBind || grant != tc.wantSS {
				t.Fatalf("inviteAcceptBinding(%q, %v, %v) = %q, %q; want %q, %q", tc.email, tc.mailed, tc.isAdmin, bind, grant, tc.wantBind, tc.wantSS)
			}
		})
	}
}
