// file: internal/cfaccess/keys.go
// version: 1.0.0
// guid: a8ab6f51-1da4-415a-9525-30b4f5565694
// last-edited: 2026-11-11

package cfaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

// maxKeysBody bounds the key-set document read from the team domain.
const maxKeysBody = 1 << 20

// keysTransport makes a failed key-set fetch a typed error. go-oidc reports a
// fetch failure as plain text, indistinguishable from a bad signature once it
// is wrapped, and matching its text would be a guess. Here every way the
// fetch can fail (a transport error, a status other than 200, a body that is
// not a JSON key set) becomes ErrKeysUnavailable, which errors.Is finds
// through go-oidc's and net/http's wrapping. The error text carries the class
// only: the underlying one can quote the URL or a response body.
type keysTransport struct{ base http.RoundTripper }

func (t keysTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(r)
	if err != nil {
		return nil, fmt.Errorf("%w: transport", ErrKeysUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: status %d", ErrKeysUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeysBody+1))
	_ = resp.Body.Close()
	if err != nil || len(body) > maxKeysBody {
		return nil, fmt.Errorf("%w: body", ErrKeysUnavailable)
	}
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if json.Unmarshal(body, &doc) != nil || len(doc.Keys) == 0 {
		return nil, fmt.Errorf("%w: not a key set", ErrKeysUnavailable)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// keyProbe is one Verify call's record of whether the key set could not be
// fetched. go-oidc's IDTokenVerifier folds the key set's error into text with
// %v, so the typed error never reaches Verify's caller through the return
// value; probingKeySet sees it where it is still typed and leaves a mark here.
type keyProbe struct{ unavailable atomic.Bool }

type keyProbeKey struct{}

// probingKeySet is an oidc.KeySet that reports a failed fetch to the Verify
// call that asked.
type probingKeySet struct{ inner oidc.KeySet }

func (k probingKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	payload, err := k.inner.VerifySignature(ctx, jwt)
	if err != nil && errors.Is(err, ErrKeysUnavailable) {
		if p, ok := ctx.Value(keyProbeKey{}).(*keyProbe); ok {
			p.unavailable.Store(true)
		}
	}
	return payload, err
}
