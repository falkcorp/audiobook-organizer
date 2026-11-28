// file: internal/cfaccess/late_verifier.go
// version: 0.1.0
// guid: 6d2f9a14-8b73-4e05-a3c1-70e5b4d96f28
// last-edited: 2026-11-21

package cfaccess

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
)

// ErrNotConfigured is a verifier's answer when Cloudflare Access is not
// configured for this app (no team domain or no AUD): nothing can be verified.
// It is neither a bad token nor a service token. The extension treats it as no
// credential (HEAD's gin binder ignored the header when Access was not
// configured), and ClientAddrTrust believes nothing, so a header a caller made
// up never picks its throttle bucket.
var ErrNotConfigured = errors.New("cfaccess: Cloudflare Access is not configured")

// CFVerifier is the whole verifier: both halves the extension (VerifyIssued)
// and the client-address rule (Verify) read. *CFAccessVerifier is one.
type CFVerifier interface {
	CFAssertionVerifier
	AssertionVerifier
}

// LateVerifier is the Access verifier as a handle: the extension is built
// before the config boot (app.Options.Authenticators), and the team domain and
// AUD it verifies against are read from settings only after it. It answers
// ErrNotConfigured until Bind, which is also what an app with Access switched
// off sees for good; it never admits anything on its own.
type LateVerifier struct{ cur atomic.Pointer[CFVerifier] }

// NewLateVerifier returns an unbound handle.
func NewLateVerifier() *LateVerifier { return &LateVerifier{} }

// Bind sets the verifier the handle forwards to. A nil verifier is refused
// (use Unbind): binding nothing would read as configured. A second Bind
// replaces the first, as a boot that rebuilds its verifier needs.
func (l *LateVerifier) Bind(v CFVerifier) error {
	if v == nil {
		return errors.New("cfaccess: LateVerifier.Bind needs a verifier")
	}
	l.cur.Store(&v)
	return nil
}

// Unbind removes the bound verifier; the handle answers ErrNotConfigured again.
func (l *LateVerifier) Unbind() { l.cur.Store(nil) }

// Bound reports whether a verifier is bound.
func (l *LateVerifier) Bound() bool { return l.cur.Load() != nil }

// Verify implements CFAssertionVerifier.
func (l *LateVerifier) Verify(ctx context.Context, rawJWT string) (*legacyoauth.IdentityClaims, error) {
	p := l.cur.Load()
	if p == nil {
		return nil, ErrNotConfigured
	}
	return (*p).Verify(ctx, rawJWT)
}

// VerifyIssued implements AssertionVerifier.
func (l *LateVerifier) VerifyIssued(ctx context.Context, rawJWT string) (*VerifiedAssertion, error) {
	p := l.cur.Load()
	if p == nil {
		return nil, ErrNotConfigured
	}
	return (*p).VerifyIssued(ctx, rawJWT)
}
