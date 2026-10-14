// file: internal/server/handlers/diagnostics_traces.go
// version: 1.1.0
// guid: 3400e7bc-d8d4-41c4-a13a-2f173b0fb736
// last-edited: 2026-10-14

package handlers

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"strings"

	diagflight "github.com/falkcorp/audiobook-organizer/internal/diag/flight"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// TraceStore is what the traces endpoints need from the runtime flight
// recorder: list the snapshots and open one by its bare file name. Open must
// reject anything that is not a snapshot name with fs.ErrNotExist.
// *diagflight.Recorder satisfies it.
type TraceStore interface {
	List() ([]diagflight.FileInfo, error)
	Open(name string) (fs.File, error)
}

// DiagnosticsTracesHandler serves the flight recorder's snapshots to admins:
// GET /diagnostics/traces and GET /diagnostics/traces/:name.
type DiagnosticsTracesHandler struct {
	traces TraceStore
}

// NewDiagnosticsTracesHandler builds the handler. A nil store lists nothing
// and downloads nothing.
func NewDiagnosticsTracesHandler(traces TraceStore) *DiagnosticsTracesHandler {
	return &DiagnosticsTracesHandler{traces: traces}
}

// List answers {"data": {"traces": [{name, size, modified}]}}, newest first.
// A snapshot is two entries: the runtime trace and its goroutine dump.
func (h *DiagnosticsTracesHandler) List(c *gin.Context) {
	items := []diagflight.FileInfo{}
	if h.traces != nil {
		got, err := h.traces.List()
		if err != nil {
			httputil.InternalError(c, "failed to list traces", err)
			return
		}
		items = append(items, got...)
	}
	httputil.RespondWithOK(c, gin.H{"traces": items})
}

// Download streams one snapshot file as an attachment: the runtime trace as
// application/octet-stream, the goroutine dump as text. An unknown or
// malformed name is a 404 whose message never repeats the name.
func (h *DiagnosticsTracesHandler) Download(c *gin.Context) {
	if h.traces == nil {
		httputil.RespondWithNotFound(c, "trace", "")
		return
	}
	f, err := h.traces.Open(c.Param("name"))
	if errors.Is(err, fs.ErrNotExist) {
		httputil.RespondWithNotFound(c, "trace", "")
		return
	}
	if err != nil {
		httputil.InternalError(c, "failed to open trace", err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		httputil.InternalError(c, "failed to read trace", err)
		return
	}
	// FormatMediaType quotes the name; the store only opens snapshot names,
	// so there is nothing to escape in practice, but the header does not
	// depend on that.
	contentType := "application/octet-stream" // a runtime trace
	if strings.HasSuffix(info.Name(), ".goroutines.txt") {
		contentType = "text/plain; charset=utf-8"
	}
	c.DataFromReader(http.StatusOK, info.Size(), contentType, f, map[string]string{
		"Content-Disposition": mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}),
	})
}
