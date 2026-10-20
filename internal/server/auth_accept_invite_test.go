// file: internal/server/auth_accept_invite_test.go
// version: 1.2.0
// guid: b3c4d5e6-f7a8-9b0c-1d2e-3f4a5b6c7d8e
// last-edited: 2026-10-20

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

// TestHandleAcceptInvite_EmptyBody tests that an empty POST body returns 400 with a clear message.
func TestHandleAcceptInvite_EmptyBody(t *testing.T) {
	srv := &Server{router: gin.New()}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/auth/accept-invite", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "application/json")

	c, _ := gin.CreateTestContext(w)
	c.Request = req

	srv.handleAcceptInvite(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, "EOF") {
		t.Errorf("response body must not contain raw 'EOF' string: %s", body)
	}
	if !strings.Contains(body, "request body required") {
		t.Errorf("response body should contain 'request body required': %s", body)
	}
}

// TestHandleAcceptInvite_MissingContentLength tests HTTP/2 streaming bodies (no Content-Length).
func TestHandleAcceptInvite_NoContentLength(t *testing.T) {
	srv := &Server{router: gin.New()}

	// Create a request with no Content-Length (simulates HTTP/2 streaming)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/auth/accept-invite", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1 // Indicates unknown/chunked

	c, _ := gin.CreateTestContext(w)
	c.Request = req

	srv.handleAcceptInvite(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, "EOF") {
		t.Errorf("response body must not contain raw 'EOF' string: %s", body)
	}
}

// TestHandleAcceptInvite_MissingFields tests that missing required fields return a clear error.
func TestHandleAcceptInvite_MissingFields(t *testing.T) {
	srv := &Server{router: gin.New()}

	reqBody := map[string]string{"token": ""}
	bodyBytes, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/auth/accept-invite", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	c, _ := gin.CreateTestContext(w)
	c.Request = req

	srv.handleAcceptInvite(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, "EOF") {
		t.Errorf("response body must not contain raw 'EOF' string: %s", body)
	}
}

// consumeFailStore swaps ConsumeInvite's result so the handler's mapping of
// store errors can be exercised without a corrupt database.
type consumeFailStore struct {
	database.Store
	err error
}

func (f consumeFailStore) ConsumeInvite(string, string, string) (*database.User, error) {
	return nil, f.err
}

func acceptInviteErrBody(t *testing.T, w interface{ String() string }) string {
	t.Helper()
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(w.String()), &resp); err != nil {
		t.Fatalf("decode %q: %v", w.String(), err)
	}
	return resp.Error
}

// Missing, used and expired tokens are indistinguishable to the caller.
func TestAcceptInvite_EveryInvalidTokenGetsOneGenericBadRequest(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	mk := func(token, username string, expires time.Time) {
		t.Helper()
		if _, err := store.CreateInvite(&database.Invite{Token: token, Username: username, RoleID: "viewer", ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}
	usedTok := strings.Repeat("u", 64)
	expTok := strings.Repeat("x", 64)
	mk(usedTok, "usedguest", time.Now().Add(time.Hour))
	mk(expTok, "expguest", time.Now().Add(-time.Hour))
	if w := acceptInvite(t, srv, usedTok); w.Code != http.StatusCreated {
		t.Fatalf("first accept = %d: %s", w.Code, w.Body.String())
	}

	var msgs []string
	for name, tok := range map[string]string{"missing": strings.Repeat("m", 64), "used": usedTok, "expired": expTok} {
		w := acceptInvite(t, srv, tok)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400: %s", name, w.Code, w.Body.String())
		}
		msgs = append(msgs, acceptInviteErrBody(t, w.Body))
		if w.Body.String() != acceptInviteBodyOf(t, srv, strings.Repeat("m", 64)) {
			t.Errorf("%s: body differs from the missing-token body: %s", name, w.Body.String())
		}
	}
	for _, m := range msgs {
		if m != acceptInviteInvalidMessage {
			t.Errorf("message = %q, want %q", m, acceptInviteInvalidMessage)
		}
	}
}

func acceptInviteBodyOf(t *testing.T, srv *Server, token string) string {
	t.Helper()
	return acceptInvite(t, srv, token).Body.String()
}

// A storage failure is a 500, never a 400, and does not echo the error.
func TestAcceptInvite_StorageFailureIs500(t *testing.T) {
	_, store := setupUserHandlerServer(t)
	srv := newTestServer(t, consumeFailStore{Store: store, err: errors.New("pebble: disk exploded")})
	w := acceptInvite(t, srv, strings.Repeat("s", 64))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "exploded") {
		t.Errorf("body leaks the storage error: %s", w.Body.String())
	}
}

// The email-taken refusal keeps its own 409.
// An invited address another account holds verified gets the same generic
// 400 as an invalid link: a distinct answer would tell the link holder the
// address has a verified account.
func TestAcceptInvite_EmailTakenIsTheGenericRefusal(t *testing.T) {
	_, store := setupUserHandlerServer(t)
	srv := newTestServer(t, consumeFailStore{Store: store, err: fmt.Errorf("%w: holder", database.ErrInviteEmailTaken)})
	w := acceptInvite(t, srv, strings.Repeat("e", 64))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), acceptInviteInvalidMessage) {
		t.Fatalf("code = %d, body %s; want the generic 400", w.Code, w.Body.String())
	}
	for _, leak := range []string{"already", "belongs", "account"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("body reveals holder status (%q): %s", leak, w.Body.String())
		}
	}
}
