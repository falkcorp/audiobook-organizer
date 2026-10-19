// file: internal/server/handlers/abs_session_revoke.go
// version: 1.0.0
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
// WHY THESE PATHS MUST CALL IT. ABS sessions slide: every refresh and every
// authenticated request pushes their expiry forward, and the absolute cap is off
// by default. A session or refresh token that leaked is therefore good for as long
// as someone keeps using it. Changing a password, an admin reset and deactivating
// an account are exactly the moments an operator expects every existing sign-in to
// end, so each of them revokes all of the user's ABS sessions.
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
// A non-nil error means at least one session may still be live. The store
// revokes best-effort and reports partial failure, so callers must treat any error
// as "not done" and fail the request rather than report success.
func RevokeAllABSSessions(r ABSSessionRevoker, userID, reason string) error {
	if r == nil {
		return nil
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
	return err
}
