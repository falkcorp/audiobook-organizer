// file: internal/database/startup_notify.go
// version: 1.0.0
// guid: 439e418b-9ebb-4fdc-b44f-92b10ad28a37
// last-edited: 2026-10-12

package database

import (
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/sdnotify"
)

var startupNotifyLog = logger.New("database.startup")

// extendStartTimeout asks systemd, under the Type=notify unit, for
// sdnotify.StartupStepBudget more start time. It is called right before a
// startup step that does real work and can legitimately take long: opening
// the store (WAL replay) and applying each pending migration. Under
// TimeoutStartSec alone a long migration run would be killed part-way. It is
// called per step, never from a timer, so a step that hangs is still killed
// once its own budget runs out. A no-op without NOTIFY_SOCKET; a failure is
// logged and never fails startup.
func extendStartTimeout() {
	if err := sdnotify.ExtendTimeout(sdnotify.StartupStepBudget); err != nil {
		startupNotifyLog.Warn("systemd start-timeout extension failed: %v", err)
	}
}
