// file: internal/server/openlibrary_service.go
// version: 2.9.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-05-15

package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
)

// --- HTTP Handlers ---

func (s *Server) getOLStatus(c *gin.Context) {
	svc := s.olService
	resp := gin.H{
		"enabled":   config.AppConfig.OpenLibraryDumpEnabled,
		"downloads": svc.Tracker.GetAll(),
	}

	if svc.OLStore != nil {
		status, err := svc.OLStore.GetStatus()
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

	tracker := s.olService.Tracker
	store := s.Store()
	opID := ulid.Make().String()
	folderPath := targetDir
	if store != nil {
		_, _ = store.CreateOperation(opID, "ol_dump_download", &folderPath)
	}

	params := olDownloadOpParams{LegacyOpID: opID, Types: req.Types, TargetDir: targetDir}
	if _, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "openlibrary.download", params); enqErr != nil {
		slog.Warn("Failed to enqueue OL download, running directly", "enqErr", enqErr)
		go func() {
			for _, dumpType := range req.Types {
				_ = openlibrary.DownloadDump(dumpType, targetDir, tracker)
			}
		}()
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

	store := s.Store()
	opID := ulid.Make().String()
	folderPath := targetDir
	if store != nil {
		_, _ = store.CreateOperation(opID, "ol_dump_import", &folderPath)
	}

	importParams := olImportOpParams{LegacyOpID: opID, Types: req.Types, TargetDir: targetDir}
	if _, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "openlibrary.import", importParams); enqErr != nil {
		slog.Warn("Failed to enqueue OL import, running directly", "enqErr", enqErr)
		go func() {
			_ = svc.Import(context.Background(), nil, targetDir, req.Types)
		}()
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{"message": "import started", "types": req.Types, "operation_id": opID})
}

func (s *Server) uploadOLDump(c *gin.Context) {
	slog.Debug("uploadOLDump Content-Type, ContentLength", "contentType", c.ContentType(), "contentLength", c.Request.ContentLength)
	dumpType := c.PostForm("type")
	slog.Debug("uploadOLDump dumpType", "dumpType", dumpType)
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

	slog.Info("OL dump uploaded ( bytes) ->", "header", header.Filename, "written", written, "sp", sp.String())
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

	if svc.OLStore != nil {
		svc.OLStore.Close()
		svc.OLStore = nil
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
