// file: internal/server/login_budget_isolation_test.go
// version: 1.1.0
// guid: 4d59a407-dbd7-4beb-9b1b-344f246c9fbd
// last-edited: 2026-10-22

package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// budgetFixture is the real server (production router and middleware order) with
// the ABS API on, behind no tunnel connector, so the visitor key is the peer's.
func budgetFixture(t *testing.T) *credGuardFixture {
	t.Helper()
	setTunnelConnectors(t, "")
	fx := setupCredGuardServerWith(t, func(c *config.Config) {
		c.ABSAPIEnabled = true
		c.ABSJWTSecret = "synthetic-throttle-test-secret-0123456789abcdef"
	})
	fx.srv.loginThrottle().SetSleep(func(time.Duration) {}) // the failure delay is not under test
	return fx
}

func budgetUser(t *testing.T, fx *credGuardFixture, name, pw string) {
	t.Helper()
	algo, hash, err := absauth.HashPassword(pw)
	require.NoError(t, err)
	_, err = fx.store.CreateUser(name, name+"@example.test", algo, hash, []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
}

func budgetPost(fx *credGuardFixture, path, remote string, hdr map[string]string, body any) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	fx.srv.router.ServeHTTP(w, req)
	return w
}

func budgetLogin(fx *credGuardFixture, path, remote, user, pw string) int {
	return budgetPost(fx, path, remote, nil, map[string]string{"username": user, "password": pw}).Code
}

func budgetRefresh(fx *credGuardFixture, remote string, n int) int {
	return budgetPost(fx, "/auth/refresh", remote, map[string]string{"x-refresh-token": fmt.Sprintf("dead-token-%d", n)}, nil).Code
}

// S1: dead ABS refresh tokens have a budget of their own. A client storming
// /auth/refresh (a stale session, or an attacker) must not lock the same
// visitor out of the WEB password login, and web mistypes must not block ABS
// token refresh.
func TestLoginBudget_DeadRefreshTokensDoNotSpendTheWebLoginBudget(t *testing.T) {
	fx := budgetFixture(t)
	budgetUser(t, fx, "budget-eve", "eve-pw")
	const v = "192.0.2.90:4000"
	for i := 0; i < absauth.MaxFailuresPerIP; i++ {
		require.Equal(t, http.StatusUnauthorized, budgetRefresh(fx, v, i), "refresh %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, budgetRefresh(fx, v, 99), "refresh past its own budget")
	require.Equal(t, http.StatusOK, budgetLogin(fx, "/api/v1/auth/login", v, "budget-eve", "eve-pw"),
		"the web login of a visitor whose refresh budget is spent is not refused")
}

func TestLoginBudget_WebTyposDoNotSpendTheRefreshBudget(t *testing.T) {
	fx := budgetFixture(t)
	budgetUser(t, fx, "budget-eve", "eve-pw")
	const v = "192.0.2.91:4000"
	for i := 0; i < absauth.MaxFailuresPerIP; i++ {
		require.Equal(t, http.StatusUnauthorized, budgetLogin(fx, "/api/v1/auth/login", v, fmt.Sprintf("typo%d", i), "x"), "typo %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, budgetLogin(fx, "/api/v1/auth/login", v, "budget-eve", "eve-pw"), "web login past its budget")
	require.Equal(t, http.StatusUnauthorized, budgetRefresh(fx, v, 0), "refresh by a visitor whose web login budget is spent: a 401 for the dead token, not a 429")
}

// S1: an exhausted refresh budget refuses the refresh endpoint only. Neither
// password login (ABS or web) reads it, so a client with a stale token can
// still sign in to obtain a fresh one.
func TestLoginBudget_SpentRefreshBudgetRefusesRefreshOnly(t *testing.T) {
	fx := budgetFixture(t)
	budgetUser(t, fx, "budget-eve", "eve-pw")
	const v = "192.0.2.92:4000"
	for i := 0; i < absauth.MaxFailuresPerIP; i++ {
		budgetRefresh(fx, v, i)
	}
	require.Equal(t, http.StatusTooManyRequests, budgetRefresh(fx, v, 99), "the refresh budget is really spent")
	require.Equal(t, http.StatusOK, budgetLogin(fx, "/login", v, "budget-eve", "eve-pw"), "ABS login is not refused for a spent refresh budget")
	require.Equal(t, http.StatusOK, budgetLogin(fx, "/login", "192.0.2.93:4000", "budget-eve", "eve-pw"))
}

// N6: the middleware's own refusals use the error shape of the door they guard:
// {"error": msg} for the Audiobookshelf POST /login, the web envelope
// ({"error","code","status"}) for POST /api/v1/auth/login.
func TestLoginCeiling_BodyRefusalsUseTheDoorsErrorShape(t *testing.T) {
	fx := budgetFixture(t)
	big := strings.Repeat("a", loginBodyLimit+1)
	for _, tc := range []struct {
		path    string
		wantKey bool // the web envelope also carries code and status
	}{{"/login", false}, {"/api/v1/auth/login", true}} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(big))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.50:4000"
		rec := httptest.NewRecorder()
		fx.srv.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, tc.path)
		var got map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), tc.path)
		require.Contains(t, got, "error", tc.path)
		if tc.wantKey {
			require.Contains(t, got, "code", tc.path)
			require.Contains(t, got, "status", tc.path)
		} else {
			require.Len(t, got, 1, "%s: the ABS shape is exactly {\"error\": msg}, got %v", tc.path, got)
		}
	}
}

// B-3 through the real router (production middleware order, gzip): a stalled
// body on either door is answered 408 within the deadline, holds no ceiling slot,
// and the ABS refusal has the ABS shape.
func TestLoginCeiling_StalledBodyRealRouterAnswers408InTheDoorsShape(t *testing.T) {
	saved := loginBodyReadTimeout
	loginBodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { loginBodyReadTimeout = saved })
	fx := budgetFixture(t)
	srv := httptest.NewServer(fx.srv.router)
	t.Cleanup(srv.Close)
	for _, path := range []string{"/api/v1/auth/login", "/login"} {
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		require.NoError(t, err)
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"us", path)
		start := time.Now()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err, path)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		conn.Close()
		require.Equal(t, http.StatusRequestTimeout, resp.StatusCode, path)
		require.Less(t, time.Since(start), 3*time.Second, path)
		if path == "/login" {
			require.JSONEq(t, `{"error":"timed out reading the request body"}`, string(raw))
		}
	}
	require.Zero(t, ceilingGlobalCount(fx.srv.loginCeiling()))
}

// S5: stalled bodies hold no in-flight slot. One visitor stalls
// maxLoginInFlightPerVisitor bodies; a normal login from the same visitor is
// admitted (answered by the handler) long before the stalled ones time out. If
// the body were read after the visitor's slot was taken, the four stalled
// requests would hold all four slots and the fifth would be refused 429.
func TestLoginCeiling_StalledBodiesHoldNoInFlightSlot(t *testing.T) {
	setTunnelConnectors(t, "")
	saved := loginBodyReadTimeout
	loginBodyReadTimeout = 4 * time.Second
	t.Cleanup(func() { loginBodyReadTimeout = saved })
	l := newLoginCeiling()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(captureRawResponseController(), compressionMiddleware()) // the production writer chain
	r.POST("/login", l.middleware(nil, nil), func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")

	var conns []net.Conn
	for range maxLoginInFlightPerVisitor {
		conn, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		conns = append(conns, conn)
		fmt.Fprintf(conn, "POST /login HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"us")
	}
	t.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
	})
	time.Sleep(300 * time.Millisecond) // let the server accept them and start reading

	start := time.Now()
	body := `{"username":"a","password":"b"}`
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(srv.URL+"/login", "application/json", strings.NewReader(body))
	require.NoError(t, err, "a normal login from the visitor with stalled bodies must be answered")
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "admitted to the handler, not refused 429")
	require.Less(t, time.Since(start), 2*time.Second, "answered before the stalled bodies' deadline")
}
