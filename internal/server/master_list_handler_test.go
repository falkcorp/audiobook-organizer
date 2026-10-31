// file: internal/server/master_list_handler_test.go
// version: 1.4.1
// guid: 2f8b4c61-9d30-4a7e-8c15-6b0e3d9a7f24
// last-edited: 2026-10-30

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
	sess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword, 0)
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

// RequireAdmin reads the signed-in user's role, not the credential's scopes, so
// an admin's API key narrowed to other permissions would pass it alone. The
// routes also demand users.manage, which a key's scopes can withhold.
func TestMasterListRoutes_APIKeyScopesNarrow(t *testing.T) {
	f := setupCredGuardServer(t)
	exp := time.Now().Add(time.Hour)
	keyWith := func(name string, scopes ...auth.Permission) string {
		t.Helper()
		raw, hash, err := database.GenerateAPIKeyToken()
		require.NoError(t, err)
		strs := make([]string, len(scopes))
		for i, sc := range scopes {
			strs[i] = string(sc)
		}
		_, err = f.store.CreateAPIKey(&database.APIKey{
			UserID: f.adminID, Name: name, TokenHash: hash, Scopes: strs,
			Status: "active", CreatedAt: time.Now(), ExpiresAt: &exp,
		})
		require.NoError(t, err)
		return raw
	}
	get := func(bearer string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/master-lists", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		f.srv.router.ServeHTTP(w, req)
		return w.Code
	}

	for name, scopes := range map[string][]auth.Permission{
		"view only": {auth.PermLibraryView},
		"editor":    {auth.PermLibraryView, auth.PermLibraryEditMetadata},
	} {
		key := keyWith(name, scopes...)
		w := f.postMasterList(t, key, bytes.NewBufferString(masterListTestCSV), "text/csv", "")
		assert.Equal(t, http.StatusForbidden, w.Code, "%s: %s", name, w.Body.String())
		assert.Equal(t, http.StatusForbidden, get(key), "%s: the listing is gated too", name)
	}
	lists, err := database.NewMasterListStore(f.store.DB()).ListLists()
	require.NoError(t, err)
	assert.Empty(t, lists, "a refused key stores nothing")

	key := keyWith("users", auth.PermUsersManage)
	w := f.postMasterList(t, key, bytes.NewBufferString(masterListTestCSV), "text/csv", "")
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, http.StatusOK, get(key))
}

func (f *credGuardFixture) doMasterList(t *testing.T, method, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	return w
}

func (f *credGuardFixture) uploadMasterListID(t *testing.T) masterListUploadResponse {
	t.Helper()
	w := f.postMasterList(t, f.sessionToken, bytes.NewBufferString(masterListTestCSV), "text/csv", "")
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp masterListUploadResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// List ids order by upload time, then by id: keep two uploads apart.
	time.Sleep(3 * time.Millisecond)
	return resp
}

func masterListIDs(t *testing.T, f *credGuardFixture) []string {
	t.Helper()
	lists, err := database.NewMasterListStore(f.store.DB()).ListLists()
	require.NoError(t, err)
	ids := make([]string, len(lists))
	for i, l := range lists {
		ids[i] = l.ID
	}
	return ids
}

// An upload prunes the uploader's oldest lists beyond master_list_retention,
// never another uploader's lists and never read-elsewhere records.
func TestMasterListUpload_RetentionPrunesTheUploadersOldestLists(t *testing.T) {
	f := setupCredGuardServer(t)
	prev := config.AppConfig.MasterListRetention
	config.AppConfig.MasterListRetention = 2
	t.Cleanup(func() { config.AppConfig.MasterListRetention = prev })

	st := database.NewMasterListStore(f.store.DB())
	other, err := st.PutList(database.MasterListMeta{Filename: "other.csv", Format: masterlist.FormatCSV, UploadedBy: "someone-else"},
		[][]byte{[]byte(`{"line":2,"title":"Synthetic Tale"}`)})
	require.NoError(t, err)
	require.NoError(t, st.CreateElsewhere(database.MasterListElsewhere{ID: database.ElsewhereID("u1", "asin:B000000001"), UserID: "u1", ListID: "gone", Title: "Kept"}))

	first := f.uploadMasterListID(t)
	assert.Empty(t, first.PrunedLists)
	assert.NotNil(t, first.PrunedLists, "the field is always a list")
	second := f.uploadMasterListID(t)
	assert.Empty(t, second.PrunedLists)
	third := f.uploadMasterListID(t)
	assert.Equal(t, []string{first.List.ID}, third.PrunedLists, "the oldest of the uploader's lists went")
	assert.Empty(t, third.PruneError)
	fourth := f.uploadMasterListID(t)
	assert.Equal(t, []string{second.List.ID}, fourth.PrunedLists)

	assert.ElementsMatch(t, []string{other.ID, third.List.ID, fourth.List.ID}, masterListIDs(t, f),
		"two of the uploader's lists and the other uploader's one")
	recs, err := st.ListElsewhere("")
	require.NoError(t, err)
	assert.Len(t, recs, 1, "retention never touches read-elsewhere records")
}

// A retention that is unset or invalid falls back to the default (10): it can
// never become "keep nothing" and prune the list just stored. Uploading past
// the default shows the fallback is the number that is applied: ten lists stay,
// the two oldest go.
func TestMasterListUpload_RetentionDefaultsWhenUnsetOrInvalid(t *testing.T) {
	for _, n := range []int{0, -5} {
		f := setupCredGuardServer(t)
		prev := config.AppConfig.MasterListRetention
		config.AppConfig.MasterListRetention = n
		t.Cleanup(func() { config.AppConfig.MasterListRetention = prev })

		require.Equal(t, database.DefaultMasterListRetention, masterListRetention(), "retention %d", n)

		var ids []string
		for i := 0; i < database.DefaultMasterListRetention+2; i++ {
			resp := f.uploadMasterListID(t)
			assert.Empty(t, resp.PruneError, "retention %d, upload %d", n, i)
			ids = append(ids, resp.List.ID)
		}
		remaining := masterListIDs(t, f)
		assert.Len(t, remaining, database.DefaultMasterListRetention, "retention %d keeps the default, not 0 and not everything", n)
		assert.NotContains(t, remaining, ids[0], "retention %d: the oldest went", n)
		assert.NotContains(t, remaining, ids[1], "retention %d: the second oldest went", n)
		assert.Contains(t, remaining, ids[len(ids)-1], "retention %d: the list just stored stays", n)
		config.AppConfig.MasterListRetention = prev
	}
}

func TestMasterListDelete_RemovesTheListAndItsRows(t *testing.T) {
	f := setupCredGuardServer(t)
	keep := f.uploadMasterListID(t)
	drop := f.uploadMasterListID(t)
	st := database.NewMasterListStore(f.store.DB())
	require.NoError(t, st.CreateElsewhere(database.MasterListElsewhere{ID: database.ElsewhereID("u1", "asin:B000000001"), UserID: "u1", ListID: drop.List.ID, Title: "Kept"}))

	w := f.doMasterList(t, http.MethodDelete, "/api/v1/master-lists/"+drop.List.ID, f.sessionToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"deleted":"`+drop.List.ID+`"}`, w.Body.String())
	assert.Equal(t, []string{keep.List.ID}, masterListIDs(t, f))
	assert.ErrorIs(t, st.ScanRows(drop.List.ID, func(int, []byte) error { return nil }), database.ErrMasterListNotFound)
	kvs, err := f.store.ScanPrefix("mlist:row:" + drop.List.ID)
	require.NoError(t, err)
	assert.Empty(t, kvs, "the rows went with the list")
	recs, err := st.ListElsewhere("")
	require.NoError(t, err)
	assert.Len(t, recs, 1, "records made from the list are the import's output and stay")
	rows := masterListRowsOf(t, f, keep.List.ID)
	assert.Len(t, rows, 3, "the other list is intact")

	w = f.doMasterList(t, http.MethodDelete, "/api/v1/master-lists/"+drop.List.ID, f.sessionToken)
	assert.Equal(t, http.StatusNotFound, w.Code, "a second delete: %s", w.Body.String())
	w = f.doMasterList(t, http.MethodDelete, "/api/v1/master-lists/no-such-list", f.sessionToken)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestMasterListDeleteAndElsewhere_AdminOnlyAndScoped(t *testing.T) {
	f := setupCredGuardServer(t)
	up := f.uploadMasterListID(t)
	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	sess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword, 0)
	require.NoError(t, err)

	exp := time.Now().Add(time.Hour)
	keyWith := func(scope auth.Permission) string {
		raw, hash, err := database.GenerateAPIKeyToken()
		require.NoError(t, err)
		_, err = f.store.CreateAPIKey(&database.APIKey{UserID: f.adminID, Name: string(scope), TokenHash: hash, Scopes: []string{string(scope)},
			Status: "active", CreatedAt: time.Now(), ExpiresAt: &exp})
		require.NoError(t, err)
		return raw
	}
	for name, bearer := range map[string]string{"anonymous": "", "viewer": sess.ID, "view-only key": keyWith(auth.PermLibraryView)} {
		want := http.StatusForbidden
		if bearer == "" {
			want = http.StatusUnauthorized
		}
		w := f.doMasterList(t, http.MethodDelete, "/api/v1/master-lists/"+up.List.ID, bearer)
		assert.Equal(t, want, w.Code, "delete as %s: %s", name, w.Body.String())
		w = f.doMasterList(t, http.MethodGet, "/api/v1/master-lists/elsewhere", bearer)
		assert.Equal(t, want, w.Code, "elsewhere as %s: %s", name, w.Body.String())
	}
	assert.Equal(t, []string{up.List.ID}, masterListIDs(t, f), "no refused caller deleted it")

	// users.manage is what the routes demand.
	w := f.doMasterList(t, http.MethodGet, "/api/v1/master-lists/elsewhere", keyWith(auth.PermUsersManage))
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestMasterListElsewhere_ListsRecordsNewestFirstAndFiltersByUser(t *testing.T) {
	f := setupCredGuardServer(t)
	w := f.doMasterList(t, http.MethodGet, "/api/v1/master-lists/elsewhere", f.sessionToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"records":[],"count":0,"total":0,"limit":200,"offset":0}`, w.Body.String(), "empty is an empty list, not null")

	st := database.NewMasterListStore(f.store.DB())
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i, rec := range []database.MasterListElsewhere{
		{ID: database.ElsewhereID("u1", "asin:B000000001"), UserID: "u1", ListID: "l1", Title: "Older", CreatedAt: base},
		{ID: database.ElsewhereID("u1", "asin:B000000002"), UserID: "u1", ListID: "l1", Title: "Newer", CreatedAt: base.Add(time.Hour)},
		{ID: database.ElsewhereID("u2", "asin:B000000001"), UserID: "u2", ListID: "l1", Title: "Other user", CreatedAt: base.Add(2 * time.Hour)},
	} {
		require.NoError(t, st.CreateElsewhere(rec), i)
	}
	type listResp struct {
		Records []database.MasterListElsewhere `json:"records"`
		Count   int                            `json:"count"`
		Total   int                            `json:"total"`
	}
	get := func(q string) listResp {
		w := f.doMasterList(t, http.MethodGet, "/api/v1/master-lists/elsewhere"+q, f.sessionToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var r listResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &r))
		return r
	}
	all := get("")
	require.Equal(t, 3, all.Count)
	assert.Equal(t, []string{"Other user", "Newer", "Older"}, []string{all.Records[0].Title, all.Records[1].Title, all.Records[2].Title})
	u1 := get("?user_id=u1")
	require.Equal(t, 2, u1.Count)
	assert.Equal(t, "Newer", u1.Records[0].Title)
	assert.Equal(t, 0, get("?user_id=nobody").Count)

	// Paged: count is the page, total is everything the filter matched.
	p1 := get("?limit=2")
	require.Equal(t, 2, p1.Count)
	require.Equal(t, 3, p1.Total)
	assert.Equal(t, []string{"Other user", "Newer"}, []string{p1.Records[0].Title, p1.Records[1].Title})
	p2 := get("?limit=2&offset=2")
	require.Equal(t, 1, p2.Count)
	require.Equal(t, 3, p2.Total)
	assert.Equal(t, "Older", p2.Records[0].Title)
	past := get("?offset=99")
	assert.Equal(t, 0, past.Count, "an offset past the end is an empty page")
	assert.Equal(t, 3, past.Total)
	assert.NotNil(t, past.Records)
	assert.Equal(t, 3, get("?limit=0&offset=-5").Count, "a bad limit or offset falls back to the defaults")
}

// With authentication switched off the master-list routes are open, like the
// rest of the API in that mode: there is no user to hold users.manage, and the
// admin-role gate used to answer 401 to every call.
func TestMasterListRoutes_AuthDisabledIsOpen(t *testing.T) {
	f := setupCredGuardServerWith(t, func(c *config.Config) { c.EnableAuth = false })

	w := f.postMasterList(t, "", bytes.NewBufferString(masterListTestCSV), "text/csv", "")
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var up masterListUploadResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &up))

	w = f.doMasterList(t, http.MethodGet, "/api/v1/master-lists", "")
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = f.doMasterList(t, http.MethodGet, "/api/v1/master-lists/elsewhere", "")
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = f.doMasterList(t, http.MethodDelete, "/api/v1/master-lists/"+up.List.ID, "")
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, masterListIDs(t, f))
}

// Policy: lists are shared admin input, the plan accepts any list_id, so any
// admin holding users.manage may delete any list, not only their own upload.
// This pins that choice; a change to it should be deliberate.
func TestMasterListDelete_AnyAdminMayDeleteAnyList(t *testing.T) {
	f := setupCredGuardServer(t)
	up := f.uploadMasterListID(t)

	sess, err := f.store.CreateSessionWithOrigin(f.targetID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword, 0)
	require.NoError(t, err)
	w := f.doMasterList(t, http.MethodDelete, "/api/v1/master-lists/"+up.List.ID, sess.ID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, masterListIDs(t, f), "the second admin deleted the first admin's list")
}
