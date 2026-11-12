// file: core/integrations/providerhttp/samehost_test.go
// version: 1.0.0
// guid: 47864b19-e16f-4031-861c-4e119e643363
// last-edited: 2026-11-11

package providerhttp_test

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// TestSameHostOrPublic_LANAllowIsNotEnough (round-2 MAJOR 1): lan_allow admits
// both servers, but an instance under EgressSameHostOrPublic reaches only the
// private endpoints it is configured for (or bound to by Trust). A download
// link an indexer's answer names on another allowed host is refused at the dial
// and the downgrade rule alike; EgressConfigured is unchanged.
func TestSameHostOrPublic_LANAllowIsNotEnough(t *testing.T) {
	a, aHits := server(t, ok) // the indexer's own host
	b, bHits := server(t, ok) // a NAS that lan_allow admits for other instances
	r := newRegistry(t, newClock())
	indexer := func(egress integrations.EgressPolicy, mut func(*providerhttp.Policy)) *providerhttp.Client {
		p := providerhttp.Policy{Limits: limits(1000, 1000), Egress: egress, ConfiguredOrigins: []string{"http://" + hostOf(a)}}
		if mut != nil {
			mut(&p)
		}
		return r.Client(ref("indexer"), p)
	}
	same := indexer(integrations.EgressSameHostOrPublic, nil)

	if _, err := get(t, same, a.URL+"/dl"); err != nil {
		t.Fatalf("the instance's own configured host: %v", err)
	}
	if _, err := get(t, same, b.URL+"/dl"); !errors.Is(err, providerhttp.ErrBlockedAddress) {
		t.Fatalf("another lan_allow host: %v, want the dial refused", err)
	}
	if bHits.Load() != 0 || aHits.Load() != 1 {
		t.Fatalf("hits a=%d b=%d, want 1 and 0", aHits.Load(), bHits.Load())
	}

	// The default port is implied in a configured origin, and case is ignored.
	_, port, _ := net.SplitHostPort(hostOf(a))
	cased := indexer(integrations.EgressSameHostOrPublic, func(p *providerhttp.Policy) {
		p.ConfiguredOrigins = []string{"HTTP://127.0.0.1:" + port, "http://", "nonsense"}
	})
	if _, err := get(t, cased, a.URL+"/dl"); err != nil {
		t.Fatalf("a configured origin written in capitals: %v", err)
	}

	// A bound origin (Trust) is admitted without being listed as configured.
	bound := indexer(integrations.EgressSameHostOrPublic, func(p *providerhttp.Policy) {
		p.ConfiguredOrigins = nil
		p.Trust = integrations.Trust{PrivateOrigins: []string{"http://" + hostOf(b)}}
	})
	if _, err := get(t, bound, b.URL+"/dl"); err != nil {
		t.Fatalf("a bound private origin: %v", err)
	}
	// With nothing configured and nothing bound, no private host is reachable.
	none := indexer(integrations.EgressSameHostOrPublic, func(p *providerhttp.Policy) { p.ConfiguredOrigins = nil })
	if _, err := get(t, none, a.URL+"/dl"); !errors.Is(err, providerhttp.ErrBlockedAddress) {
		t.Fatalf("no configured origin: %v, want the dial refused", err)
	}

	// EgressConfigured keeps lan_allow as it was: both are reachable.
	conf := indexer(integrations.EgressConfigured, nil)
	if _, err := get(t, conf, b.URL+"/dl"); err != nil {
		t.Fatalf("EgressConfigured must still reach lan_allow: %v", err)
	}

	// The downgrade rule asks the same question: an https to http hop onto a
	// lan_allow host that is not the instance's own is refused as a downgrade;
	// onto its own host it passes that rule (and the dial then refuses the
	// crossed hop, which only the public transport carries).
	hop := func(c *providerhttp.Client, to string) error {
		req, _ := http.NewRequest(http.MethodGet, to, nil)
		req.Response = &http.Response{StatusCode: http.StatusFound,
			Request: &http.Request{URL: &url.URL{Scheme: "https", Host: "127.0.0.1:1", Path: "/"}}}
		resp, err := c.Client().Transport.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := hop(same, b.URL+"/dl"); !errors.Is(err, providerhttp.ErrInsecureRedirect) {
		t.Fatalf("downgrade onto another lan_allow host: %v, want ErrInsecureRedirect", err)
	}
	if err := hop(same, a.URL+"/dl"); errors.Is(err, providerhttp.ErrInsecureRedirect) {
		t.Fatalf("downgrade onto the instance's own host must pass the downgrade rule: %v", err)
	}
}
