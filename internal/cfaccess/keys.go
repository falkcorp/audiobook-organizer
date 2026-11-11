// file: internal/cfaccess/keys.go
// version: 2.0.0
// guid: a8ab6f51-1da4-415a-9525-30b4f5565694
// last-edited: 2026-11-11

package cfaccess

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const (
	// maxKeysBody bounds the key-set document read from the team domain.
	maxKeysBody = 1 << 20
	// keysTTL is how long a fetched key set is trusted. Past it the cache
	// refetches before judging any token, so a key Cloudflare has withdrawn
	// stops verifying within the hour instead of living as long as the process.
	keysTTL = time.Hour
	// keysMissWindow is the least time between two fetch attempts. A token
	// under a kid the cache does not hold is how a caller (the kid is chosen by
	// whoever builds the token, signed or not) asks for a fetch, so the answer
	// must not cost one: at most one attempt per window, and requests that
	// arrive during one wait for it instead of starting their own.
	keysMissWindow = 30 * time.Second
)

// errUnknownKey and errBadSignature are the two ways a token fails against a
// key set that was fetched: both are "this token is not valid", the class a
// caller refuses, and neither is ErrKeysUnavailable.
var (
	errUnknownKey   = errors.New("cfaccess: no signing key for the token's kid")
	errBadSignature = errors.New("cfaccess: signature does not verify")
)

// keyCache is the team domain's signing keys, as an oidc.KeySet.
//
// Rules, each of them a way the previous remote key set (go-oidc's, which
// refetches on every unknown kid and never expires a key) was exploitable:
//
//   - A token whose kid is cached is judged by that key alone, with no fetch,
//     so a forged token under a known kid is a plain invalid credential even
//     while Cloudflare is down.
//   - An unknown kid triggers a fetch only if none was attempted in the last
//     keysMissWindow; concurrent callers share one in-flight fetch. Inside the
//     window the answer is "invalid" when the last attempt succeeded (the key
//     is not there) and "unavailable" when it failed (we do not know).
//   - Keys older than keysTTL are not trusted: the next request refetches, and
//     a failed refetch is "unavailable", never a stale yes.
//   - A fetch that fails for any reason (transport, any status other than 200
//     including redirects, a body that is not a key set) is ErrKeysUnavailable
//     with the class only: the underlying error can quote the URL or a body.
type keyCache struct {
	url    string
	client *http.Client
	base   context.Context // bounds a fetch; a caller's cancellation must not poison the callers waiting on it
	now    func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetched   time.Time // when keys were last fetched successfully; zero = never
	attempted time.Time // when a fetch was last started; zero = never
	lastOK    bool      // whether that attempt succeeded
	inflight  *keyFetch
}

type keyFetch struct {
	done chan struct{}
	err  error
}

func newKeyCache(base context.Context, url string, client *http.Client, now func() time.Time) *keyCache {
	// A redirect from the certs URL is never followed: whatever answers a
	// redirect is not the team domain's key set, and following it would let a
	// compromised or misrouted hop choose the keys.
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.Timeout == 0 {
		c.Timeout = keysTimeout
	}
	return &keyCache{url: url, client: &c, base: base, now: now}
}

// keyProbe is one Verify call's record of whether the key set could not be
// fetched. go-oidc's IDTokenVerifier folds the key set's error into text with
// %v, so the typed error never reaches Verify's caller through the return
// value; the cache sees it where it is still typed and leaves a mark here.
type keyProbe struct{ unavailable atomic.Bool }

type keyProbeKey struct{}

// VerifySignature implements oidc.KeySet.
func (c *keyCache) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	payload, err := c.verify(ctx, raw)
	if err != nil && errors.Is(err, ErrKeysUnavailable) {
		if p, ok := ctx.Value(keyProbeKey{}).(*keyProbe); ok {
			p.unavailable.Store(true)
		}
	}
	return payload, err
}

func (c *keyCache) verify(ctx context.Context, raw string) ([]byte, error) {
	jws, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(jws.Signatures) != 1 {
		return nil, errors.New("cfaccess: malformed token")
	}
	kid := jws.Signatures[0].Header.KeyID
	if kid == "" {
		return nil, errUnknownKey // Cloudflare always sets one; no fetch for a token that names none
	}
	key, err := c.key(ctx, kid)
	if err != nil {
		return nil, err
	}
	payload, err := jws.Verify(key)
	if err != nil {
		return nil, errBadSignature
	}
	return payload, nil
}

// key returns the key for kid, fetching under the rules on keyCache.
func (c *keyCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	now := c.now()
	fresh := !c.fetched.IsZero() && now.Sub(c.fetched) < keysTTL
	if fresh {
		if k, ok := c.keys[kid]; ok {
			c.mu.Unlock()
			return k, nil
		}
	}
	if f := c.inflight; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: request ended", ErrKeysUnavailable)
		}
		return c.afterFetch(f, kid)
	}
	if !c.attempted.IsZero() && now.Sub(c.attempted) < keysMissWindow {
		known := fresh && c.lastOK
		c.mu.Unlock()
		if known {
			return nil, errUnknownKey
		}
		return nil, fmt.Errorf("%w: a recent fetch failed", ErrKeysUnavailable)
	}
	f := &keyFetch{done: make(chan struct{})}
	c.inflight, c.attempted = f, now
	c.mu.Unlock()

	keys, err := c.fetch()

	c.mu.Lock()
	if err == nil {
		c.keys, c.fetched = keys, c.now()
	}
	c.lastOK = err == nil
	f.err = err
	c.inflight = nil
	c.mu.Unlock()
	close(f.done)
	return c.afterFetch(f, kid)
}

// afterFetch is the answer for kid once the fetch f has finished.
func (c *keyCache) afterFetch(f *keyFetch, kid string) (*rsa.PublicKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	c.mu.Lock()
	k, ok := c.keys[kid]
	c.mu.Unlock()
	if !ok {
		return nil, errUnknownKey
	}
	return k, nil
}

// fetch reads the team domain's key set. Every failure is ErrKeysUnavailable,
// by class only.
func (c *keyCache) fetch() (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(c.base, keysTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: request", ErrKeysUnavailable)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: transport", ErrKeysUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, fmt.Errorf("%w: redirect refused", ErrKeysUnavailable)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: status %d", ErrKeysUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeysBody+1))
	if err != nil || len(body) > maxKeysBody {
		return nil, fmt.Errorf("%w: body", ErrKeysUnavailable)
	}
	var set jose.JSONWebKeySet
	if json.Unmarshal(body, &set) != nil {
		return nil, fmt.Errorf("%w: not a key set", ErrKeysUnavailable)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		pub, ok := k.Key.(*rsa.PublicKey)
		if !ok || k.KeyID == "" || !k.Valid() ||
			(k.Use != "" && k.Use != "sig") || (k.Algorithm != "" && k.Algorithm != string(jose.RS256)) {
			continue
		}
		keys[k.KeyID] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no usable keys", ErrKeysUnavailable)
	}
	return keys, nil
}
