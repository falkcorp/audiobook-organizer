// file: core/integrations/providerhttp/egress_dial.go
// version: 1.0.0
// guid: b7d2c940-3f1e-4a85-9c6d-0e48a1f27b53
// last-edited: 2026-11-19

package providerhttp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// DialContext is integrations.GuardedDialer: the egress guard for an adapter
// that opens its own connection (core.email's SMTP sender), where there is no
// URL and no http.Transport to put the guard in.
//
// It is the decision transport.pick makes for an HTTP request, applied to
// "host:port": the private-capable guard when the instance may reach this
// endpoint privately (the Trust the Registry derived from the instance's
// effective origins, or core.integrations.egress.lan_allow), the public-only
// guard otherwise and always for EgressPublicOnly. The guard itself is the
// transport's own dial, so the resolution is done once, every answer is
// checked, and the literal address that was checked is the one dialled: there
// is no policy here beyond choosing the guard.
//
// Order, as Acquire's: the disabled switch, then ctx, then the dial. A refusal
// by the guard is returned as the *integrations.Error (ClassRefusedLocal,
// ReasonEgress) an HTTP request gets: it names neither the host nor the
// address, and it is a verdict on the destination, so it is never fed to the
// breaker or a hold. A lookup that fails is the resolver's error, unchanged
// (the destination was not judged; a retry resolves again), and nothing is
// dialled.
func (g *gate) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	r := g.r
	if r.isDisabled() {
		r.met.refused(g.ref, ReasonDisabled)
		return nil, refuse(g.ref, "dial", ReasonDisabled, time.Time{}, integrations.ErrDisabled)
	}
	if err := ctx.Err(); err != nil {
		return nil, canceled(g.ref, "dial", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		r.met.refused(g.ref, ReasonEgress)
		return nil, refuse(g.ref, "dial", ReasonEgress, time.Time{},
			fmt.Errorf("%w: the destination is not a host and a port", ErrBlockedAddress))
	}
	tr := r.strict
	if g.pol.Egress != integrations.EgressPublicOnly && g.private != nil &&
		g.private(net.JoinHostPort(strings.ToLower(host), port)) {
		tr = r.lan
	}
	conn, err := tr.DialContext(ctx, network, addr)
	if err != nil {
		if isEgressRefusal(err) {
			r.met.refused(g.ref, ReasonEgress)
			return nil, refuse(g.ref, "dial", ReasonEgress, time.Time{}, err)
		}
		return nil, err
	}
	return conn, nil
}

var _ integrations.GuardedDialer = (*gate)(nil)
