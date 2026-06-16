// file: internal/itunes/service/transfer.go
// version: 2.2.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-06-16
//
// ITL file transfer handlers: download, upload+validate, backup
// list, and restore. Part of backlog 6.4.

package itunesservice

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// maxITLUploadSize is the maximum allowed ITL upload (500 MB).
const maxITLUploadSize = 500 << 20

// TransferService owns the ITL file transfer HTTP handlers: download,
// upload+validate, backup list, and restore. No store / batcher deps —
// pure filesystem operations keyed off config.AppConfig.ITunes.LibraryWritePath.
type TransferService struct{}

func newTransferService() *TransferService { return &TransferService{} }

// HandleDownload serves the current ITL file as a binary download.
//
// GET /api/v1/itunes/library/download
func (t *TransferService) HandleDownload(c *gin.Context) {
	itlPath := config.AppConfig.ITunes.LibraryWritePath
	if itlPath == "" {
		httputil.RespondWithNotFound(c, "ITunesLibraryWritePath is not configured", "")
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

// ITLUploadResponse is returned after uploading an ITL file.
type ITLUploadResponse struct {
	Valid     bool   `json:"valid"`
	Installed bool   `json:"installed"`
	Tracks    int    `json:"tracks"`
	Playlists int    `json:"playlists"`
	Version   string `json:"version"`
	Error     string `json:"error,omitempty"`
}

// HandleUpload accepts a multipart ITL upload, validates it, and
// optionally installs it as the active library.
//
// POST /api/v1/itunes/library/upload?install=true|false
func (t *TransferService) HandleUpload(c *gin.Context) {
	itlPath := config.AppConfig.ITunes.LibraryWritePath
	if itlPath == "" {
		httputil.RespondWithBadRequest(c, "ITunesLibraryWritePath is not configured")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxITLUploadSize)

	file, _, err := c.Request.FormFile("library")
	if err != nil {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("missing or invalid 'library' form field: %v", err))
		return
	}
	defer file.Close()

	dir := filepath.Dir(itlPath)
	tmp, err := os.CreateTemp(dir, "itl-upload-*.tmp")
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("cannot create temp file: %v", err))
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		httputil.RespondWithInternalError(c, fmt.Sprintf("failed writing upload to disk: %v", err))
		return
	}
	tmp.Close()

	lib, parseErr := itunes.ParseITL(tmpPath)
	if parseErr != nil {
		httputil.RespondWithSuccess(c, http.StatusBadRequest, ITLUploadResponse{
			Valid: false,
			Error: fmt.Sprintf("invalid ITL file: %v", parseErr),
		})
		return
	}

	resp := ITLUploadResponse{
		Valid:     true,
		Tracks:    len(lib.Tracks),
		Playlists: len(lib.Playlists),
		Version:   lib.Version,
	}

	install := c.Query("install") == "true"
	if !install {
		httputil.RespondWithOK(c, resp)
		return
	}

	if err := backupITLFile(itlPath); err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("failed to back up current ITL: %v", err))
		return
	}

	if err := os.Rename(tmpPath, itlPath); err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("failed to install uploaded ITL: %v", err))
		return
	}

	resp.Installed = true
	httputil.RespondWithOK(c, resp)
}

// ITLBackupEntry describes a single .bak-* ITL backup file.
type ITLBackupEntry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Timestamp time.Time `json:"timestamp"`
}

// HandleBackupList returns all .bak-* backups of the ITL file,
// sorted newest-first.
//
// GET /api/v1/itunes/library/backups
func (t *TransferService) HandleBackupList(c *gin.Context) {
	itlPath := config.AppConfig.ITunes.LibraryWritePath
	if itlPath == "" {
		httputil.RespondWithBadRequest(c, "ITunesLibraryWritePath is not configured")
		return
	}

	dir := filepath.Dir(itlPath)
	base := filepath.Base(itlPath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("cannot read directory: %v", err))
		return
	}

	var backups []ITLBackupEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, base+".bak-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, ITLBackupEntry{
			Name:      name,
			Size:      info.Size(),
			Timestamp: info.ModTime(),
		})
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	httputil.RespondWithOK(c, gin.H{
		"backups": backups,
		"count":   len(backups),
	})
}

// ITLRestoreRequest specifies which backup to restore.
type ITLRestoreRequest struct {
	BackupName string `json:"backup_name" binding:"required"`
}

// HandleRestore restores a named backup as the active ITL file.
//
// POST /api/v1/itunes/library/restore
func (t *TransferService) HandleRestore(c *gin.Context) {
	itlPath := config.AppConfig.ITunes.LibraryWritePath
	if itlPath == "" {
		httputil.RespondWithBadRequest(c, "ITunesLibraryWritePath is not configured")
		return
	}

	var req ITLRestoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("invalid request: %v", err))
		return
	}

	if filepath.Base(req.BackupName) != req.BackupName {
		httputil.RespondWithBadRequest(c, "backup_name must be a filename, not a path")
		return
	}

	dir := filepath.Dir(itlPath)
	base := filepath.Base(itlPath)
	backupPath := filepath.Join(dir, req.BackupName)

	if !strings.HasPrefix(req.BackupName, base+".bak-") {
		httputil.RespondWithBadRequest(c, "not a recognized ITL backup file")
		return
	}

	lib, err := itunes.ParseITL(backupPath)
	if err != nil {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("backup file is invalid: %v", err))
		return
	}

	if err := backupITLFile(itlPath); err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("failed to back up current ITL before restore: %v", err))
		return
	}

	if err := copyFile(backupPath, itlPath); err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("failed to restore backup: %v", err))
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"restored":  true,
		"tracks":    len(lib.Tracks),
		"playlists": len(lib.Playlists),
		"version":   lib.Version,
	})
}

// backupITLFile creates a timestamped .bak-* copy of the given path.
func backupITLFile(itlPath string) error {
	if _, err := os.Stat(itlPath); os.IsNotExist(err) {
		return nil
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	backupPath := itlPath + ".bak-" + ts
	return copyFile(itlPath, backupPath)
}

// copyFile copies src to dst using a temp-write + rename for atomicity.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".itl-copy-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()

	if err := os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
