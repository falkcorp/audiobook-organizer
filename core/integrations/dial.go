// file: core/integrations/dial.go
// version: 1.1.0
// guid: 169ba926-c33d-4860-a931-0359141a5366
// last-edited: 2026-11-19

package integrations

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// GuardedDialer is the connect an adapter that opens its own connections (the
// SMTP sender of core.email, a subprocess that dials) takes from its Gate: the
// same egress decision the outbound HTTP client applies, for a destination that
// is not a URL. It resolves the host once, refuses the whole dial when any
// answer is a private, loopback, link-local or reserved address the instance is
// not trusted for (InstanceConfig.Local, core.integrations.egress.lan_allow),
// and dials the literal address it checked, so a rebinding answer cannot
// redirect the connection after the check.
//
// It is an optional capability of a Gate, not a method of it: Gate has fakes
// everywhere, and an HTTP-only adapter never needs it. The Registry's own gate
// wrappers forward it. An adapter does not assert it itself: GuardedDial fails
// closed when the gate offers none.
type GuardedDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// ErrNoEgressGuard: an adapter asked for a guarded connection and nothing can
// guard it: its Gate offers no guarded dial (a Registry built without an
// outbound layer, a gate that wraps another and does not forward the dial, or no
// gate at all), or the sender was given no dial. Nothing is dialled. Every one
// of those refusals is an *Error of ClassRefusedLocal for which
// errors.Is(err, ErrNoEgressGuard) holds.
var ErrNoEgressGuard = errors.New("integrations: no egress guard is available for this connection")

// GuardedDial is the dial an adapter hands its connection code: g's
// GuardedDialer, or, when g offers none (g may be nil), a dial that refuses
// with a ClassRefusedLocal *Error wrapping ErrNoEgressGuard for ref. It never
// falls back to an unguarded dial.
func GuardedDial(g Gate, ref Ref) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if d, ok := g.(GuardedDialer); ok {
		return d.DialContext
	}
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, refused(ref, "dial", ErrNoEgressGuard)
	}
}

// DialContext refuses: the Registry has no outbound policy, so there is no
// egress policy to dial under. The refusal is ErrNoEgressGuard, with
// errNoOutbound as the reason.
func (g noGate) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, refused(g.ref, "dial", fmt.Errorf("%w: %w", ErrNoEgressGuard, errNoOutbound))
}

// DialContext refuses while core.integrations.disabled is set, then defers to
// the instance's gate. A gate that offers no guarded dial refuses: the wrapper
// must not turn "the outbound layer cannot guard this" into an unguarded dial.
func (g guardedGate) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if err := g.r.disabledErr(g.ref, "dial"); err != nil {
		return nil, err
	}
	d, ok := g.inner.(GuardedDialer)
	if !ok {
		return nil, refused(g.ref, "dial", ErrNoEgressGuard)
	}
	return d.DialContext(ctx, network, addr)
}

var (
	_ GuardedDialer = noGate{}
	_ GuardedDialer = guardedGate{}
)
