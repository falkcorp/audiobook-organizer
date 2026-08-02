// file: internal/server/import_collision.go
// version: 1.3.0
// guid: 4b2c3d1e-5f6a-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-05-11
//
// HTTP handler for import-time collision preview. Delegates to
// internal/importer for the core collision detection logic.

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/gin-gonic/gin"
)

// handleImportCollisionPreview checks whether importing a file
// would collide with existing library content.
// POST /api/v1/import/collision-preview
func (s *Server) handleImportCollisionPreview(c *gin.Context) {
	var req struct {
		FilePath    string `json:"file_path" binding:"required"`
		TorrentHash string `json:"torrent_hash,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	// Delegate to importer package for collision detection.
	result := importer.CheckImportCollisions(
		s.Store(),
		&importer.CollisionPreviewRequest{
			FilePath:    req.FilePath,
			TorrentHash: req.TorrentHash,
		},
	)

	httputil.RespondWithOK(c, struct {
		Collisions   interface{} `json:"collisions"`
		Count        int         `json:"count"`
		HasCollision bool        `json:"has_collision"`
	}{
		Collisions:   result.Collisions,
		Count:        result.Count,
		HasCollision: result.HasCollision,
	})
}
