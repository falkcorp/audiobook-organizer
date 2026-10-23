// file: internal/server/master_list_handler_test.go
// version: 1.0.0
// guid: 2f8b4c61-9d30-4a7e-8c15-6b0e3d9a7f24
// last-edited: 2026-10-23

package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/masterlist"
)

// Synthetic list: the ISBNs are the standard valid-checksum examples, the ASINs
// are fake. Line 3 carries a bad ISBN (checksum), line 4 names no book.
const masterListTestCSV = "title,author,asin,isbn13,status\n" +
	"Synthetic Tale One,Author A,B000000001,9780306406157,finished\n" +
	"Synthetic Tale Two,Author B,B000000002,9780306406158,in_progress\n" +
	",,,,finished\n"

func masterListMultipart(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = fw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

func (f *credGuardFixture) postMasterList(t *testing.T, bearer string, body *bytes.Buffer, contentType, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/master-lists"+query, body)
	req.Header.Set("Content-Type", contentType)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	return w
}

func masterListRowsOf(t *testing.T, f *credGuardFixture, id string) []masterlist.Row {
	t.Helper()
	store := database.NewMasterListStore(f.store.DB())
	var rows []masterlist.Row
	require.NoError(t, store.ScanRows(id, func(_ int, raw []byte) error {
		var r masterlist.Row
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	}))
	return rows
}

func TestMasterListUpload_AdminStoresRowsWithProvenance(t *testing.T) {
	f := setupCredGuardServer(t)
	body, ct := masterListMultipart(t, "my list.csv", masterListTestCSV)
	w := f.postMasterList(t, f.sessionToken, body, ct, "")
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp masterListUploadResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 3, resp.List.RowCount)
	assert.Equal(t, "csv", resp.List.Format)
	assert.Equal(t, "my list.csv", resp.List.Filename)
	assert.Equal(t, f.adminID, resp.List.UploadedBy)
	assert.Len(t, resp.List.SHA256, 64)
	assert.Equal(t, int64(len(masterListTestCSV)), resp.List.Bytes)
	assert.Equal(t, 1, resp.Issues[masterlist.IssueBadIdentifier], "the checksum-failing ISBN is a finding on its row: %v", resp.Issues)
	assert.Equal(t, 1, resp.RowsWithIssues)
	assert.Equal(t, 1, resp.RowsWithoutIdentity)

	rows := masterListRowsOf(t, f, resp.List.ID)
	require.Len(t, rows, 3)
	assert.Equal(t, 2, rows[0].Line, "line numbers are the file's, the header being line 1")
	assert.Equal(t, "B000000001", rows[0].ASIN)
	assert.Equal(t, "9780306406157", rows[0].ISBN13)
	assert.Empty(t, rows[1].ISBN13, "an invalid ISBN is dropped from the row")
	assert.True(t, rows[1].HasIssue(masterlist.IssueBadIdentifier))

	// And the listing.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/master-lists", nil)
	req.Header.Set("Authorization", "Bearer "+f.sessionToken)
	lw := httptest.NewRecorder()
	f.srv.router.ServeHTTP(lw, req)
	require.Equal(t, http.StatusOK, lw.Code, lw.Body.String())
	var listing struct {
		Lists []database.MasterListMeta `json:"lists"`
	}
	require.NoError(t, json.Unmarshal(lw.Body.Bytes(), &listing))
	require.Len(t, listing.Lists, 1)
	assert.Equal(t, resp.List.ID, listing.Lists[0].ID)
}

func TestMasterListUpload_RawJSONBodyAndFormatSniffing(t *testing.T) {
	f := setupCredGuardServer(t)
	js := `{"rows":[{"title":"Synthetic Tale","authors":["Author A"],"asin":"B000000003","status":"finished"}]}`
	w := f.postMasterList(t, f.sessionToken, bytes.NewBufferString(js), "application/octet-stream", "?filename=export")
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp masterListUploadResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "json", resp.List.Format, "a body starting with { is JSON")
	assert.Equal(t, "export", resp.List.Filename)
	assert.Equal(t, 1, resp.List.RowCount)
}

func TestMasterListUpload_RefusesBadUploadsAndStoresNothing(t *testing.T) {
	f := setupCredGuardServer(t)
	cases := map[string]struct {
		body, ct, query string
	}{
		"no known column": {"foo,bar\n1,2\n", "text/csv", ""},
		"header only":     {"title,asin\n", "text/csv", ""},
		"empty":           {"  \n", "text/csv", ""},
		"bad format":      {masterListTestCSV, "text/csv", "?format=xml"},
		"broken json":     {"[{", "application/json", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := f.postMasterList(t, f.sessionToken, bytes.NewBufferString(tc.body), tc.ct, tc.query)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
	lists, err := database.NewMasterListStore(f.store.DB()).ListLists()
	require.NoError(t, err)
	assert.Empty(t, lists, "a refused upload stores nothing")
}

func TestMasterListUpload_OversizedBodyIsRefusedBeforeParsing(t *testing.T) {
	f := setupCredGuardServer(t)
	config.AppConfig.UploadBodyLimitMB = 1
	big := "title,asin\n" + strings.Repeat("Synthetic Tale,B000000001\n", 60000) // ~1.5 MB
	w := f.postMasterList(t, f.sessionToken, bytes.NewBufferString(big), "text/csv", "")
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())

	body, ct := masterListMultipart(t, "big.csv", big)
	w = f.postMasterList(t, f.sessionToken, body, ct, "")
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "the multipart route is capped as well: %s", w.Body.String())

	lists, err := database.NewMasterListStore(f.store.DB()).ListLists()
	require.NoError(t, err)
	assert.Empty(t, lists)
}

func TestMasterListUpload_RowCapIsAFileError(t *testing.T) {
	f := setupCredGuardServer(t)
	config.AppConfig.UploadBodyLimitMB = 10
	var sb strings.Builder
	sb.WriteString("asin\n")
	for i := 0; i <= masterlist.MaxRows; i++ {
		sb.WriteString("B0")
		sb.WriteString(strings.Repeat("0", 8-len(strconv.Itoa(i))))
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	w := f.postMasterList(t, f.sessionToken, bytes.NewBufferString(sb.String()), "text/csv", "")
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "more than")
}

func TestMasterListUpload_AuthAndRole(t *testing.T) {
	f := setupCredGuardServer(t)

	// Unauthenticated: 401, nothing stored.
	w := f.postMasterList(t, "", bytes.NewBufferString(masterListTestCSV), "text/csv", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())

	// A signed-in non-admin: 403.
	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	sess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)
	w = f.postMasterList(t, sess.ID, bytes.NewBufferString(masterListTestCSV), "text/csv", "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/master-lists", nil)
	req.Header.Set("Authorization", "Bearer "+sess.ID)
	gw := httptest.NewRecorder()
	f.srv.router.ServeHTTP(gw, req)
	assert.Equal(t, http.StatusForbidden, gw.Code, "the listing is admin-only too")

	// An admin API key is accepted: the upload is not a credential change.
	w = f.postMasterList(t, f.apiKey, bytes.NewBufferString(masterListTestCSV), "text/csv", "")
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	lists, err := database.NewMasterListStore(f.store.DB()).ListLists()
	require.NoError(t, err)
	assert.Len(t, lists, 1, "only the admin key's upload was stored")
}
