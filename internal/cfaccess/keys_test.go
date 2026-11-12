// file: internal/cfaccess/keys_test.go
// version: 1.0.0
// guid: 328dc2a2-246b-48be-8b9d-bcd9cc43ce8e
// last-edited: 2026-11-11

package cfaccess

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	coreauth "github.com/falkcorp/mediacore/auth"
)

// warm makes the rig's verifier fetch and cache the served keys.
func (g *rig) warm(t testing.TB) {
	t.Helper()
	if _, err := g.do(g.acc.token(human("dick@wayne.example.test")), nil); err != nil {
		t.Fatalf("warm: %v", err)
	}
}

func isInvalid(err error) bool {
	return errors.Is(err, coreauth.ErrInvalidCredentials) && !errors.Is(err, coreauth.ErrExtensionUnavailable)
}

// A token's kid is chosen by whoever builds the token, signed or not, so it must
// not choose when the key set is fetched: at most one attempt per window.
func TestKeyCache_RandomKidsCostOneFetchPerWindow(t *testing.T) {
	stranger := newKey(t)
	flood := func(g *rig, n int) {
		t.Helper()
		for i := range n {
			raw := g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Kid: fmt.Sprintf("kid-%d", i), Key: stranger})
			if _, err := g.do(raw, nil); !isInvalid(err) {
				t.Fatalf("kid %d: %v, want a plain invalid credential", i, err)
			}
		}
	}
	t.Run("warm", func(t *testing.T) {
		g := newRig(t)
		g.warm(t)
		base := g.acc.fetchCount()
		flood(g, 50)
		if n := g.acc.fetchCount() - base; n != 0 {
			t.Fatalf("%d fetches inside the window, want 0", n)
		}
		g.acc.clk.advance(keysMissWindow + time.Second)
		flood(g, 50)
		if n := g.acc.fetchCount() - base; n != 1 {
			t.Fatalf("%d fetches for 50 unknown kids in the next window, want exactly 1", n)
		}
	})
	t.Run("cold", func(t *testing.T) {
		g := newRig(t)
		flood(g, 50)
		if n := g.acc.fetchCount(); n != 1 {
			t.Fatalf("%d fetches for 50 unknown kids on a cold cache, want exactly 1", n)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		g := newRig(t)
		var wg sync.WaitGroup
		for i := range 50 {
			wg.Go(func() {
				raw := g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Kid: fmt.Sprintf("kid-%d", i), Key: stranger})
				if _, err := g.do(raw, nil); !isInvalid(err) {
					t.Errorf("kid %d: %v", i, err)
				}
			})
		}
		wg.Wait()
		if n := g.acc.fetchCount(); n != 1 {
			t.Fatalf("%d fetches for 50 concurrent unknown kids, want exactly 1", n)
		}
	})
}

// Callers that need a key nobody has cached yet share the one fetch in flight.
func TestKeyCache_ConcurrentMissesShareOneFetch(t *testing.T) {
	g := newRig(t)
	g.warm(t)
	k2 := newKey(t)
	g.acc.mu.Lock()
	g.acc.keys["k2"] = k2
	g.acc.mu.Unlock()
	g.acc.clk.advance(keysMissWindow + time.Second)
	base := g.acc.fetchCount()
	release := g.acc.hold()
	defer release()
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			raw := g.acc.token(tokenSpec{Sub: fmt.Sprintf("s%d", i), Email: "tim@wayne.example.test", Kid: "k2"})
			if _, err := g.do(raw, nil); err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
		})
	}
	time.Sleep(150 * time.Millisecond) // every caller is waiting on, or about to join, the one fetch
	release()
	wg.Wait()
	if n := g.acc.fetchCount() - base; n != 1 {
		t.Fatalf("%d fetches for 10 concurrent callers, want 1", n)
	}
}

// Key rotation: a key added under a new kid is picked up by the first miss after
// the window; inside it the token is not valid (the window is the price of the
// flood limit). A token under a kid that is never served stays invalid.
func TestKeyCache_RotationIsPickedUpAfterTheWindow(t *testing.T) {
	g := newRig(t)
	g.warm(t)
	k2 := newKey(t)
	g.acc.mu.Lock()
	g.acc.keys["k2"] = k2
	g.acc.mu.Unlock()
	tok := func() string { return g.acc.token(tokenSpec{Sub: "s2", Email: "tim@wayne.example.test", Kid: "k2"}) }
	if _, err := g.do(tok(), nil); !isInvalid(err) {
		t.Fatalf("a rotated-in key inside the window: %v, want invalid", err)
	}
	g.acc.clk.advance(keysMissWindow + time.Second)
	if _, err := g.do(tok(), nil); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	// The old key still verifies from the cache without another fetch.
	before := g.acc.fetchCount()
	if _, err := g.do(g.acc.token(human("dick@wayne.example.test")), nil); err != nil {
		t.Fatal(err)
	}
	if g.acc.fetchCount() != before {
		t.Fatal("a cached key caused a refetch")
	}
}

// A key Cloudflare withdraws keeps verifying from the cache only until the TTL;
// then the cache refetches and the token no longer verifies.
func TestKeyCache_WithdrawnKeyStopsVerifyingAfterTheTTL(t *testing.T) {
	g := newRig(t)
	g.warm(t)
	k1 := g.acc.keys["k1"]
	g.acc.mu.Lock()
	delete(g.acc.keys, "k1")
	g.acc.keys["k2"] = newKey(t)
	g.acc.mu.Unlock()
	old := func() string { return g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Kid: "k1", Key: k1}) }
	base := g.acc.fetchCount()
	g.acc.clk.advance(keysTTL - time.Minute)
	if _, err := g.do(old(), nil); err != nil {
		t.Fatalf("before the TTL the cached key verifies: %v", err)
	}
	if g.acc.fetchCount() != base {
		t.Fatal("a cached key caused a fetch")
	}
	g.acc.clk.advance(2 * time.Minute)
	if _, err := g.do(old(), nil); !isInvalid(err) {
		t.Fatalf("after the TTL a withdrawn key: %v, want invalid", err)
	}
	if n := g.acc.fetchCount() - base; n != 1 {
		t.Fatalf("%d fetches at the TTL, want 1", n)
	}
	if _, err := g.do(g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Kid: "k2"}), nil); err != nil {
		t.Fatalf("the new key: %v", err)
	}
}

// Outage behaviour: a token under a cached kid is judged by that key and never
// by the outage; an unknown kid is unavailable (we cannot tell), not forged;
// and keys past the TTL are not trusted while the refetch fails.
func TestKeyCache_Outage(t *testing.T) {
	stranger := newKey(t)
	t.Run("a forged token under a cached kid is a plain 401", func(t *testing.T) {
		g := newRig(t)
		g.warm(t)
		g.acc.setMode("503")
		base := g.acc.fetchCount()
		forged := g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Kid: "k1", Key: stranger})
		if _, err := g.do(forged, nil); !isInvalid(err) {
			t.Fatalf("forged token under a cached kid during an outage: %v, want invalid", err)
		}
		// And a genuine one still verifies: the outage is not a reason to refuse it.
		if _, err := g.do(g.acc.token(human("dick@wayne.example.test")), nil); err != nil {
			t.Fatalf("genuine token during an outage: %v", err)
		}
		if g.acc.fetchCount() != base {
			t.Fatal("a cached kid caused a fetch")
		}
	})
	t.Run("an unknown kid is unavailable, once per window", func(t *testing.T) {
		g := newRig(t)
		g.warm(t)
		g.acc.setMode("503")
		g.acc.clk.advance(keysMissWindow + time.Second)
		base := g.acc.fetchCount()
		ghost := g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Kid: "k-ghost", Key: stranger})
		for i := range 5 {
			if _, err := g.do(ghost, nil); !errors.Is(err, coreauth.ErrExtensionUnavailable) {
				t.Fatalf("attempt %d: %v, want unavailable", i, err)
			}
		}
		if n := g.acc.fetchCount() - base; n != 1 {
			t.Fatalf("%d fetches for 5 unknown-kid requests in an outage, want 1", n)
		}
	})
	t.Run("stale keys are not trusted", func(t *testing.T) {
		g := newRig(t)
		g.warm(t)
		g.acc.setMode("503")
		g.acc.clk.advance(keysTTL + time.Minute)
		if _, err := g.do(g.acc.token(human("dick@wayne.example.test")), nil); !errors.Is(err, coreauth.ErrExtensionUnavailable) {
			t.Fatalf("a token under a key older than the TTL with the refetch failing: %v, want unavailable", err)
		}
		// The failed refetch must not make the old keys fresh again: the next
		// request, inside the window, is still unavailable.
		if _, err := g.do(g.acc.token(human("dick@wayne.example.test")), nil); !errors.Is(err, coreauth.ErrExtensionUnavailable) {
			t.Fatalf("a second request after a failed refetch trusted the stale keys: %v", err)
		}
		g.acc.setMode("")
		g.acc.clk.advance(keysMissWindow + time.Second)
		if _, err := g.do(g.acc.token(human("dick@wayne.example.test")), nil); err != nil {
			t.Fatalf("after recovery: %v", err)
		}
	})
}

// The cache restricts algorithms itself, whatever the verifier in front of it
// lets through: an HS256 token under a served kid is rejected without a fetch.
func TestKeyCache_RefusesNonRS256Directly(t *testing.T) {
	acc := newAccess(t)
	c := newKeyCache(context.Background(), acc.srv.URL+"/cdn-cgi/access/certs", acc.srv.Client(), acc.clk.Now)
	hs, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(strings.Repeat("k", 32))}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	obj, err := hs.Sign([]byte(`{"sub":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := obj.CompactSerialize()
	if _, err := c.VerifySignature(context.Background(), raw); err == nil || errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("HS256 token: %v, want a plain refusal", err)
	}
	if n := acc.fetchCount(); n != 0 {
		t.Fatalf("%d fetches for an HS256 token, want 0", n)
	}
}

// A redirect from the certs URL is never followed and is an outage, not a key set.
func TestKeyCache_RefusesRedirects(t *testing.T) {
	g := newRig(t)
	g.acc.setMode("redirect")
	if _, err := g.do(g.acc.token(human(ownerMail)), nil); !errors.Is(err, coreauth.ErrExtensionUnavailable) {
		t.Fatalf("%v, want unavailable", err)
	}
	if n := g.acc.elsewhereCount(); n != 0 {
		t.Fatalf("the redirect target was requested %d times", n)
	}
}

// Only RS256 is accepted, and a token with no kid names no key and costs no fetch.
func TestKeyCache_AlgorithmAndKidChecks(t *testing.T) {
	g := newRig(t)
	hs, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(strings.Repeat("k", 32))}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	obj, err := hs.Sign([]byte(`{"iss":"` + g.acc.srv.URL + `","aud":["` + testAUD + `"],"email":"` + ownerMail + `","exp":` + fmt.Sprint(time.Now().Add(time.Hour).Unix()) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := obj.CompactSerialize()
	if _, err := g.do(raw, nil); !isInvalid(err) {
		t.Fatalf("HS256 token: %v", err)
	}
	// RS256 with no kid in the header.
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: g.acc.keys["k1"]}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	obj, _ = signer.Sign([]byte(`{"iss":"` + g.acc.srv.URL + `"}`))
	raw, _ = obj.CompactSerialize()
	if _, err := g.do(raw, nil); !isInvalid(err) {
		t.Fatalf("token with no kid: %v", err)
	}
	if n := g.acc.fetchCount(); n != 0 {
		t.Fatalf("%d fetches for tokens that name no usable key, want 0", n)
	}
}

// Every way the fetch can fail is ErrKeysUnavailable by class only.
func TestKeyCache_FetchClassifiesEveryFailure(t *testing.T) {
	acc := newAccess(t)
	c := newKeyCache(context.Background(), acc.srv.URL+"/cdn-cgi/access/certs", acc.srv.Client(), acc.clk.Now)
	if keys, err := c.fetch(); err != nil || len(keys) != 1 {
		t.Fatalf("a good fetch: %v, %v", keys, err)
	}
	for mode, class := range map[string]string{
		"503": "status 503", "garbage": "not a key set", "empty": "no usable keys", "drop": "transport",
		"redirect": "redirect refused", "huge": "body", "nokid": "no usable keys", "503keys": "status 503",
	} {
		acc.setMode(mode)
		keys, err := c.fetch()
		if keys != nil || !errors.Is(err, ErrKeysUnavailable) || !strings.Contains(err.Error(), class) {
			t.Fatalf("%s: %v, %v (want class %q)", mode, keys, err, class)
		}
		for _, leak := range []string{acc.srv.URL, "secret-upstream-detail", "captive"} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("%s: the error quotes the fetch: %v", mode, err)
			}
		}
	}
	bad := newKeyCache(context.Background(), "https://192.0.2.9/cdn-cgi/access/certs",
		&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp 192.0.2.9:443: refused") })}, acc.clk.Now)
	if _, err := bad.fetch(); !errors.Is(err, ErrKeysUnavailable) || strings.Contains(err.Error(), "192.0.2.9") {
		t.Fatalf("transport error: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
