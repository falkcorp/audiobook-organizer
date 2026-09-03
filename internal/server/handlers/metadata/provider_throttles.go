// file: internal/server/handlers/metadata/provider_throttles.go
// version: 1.0.0
// guid: 9631cf4b-f3a1-4989-ba56-688854cbe69e
// last-edited: 2026-09-03

package metadatahandler

import (
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	metadatapkg "github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/gin-gonic/gin"
)

// providerThrottleView is one hold as the UI sees it.
//
// SecondsRemaining is served alongside Until because the UI polls on a slow
// cadence (the user asked for something like every four hours) and a countdown
// it can render without doing clock arithmetic against a server timestamp is
// less to get wrong -- a client whose clock is off would otherwise show a hold
// as expired while the server is still enforcing it.
type providerThrottleView struct {
	ProviderID       string `json:"provider_id"`
	Reason           string `json:"reason"`
	Detail           string `json:"detail,omitempty"`
	Until            string `json:"until"`
	SetAt            string `json:"set_at"`
	SecondsRemaining int64  `json:"seconds_remaining"`
}

func toThrottleView(t metadatapkg.ProviderThrottle, now time.Time) providerThrottleView {
	return providerThrottleView{
		ProviderID:       t.ProviderID,
		Reason:           string(t.Reason),
		Detail:           t.Detail,
		Until:            t.Until.UTC().Format(time.RFC3339),
		SetAt:            t.SetAt.UTC().Format(time.RFC3339),
		SecondsRemaining: int64(t.Remaining(now).Seconds()),
	}
}

// listProviderThrottlesImpl handles GET /api/v1/metadata/providers/throttles.
func (h *Handler) listProviderThrottlesImpl(c *gin.Context) {
	now := time.Now()
	active := metadatapkg.DefaultThrottleRegistry().List()
	views := make([]providerThrottleView, 0, len(active))
	for _, t := range active {
		views = append(views, toThrottleView(t, now))
	}
	httputil.RespondWithOK(c, gin.H{
		"throttles": views,
		"count":     len(views),
	})
}

// clearProviderThrottleImpl handles DELETE /api/v1/metadata/providers/throttles/:id.
//
// This is the manual reset the user asked for: "unless the user manually resets
// the timeout". It is deliberately unconditional — if someone has topped up the
// quota or fixed the credential, waiting out a 4- or 6-hour timer helps nobody.
func (h *Handler) clearProviderThrottleImpl(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		httputil.RespondWithBadRequest(c, "provider id is required")
		return
	}
	_, wasHeld := metadatapkg.DefaultThrottleRegistry().Get(id)
	if err := metadatapkg.DefaultThrottleRegistry().Clear(id); err != nil {
		httputil.RespondWithInternalError(c, err.Error())
		return
	}
	// Clearing an id that was not held is reported honestly rather than as an
	// error: the UI polls, so a hold can expire between the render and the
	// click, and a 404 there would look like a broken button.
	httputil.RespondWithOK(c, gin.H{"provider_id": id, "cleared": wasHeld})
}

// clearAllProviderThrottlesImpl handles DELETE /api/v1/metadata/providers/throttles.
func (h *Handler) clearAllProviderThrottlesImpl(c *gin.Context) {
	n, err := metadatapkg.DefaultThrottleRegistry().ClearAll()
	if err != nil {
		httputil.RespondWithInternalError(c, err.Error())
		return
	}
	httputil.RespondWithOK(c, gin.H{"cleared": n})
}
