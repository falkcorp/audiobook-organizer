// file: internal/books/cutover/registered_key_for_another_user_test.go
// version: 0.1.0
// guid: 9b4d1565-8bdc-41b8-9617-73b2899abaaf
// last-edited: 2026-11-22

package cutover_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// keyForAnotherUserMessage is books' own refusal of an API key that asks POST
// /api/v1/auth/api-keys for a key owned by someone else
// (internal/server/handlers/apikeys_identity.go, B10rn STOP 2). It is the
// message itself, not the status, that tells this refusal from the scope clamp's.
const keyForAnotherUserMessage = "API keys cannot create keys for other users; sign in to do that"

// TestReachRegisteredPath_KeyForAnotherUserIsRefusedByTheHandler is the B10g STOP 2
// cell, full stack. With gin's conditional credential guard gone (B10g), the
// books handler is the only thing that refuses an API key minting a key for
// another user on POST /api/v1/auth/api-keys. A users.manage key naming
// dick.grayson gets the handler's exact refusal and nothing is minted, for the
// imported abk_ key and for its core-native twin.
//
// The ABS-surface (compat) credentials are refused one stage earlier, and also
// mint nothing: a books.abs principal is bound only by ABSRequireAuth on the
// OwnAuth ABS routes, and no ABS route reaches the api-keys handler. On /api/v1,
// stage 12 runs core's session, key and Access authenticators, which do not
// know an ABS access token or a key on the ABS ?token= transport, so both get
// 401 authentication_required before gin runs, guard or no guard (F4-08 B lead
// 2026-11-22, STOP 2 compat leg approved as a refusal with no mint).
func TestReachRegisteredPath_KeyForAnotherUserIsRefusedByTheHandler(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	dick := rnUserID(t, h, "dick.grayson")
	before := rnKeys(t, h, dick)
	if len(before) == 0 {
		t.Fatal("dick.grayson has no key: the mint count proves nothing")
	}
	body := map[string]any{"name": "batarang", "scopes": []string{"users.manage"}, "user_id": dick}

	for _, name := range []string{"key:admin:users", coreKeyName("key:admin:users")} {
		t.Run(name, func(t *testing.T) {
			res := h.MustDo(t, h.Principal(name), http.MethodPost, "/api/v1/auth/api-keys", body)
			var got struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(res.Body, &got)
			if res.Status != http.StatusForbidden || got.Error != keyForAnotherUserMessage {
				t.Errorf("%s naming dick.grayson: %d %s, want 403 %q from the handler", name, res.Status, res.excerpt(), keyForAnotherUserMessage)
			}
			if after := rnKeys(t, h, dick); !maps.Equal(before, after) {
				t.Errorf("%s minted or changed a key of dick.grayson: %v -> %v", name, before, after)
			}
		})
	}

	raw := `{"name":"batarang","scopes":["users.manage"],"user_id":"` + dick + `"}`
	token, _, _ := absLogin(t, h, "lucius.fox", passwordGotham)
	if token == "" {
		t.Fatal("ABS /login gave lucius.fox no access token")
	}
	abk := h.Principal("key:admin:users")
	compat := []struct {
		name string
		req  func() *http.Request
	}{
		{"an ABS access token as a Bearer", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/api-keys", strings.NewReader(raw))
			r.Header.Set("Authorization", "Bearer "+token)
			return r
		}},
		{"an abk_ key on the ABS ?token= transport", func() *http.Request {
			return httptest.NewRequest(http.MethodPost, "/api/v1/auth/api-keys?token="+abk.bearer, strings.NewReader(raw))
		}},
	}
	for _, c := range compat {
		t.Run(c.name, func(t *testing.T) {
			req := c.req()
			req.Header.Set("Content-Type", "application/json")
			res := h.Raw(req)
			if res.Status != http.StatusUnauthorized || res.Stage13 != Stage13Refused || res.Code != "authentication_required" {
				t.Errorf("%s: %d %s (stage 13 %v, code %q), want stage 12's 401 authentication_required", c.name, res.Status, res.excerpt(), res.Stage13, res.Code)
			}
			if after := rnKeys(t, h, dick); !maps.Equal(before, after) {
				t.Errorf("%s minted or changed a key of dick.grayson: %v -> %v", c.name, before, after)
			}
		})
	}
}
