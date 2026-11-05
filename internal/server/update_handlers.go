// file: internal/server/update_handlers.go
// version: 2.3.0
// guid: 4c5d6e7f-8a9b-0c1d-2e3f-4a5b6c7d8e9f
// last-edited: 2026-11-05

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/updater"
	"github.com/gin-gonic/gin"
)

// maxApplyBody bounds the optional POST /update/apply body ({"version": tag}).
const maxApplyBody = 4 << 10

// withLivePause returns a copy of info whose AutoApplyPaused reflects the
// operator's setting now: auto_update.enabled asks for automatic apply, which
// this build pauses (check only). The recorded check may predate a change of
// the setting, or come from a manual check, so the response never reads it.
func withLivePause(info *updater.UpdateInfo) *updater.UpdateInfo {
	out := *info
	out.AutoApplyPaused = config.AppConfig.AutoUpdate.Enabled
	return &out
}

// getUpdateStatus returns the last update check info.
func (s *Server) getUpdateStatus(c *gin.Context) {
	info := s.updater.LastCheck()
	if info == nil {
		// No check yet: the same shape as a check result, so the banner and
		// the local-build note show before the first check.
		local := updater.IsLocalBuild(appVersion)
		note := ""
		if local {
			note = updater.LocalBuildNote
		}
		httputil.RespondWithOK(c, struct {
			CurrentVersion  string `json:"current_version"`
			LatestVersion   string `json:"latest_version"`
			Channel         string `json:"channel"`
			UpdateAvailable bool   `json:"update_available"`
			LastChecked     any    `json:"last_checked"`
			LocalBuild      bool   `json:"local_build"`
			AutoApplyPaused bool   `json:"auto_apply_paused"`
			Note            string `json:"note"`
		}{
			CurrentVersion:  appVersion,
			LatestVersion:   "",
			Channel:         config.AppConfig.AutoUpdate.Channel,
			UpdateAvailable: false,
			LastChecked:     nil,
			LocalBuild:      local,
			AutoApplyPaused: config.AppConfig.AutoUpdate.Enabled,
			Note:            note,
		})
		return
	}
	httputil.RespondWithOK(c, withLivePause(info))
}

// checkForUpdate triggers an immediate update check and returns the result.
func (s *Server) checkForUpdate(c *gin.Context) {
	channel := config.AppConfig.AutoUpdate.Channel
	info, err := s.updater.CheckForUpdate(channel)
	if err != nil {
		httputil.InternalError(c, "failed to check for updates", err)
		return
	}
	httputil.RespondWithOK(c, withLivePause(info))
}

// applyUpdateRequest is the optional body of POST /update/apply. Version names
// the release tag to apply ("v" optional); empty means the release the last
// check offered. A local build applies only a named version.
type applyUpdateRequest struct {
	Version string `json:"version"`
}

// applyUpdate downloads and applies an update, then exits for restart. It is
// the only caller of DownloadAndReplace and RestartSelf: the scheduled check
// and the maintenance window only check (R0 hotfix).
//
// Refusals, each before anything is downloaded: 400 when nothing is offered
// and no version is named (HEAD's), or the version is not a release tag; 409
// for a local build with no version, a target that is not newer than the
// running version, or a running build with no release version; 404 when the
// named tag has no release.
func (s *Server) applyUpdate(c *gin.Context) {
	var req applyUpdateRequest
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxApplyBody+1))
	if err != nil {
		httputil.RespondWithBadRequest(c, "could not read request body")
		return
	}
	if len(body) > maxApplyBody {
		httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "PAYLOAD_TOO_LARGE")
		return
	}
	// An empty body is HEAD's request: apply what the last check offered.
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httputil.RespondWithBadRequest(c, "invalid request body: want {\"version\": \"<tag>\"}")
			return
		}
	}

	target, err := s.updater.PrepareApply(req.Version)
	if err != nil {
		respondApplyRefusal(c, err)
		return
	}

	if err := s.updater.DownloadAndReplace(target); err != nil {
		if isApplyRefusal(err) {
			respondApplyRefusal(c, err)
			return
		}
		httputil.InternalError(c, "failed to apply update", err)
		return
	}

	httputil.RespondWithOK(c, httputil.MessageResponse{Message: "update applied, restarting..."})

	// Exit after response is sent; systemd/launchd restarts the process
	go s.updater.RestartSelf()
}

// isApplyRefusal reports whether err is one of the updater's refusals, which
// are answered with their own status rather than a 500.
func isApplyRefusal(err error) bool {
	for _, refusal := range []error{
		updater.ErrNoUpdate, updater.ErrInvalidVersion, updater.ErrLocalBuildNeedsVersion,
		updater.ErrNotNewer, updater.ErrUnversioned, updater.ErrReleaseNotFound,
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// respondApplyRefusal maps an updater refusal to its status and message.
func respondApplyRefusal(c *gin.Context, err error) {
	switch {
	case errors.Is(err, updater.ErrNoUpdate):
		httputil.RespondWithBadRequest(c, updater.ErrNoUpdate.Error())
	case errors.Is(err, updater.ErrInvalidVersion):
		httputil.RespondWithBadRequest(c, updater.ErrInvalidVersion.Error())
	case errors.Is(err, updater.ErrLocalBuildNeedsVersion):
		httputil.RespondWithConflict(c, updater.ErrLocalBuildNeedsVersion.Error())
	case errors.Is(err, updater.ErrNotNewer):
		httputil.RespondWithConflict(c, updater.ErrNotNewer.Error())
	case errors.Is(err, updater.ErrUnversioned):
		httputil.RespondWithConflict(c, updater.ErrUnversioned.Error())
	case errors.Is(err, updater.ErrReleaseNotFound):
		httputil.RespondWithError(c, http.StatusNotFound, err.Error(), "NOT_FOUND")
	default:
		httputil.InternalError(c, "failed to apply update", err)
	}
}
