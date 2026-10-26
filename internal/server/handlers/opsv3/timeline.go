// file: internal/server/handlers/opsv3/timeline.go
// version: 1.2.0
// guid: a9a8ff32-bf89-4265-a016-59ee9e75d04e
// last-edited: 2026-10-26

package opsv3

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// Timeline bounds. The store returns finished runs oldest first up to the
// scan cap; a window with more runs than that says complete=false.
const (
	TimelineScanCap       = 5000
	timelineDefaultWindow = 24 * time.Hour
)

// Timeline implements GET /api/v3/ops/timeline?from=&to= (RFC 3339). It lists
// the runs that finished in [from, to], oldest first. complete is false when
// the scan cap cut the window short: items then holds the oldest scan_cap runs
// of it and the rest are not shown. A timeline is a window, not a census; use
// /runs and /counts for totals.
func (h *Handler) Timeline(c *gin.Context) {
	rd, ok := h.runs(c)
	if !ok {
		return
	}
	now := h.d.Now().UTC()
	to, ok := timeParam(c, "to", now)
	if !ok {
		return
	}
	from, ok := timeParam(c, "from", to.Add(-timelineDefaultWindow))
	if !ok {
		return
	}
	if from.After(to) {
		httputil.RespondWithValidationError(c, "from", "is after to")
		return
	}
	recs, complete, err := rd.ListDoneRunsSinceV3(from, TimelineScanCap)
	if err != nil {
		httputil.InternalError(c, "timeline", err)
		return
	}
	// Repairs runs the caller may not read are not in their timeline.
	reader := h.d.RepairsAccess.For(c)
	inWindow := make([]database.RunRecord, 0, len(recs))
	pastTo := false
	for _, r := range recs {
		if !reader.MayReadRun(&r) {
			continue
		}
		if r.CompletedAt != nil && r.CompletedAt.After(to) {
			pastTo = true
			continue
		}
		inWindow = append(inWindow, r)
	}
	// Oldest first, so once the scan reached a run past `to` the whole window
	// was covered even if the cap was hit afterwards.
	if pastTo {
		complete = true
	}
	c.JSON(http.StatusOK, gin.H{
		"items": h.views(c, inWindow), "complete": complete,
		"from": from, "to": to, "scan_cap": TimelineScanCap,
	})
}

func timeParam(c *gin.Context, key string, def time.Time) (time.Time, bool) {
	raw := c.Query(key)
	if raw == "" {
		return def, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		httputil.RespondWithValidationError(c, key, "must be an RFC 3339 time")
		return time.Time{}, false
	}
	return t.UTC(), true
}
