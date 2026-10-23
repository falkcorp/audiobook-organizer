// file: internal/server/master_list_handler.go
// version: 1.0.0
// guid: 7d1e8a40-2b63-4c59-9f0a-5e3c6b7d8a12
// last-edited: 2026-10-23

package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/masterlist"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// masterListDefaultMaxBytes is the upload cap when upload_body_limit_mb is
// unset: a personal list is a few hundred KB, so 10 MB is generous.
const masterListDefaultMaxBytes = 10 << 20

// masterListMaxBytes is the most an upload may be: the upload class limit, the
// same as the middleware applies to this path (selectBodyLimit). The handler
// enforces it again so the cap does not depend on the middleware being mounted.
func masterListMaxBytes() int64 {
	if mb := config.AppConfig.UploadBodyLimitMB; mb > 0 {
		return int64(mb) << 20
	}
	return masterListDefaultMaxBytes
}

// masterListUploadResponse is what an upload answers.
type masterListUploadResponse struct {
	List database.MasterListMeta `json:"list"`
	// Issues counts the row findings by code (a bad ISBN dropped, a time with
	// no zone, ...). The rows are kept; the plan flags them.
	Issues map[string]int `json:"issues"`
	// RowsWithIssues is how many rows carry at least one finding.
	RowsWithIssues int `json:"rows_with_issues"`
	// RowsWithoutIdentity is how many rows name no book at all (no title, no
	// valid identifier, no source id); the plan lists them as invalid.
	RowsWithoutIdentity int `json:"rows_without_identity"`
}

// masterListFormat picks the upload's format: the explicit format field, the
// file name's extension, the content type, then the first byte of the body.
func masterListFormat(explicit, filename, contentType string, data []byte) (string, error) {
	switch f := strings.ToLower(strings.TrimSpace(explicit)); f {
	case masterlist.FormatCSV, masterlist.FormatJSON:
		return f, nil
	case "":
	default:
		return "", fmt.Errorf("format %q is not csv or json", explicit)
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".csv":
		return masterlist.FormatCSV, nil
	case ".json":
		return masterlist.FormatJSON, nil
	}
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "csv"):
		return masterlist.FormatCSV, nil
	case strings.Contains(ct, "json"):
		return masterlist.FormatJSON, nil
	}
	if t := bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), " \t\r\n"); len(t) > 0 && (t[0] == '[' || t[0] == '{') {
		return masterlist.FormatJSON, nil
	}
	return masterlist.FormatCSV, nil
}

// uploadMasterList stores an uploaded master list (CSV or JSON): a multipart
// "file" field, or the raw request body. It validates every row (a bad
// identifier is dropped from the row and recorded as a finding on it) and
// stores the rows; nothing reads a book or a user, and nothing is written but
// the list. The Repairs plan master-list-import reads it by the returned id.
func (s *Server) uploadMasterList(c *gin.Context) {
	store := database.NewMasterListStoreFromStore(s.storeForWiring())
	if store == nil {
		httputil.RespondWithError(c, http.StatusServiceUnavailable, "this database cannot keep master lists", "MASTER_LIST_UNAVAILABLE")
		return
	}
	limit := masterListMaxBytes()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)

	var (
		data        []byte
		filename    string
		contentType = c.GetHeader("Content-Type")
		format      string
		err         error
	)
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		fh, ferr := c.FormFile("file")
		if ferr != nil {
			respondMasterListReadError(c, ferr, limit, "multipart field \"file\" is required")
			return
		}
		f, oerr := fh.Open()
		if oerr != nil {
			httputil.RespondWithInternalError(c, "open the uploaded file: "+oerr.Error())
			return
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, limit+1))
		filename, contentType, format = fh.Filename, fh.Header.Get("Content-Type"), c.PostForm("format")
	} else {
		data, err = io.ReadAll(c.Request.Body)
		format = c.Query("format")
		filename = c.Query("filename")
	}
	if err != nil {
		respondMasterListReadError(c, err, limit, "read the upload")
		return
	}
	if int64(len(data)) > limit {
		httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, fmt.Sprintf("the list is larger than %d bytes", limit), "REQUEST_TOO_LARGE")
		return
	}
	if len(bytes.TrimSpace(data)) == 0 {
		httputil.RespondWithBadRequest(c, "the upload is empty")
		return
	}
	format, err = masterListFormat(format, filename, contentType, data)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	rows, err := masterlist.Parse(format, bytes.NewReader(data))
	if err != nil {
		// Every parse error is the file's: an empty list, no known column, too
		// many rows, or text that is not the format.
		httputil.RespondWithError(c, http.StatusBadRequest, err.Error(), "MASTER_LIST_INVALID")
		return
	}
	raw := make([][]byte, len(rows))
	resp := masterListUploadResponse{Issues: map[string]int{}}
	for i := range rows {
		b, merr := json.Marshal(rows[i])
		if merr != nil {
			httputil.RespondWithInternalError(c, fmt.Sprintf("encode row %d: %v", rows[i].Line, merr))
			return
		}
		raw[i] = b
		if len(rows[i].Issues) > 0 {
			resp.RowsWithIssues++
		}
		for _, is := range rows[i].Issues {
			resp.Issues[is.Code]++
		}
		if !rows[i].HasIdentity() {
			resp.RowsWithoutIdentity++
		}
	}
	sum := sha256.Sum256(data)
	meta := database.MasterListMeta{
		Filename: masterListDisplayName(filename),
		Format:   format,
		Bytes:    int64(len(data)),
		SHA256:   hex.EncodeToString(sum[:]),
	}
	if u, ok := servermiddleware.CurrentUser(c); ok {
		meta.UploadedBy = u.ID
	}
	stored, err := store.PutList(meta, raw)
	if err != nil {
		httputil.RespondWithInternalError(c, "store the list: "+err.Error())
		return
	}
	resp.List = stored
	c.JSON(http.StatusCreated, resp)
}

// masterListDisplayName is the uploaded file's base name for display: the
// client chose it, so it is cut to a bounded length and never a path.
func masterListDisplayName(filename string) string {
	if filename == "" {
		return ""
	}
	name := filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if r := []rune(name); len(r) > 255 {
		name = string(r[:255])
	}
	return name
}

// respondMasterListReadError answers a failed body read: 413 for the size cap,
// 400 for anything else the client sent wrong.
func respondMasterListReadError(c *gin.Context, err error, limit int64, what string) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, fmt.Sprintf("the list is larger than %d bytes", limit), "REQUEST_TOO_LARGE")
		return
	}
	httputil.RespondWithBadRequest(c, what+": "+err.Error())
}

// listMasterLists answers the stored lists' metadata, newest first.
func (s *Server) listMasterLists(c *gin.Context) {
	store := database.NewMasterListStoreFromStore(s.storeForWiring())
	if store == nil {
		httputil.RespondWithError(c, http.StatusServiceUnavailable, "this database cannot keep master lists", "MASTER_LIST_UNAVAILABLE")
		return
	}
	lists, err := store.ListLists()
	if err != nil {
		httputil.RespondWithInternalError(c, "list master lists: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"lists": lists})
}
