// file: internal/server/deluge_integration.go
// version: 2.1.0
// guid: 1c9d0e8f-2a3b-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-07-13
//
// Deluge integration — HTTP handlers and thin shims.
//
// Service logic (GetClient, NotifyDeluge*, etc.) has moved to
// internal/deluge/integration.go. This file contains:
//   - getDelugeClient() — package-level shim for server-internal callers
//   - HTTP handlers: handleDelugeTestConnection, handleDelugeListTorrents,
//     handleDelugeStatus, registerDelugeRoutes
//   - Package-level re-exports: NotifyDelugeMoveStorage, NotifyDelugeAfterOrganize,
//     NotifyDelugeAfterUndo, NotifyDelugeAfterVersionSwap (for callers still
//     referencing the server package — prefer deluge.* directly in new code)

package server

import (
	"fmt"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// getDelugeClient is a package-level shim so server-internal code that still
// calls getDelugeClient() continues to compile without change. Prefer
// deluge.GetClient() in new code outside this package.
func getDelugeClient() *deluge.Client {
	return deluge.GetClient()
}

// NotifyDelugeMoveStorage re-exports deluge.NotifyDelugeMoveStorage for callers
// within the server package that predate the extraction.
func NotifyDelugeMoveStorage(torrentHash, newPath string) {
	deluge.NotifyDelugeMoveStorage(torrentHash, newPath)
}

// NotifyDelugeAfterOrganize re-exports deluge.NotifyDelugeAfterOrganize.
func NotifyDelugeAfterOrganize(store interface {
	database.BookVersionStore
}, bookID, newPath string) {
	deluge.NotifyDelugeAfterOrganize(store, bookID, newPath)
}

// NotifyDelugeAfterUndo re-exports deluge.NotifyDelugeAfterUndo.
func NotifyDelugeAfterUndo(store interface {
	database.BookReader
	database.BookVersionStore
}, bookID, oldFilePath string) {
	deluge.NotifyDelugeAfterUndo(store, bookID, oldFilePath)
}

// NotifyDelugeAfterVersionSwap re-exports deluge.NotifyDelugeAfterVersionSwap.
func NotifyDelugeAfterVersionSwap(store interface {
	database.BookReader
	database.BookVersionStore
}, fromVer, toVer *database.BookVersion, bookFilePath string) {
	deluge.NotifyDelugeAfterVersionSwap(store, fromVer, toVer, bookFilePath)
}

// handleDelugeTestConnection tests the Deluge Web UI connection.
// POST /api/v1/deluge/test-connection
func (s *Server) handleDelugeTestConnection(c *gin.Context) {
	url := config.AppConfig.DelugeWebURL
	if url == "" {
		httputil.RespondWithBadRequest(c, "deluge_web_url not configured")
		return
	}
	pass := config.AppConfig.DelugeWebPassword
	if pass == "" {
		pass = "deluge"
	}
	client, err := deluge.New(url, pass)
	if err != nil {
		httputil.InternalError(c, "failed to create deluge client", err)
		return
	}
	if err := client.Login(); err != nil {
		httputil.RespondWithError(c, http.StatusBadGateway, err.Error(), "BAD_GATEWAY")
		return
	}
	connected, err := client.Connected()
	if err != nil {
		httputil.RespondWithError(c, http.StatusBadGateway, err.Error(), "BAD_GATEWAY")
		return
	}
	httputil.RespondWithOK(c, struct {
		Connected bool   `json:"connected"`
		URL       string `json:"url"`
	}{Connected: connected, URL: url})
}

// handleDelugeListTorrents returns all torrents from Deluge.
// GET /api/v1/deluge/torrents
func (s *Server) handleDelugeListTorrents(c *gin.Context) {
	client := getDelugeClient()
	if client == nil {
		httputil.RespondWithBadRequest(c, "deluge not configured")
		return
	}
	torrents, err := client.ListTorrents()
	if err != nil {
		httputil.RespondWithError(c, http.StatusBadGateway, err.Error(), "BAD_GATEWAY")
		return
	}
	httputil.RespondWithOK(c, struct {
		Torrents any `json:"torrents"`
		Count    int `json:"count"`
	}{Torrents: torrents, Count: len(torrents)})
}

// handleDelugeStatus returns Deluge config status.
// GET /api/v1/deluge/status
func (s *Server) handleDelugeStatus(c *gin.Context) {
	url := config.AppConfig.DelugeWebURL
	if url == "" {
		dc := config.AppConfig.DownloadClient.Torrent.Deluge
		if dc.Host != "" {
			port := dc.Port
			if port == 0 {
				port = 8112
			}
			url = fmt.Sprintf("http://%s:%d", dc.Host, port)
		}
	}
	httputil.RespondWithOK(c, struct {
		Configured       bool   `json:"configured"`
		URL              string `json:"url"`
		DiscoveryEnabled bool   `json:"discovery_enabled"`
		MoveEnabled      bool   `json:"move_enabled"`
		DiscoveryLabel   string `json:"discovery_label"`
	}{
		Configured:       url != "",
		URL:              url,
		DiscoveryEnabled: config.AppConfig.DelugeDiscoveryEnabled,
		MoveEnabled:      config.AppConfig.DelugeMoveEnabled,
		DiscoveryLabel:   config.AppConfig.DelugeDiscoveryLabel,
	})
}

// registerDelugeRoutes wires the Deluge integration endpoints.
func (s *Server) registerDelugeRoutes(protected *gin.RouterGroup) {
	dg := protected.Group("/deluge")
	{
		dg.GET("/status", s.perm("integrations.manage"), s.handleDelugeStatus)
		dg.POST("/test-connection", s.perm("integrations.manage"), s.handleDelugeTestConnection)
		dg.GET("/torrents", s.perm("integrations.manage"), s.handleDelugeListTorrents)
		dg.GET("/labels", s.perm("integrations.manage"), s.handleDelugeListLabels)
		dg.GET("/discover", s.perm("integrations.manage"), s.handleDelugeDiscover)
		dg.POST("/discover/import", s.perm("integrations.manage"), s.handleDelugeDiscoverImport)
	}

	// Bulk-import pending Deluge files (settings.manage permission).
	protected.POST("/discovery/import", s.perm("settings.manage"), s.handleDiscoveryImport)
}
