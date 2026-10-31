// file: internal/server/handlers/web_session_revoke.go
// version: 1.0.0
// guid: b8480eb9-b418-4455-afde-6d50f7911f04
// last-edited: 2026-10-30

package handlers

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var webSessionLog = logger.New("handlers.web-sessions")

// WebSessionRevoker is what the account-security paths need to tidy a user's
// web-UI sessions after a credential change.
//
// WHAT ACTUALLY ENDS THE SESSIONS. Every web session is stamped with the
// User.CredentialEpoch of the sign-in that created it (database.Session
// .CredentialEpoch), and the auth middleware refuses a session whose stamp is not
// the user's current epoch (middleware.RequireAuth, exactly like an expired
// session). A password change, admin reset, reset link, deactivation and
// reactivation each bump the epoch in the SAME write as the change (the
// database.UserCredentialWriter ops, which re-read the row under the user write
// lock), so every older session is dead from that instant, including one minted
// with the old password while the change was in flight, which no revoke that runs
// before the write can reach.
//
// RevokeStaleWebSessions that follows the write is cleanup: it marks the dead
// rows revoked so they leave the device list and get swept. It is not what makes
// the change safe, so its failure is logged and does not fail the request.
type WebSessionRevoker interface {
	ListUserSessions(userID string) ([]database.Session, error)
	RevokeSession(id string) error
}

// RevokeStaleWebSessions revokes every unrevoked web session of userID stamped
// with an epoch OLDER than currentEpoch (the epoch the credential write just
// produced) and logs the outcome under reason ("password-changed",
// "admin-password-reset", "password-reset-link", "user-deactivated",
// "user-reactivated"). It returns how many it revoked.
//
// Older, not "different": a session stamped currentEpoch is a sign-in that
// happened after the change (or the caller's own session, re-stamped by
// SetUserPassword in the same write), and one stamped newer comes from a later
// change racing this one. Revoking either would sign out a legitimate session.
// A nil revoker is a no-op.
//
// Call it only AFTER the CredentialEpoch bump has been written (see
// WebSessionRevoker). A failure is logged, not returned.
func RevokeStaleWebSessions(r WebSessionRevoker, userID string, currentEpoch int64, reason string) int {
	if r == nil {
		return 0
	}
	sessions, err := r.ListUserSessions(userID)
	if err != nil {
		webSessionLog.Warn("%s: listing web sessions failed: %s", reason, logger.SanitizeLogValue(err.Error()))
		return 0
	}
	revoked := 0
	for _, s := range sessions {
		if s.Revoked || s.CredentialEpoch >= currentEpoch {
			continue
		}
		if err := r.RevokeSession(s.ID); err != nil {
			webSessionLog.Warn("%s: revoking a stale web session failed: %s", reason, logger.SanitizeLogValue(err.Error()))
			continue
		}
		revoked++
	}
	if revoked > 0 {
		webSessionLog.Info("%s: revoked %d web session(s)", reason, revoked)
	}
	return revoked
}
