// file: internal/server/handlers/abs_session_revoke.go
// version: 1.1.1
// guid: 4b97077b-7dcf-4b7e-85de-cae1671df311
// last-edited: 2026-10-19

package handlers

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
)

// ABSSessionRevoker is the one ABS session capability the account-security
// paths need: sign a user out of every Audiobookshelf-compatible client.
//
// WHAT ACTUALLY ENDS THE SESSIONS. ABS sessions slide (every refresh and every
// authenticated request pushes their expiry forward) and the absolute cap is off
// by default, so a session that outlived a password change would be good for as
// long as someone kept using it. The guarantee comes from User.CredentialEpoch:
// a password change, admin reset, reset link, deactivation and reactivation each
// bump it in the SAME write as the change (the database.UserCredentialWriter ops,
// which re-read the row under the user write lock), and every ABS refresh and
// bearer request
// refuses a session stamped with an older epoch. That also covers a session
// minted with the old password while the change was in flight, which no revoke
// that runs before the write can reach.
//
// The revoke-all that follows the write is cleanup: it marks the dead rows
// revoked so they leave the device list and get swept. It is not what makes the
// change safe, so its failure is audited and does not fail the request.
type ABSSessionRevoker interface {
	RevokeAllABSSessionsForUser(userID string) (int, error)
}

// ResolveABSSessionRevoker finds the capability through the store's decorator
// chain (database.AsCapability, never a bare assertion: the wired store is a
// decorator that does not carry the ABS keyspace methods itself).
//
// It returns nil when the store has no ABS keyspace. That is safe, not a silent
// skip: wireABSRoutes refuses to start the ABS surface on such a store, so no ABS
// session can exist to revoke.
func ResolveABSSessionRevoker(store any) ABSSessionRevoker {
	r, ok := database.AsCapability[ABSSessionRevoker](store)
	if !ok {
		return nil
	}
	return r
}

// RevokeAllABSSessions revokes every ABS session for userID and audits the
// outcome under reason (a fixed vocabulary: "password-changed",
// "admin-password-reset", "password-reset-link", "user-deactivated",
// "user-reactivated"). A nil revoker is a no-op (see ResolveABSSessionRevoker).
//
// Call it only AFTER the CredentialEpoch bump has been written: by then every
// session it touches is already refused, so this is cleanup and a failure is
// audited (reason + "-revoke-failed") rather than returned.
func RevokeAllABSSessions(r ABSSessionRevoker, userID, reason string) {
	if r == nil {
		return
	}
	n, err := r.RevokeAllABSSessionsForUser(userID)
	ev := absauth.AuditEvent{Action: "revoke-all", UserID: userID, Reason: reason, Outcome: absauth.OutcomeSuccess}
	if err != nil {
		ev.Outcome = absauth.OutcomeError
		ev.Reason = reason + "-revoke-failed"
	}
	if err != nil || n > 0 {
		absauth.Audit(ev)
	}
}
