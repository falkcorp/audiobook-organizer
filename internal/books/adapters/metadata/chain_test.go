// file: internal/books/adapters/metadata/chain_test.go
// version: 0.1.0
// guid: 97a109e8-790f-4f1b-84b6-17e839527500
// last-edited: 2026-11-20

package metadata

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	cmeta "github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/secrets"
)

func gbSecret(id, field string) secrets.Name {
	return secrets.Name("integrations.metadata." + id + "." + field)
}

const gbBody = `{"totalItems":1,"items":[{"volumeInfo":{"title":"Batman: Year One","authors":["Frank Miller"]}}]}`

func googleHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(gbBody))
}

func search(t *testing.T, reg *integrations.Registry, q Query) ([]cmeta.Result[Result], []cmeta.Outcome, error) {
	t.Helper()
	ch, err := NewChain(reg, cmeta.FirstHit)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	return ch.Search(context.Background(), q)
}

// An M6-shaped google-books row (base_url plus the api_key ref M2 stored) answers
// through the chain, and the key travels in the header, read from the secret store
// at request time.
func TestChain_GoogleBooksSendsTheStoredKeyInAHeader(t *testing.T) {
	rt := newRoute(t, googleHandler)
	r := row("google-books", map[string]any{
		"base_url": "https://books.example.test/v1",
		"api_key":  secrets.RefTo(gbSecret("google-books", "api_key")),
	})
	r.SecretsOrigin = "base_url=https://books.example.test:443"
	reg := load(t, rt.outbound(), memSecrets{gbSecret("google-books", "api_key"): "alfred-key"}, r)
	rs, outs, err := search(t, reg, Query{Title: "Year One"})
	if err != nil || len(rs) != 1 || rs[0].Value.Title != "Batman: Year One" {
		t.Fatalf("rs=%v outs=%v err=%v", rs, outs, err)
	}
	reqs := rt.requests()
	if len(reqs) == 0 {
		t.Fatal("nothing was sent")
	}
	if got := reqs[0].Header.Get("X-Goog-Api-Key"); got != "alfred-key" {
		t.Errorf("X-Goog-Api-Key = %q", got)
	}
	if strings.Contains(reqs[0].URL.String(), "alfred-key") {
		t.Error("the key is in the URL")
	}
}

// google-books without a key is HEAD's anonymous mode: it still answers.
func TestChain_GoogleBooksWithoutAKeyIsAnonymous(t *testing.T) {
	rt := newRoute(t, googleHandler)
	reg := load(t, rt.outbound(), nil, row("google-books", map[string]any{}))
	rs, _, err := search(t, reg, Query{Title: "Year One"})
	if err != nil || len(rs) != 1 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
	if got := rt.requests()[0].Header.Get("X-Goog-Api-Key"); got != "" {
		t.Errorf("anonymous request carried a key %q", got)
	}
}

// A stored key that cannot be read is an error before sending, never an
// anonymous request: spending the shared daily quota unauthenticated would look
// like a working source.
func TestChain_GoogleBooksWithAnUnreadableKeyDoesNotSend(t *testing.T) {
	rt := newRoute(t, googleHandler)
	r := row("google-books", map[string]any{"api_key": secrets.RefTo(gbSecret("google-books", "api_key"))})
	reg := load(t, rt.outbound(), memSecrets{}, r) // the ref names a secret that is not stored
	_, outs, err := search(t, reg, Query{Title: "Year One"})
	if !errors.Is(err, cmeta.ErrNoProviderAnswered) {
		t.Fatalf("err = %v, outs = %+v", err, outs)
	}
	if n := len(rt.requests()); n != 0 {
		t.Fatalf("%d requests sent without the key", n)
	}
}

// hardcover requires its token: a row without one is not served, and nothing is sent.
func TestChain_HardcoverWithoutATokenIsNotServed(t *testing.T) {
	rt := newRoute(t, googleHandler)
	reg := load(t, rt.outbound(), nil, row("hardcover", map[string]any{}))
	_, _, err := search(t, reg, Query{Title: "Year One"})
	if !errors.Is(err, cmeta.ErrNoProviderAnswered) || len(rt.requests()) != 0 {
		t.Fatalf("err=%v sent=%d", err, len(rt.requests()))
	}
	h, _ := reg.Health(context.Background(), integrations.Ref{Kind: integrations.KindMetadata, Instance: "hardcover"})
	if h.Status == integrations.HealthOK {
		t.Errorf("hardcover with no token reads healthy: %+v", h)
	}
}

// Audnexus has no title search, so the chain never spends a request on a query
// without an ASIN.
func TestChain_AudnexusIsSkippedWithoutAnASIN(t *testing.T) {
	rt := newRoute(t, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	reg := load(t, rt.outbound(), nil, row("audnexus", map[string]any{}))
	_, outs, _ := search(t, reg, Query{Title: "Year One"})
	if len(outs) != 1 || outs[0].Status != cmeta.SkippedCaps {
		t.Fatalf("outcomes = %+v", outs)
	}
	if n := len(rt.requests()); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}

// HEAD's hardcover and wikipedia clients dial a fixed public endpoint and never
// read a configured base URL. An M6 row for either carries base_url whenever the
// source row had one (and Local with its origins, since the URL is a
// destination); the adapter accepts it and ignores it, so the request still goes
// to the endpoint HEAD sent it to, the instance is healthy, and the egress
// policy is public-only (a Local grant for the ignored URL must not let the
// client reach a private address).
func TestM6Shape_BaseURLIsAcceptedAndIgnoredByHardcoverAndWikipedia(t *testing.T) {
	const mirror = "https://mirror.example.test/api"
	const sig = "base_url=https://mirror.example.test:443"
	for _, c := range []struct {
		id       string
		set      map[string]any
		sec      memSecrets
		q        Query
		wantHost string
	}{
		{"hardcover", map[string]any{"base_url": mirror, "api_token": secrets.RefTo(gbSecret("hardcover", "api_token"))},
			memSecrets{gbSecret("hardcover", "api_token"): "bat-token"}, Query{ISBN13: "9780000000002"}, "api.hardcover.app"},
		{"wikipedia", map[string]any{"base_url": mirror}, nil, Query{Title: "Knightfall"}, "en.wikipedia.org"},
	} {
		t.Run(c.id, func(t *testing.T) {
			rt := newRoute(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
			r := row(c.id, c.set)
			r.Config.Local, r.LocalOrigin, r.SecretsOrigin = true, sig, sig
			reg := load(t, rt.outbound(), c.sec, r)
			ref := integrations.Ref{Kind: integrations.KindMetadata, Instance: integrations.InstanceID(c.id)}
			if h, err := reg.Health(context.Background(), ref); err != nil || h.Status != integrations.HealthOK {
				t.Fatalf("health = %+v, %v: a base_url the adapter ignores must not make the instance unhealthy", h, err)
			}
			_, _, _ = search(t, reg, c.q)
			urls := rt.urls()
			if len(urls) == 0 {
				t.Fatal("nothing was sent")
			}
			for _, u := range urls {
				if strings.Contains(u, "mirror.example.test") || !strings.Contains(u, c.wantHost) {
					t.Errorf("request went to %s, want the fixed %s endpoint", u, c.wantHost)
				}
			}
			spec, ok := rt.specFor(ref)
			if !ok || spec.Egress != integrations.EgressPublicOnly {
				t.Errorf("outbound spec = %+v (ok=%v), want EgressPublicOnly", spec.Egress, ok)
			}
		})
	}
}

// ---- the M6 cutover's credentials.<k> settings ------------------------------

// M6 writes each secret M2 stored under integrations.metadata.<id>.<field> as
// the setting <field>, a secrets.Ref, field names with dots included
// (credentials.api_key, credentials.token ...). Core's schema has no
// declarable dotted secret field (a secret name's last segment has no dot, and
// settings with a name no field declares are refused: additionalProperties
// false), so a row carrying one is NOT SERVED: the whole instance reads
// misconfigured, including a key it holds under a field the adapter does
// declare. This test records that measured outcome; it is the reason the M6
// cutover must fold credentials.<k> into the declared field (the follow-up the
// hand-off of this PR spells out) before the metadata flip.
func TestM6Shape_DottedCredentialSettingsMakeTheInstanceMisconfigured(t *testing.T) {
	for _, c := range []struct {
		name string
		id   string
		set  map[string]any
	}{
		{"google-books credentials.api_key alone", "google-books", map[string]any{
			"credentials.api_key": secrets.RefTo(gbSecret("google-books", "credentials.api_key"))}},
		{"google-books api_key and credentials.api_key", "google-books", map[string]any{
			"api_key":             secrets.RefTo(gbSecret("google-books", "api_key")),
			"credentials.api_key": secrets.RefTo(gbSecret("google-books", "credentials.api_key"))}},
		{"hardcover credentials.api_token", "hardcover", map[string]any{
			"credentials.api_token": secrets.RefTo(gbSecret("hardcover", "credentials.api_token"))}},
		{"audible credentials.token", "audible", map[string]any{
			"credentials.token": secrets.RefTo(gbSecret("audible", "credentials.token"))}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := newRoute(t, googleHandler)
			reg := load(t, rt.outbound(), memSecrets{
				gbSecret(c.id, "api_key"):   "k",
				gbSecret(c.id, "api_token"): "t",
			}, row(c.id, c.set))
			h, err := reg.Health(context.Background(), integrations.Ref{Kind: integrations.KindMetadata, Instance: integrations.InstanceID(c.id)})
			if err != nil {
				t.Fatal(err)
			}
			if h.Status != integrations.HealthMisconfigured || h.Message != integrations.ReasonInvalidSettings {
				t.Fatalf("health = %+v, want misconfigured/%s", h, integrations.ReasonInvalidSettings)
			}
			if _, _, err := search(t, reg, Query{Title: "Year One", ASIN: "B000000001"}); err == nil {
				t.Error("an instance that reads misconfigured answered a search")
			}
			if n := len(rt.requests()); n != 0 {
				t.Errorf("%d requests sent by a misconfigured instance", n)
			}
		})
	}
}
