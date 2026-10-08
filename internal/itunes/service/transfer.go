// file: internal/itunes/service/transfer.go
// version: 2.5.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-10-07
//
// ITL file transfer handler: download. Part of backlog 6.4. Upload, backup
// list and restore went with iTunes write-back on 2026-10-07: the app never
// writes the iTunes library.

package itunesservice

import (
	"fmt"
	"net/http"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// TransferService owns the ITL download HTTP handler. No store deps — a pure
// filesystem read keyed off config.AppConfig.ITunes.LibraryITLPath.
type TransferService struct{}

func newTransferService() *TransferService { return &TransferService{} }

// HandleDownload serves the current ITL file as a binary download.
//
// GET /api/v1/itunes/library/download
func (t *TransferService) HandleDownload(c *gin.Context) {
	itlPath := config.AppConfig.ITunes.LibraryITLPath
	if itlPath == "" {
		httputil.RespondWithNotFound(c, "the iTunes library ITL path (itunes.library_write_path) is not configured", "")
		return
	}

	info, err := os.Stat(itlPath)
	if err != nil {
		if os.IsNotExist(err) {
			httputil.RespondWithNotFound(c, "ITL file not found at configured path", "")
			return
		}
		httputil.RespondWithInternalError(c, fmt.Sprintf("cannot stat ITL file: %v", err))
		return
	}

	c.Header("Content-Disposition", `attachment; filename="iTunes Library.itl"`)
	c.Header("Content-Length", fmt.Sprintf("%d", info.Size()))
	c.Header("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	c.File(itlPath)
}
