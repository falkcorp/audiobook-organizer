// file: internal/server/handlers/oauth_login_sanitize_test.go
// version: 1.0.0
// guid: 7b2e0c94-5a18-4d63-8f07-1a6c5b2e9d34

package handlers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
)

// TestStart_ReturnPathPayloadTable drives the legacy OAuth Start handler with
// every payload of core's shared return-path table and reads the Return it
// sealed into the state cookie: a reject payload must be dropped (the callback
// then lands on the SPA's default), an accept payload kept byte-for-byte. The
// handler calls core's oidc.SanitizeReturn, the one Go implementation; this
// test pins that it does, through the real route.
func TestStart_ReturnPathPayloadTable(t *testing.T) {
	raw, err := os.ReadFile("../../../core/auth/oidc/testdata/return_payloads.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Reject []string `json:"reject"`
		Accept []string `json:"accept"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Reject) == 0 || len(table.Accept) == 0 {
		t.Fatal("return_payloads.json has an empty list")
	}
	h := newDeepLinkHarness(t)
	sealed := func(ret string) string {
		t.Helper()
		rec, ck := h.start(t, "?return="+url.QueryEscape(ret))
		if rec.Code != http.StatusFound || ck == nil {
			t.Fatalf("Start(return=%q) = %d with no state cookie, want 302", ret, rec.Code)
		}
		payload, err := h.codec.Decode(ck.Value)
		if err != nil {
			t.Fatalf("decode state: %v", err)
		}
		return payload.Return
	}
	for _, in := range table.Reject {
		if got := sealed(in); got != "" {
			t.Errorf("Start sealed Return %q for return=%q, want it dropped", got, in)
		}
	}
	for _, in := range table.Accept {
		if got := sealed(in); got != in {
			t.Errorf("Start sealed Return %q for return=%q, want it unchanged", got, in)
		}
	}
}
