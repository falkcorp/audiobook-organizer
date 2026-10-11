// file: internal/server/openlibrary_service.go
// version: 2.12.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-10-11

package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
	"github.com/gin-gonic/gin"
)

// --- HTTP Handlers ---

func (s *Server) getOLStatus(c *gin.Context) {
	svc := s.olService
	resp := gin.H{
		"enabled":   config.AppConfig.OpenLibraryDumpEnabled,
		"downloads": svc.Tracker.GetAll(),
	}

	// One load: reading svc.OLStore twice (check, then call) raced a delete or
	// factory reset niling it in between, and the nil deref in a handler is a
	// 500 at best. A store closed after the load answers with an error.
	if store := svc.LiveStore(); store != nil {
		status, err := store.GetStatus()
		if err != nil {
			httputil.InternalError(c, "failed to get OpenLibrary status", err)
			return
		}
		resp["status"] = status
	}

	// Check for uploaded dump files on disk
	dumpDir := metafetch.GetOLDumpDir()
	if dumpDir != "" {
		files := map[string]metafetch.UploadedFileInfo{}
		for _, dumpType := range []string{"editions", "authors", "works"} {
			if sp, err := safepath.Join(dumpDir, openlibrary.DumpFilename(dumpType)); err == nil {
				path := sp.String()
				if info, err := os.Stat(path); err == nil {
					files[dumpType] = metafetch.UploadedFileInfo{
						Filename: info.Name(),
						Size:     info.Size(),
						ModTime:  info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
					}
				}
			}
		}
		if len(files) > 0 {
			resp["uploaded_files"] = files
		}
	}

	httputil.RespondWithOK(c, resp)
}

type olDownloadRequest struct {
	Types []string `json:"types"`
}

func (s *Server) startOLDownload(c *gin.Context) {
	var req olDownloadRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Types) == 0 {
		req.Types = []string{"editions", "authors", "works"}
	}

	for _, t := range req.Types {
		if !metafetch.ValidDumpTypes[t] {
			httputil.RespondWithBadRequest(c, fmt.Sprintf("invalid dump type: %s", t))
			return
		}
	}

	targetDir := metafetch.GetOLDumpDir()
	if targetDir == "" {
		httputil.RespondWithBadRequest(c, "openlibrary_dump_dir not configured")
		return
	}

	// A failed enqueue is reported as a failure. This used to fall back to running
	// the download in a detached goroutine and still answer 202 with an id — but
	// that id tracked nothing, so the work had no progress, no cancellation, no
	// resume and no record that it ever ran. "Started" was not true of it in any
	// sense a caller could act on.
	params := olDownloadOpParams{Types: req.Types, TargetDir: targetDir}
	opID, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "openlibrary.download", params)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue OpenLibrary download", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{"message": "download started", "types": req.Types, "operation_id": opID})
}

func (s *Server) startOLImport(c *gin.Context) {
	var req olDownloadRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Types) == 0 {
		req.Types = []string{"editions", "authors", "works"}
	}

	targetDir := metafetch.GetOLDumpDir()
	if targetDir == "" {
		httputil.RespondWithBadRequest(c, "openlibrary_dump_dir not configured")
		return
	}

	svc := s.olService
	if err := svc.EnsureStore(targetDir); err != nil {
		httputil.InternalError(c, "failed to open store", err)
		return
	}

	// See startOLDownload: the detached-goroutine fallback is gone for the same
	// reason. An enqueue failure is a failure.
	importParams := olImportOpParams{Types: req.Types, TargetDir: targetDir}
	opID, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "openlibrary.import", importParams)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue OpenLibrary import", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{"message": "import started", "types": req.Types, "operation_id": opID})
}

func (s *Server) uploadOLDump(c *gin.Context) {
	slog.Debug("uploadOLDump Content-Type, ContentLength", "contentType", logger.SanitizeLogValue(c.ContentType()), "contentLength", c.Request.ContentLength)
	dumpType := c.PostForm("type")
	slog.Debug("uploadOLDump dumpType", "dumpType", logger.SanitizeLogValue(dumpType))
	if !metafetch.ValidDumpTypes[dumpType] {
		httputil.RespondWithBadRequest(c, "type must be one of: editions, authors, works")
		return
	}

	targetDir := metafetch.GetOLDumpDir()
	if targetDir == "" {
		httputil.RespondWithBadRequest(c, "openlibrary_dump_dir not configured")
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		httputil.RespondWithBadRequest(c, "file is required")
		return
	}
	defer file.Close()

	if !strings.HasSuffix(header.Filename, ".gz") {
		httputil.RespondWithBadRequest(c, "file must be a .gz dump file")
		return
	}

	if err := os.MkdirAll(targetDir, 0o775); err != nil {
		httputil.RespondWithInternalError(c, "failed to create dump dir")
		return
	}

	sp, err := safepath.Join(targetDir, openlibrary.DumpFilename(dumpType))
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid target path")
		return
	}
	out, err := os.Create(sp.String())
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to create target file")
		return
	}
	defer out.Close()

	written, err := io.Copy(out, file)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to save file")
		return
	}

	slog.Info("OL dump uploaded ( bytes) ->", "header", logger.SanitizeLogValue(header.Filename), "written", written, "sp", logger.SanitizeLogValue(sp.String()))
	httputil.RespondWithOK(c, gin.H{
		"message":  "dump file uploaded",
		"type":     dumpType,
		"filename": header.Filename,
		"size":     written,
	})
}

func (s *Server) deleteOLData(c *gin.Context) {
	svc := s.olService
	if svc == nil {
		httputil.RespondWithOK(c, httputil.MessageResponse{Message: "no data to delete"})
		return
	}

	svc.Mu.Lock()
	defer svc.Mu.Unlock()

	// The store refuses all work once its Close has begun. But a Close that
	// timed out (a lookup or import outlived the drain) leaves the engine
	// open, and deleting its directory would pull files out from under it, so
	// the data is kept and the failure reported. The service holds on to the
	// half-closed store, so deleting again retries the close, and so does the
	// next import (EnsureStore); either succeeds once the straggler is done.
	if closeErr := svc.CloseStoreLocked(); closeErr != nil {
		httputil.InternalError(c, "failed to close the Open Library store; data not deleted", closeErr)
		return
	}

	targetDir := metafetch.GetOLDumpDir()
	if targetDir != "" {
		if err := os.RemoveAll(targetDir); err != nil {
			httputil.InternalError(c, "failed to delete data", err)
			return
		}
	}

	httputil.RespondWithOK(c, httputil.MessageResponse{Message: "data deleted"})
}
