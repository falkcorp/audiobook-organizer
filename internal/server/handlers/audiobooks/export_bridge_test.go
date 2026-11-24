// file: internal/server/handlers/audiobooks/export_bridge_test.go
// version: 0.1.0
// guid: 07bd1878-cd0a-43dc-aba3-3cb89a594fb2
// last-edited: 2026-11-21

package audiobookshandler

import "github.com/gin-gonic/gin"

// TrashProgressViewer exposes trashProgressViewer.
func TrashProgressViewer(c *gin.Context) (viewerID string, seeAll bool) {
	return trashProgressViewer(c)
}
