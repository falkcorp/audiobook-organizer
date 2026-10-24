// file: internal/server/master_list_handler.go
// version: 1.2.0
// guid: 7d1e8a40-2b63-4c59-9f0a-5e3c6b7d8a12
// last-edited: 2026-10-24

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
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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
	// PrunedLists are the ids of the uploader's older lists this upload's
	// retention removed (master_list_retention lists are kept per uploader).
	PrunedLists []string `json:"pruned_list_ids"`
	// PruneError is set when retention could not finish: the upload itself
	// succeeded and is stored, the listed ids were removed before the failure.
	PruneError string `json:"prune_error,omitempty"`
}

// masterListRetention is how many lists an uploader keeps: the configured
// master_list_retention, or the default when it is unset (0). A negative value
// is refused by config validation; it is treated as unset here too, so a bad
// stored setting can never turn retention into "delete everything".
func masterListRetention() int {
	if n := config.AppConfig.MasterListRetention; n > 0 {
		return n
	}
	return database.DefaultMasterListRetention
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
	resp.PrunedLists = []string{}
	// Retention runs after the new list is durably stored, so a failure here
	// can never cost the upload. It touches lists only: read-elsewhere records
	// are the import's output, are not retained by list, and are never pruned.
	pruned, perr := store.PruneLists(stored.UploadedBy, masterListRetention(), stored.ID)
	if pruned != nil {
		resp.PrunedLists = pruned
	}
	if perr != nil {
		resp.PruneError = perr.Error()
		logger.New("master-list").Warn("master list retention stopped early: list=%s pruned=%d err=%s",
			logger.SanitizeLogValue(stored.ID), len(pruned), logger.SanitizeLogValue(perr.Error()))
	}
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

// deleteMasterList removes one stored list and its rows. Read-elsewhere records
// an import made from it are the import's output, not part of the list, and are
// kept. A pending Repairs apply of a plan made from the list is unaffected: the
// plan rows carry the list rows they were planned from.
func (s *Server) deleteMasterList(c *gin.Context) {
	store := database.NewMasterListStoreFromStore(s.storeForWiring())
	if store == nil {
		httputil.RespondWithError(c, http.StatusServiceUnavailable, "this database cannot keep master lists", "MASTER_LIST_UNAVAILABLE")
		return
	}
	id := c.Param("id")
	if err := store.DeleteList(id); err != nil {
		if errors.Is(err, database.ErrMasterListNotFound) {
			httputil.RespondWithNotFound(c, "master list", id)
			return
		}
		httputil.RespondWithInternalError(c, "delete the master list: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// masterListElsewhereDefaultLimit and masterListElsewhereMaxLimit bound one page
// of GET /master-lists/elsewhere: the records are the import's output for the
// whole library and grow with every list.
const (
	masterListElsewhereDefaultLimit = 200
	masterListElsewhereMaxLimit     = 1000
)

// listMasterListElsewhere answers the read-elsewhere records the master-list
// import has made (rows of a list that match no library book), newest first;
// ?user_id= narrows them to one user. It pages: ?limit= (default 200, at most
// 1000) and ?offset=; "total" is the number of records before paging, "count"
// the number in this page.
func (s *Server) listMasterListElsewhere(c *gin.Context) {
	store := database.NewMasterListStoreFromStore(s.storeForWiring())
	if store == nil {
		httputil.RespondWithError(c, http.StatusServiceUnavailable, "this database cannot keep master lists", "MASTER_LIST_UNAVAILABLE")
		return
	}
	records, err := store.ListElsewhere(strings.TrimSpace(c.Query("user_id")))
	if err != nil {
		httputil.RespondWithInternalError(c, "list read-elsewhere records: "+err.Error())
		return
	}
	total := len(records)
	limit := httputil.ParseQueryInt(c, "limit", masterListElsewhereDefaultLimit)
	if limit < 1 {
		limit = masterListElsewhereDefaultLimit
	}
	limit = min(limit, masterListElsewhereMaxLimit)
	offset := min(max(httputil.ParseQueryInt(c, "offset", 0), 0), total)
	page := records[offset:min(offset+limit, total)]
	if page == nil {
		page = []database.MasterListElsewhere{}
	}
	c.JSON(http.StatusOK, gin.H{"records": page, "count": len(page), "total": total, "limit": limit, "offset": offset})
}
