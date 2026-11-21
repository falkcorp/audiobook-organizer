// file: internal/server/client_trust_access_parity_test.go
// version: 0.1.0
// guid: 5a2d7c90-1e4b-4f83-9c06-b8e3f71d42a5
// last-edited: 2026-11-21

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
)

// parityVerifier verifies "jwt-person" as a person and "jwt-service" as a
// service token; anything else does not verify.
type parityVerifier struct{}

func (parityVerifier) Verify(_ context.Context, raw string) (*legacyoauth.IdentityClaims, error) {
	switch raw {
	case "jwt-person":
		return &legacyoauth.IdentityClaims{Email: "barbara@gordon.example.test", EmailVerified: true}, nil
	case "jwt-service":
		return nil, &cfaccess.NonIdentityAssertionError{CommonName: "clocktower-relay"}
	}
	return nil, errors.New("parity test: does not verify")
}

// TestTrustedClientIP_AccessBranchParity (F4-08 A8): for a local peer,
// cfaccess.ClientAddrTrust believes exactly the CF-Connecting-IP values
// trustedClientIP's "local peer + verified Access assertion" branch believes,
// with the gin binder's verified marker standing in for its verification
// (set when the assertion verifies or is a service token, never with an API
// key). B10 deletes the gin branch and keeps the cfaccess one, so the two must
// not drift before then.
func TestTrustedClientIP_AccessBranchParity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	local := func(addr string) bool {
		return strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "[::1]")
	}
	keyed := func(r *http.Request) bool { return r.Header.Get("X-Api-Key") != "" }
	trust := cfaccess.ClientAddrTrust{Verifier: parityVerifier{}, IsLocalPeer: local, KeyPresented: keyed}
	for _, remote := range []string{"127.0.0.1:5000", "[::1]:5000"} {
		for _, jwt := range []string{"", "jwt-person", "jwt-service", "jwt-forged"} {
			for _, ip := range []string{"", "203.0.113.9", " 203.0.113.9 ", "2001:DB8::1", "::ffff:203.0.113.9", "arkham", "203.0.113.9:443", "fe80::1%eth0", "203.0.113.9, 198.51.100.4"} {
				for _, key := range []bool{false, true} {
					req := httptest.NewRequest(http.MethodGet, "/x", nil)
					req.RemoteAddr = remote
					if jwt != "" {
						req.Header.Set(cfaccess.CFAccessHeader, jwt)
					}
					if ip != "" {
						req.Header.Set("CF-Connecting-IP", ip)
					}
					if key {
						req.Header.Set("X-Api-Key", "abk_lucius")
					}
					// What the gin binder would have marked.
					_, verr := parityVerifier{}.Verify(context.Background(), jwt)
					verified := jwt != "" && !key && (verr == nil || errors.Is(verr, cfaccess.ErrNonIdentityAssertion))

					r := gin.New()
					if err := r.SetTrustedProxies(nil); err != nil {
						t.Fatal(err)
					}
					var oldIP string
					var oldOK bool
					r.GET("/x", func(c *gin.Context) {
						if verified {
							cfaccess.MarkCFAccessVerified(c)
						}
						oldIP, oldOK = trustedClientIP(c, clientTrust{isLocalPeer: local})
					})
					r.ServeHTTP(httptest.NewRecorder(), req.Clone(context.Background()))

					newAddr, newOK := trust.AccessClientAddr(req)
					newIP := ""
					if newOK {
						newIP = newAddr.String()
					}
					if !oldOK {
						oldIP = ""
					}
					if oldOK != newOK || oldIP != newIP {
						t.Errorf("remote %s jwt %q ip %q key %v: gin (%q, %v), cfaccess (%q, %v)",
							remote, jwt, ip, key, oldIP, oldOK, newIP, newOK)
					}
				}
			}
		}
	}
}
