// file: internal/server/handlers/audiobooks/list_flight_test.go
// version: 1.1.0
// guid: 1f8d3b62-9a47-4c05-b7e2-6c4a0e9d5f13
// last-edited: 2026-10-06

package audiobookshandler

import (
	"context"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The sharing/cancellation behaviour is tested in internal/flight. Here: the
// list's build carries listFlightBuildTimeout even while callers wait.
func TestListFlight_BuildHasDeadline(t *testing.T) {
	var f listFlight
	var deadline time.Time
	var ok bool
	_, _ = f.do(context.Background(), "k", func(ctx context.Context) (gin.H, error) {
		deadline, ok = ctx.Deadline()
		return gin.H{}, nil
	})
	if !ok || time.Until(deadline) > listFlightBuildTimeout || time.Until(deadline) < listFlightBuildTimeout-time.Minute {
		t.Fatalf("build deadline = %v (set %v), want about %s from now", deadline, ok, listFlightBuildTimeout)
	}
}
