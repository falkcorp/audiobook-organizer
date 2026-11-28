// file: internal/cfaccess/late_verifier_test.go
// version: 0.1.0
// guid: 0b7e3c58-4a19-4f62-8d05-e1a96c3b7d40
// last-edited: 2026-11-21

package cfaccess

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	coreauth "github.com/falkcorp/mediacore/auth"
	coreoidc "github.com/falkcorp/mediacore/auth/oidc"
)

// TestLateVerifier_UnboundVerifiesNothing: before Bind (and for an app with
// Access off) both halves answer ErrNotConfigured, never a claim and never the
// service-token answer that ClientAddrTrust would believe.
func TestLateVerifier_UnboundVerifiesNothing(t *testing.T) {
	l := NewLateVerifier()
	if l.Bound() {
		t.Fatal("a new handle reports itself bound")
	}
	if c, err := l.Verify(context.Background(), goodJWT); c != nil || !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Verify unbound = %v, %v; want nil and ErrNotConfigured", c, err)
	}
	if v, err := l.VerifyIssued(context.Background(), goodJWT); v != nil || !errors.Is(err, ErrNotConfigured) {
		t.Errorf("VerifyIssued unbound = %v, %v; want nil and ErrNotConfigured", v, err)
	}
	if errors.Is(ErrNotConfigured, ErrNonIdentityAssertion) {
		t.Error("ErrNotConfigured must not be a service-token answer: ClientAddrTrust believes those")
	}
}

type forwardingVerifier struct{ tokenVerifier }

func (v *forwardingVerifier) VerifyIssued(ctx context.Context, raw string) (*VerifiedAssertion, error) {
	c, err := v.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &VerifiedAssertion{Claims: c}, nil
}

// TestLateVerifier_BindForwardsAndUnbindStops: a bound handle is its verifier,
// Bind(nil) is refused (it would read as configured), and Unbind goes back to
// refusing everything.
func TestLateVerifier_BindForwardsAndUnbindStops(t *testing.T) {
	l := NewLateVerifier()
	if err := l.Bind(nil); err == nil {
		t.Fatal("Bind(nil) was accepted")
	}
	if err := l.Bind(&forwardingVerifier{}); err != nil {
		t.Fatal(err)
	}
	if !l.Bound() {
		t.Fatal("not bound after Bind")
	}
	if c, err := l.Verify(context.Background(), goodJWT); err != nil || c == nil || c.Email != "dick@wayne.example.test" {
		t.Errorf("Verify bound = %v, %v", c, err)
	}
	if _, err := l.VerifyIssued(context.Background(), "forged"); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Errorf("VerifyIssued of a bad token = %v, want the bound verifier's refusal", err)
	}
	l.Unbind()
	if _, err := l.Verify(context.Background(), goodJWT); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Verify after Unbind = %v, want ErrNotConfigured", err)
	}
}

// TestExtension_AccessNotConfiguredIsNoCredential: an extension over an unbound
// LateVerifier takes the Access header as nothing (HEAD's binder ignored it
// with no verifier), never as a refusal and never as an identity, while its
// OwnerSet still reads the owner e-mail, so the owner gate is derived with
// Access off too (core.auth.owner.required, ruling b).
func TestExtension_AccessNotConfiguredIsNoCredential(t *testing.T) {
	late := NewLateVerifier()
	res := &resolverFake{user: &coreauth.User{ID: "01JBATCAVE0000000000000021"}}
	owner := ""
	ext, err := Extension(ExtensionConfig{
		Verifier: late, Resolver: res,
		Policy:     coreoidc.Policy{Provider: "cfaccess"},
		OwnerEmail: func() string { return owner },
		Log:        slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://signal.example.test/api/v1/books", nil)
	r.Header.Set(CFAccessHeader, goodJWT)
	a, err := ext.Authenticator.Authenticate(r)
	if a != nil || !errors.Is(err, coreauth.ErrNoCredential) {
		t.Fatalf("Authenticate with Access unconfigured = %+v, %v; want nil and ErrNoCredential", a, err)
	}
	if len(res.calls) != 0 {
		t.Errorf("the resolver was asked about %d claim(s) with no verifier", len(res.calls))
	}
	if ext.OwnerSet() {
		t.Error("OwnerSet true with no owner e-mail")
	}
	owner = "bruce@wayne.example.test"
	if !ext.OwnerSet() {
		t.Error("OwnerSet false with an owner e-mail set and Access unconfigured: core would derive owner.required=false")
	}
}

// TestClientAddrTrust_UnboundLateVerifierBelievesNothing: with Access
// unconfigured a local peer's CF-Connecting-IP is not believed, so the header
// never picks a throttle bucket.
func TestClientAddrTrust_UnboundLateVerifierBelievesNothing(t *testing.T) {
	tr := ClientAddrTrust{Verifier: NewLateVerifier(), IsLocalPeer: localPeer}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8484/login", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set(CFAccessHeader, goodJWT)
	r.Header.Set("CF-Connecting-IP", "192.0.2.77")
	if a, ok := tr.AccessClientAddr(r); ok {
		t.Fatalf("AccessClientAddr believed %v with Access unconfigured", a)
	}
}
