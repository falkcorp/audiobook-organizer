// file: internal/cfaccess/late_resolver_test.go
// version: 0.1.0
// guid: 4c9e2b71-0d5a-48f3-a1e6-8b7d3f20c915
// last-edited: 2026-11-21

package cfaccess

import (
	"errors"
	"log/slog"
	"net/http"
	"testing"

	coreauth "github.com/falkcorp/mediacore/auth"
	coreoidc "github.com/falkcorp/mediacore/auth/oidc"
)

// TestExtension_UnboundLateResolverAssertsNobody: the Extension is built
// before the kernel binds the account resolver, over core's late handle. While
// the handle is unbound (before the boot, or after close) a request carrying a
// perfectly valid Access token produces no Assertion: the extension treats the
// handle's ErrResolverUnbound as it treats any resolver error it does not name,
// as no credential, so the request falls through to the session authenticator
// and is never admitted, linked or created by the extension.
func TestExtension_UnboundLateResolverAssertsNobody(t *testing.T) {
	acc := newAccess(t)
	late := coreoidc.NewLateResolver()
	ext, err := Extension(ExtensionConfig{
		Verifier: acc.verifier(), Resolver: late,
		Policy:     coreoidc.Policy{Provider: "cfaccess", AllowedDomains: []string{"wayne.example.test"}, LinkExistingByEmail: true},
		OwnerEmail: func() string { return ownerMail },
		Log:        slog.New(slog.DiscardHandler),
		Clock:      acc.clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &rig{acc: acc, ext: ext, owner: ownerMail}
	good := acc.token(human("dick@wayne.example.test"))

	for name, mod := range map[string]func(*http.Request){"header": nil, "cookie": withCookies(good)} {
		raw := good
		if name == "cookie" {
			raw = ""
		}
		a, err := g.do(raw, mod)
		if a != nil {
			t.Errorf("%s: an unbound resolver produced the assertion %+v", name, a)
		}
		if !errors.Is(err, coreauth.ErrNoCredential) {
			t.Errorf("%s: error %v, want ErrNoCredential so the session authenticator runs", name, err)
		}
		if errors.Is(err, coreauth.ErrInvalidCredentials) {
			t.Errorf("%s: an unbound resolver refused the request outright", name)
		}
	}
}
