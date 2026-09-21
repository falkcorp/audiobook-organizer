// file: internal/server/handlers/operations/pause.go
// version: 1.1.0
// guid: 3f6b0c28-5a17-4e93-b2d4-91e7c05a8b63
// last-edited: 2026-09-20

package operations

import (
	"encoding/json"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

// pauseLog is this package's sanitizing logger. NOT a direct slog call: those
// bypass sanitizeLogLine entirely, which is how main accumulated 307
// go/log-injection alerts (docs/audits/2026-09-13-log-injection-sweep.md) and
// why internal/logger's guard test refuses new ones.
var pauseLog = logger.New("operations.pause")

// Operator pause endpoints.
//
// Pause drains: an item already running finishes, the next one parks. It is not
// cancel — nothing is ended and no progress is lost — and it is not the scan
// stand-down, which is an internal per-def gate an apply op takes.
//
// Guarded by PermSettingsManage, the same permission as cancel: acting on runs
// that already exist, not triggering new work.

// pauseRequest is the POST body. Both fields are optional; Reason is strongly
// encouraged because it is what the banner shows the next person to look.
type pauseRequest struct {
	Reason string `json:"reason"`
}

// pauseResponse carries the gate state plus the coverage caveat. NotPausable is
// populated rather than omitted because a running op that CANNOT park is the
// one thing an operator must not have to discover for themselves: without it,
// "paused" plus a still-advancing library.scan reads as a broken pause.
type pauseResponse struct {
	registry.PauseState
	// RunningPausable lists running ops that dispatch through RunItems and will
	// therefore park.
	RunningPausable []string `json:"running_pausable"`
	// RunningNotPausable lists running ops with no item loop to gate. They keep
	// going. library.scan is the important member.
	RunningNotPausable []string `json:"running_not_pausable"`
	// Note explains the above in one sentence for a human reading raw JSON.
	Note string `json:"note,omitempty"`
}

// pausableDefs are the def IDs known to dispatch their work through
// registry.RunItems, so a pause parks them between items.
//
// An allow-list, deliberately, and kept small: it drives only what the API
// REPORTS, never what the gate does. The gate lives inside RunItems and is
// therefore automatically correct for all 60 call sites; if this list drifts,
// the failure is a misleading label, not a pause that silently misses an op.
// The inverse (a denylist) would fail the other way and claim ops pause when
// they do not.
var pausableDefs = map[string]bool{
	"acoustid.window-backfill":         true,
	"acoustid.backfill":                true,
	"acoustid.lsh-backfill":            true,
	"metadata.batch-apply-cached":      true,
	"maintenance.duration-backfill":    true,
	"maintenance.author-path-link":     true,
	"maintenance.repair-library-state": true,
	"maintenance.rewrite-path-prefix":  true,
	"dedup.full-scan":                  true,
}

// PauseOperations holds item dispatch across every op that runs items.
func (h *Handler) PauseOperations(c *gin.Context) {
	var req pauseRequest
	if c.Request != nil && c.Request.Body != nil {
		// A malformed or absent body is not an error: pausing with no reason is
		// still a valid, useful action, and refusing it would put a parsing
		// nicety between an operator and the stop button.
		_ = json.NewDecoder(c.Request.Body).Decode(&req)
	}
	reason := strings.TrimSpace(req.Reason)

	by := ""
	if v, ok := c.Get("username"); ok {
		if s, ok := v.(string); ok {
			by = s
		}
	}

	// The hold is in effect regardless; the error is the marker write only.
	if err := registry.PauseOperations(reason, by); err != nil {
		pauseLog.Warn("operations pause: hold is active but the marker could not be persisted; a restart would resume: %v", logging.SanitizeErr(err))
	}
	httputil.RespondWithOK(c, h.pauseStatePayload())
}

// ResumeOperations releases the hold.
func (h *Handler) ResumeOperations(c *gin.Context) {
	if err := registry.ResumeOperations(); err != nil {
		pauseLog.Warn("operations resume: hold released but the marker could not be cleared: %v", logging.SanitizeErr(err))
	}
	httputil.RespondWithOK(c, h.pauseStatePayload())
}

// GetPauseState reports the current gate state.
func (h *Handler) GetPauseState(c *gin.Context) {
	httputil.RespondWithOK(c, h.pauseStatePayload())
}

// pauseStatePayload builds the response, including which running ops will
// actually park.
func (h *Handler) pauseStatePayload() pauseResponse {
	resp := pauseResponse{PauseState: registry.OperationsPauseState()}
	if h.store != nil {
		// GetRecentOperations + a terminal-status filter, because the store
		// exposes no "active" reader here. isTerminalStatus must stay in sync
		// with the backend's interrupted_* family; enumerating the three
		// obvious statuses is the bug this codebase has hit before.
		if ops, err := h.store.GetRecentOperations(200); err == nil {
			for _, op := range ops {
				if isTerminalOpStatus(op.Status) {
					continue
				}
				if pausableDefs[op.Type] {
					resp.RunningPausable = append(resp.RunningPausable, op.Type)
				} else {
					resp.RunningNotPausable = append(resp.RunningNotPausable, op.Type)
				}
			}
		}
	}
	if len(resp.RunningNotPausable) > 0 {
		resp.Note = "the ops under running_not_pausable have no per-item loop to hold at and will keep running until they finish or are cancelled"
	}
	return resp
}

// isTerminalOpStatus reports whether a run has ended.
//
// The prefix test on "interrupted" is deliberate: the backend mints a family of
// interrupted_* statuses (one per ResumePolicy) and enumerating them has gone
// wrong here before — an op finished as interrupted_dropped was counted as
// in-progress forever, inflating the bell badge on every restart.
func isTerminalOpStatus(status string) bool {
	switch status {
	case "completed", "failed", "canceled", "cancelled":
		return true
	}
	return strings.HasPrefix(status, "interrupted")
}
