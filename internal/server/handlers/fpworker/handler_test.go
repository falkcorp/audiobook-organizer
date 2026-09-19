// file: internal/server/handlers/fpworker/handler_test.go
// version: 1.1.0
// guid: 1cf62cc0-1b49-43dd-a9f1-317ac56e7aa0
// last-edited: 2026-09-19

package fpworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
	"github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// fakeHub answers every call with err, or with a canned response.
type fakeHub struct {
	err     error
	lease   *workerapi.LeaseResponse
	results atomic.Int64
}

func (f *fakeHub) Hello(context.Context) (*workerapi.HelloResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &workerapi.HelloResponse{Pipeline: "p"}, nil
}
func (f *fakeHub) Lease(context.Context, workerapi.LeaseRequest) (*workerapi.LeaseResponse, error) {
	return f.lease, f.err
}
func (f *fakeHub) Renew(id string, _ workerapi.RenewRequest) (*workerapi.RenewResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &workerapi.RenewResponse{LeaseID: id}, nil
}
func (f *fakeHub) Release(string, workerapi.ReleaseRequest) (*workerapi.ReleaseResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &workerapi.ReleaseResponse{}, nil
}
func (f *fakeHub) Results(_ context.Context, req workerapi.ResultsRequest) (*workerapi.ResultsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.results.Add(1)
	return &workerapi.ResultsResponse{Statuses: []workerapi.JobStatus{}}, nil
}

func router(hub Hub, enabled bool, guards ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// The production /api/v1 body limit at its 1 MB JSON default, so the
	// cap is tested through the chain it really sits behind.
	api := r.Group("/api/v1", middleware.MaxRequestBodySize(1<<20, 10<<20))
	var h *Handler
	if hub == nil {
		h = New(nil, func() bool { return enabled })
	} else {
		h = New(hub, func() bool { return enabled })
	}
	h.Register(api, guards...)
	return r
}

func do(r http.Handler, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "192.0.2.10:5000"
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

var allRoutes = []struct{ method, path, body string }{
	{http.MethodGet, "/api/v1/fingerprint/worker/hello", ""},
	{http.MethodPost, "/api/v1/fingerprint/worker/lease", `{"worker_id":"w1"}`},
	{http.MethodPost, "/api/v1/fingerprint/worker/lease/L1/renew", `{"worker_id":"w1"}`},
	{http.MethodPost, "/api/v1/fingerprint/worker/lease/L1/release", ""},
	{http.MethodPost, "/api/v1/fingerprint/worker/results", `{"worker_id":"w1","results":[]}`},
}

func TestRoutes_404WhenFlagOff(t *testing.T) {
	r := router(&fakeHub{}, false)
	for _, rt := range allRoutes {
		w := do(r, rt.method, rt.path, rt.body)
		require.Equal(t, http.StatusNotFound, w.Code, "%s %s", rt.method, rt.path)
	}
}

func TestRoutes_503WithoutARunningOp(t *testing.T) {
	for name, hub := range map[string]Hub{"no plugin": nil, "op not running": &fakeHub{err: workerapi.ErrNoRun}} {
		r := router(hub, true)
		for _, rt := range allRoutes {
			w := do(r, rt.method, rt.path, rt.body)
			require.Equal(t, http.StatusServiceUnavailable, w.Code, "%s: %s %s", name, rt.method, rt.path)
		}
	}
}

func TestRoutes_StatusMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{workerapi.ErrLeaseGone, http.StatusGone},
		{fmt.Errorf("%w: fpcalc=1.0", workerapi.ErrToolsNotAllowed), http.StatusConflict},
		{workerapi.ErrTooManyLeases, http.StatusTooManyRequests},
		{fmt.Errorf("%w: worker_id", workerapi.ErrBadRequest), http.StatusBadRequest},
	}
	for _, c := range cases {
		r := router(&fakeHub{err: c.err}, true)
		w := do(r, http.MethodPost, "/api/v1/fingerprint/worker/lease", `{"worker_id":"w1"}`)
		require.Equal(t, c.want, w.Code, "%v", c.err)
	}
	r := router(&fakeHub{err: workerapi.ErrLeaseGone}, true)
	require.Equal(t, http.StatusGone, do(r, http.MethodPost, "/api/v1/fingerprint/worker/lease/L1/renew", `{"worker_id":"w1"}`).Code)
}

func TestLease_204WhenNothingLeaseable(t *testing.T) {
	r := router(&fakeHub{}, true)
	w := do(r, http.MethodPost, "/api/v1/fingerprint/worker/lease", `{"worker_id":"w1"}`)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Empty(t, w.Body.String())

	r = router(&fakeHub{lease: &workerapi.LeaseResponse{LeaseID: "L1"}}, true)
	w = do(r, http.MethodPost, "/api/v1/fingerprint/worker/lease", `{"worker_id":"w1"}`)
	require.Equal(t, http.StatusOK, w.Code)
	var got workerapi.LeaseResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "L1", got.LeaseID)
}

func TestRoutes_BadBodies(t *testing.T) {
	r := router(&fakeHub{}, true)
	require.Equal(t, http.StatusBadRequest, do(r, http.MethodPost, "/api/v1/fingerprint/worker/lease", "").Code)
	require.Equal(t, http.StatusBadRequest, do(r, http.MethodPost, "/api/v1/fingerprint/worker/results", "{nope").Code)
	require.Equal(t, http.StatusBadRequest, do(r, http.MethodPost, "/api/v1/fingerprint/worker/lease/L1/renew", "").Code,
		"renew must name the worker that holds the lease")
	require.Equal(t, http.StatusOK, do(r, http.MethodPost, "/api/v1/fingerprint/worker/lease/L1/release", "").Code,
		"an empty release body hands back the whole lease")
}

// resultsBody builds a syntactically valid results body of about n bytes.
func resultsBody(n int) string {
	pad := strings.Repeat("A", max(0, n-80))
	return `{"worker_id":"w1","results":[{"job_id":"j","ref":"f:x","outcome":"ok","error":"` + pad + `"}]}`
}

func TestResults_BodyCap8MB(t *testing.T) {
	hub := &fakeHub{}
	r := router(hub, true)
	// 2 MB: over the 1 MB JSON default of the /api/v1 group, under 8 MB.
	w := do(r, http.MethodPost, "/api/v1/fingerprint/worker/results", resultsBody(2<<20))
	require.Equal(t, http.StatusOK, w.Code, "the worker path must get the 8 MB class, not the 1 MB default")
	require.EqualValues(t, 1, hub.results.Load())

	w = do(r, http.MethodPost, "/api/v1/fingerprint/worker/results", resultsBody(9<<20))
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)

	// A chunked body with no declared length is cut off by the reader.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fingerprint/worker/results",
		io.MultiReader(strings.NewReader(resultsBody(9<<20))))
	req.ContentLength = -1
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.EqualValues(t, 1, hub.results.Load(), "an oversized body must never reach the hub")
}

// TestRoutes_PermissionAndScope drives the real auth middleware with API
// keys: a key scoped to fingerprint.worker passes, one scoped to anything
// else is 403, and a worker-scoped key cannot reach other admin routes.
func TestRoutes_PermissionAndScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, _, err = auth.SeedRoles(store)
	require.NoError(t, err)
	svc, err := store.CreateUser("fp-worker-svc", "svc@example.test", "bcrypt", "hash", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	mint := func(scopes ...string) string {
		raw, hash, err := database.GenerateAPIKeyToken()
		require.NoError(t, err)
		_, err = store.CreateAPIKey(&database.APIKey{UserID: svc.ID, Name: "k", TokenHash: hash, Scopes: scopes, Status: "active"})
		require.NoError(t, err)
		return raw
	}
	workerKey := mint(auth.PermFingerprintWorker)
	viewKey := mint(auth.PermLibraryView)

	r := router(&fakeHub{}, true, middleware.RequireAuth(store), middleware.RequirePermission(store, auth.PermFingerprintWorker))
	r.GET("/api/v1/users", middleware.RequireAuth(store), middleware.RequirePermission(store, auth.PermUsersManage),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	bearer := func(k string) []string { return []string{"Authorization", "Bearer " + k} }
	w := do(r, http.MethodGet, "/api/v1/fingerprint/worker/hello", "", bearer(workerKey)...)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = do(r, http.MethodGet, "/api/v1/fingerprint/worker/hello", "", bearer(viewKey)...)
	require.Equal(t, http.StatusForbidden, w.Code, "a key without the fingerprint.worker scope is refused")
	w = do(r, http.MethodGet, "/api/v1/fingerprint/worker/hello", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
	w = do(r, http.MethodGet, "/api/v1/users", "", bearer(workerKey)...)
	require.Equal(t, http.StatusForbidden, w.Code, "a worker-scoped key can do nothing else")
	require.NotContains(t, w.Body.String(), workerKey, "the key is never echoed")
}

// TestRoutes_FlagIsCheckedBeforeAuth: with the flag off an unauthenticated
// caller learns nothing (404, not 401).
func TestRoutes_FlagIsCheckedBeforeAuth(t *testing.T) {
	deny := func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }
	r := router(&fakeHub{}, false, deny)
	require.Equal(t, http.StatusNotFound, do(r, http.MethodGet, "/api/v1/fingerprint/worker/hello", "").Code)
	var buf bytes.Buffer
	buf.WriteString("{}")
	r = router(&fakeHub{}, true, deny)
	require.Equal(t, http.StatusUnauthorized, do(r, http.MethodPost, "/api/v1/fingerprint/worker/lease", buf.String()).Code)
}
