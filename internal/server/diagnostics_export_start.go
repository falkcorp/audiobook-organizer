// file: internal/server/diagnostics_export_start.go
// version: 1.0.0
// guid: 8a66b718-17fa-4493-b04e-5e6f99fde1fc
// last-edited: 2026-11-28

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// diagnosticsStartCheck is the check POST /diagnostics/export makes before it
// enqueues an export (handlers.ExportStartCheck). Under core identity an export
// is built for an account: the job runs after its request is gone, narrowed to
// what the account that queued it may read (opActorHolds), and a job with no
// account fails (errNoExportActor) rather than run narrowed to nothing or widened
// to everything. With core's auth disabled a request binds no user, so an export
// started then could only be queued and then fail with nothing the caller can do
// about it. It is refused here, 409, with the reason.
//
// This does not widen anything: it refuses a start that would have failed. Whether
// an install with auth off should get whole exports is an owner decision
// (todo.d AUTH-OFF-EXPORT); until it is made an export under identity needs an
// account. Off core identity there is no such refusal (HEAD's rule, unchanged:
// its export runs with auth off and holds everything).
func (s *Server) diagnosticsStartCheck() handlers.ExportStartCheck {
	id := s.identity
	if id == nil {
		return nil
	}
	return func(c *gin.Context) string {
		ctx := c.Request.Context()
		if _, ok := auth.UserFromContext(ctx); ok {
			return ""
		}
		if id.Bridge != nil && id.Bridge.AuthOff(ctx) {
			return "a diagnostics export needs a signed-in account to be built for, and core authentication is " +
				"disabled on this server, so there is no account; turn core authentication on and sign in to start one"
		}
		return "a diagnostics export needs a signed-in account to be built for, and this request carries none; sign in and start it again"
	}
}
