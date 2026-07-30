// file: internal/server/handlers/abs/me.go
// version: 1.0.0
// guid: 63b8c105-2f74-4ea9-8d16-947c0be5a2f3
// last-edited: 2026-07-30

package abs

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Me handles GET /api/me.
//
// 🔴 THIS ENDPOINT CAN DESTROY USER DATA IF IT UNDER-REPORTS (§1.8.1).
// AudioBooth's MediaProgress.syncFromAPI iterates the device's local progress rows and
// DELETES any whose bookID is absent from `user.mediaProgress`, sparing only the
// currently-playing book and books with unsynced offline sessions. So:
//
//   - the array must always be PRESENT and non-null (a Swift non-optional decode);
//   - it must be COMPLETE — never paginated, never limited, never partially filled;
//   - if we cannot read the complete list we must answer 5xx, not a 200 with an empty
//     array. A retry costs nothing; a lying 200 costs the user their place in every
//     book, on every home-screen refresh.
//
// Any `page`/`limit` query parameter is ignored here by design.
func (h *Handler) Me(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}

	progress, bookmarks, err := h.userPayload(user.ID)
	if err != nil {
		absauth.Audit(absauth.AuditEvent{
			Action: "me", Outcome: absauth.OutcomeError, Mode: servermiddleware.ABSAuthMode(c),
			SourceIP: c.ClientIP(), UserID: user.ID, Username: user.Username,
			Reason: "user-data-unavailable", Path: c.Request.URL.Path,
		})
		// Never a 200 with an incomplete list. See the note above.
		respondError(c, http.StatusInternalServerError, "could not load user data")
		return
	}

	// Real ABS returns only the legacy `token` on this endpoint, not accessToken /
	// refreshToken. Echo the bearer the caller presented when there is one; in Mode C
	// there may be no bearer at all, so mint a fresh access token rather than emit an
	// empty string for a field clients read as a String.
	token := currentAccessToken(c)
	if token == "" {
		if minted, _, err := h.cfg.MintAccessToken(user.ID, servermiddleware.ABSSessionID(c), h.now()); err == nil {
			token = minted
		}
	}

	respondJSON(c, http.StatusOK, h.buildUser(user, "", "", progress, bookmarks).withLegacyTokenOnly(token))
}

// withLegacyTokenOnly clears the access/refresh fields (which are `omitempty`) and
// sets the legacy `token`, matching real ABS's /api/me shape.
func (u userDTO) withLegacyTokenOnly(token string) userDTO {
	u.AccessToken = ""
	u.RefreshToken = ""
	u.Token = token
	return u
}

// currentAccessToken recovers the bearer the caller presented, from the header or the
// ?token= query parameter that Absorb and CarPlay use on GETs.
func currentAccessToken(c *gin.Context) string {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		if tok := strings.TrimSpace(header[7:]); tok != "" && !strings.HasPrefix(tok, "abk_") {
			return tok
		}
	}
	return strings.TrimSpace(c.Query("token"))
}

// Sessions handles GET /api/me/sessions — the caller's own ABS auth sessions.
//
// Every count is an INTEGER and every timestamp an integer ms epoch (§1.7.3 item 5,
// §1.8.5 item 1): Dart throws on `42.0 as int?` and AudioBooth decodes dates as Int64.
func (h *Handler) Sessions(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}

	sessions, err := h.store.ListABSSessionsForUser(user.ID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not list sessions")
		return
	}

	// Newest first, matching real ABS. Sorted HERE rather than relying on the store so
	// the client-visible ordering is a property of the endpoint, not of whichever store
	// implementation is wired underneath.
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
	})

	currentID := servermiddleware.ABSSessionID(c)
	out := make([]sessionDTO, 0, len(sessions))
	for i := range sessions {
		s := sessions[i]
		if s.Revoked {
			// A revoked session cannot be used and would only invite a pointless
			// DELETE from the UI.
			continue
		}
		out = append(out, sessionDTO{
			CreatedAt:  msEpoch(s.CreatedAt),
			Current:    s.ID == currentID,
			DeviceInfo: decodeDeviceInfo(s.DeviceInfo),
			ID:         s.ID,
			IPAddress:  s.IP,
			UpdatedAt:  msEpoch(s.LastUsedAt),
			UserAgent:  s.UserAgent,
		})
	}

	respondJSON(c, http.StatusOK, sessionsResponse{
		ItemsPerPage: len(out),
		NumPages:     1,
		Page:         0,
		Sessions:     out,
		Total:        len(out),
	})
}

// decodeDeviceInfo turns the stored deviceInfo text back into the object the client
// sent. Real ABS returns null when it has none, and null is what both clients expect
// for that case.
func decodeDeviceInfo(raw string) any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

// DeleteSession handles DELETE /api/me/sessions/:id.
//
// Ownership is enforced, not assumed: without the UserID check any authenticated user
// could log out anybody by guessing a session id. A session that exists but belongs to
// someone else is reported as 404 rather than 403, so the endpoint does not confirm
// the existence of other users' sessions.
func (h *Handler) DeleteSession(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		respondError(c, http.StatusNotFound, "session not found")
		return
	}

	session, err := h.store.GetABSSession(id)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load session")
		return
	}
	if session == nil || session.UserID != user.ID {
		absauth.Audit(absauth.AuditEvent{
			Action: "session-delete", Outcome: absauth.OutcomeDenied,
			Mode: servermiddleware.ABSAuthMode(c), SourceIP: c.ClientIP(),
			UserID: user.ID, Username: user.Username, SessionID: id,
			Reason: "not-found-or-not-owned", Path: c.Request.URL.Path,
		})
		respondError(c, http.StatusNotFound, "session not found")
		return
	}

	if err := h.store.RevokeABSSession(id); err != nil {
		respondError(c, http.StatusInternalServerError, "could not revoke session")
		return
	}
	absauth.Audit(absauth.AuditEvent{
		Action: "session-delete", Outcome: absauth.OutcomeSuccess,
		Mode: servermiddleware.ABSAuthMode(c), SourceIP: c.ClientIP(),
		UserID: user.ID, Username: user.Username, SessionID: id, Path: c.Request.URL.Path,
	})
	respondJSON(c, http.StatusOK, gin.H{"success": true})
}

// Logout handles POST /logout, with ?allDevices=1 for a full revoke.
//
// The response body is never empty: an empty 200 is fatal to these decoders (§1.8.6).
func (h *Handler) Logout(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	mode := servermiddleware.ABSAuthMode(c)
	ip := c.ClientIP()

	if allDevicesRequested(c) {
		n, err := h.store.RevokeAllABSSessionsForUser(user.ID)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "could not revoke sessions")
			return
		}
		absauth.Audit(absauth.AuditEvent{
			Action: "logout", Outcome: absauth.OutcomeSuccess, Mode: mode, SourceIP: ip,
			UserID: user.ID, Username: user.Username, Reason: "all-devices",
			Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
		})
		respondJSON(c, http.StatusOK, gin.H{"success": true, "revoked": n})
		return
	}

	// Which session is "this one"? In Mode B the access token names it. In Mode C
	// there is no bearer, so fall back to the refresh token the client may have sent —
	// and verify it belongs to this user before revoking anything.
	sessionID := servermiddleware.ABSSessionID(c)
	if sessionID == "" {
		if presented := refreshTokenFromRequest(c); presented != "" {
			if s, err := h.store.GetABSSessionByRefreshHash(absauth.HashRefreshToken(presented)); err == nil &&
				s != nil && s.UserID == user.ID {
				sessionID = s.ID
			}
		}
	}
	if sessionID != "" {
		if err := h.store.RevokeABSSession(sessionID); err != nil {
			respondError(c, http.StatusInternalServerError, "could not revoke session")
			return
		}
	}

	absauth.Audit(absauth.AuditEvent{
		Action: "logout", Outcome: absauth.OutcomeSuccess, Mode: mode, SourceIP: ip,
		UserID: user.ID, Username: user.Username, SessionID: sessionID,
		Path: c.Request.URL.Path, UserAgent: c.Request.UserAgent(),
	})
	respondJSON(c, http.StatusOK, gin.H{"success": true})
}

// allDevicesRequested accepts the shapes clients actually send: ?allDevices=1, =true,
// or a bare ?allDevices.
func allDevicesRequested(c *gin.Context) bool {
	raw, present := c.GetQuery("allDevices")
	if !present {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "1", "true", "yes":
		return true
	default:
		return false
	}
}
