// file: internal/server/handlers/abs/authorize.go
// version: 1.0.0
// guid: 7e14b0c9-53a6-4d82-91f7-2c8de6a04b13
// last-edited: 2026-08-01

package abs

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Authorize handles POST /api/authorize — the endpoint an already-logged-in client
// calls to re-validate its token and refresh its view of the server.
//
// 🔴 THIS ENDPOINT CAN DESTROY USER DATA IF IT UNDER-REPORTS (§1.8.1), for exactly
// the same reason Me does: AudioBooth's MediaProgress.syncFromAPI DELETES every local
// progress row whose bookID is absent from `user.mediaProgress`. AudioBooth calls this
// on foreground, so a 200 carrying an empty or partial list wipes the user's place in
// every book, repeatedly, with no error shown. The complete list or a 5xx — never a
// convenient empty array.
//
// # Why it must exist at all, and why its absence was invisible
//
// Without this route, gin's path-fixer answered POST /api/authorize with a 301 to
// /api/v1/authorize. A 301 lets a client downgrade the method, so the app re-issued it
// as GET, got a 404 from the app API, and silently never refreshed its session — the
// app showed "connected", then failed on the next authenticated call. Observed in
// production 2026-08-01:
//
//	POST /api/authorize          → 301
//	GET  /api/v1/authorize       → 404
//	GET  /api/libraries          → 401   (50s later, session never refreshed)
//
// That is the failure mode absReservedPaths exists to prevent, which is why this route
// is registered in Handler.Register AND listed in absReservedPaths in
// wire_abs_routes.go. Adding one without the other reintroduces the bug.
//
// # No new session is minted here
//
// Authorize VALIDATES the caller's existing credential; it does not issue one. Minting
// a session per call would create a row on every app foreground. So the tokens the
// caller presented are echoed back rather than reissued:
//
//   - accessToken: echoed from the request. In Mode C there may be no bearer at all, so
//     a token is minted for the CURRENT session rather than emitting "", which clients
//     decode as a non-optional String.
//   - refreshToken: echoed only if the caller sent one. It is never regenerated — we
//     store only its hash and could not reproduce it anyway. Echoing what the client
//     already holds keeps Absorb's isLegacy flag clear (§1.7.2) without minting secrets.
func (h *Handler) Authorize(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}

	progress, bookmarks, err := h.userPayload(user.ID)
	if err != nil {
		absauth.Audit(absauth.AuditEvent{
			Action: "authorize", Outcome: absauth.OutcomeError,
			Mode: servermiddleware.ABSAuthMode(c), SourceIP: c.ClientIP(),
			UserID: user.ID, Username: user.Username,
			Reason: "user-data-unavailable", Path: c.Request.URL.Path,
		})
		// Never a 200 with an incomplete list. See the note above.
		respondError(c, http.StatusInternalServerError, "could not load user data")
		return
	}

	accessToken := currentAccessToken(c)
	if accessToken == "" {
		if minted, _, mintErr := h.cfg.MintAccessToken(user.ID, servermiddleware.ABSSessionID(c), h.now()); mintErr == nil {
			accessToken = minted
		}
	}

	absauth.Audit(absauth.AuditEvent{
		Action: "authorize", Outcome: absauth.OutcomeSuccess,
		Mode: servermiddleware.ABSAuthMode(c), SourceIP: c.ClientIP(),
		UserID: user.ID, Username: user.Username,
		SessionID: servermiddleware.ABSSessionID(c),
		Path:      c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})

	respondJSON(c, http.StatusOK, h.buildAuthResponse(
		user, accessToken, refreshTokenFromRequest(c), progress, bookmarks))
}
