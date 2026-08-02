// file: internal/server/update_handlers.go
// version: 2.2.0
// guid: 4c5d6e7f-8a9b-0c1d-2e3f-4a5b6c7d8e9f
// last-edited: 2026-06-16

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// getUpdateStatus returns the last update check info.
func (s *Server) getUpdateStatus(c *gin.Context) {
	info := s.updater.LastCheck()
	if info == nil {
		httputil.RespondWithOK(c, struct {
			CurrentVersion  string `json:"current_version"`
			LatestVersion   string `json:"latest_version"`
			Channel         string `json:"channel"`
			UpdateAvailable bool   `json:"update_available"`
			LastChecked     any    `json:"last_checked"`
		}{
			CurrentVersion:  appVersion,
			LatestVersion:   "",
			Channel:         config.AppConfig.AutoUpdate.Channel,
			UpdateAvailable: false,
			LastChecked:     nil,
		})
		return
	}
	httputil.RespondWithOK(c, info)
}

// checkForUpdate triggers an immediate update check and returns the result.
func (s *Server) checkForUpdate(c *gin.Context) {
	channel := config.AppConfig.AutoUpdate.Channel
	info, err := s.updater.CheckForUpdate(channel)
	if err != nil {
		httputil.InternalError(c, "failed to check for updates", err)
		return
	}
	httputil.RespondWithOK(c, info)
}

// applyUpdate downloads and applies an available update, then exits for restart.
func (s *Server) applyUpdate(c *gin.Context) {
	info := s.updater.LastCheck()
	if info == nil || !info.UpdateAvailable {
		httputil.RespondWithBadRequest(c, "no update available")
		return
	}

	if err := s.updater.DownloadAndReplace(info); err != nil {
		httputil.InternalError(c, "failed to apply update", err)
		return
	}

	httputil.RespondWithOK(c, httputil.MessageResponse{Message: "update applied, restarting..."})

	// Exit after response is sent; systemd/launchd restarts the process
	go s.updater.RestartSelf()
}
