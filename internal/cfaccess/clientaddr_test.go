// file: internal/cfaccess/clientaddr_test.go
// version: 0.1.0
// guid: 9b4f2e61-c7a3-48d5-a0e9-6d1c83f5b274
// last-edited: 2026-11-21

package cfaccess

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
)

// The "local peer + verified Access assertion" client-address rule, moved from
// internal/server/client_trust.go for the identity flip (F4-08 A8).

const (
	goodJWT    = "jwt-signal-from-the-batcave"
	serviceJWT = "jwt-service-token-oracle"
	outageJWT  = "jwt-keys-down"
)

// tokenVerifier verifies goodJWT as a person, serviceJWT as a service token,
// outageJWT as a key outage and anything else as invalid, and counts calls.
type tokenVerifier struct{ calls int }

func (v *tokenVerifier) Verify(_ context.Context, raw string) (*legacyoauth.IdentityClaims, error) {
	v.calls++
	switch raw {
	case goodJWT:
		return &legacyoauth.IdentityClaims{Email: "dick@wayne.example.test", EmailVerified: true}, nil
	case serviceJWT:
		return nil, &NonIdentityAssertionError{CommonName: "oracle-relay"}
	case outageJWT:
		return nil, ErrKeysUnavailable
	}
	return nil, errors.New("cfaccess test: signature does not verify")
}

func localPeer(addr string) bool {
	return strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "[::1]")
}

type addrCase struct {
	name   string
	remote string
	jwt    string // the Cf-Access-Jwt-Assertion header
	cookie string // the CF_Authorization cookie
	ip     string // CF-Connecting-IP
	key    bool   // an API key is presented
	want   string // "" = not believed
}

func (c addrCase) request() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.RemoteAddr = c.remote
	if c.jwt != "" {
		r.Header.Set(CFAccessHeader, c.jwt)
	}
	if c.cookie != "" {
		r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: c.cookie})
	}
	if c.ip != "" {
		r.Header.Set("CF-Connecting-IP", c.ip)
	}
	if c.key {
		r.Header.Set("X-Api-Key", "abk_alfred")
	}
	return r
}

func keyHeader(r *http.Request) bool { return r.Header.Get("X-Api-Key") != "" }

// TestAccessClientAddr: each condition of the rule refuses on its own; the
// first two rows are the controls every refusal differs from by one input.
func TestAccessClientAddr(t *testing.T) {
	cases := []addrCase{
		{name: "local peer, verified header, valid address", remote: "127.0.0.1:5000", jwt: goodJWT, ip: "203.0.113.9", want: "203.0.113.9"},
		{name: "local ipv6 peer, verified cookie, valid address", remote: "[::1]:5000", cookie: goodJWT, ip: "2001:DB8::1", want: "2001:db8::1"},
		{name: "service token counts as verified", remote: "127.0.0.1:5000", jwt: serviceJWT, ip: "203.0.113.9", want: "203.0.113.9"},
		{name: "address with spaces", remote: "127.0.0.1:5000", jwt: goodJWT, ip: " 203.0.113.9 ", want: "203.0.113.9"},
		{name: "ipv4-mapped address is returned as ipv4", remote: "127.0.0.1:5000", jwt: goodJWT, ip: "::ffff:203.0.113.9", want: "203.0.113.9"},

		{name: "public peer is refused", remote: "198.51.100.7:5000", jwt: goodJWT, ip: "203.0.113.9"},
		{name: "private LAN peer is refused", remote: "10.0.0.11:5000", jwt: goodJWT, ip: "203.0.113.9"},
		{name: "no assertion is refused", remote: "127.0.0.1:5000", ip: "203.0.113.9"},
		{name: "an assertion that does not verify is refused", remote: "127.0.0.1:5000", jwt: "jwt-forged-by-the-joker", ip: "203.0.113.9"},
		{name: "a cookie that does not verify is refused", remote: "127.0.0.1:5000", cookie: "jwt-forged-by-the-joker", ip: "203.0.113.9"},
		{name: "a key outage is refused", remote: "127.0.0.1:5000", jwt: outageJWT, ip: "203.0.113.9"},
		{name: "an API key request is refused", remote: "127.0.0.1:5000", jwt: goodJWT, ip: "203.0.113.9", key: true},
		{name: "no address", remote: "127.0.0.1:5000", jwt: goodJWT},
		{name: "garbage address", remote: "127.0.0.1:5000", jwt: goodJWT, ip: "arkham"},
		{name: "address with a port", remote: "127.0.0.1:5000", jwt: goodJWT, ip: "203.0.113.9:443"},
		{name: "address list", remote: "127.0.0.1:5000", jwt: goodJWT, ip: "203.0.113.9, 198.51.100.4"},
		{name: "zoned address", remote: "127.0.0.1:5000", jwt: goodJWT, ip: "fe80::1%eth0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			trust := ClientAddrTrust{Verifier: &tokenVerifier{}, IsLocalPeer: localPeer, KeyPresented: keyHeader}
			got, ok := trust.AccessClientAddr(c.request())
			if c.want == "" {
				if ok || got.IsValid() {
					t.Fatalf("AccessClientAddr = (%v, %v), want not believed", got, ok)
				}
				return
			}
			if !ok || got != netip.MustParseAddr(c.want) {
				t.Fatalf("AccessClientAddr = (%v, %v), want (%s, true)", got, ok, c.want)
			}
		})
	}
}

// TestAccessClientAddr_HeaderWinsOverCookie: as the gin binder, the header is
// the assertion when present; a valid cookie does not rescue a forged header.
func TestAccessClientAddr_HeaderWinsOverCookie(t *testing.T) {
	trust := ClientAddrTrust{Verifier: &tokenVerifier{}, IsLocalPeer: localPeer}
	c := addrCase{remote: "127.0.0.1:5000", jwt: "jwt-forged-by-the-joker", cookie: goodJWT, ip: "203.0.113.9"}
	if got, ok := trust.AccessClientAddr(c.request()); ok {
		t.Fatalf("a forged header with a valid cookie believed %v", got)
	}
}

// TestAccessClientAddr_UnconfiguredBelievesNothing: a rule missing its
// verifier or its local-peer test believes no address, and a peer that is not
// local never reaches the verifier.
func TestAccessClientAddr_UnconfiguredBelievesNothing(t *testing.T) {
	c := addrCase{remote: "127.0.0.1:5000", jwt: goodJWT, ip: "203.0.113.9"}
	for name, trust := range map[string]ClientAddrTrust{
		"no verifier":    {IsLocalPeer: localPeer},
		"no local test":  {Verifier: &tokenVerifier{}},
		"zero value":     {},
		"peer not local": {Verifier: &tokenVerifier{}, IsLocalPeer: func(string) bool { return false }},
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := trust.AccessClientAddr(c.request()); ok {
				t.Fatalf("believed %v", got)
			}
		})
	}
	if _, ok := (ClientAddrTrust{}).AccessClientAddr(nil); ok {
		t.Fatal("a nil request was believed")
	}
	v := &tokenVerifier{}
	trust := ClientAddrTrust{Verifier: v, IsLocalPeer: localPeer}
	_, _ = trust.AccessClientAddr(addrCase{remote: "198.51.100.7:5000", jwt: goodJWT, ip: "203.0.113.9"}.request())
	if v.calls != 0 {
		t.Fatalf("a public peer's assertion was verified %d times; the peer check must come first", v.calls)
	}
}

// TestAccessClientAddr_LogsNothing: the rule has no logger, and the default
// logger sees neither the token nor the address.
func TestAccessClientAddr_LogsNothing(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	trust := ClientAddrTrust{Verifier: &tokenVerifier{}, IsLocalPeer: localPeer}
	for _, jwt := range []string{goodJWT, "jwt-forged-by-the-joker", outageJWT} {
		_, _ = trust.AccessClientAddr(addrCase{remote: "127.0.0.1:5000", jwt: jwt, ip: "203.0.113.9"}.request())
	}
	if buf.Len() != 0 {
		t.Fatalf("the rule logged: %q", buf.String())
	}
}
