// file: core/integrations/providerhttp/round2_test.go
// version: 1.0.0
// guid: 47bd634f-44b8-41a2-a318-a75606c71e2f
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// TestGetJSON_EchoedQueryCredentialNotInDetail (round-2 MINOR 2): a provider
// whose error page echoes the request leaves neither the credential nor its
// query-escaped form in the error's Detail, whatever shape the echo has.
func TestGetJSON_EchoedQueryCredentialNotInDetail(t *testing.T) {
	const secret = "riddler key/777+x&y"
	esc := url.QueryEscape(secret)
	srv, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// A form no name rule can see: the bare value inside prose and JSON, raw
		// and escaped, and the same value at the very end of a long body.
		_, _ = fmt.Fprintf(w, `{"error":"bad request to %s","echo":"%s","q":"%s"}`, r.URL.String(), secret, esc)
		_, _ = fmt.Fprint(w, strings.Repeat(" ", 4*integrations.MaxDetail), "tail "+secret)
	})
	r := newRegistry(t, newClock())
	c := r.Client(ref("riddler"), providerhttp.Policy{Limits: limits(100, 100), Redact: nil,
		QueryCredential: &integrations.QueryCredential{Name: "gotham_pin", Origins: []string{srv.URL},
			Value: func(context.Context) (string, error) { return secret, nil }}})
	err := c.GetJSON(context.Background(), srv.URL+"/x?q=1", &struct{}{})
	var ie *integrations.Error
	if !errors.As(err, &ie) || ie.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v, want a classified 500", err)
	}
	for _, leak := range []string{secret, esc, "key/777", "key%2F777"} {
		if strings.Contains(ie.Detail, leak) || strings.Contains(err.Error(), leak) {
			t.Errorf("the credential (%q) is in the error: %q", leak, ie.Detail)
		}
	}
	if !strings.Contains(ie.Detail, "REDACTED") {
		t.Errorf("Detail = %q, want the echoed value replaced", ie.Detail)
	}

	// An origin the credential is not bound to is not asked for it at all (a
	// fresh instance: the one above has its breaker open after the 500s).
	other, _ := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	reads := 0
	c2 := r.Client(ref("riddler2"), providerhttp.Policy{Limits: limits(100, 100),
		QueryCredential: &integrations.QueryCredential{Name: "gotham_pin", Origins: []string{srv.URL},
			Value: func(context.Context) (string, error) { reads++; return secret, nil }}})
	var ie2 *integrations.Error
	if err := c2.GetJSON(context.Background(), other.URL+"/x", &struct{}{}); !errors.As(err, &ie2) || ie2.Status != http.StatusBadRequest {
		t.Fatalf("the request to an unbound origin: %v, want a classified 400", err)
	}
	if reads != 0 {
		t.Errorf("the credential was read %d times for an origin it is not bound to", reads)
	}
}
