// file: core/httpapi/auth.go
// version: 0.1.0
// guid: 7fcabdc3-44d5-41a0-b723-ba043b5d00e0
// last-edited: 2026-11-21

package httpapi

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/httpmw"
	"github.com/falkcorp/mediacore/rbac"
)

// Stages 12 and 13 on the router (05-http-api §4.2 rows 12 and 13). Until
// SetAuth is called the router runs neither, and Build refuses every non-Public
// native route and every LegacyAuthCore mount (ErrNoAuthService): the router
// never serves a route it cannot authorize. SetAuth is the one predicate for
// both halves, "auth is set": it wraps the routes in stages 12 and 13 and it
// lifts the refusal, so a router that no longer refuses cannot be left
// without the stages that make the refusal unnecessary.

// ErrAuthWiring is SetAuth's refusal of a wiring it cannot run with.
var ErrAuthWiring = errors.New("httpapi: auth wiring refused")

// Authenticator is what the router takes of auth.Service: stage 12's
// middleware and the session cookie stage 13 clears. *auth.Service is one.
type Authenticator interface {
	Middleware() func(http.Handler) http.Handler
	SessionCookie(c auth.ClientInfo, token string, expires time.Time) *http.Cookie
}

// AuthWiring is what SetAuth installs.
type AuthWiring struct {
	// Service is stage 12 (Middleware) and the cookie stage 13 clears. Required.
	Service Authenticator
	// Client resolves the connection for the cookie stage 13 clears. It must be
	// the resolver Service itself uses (auth.Deps.Client), so the cleared cookie
	// has the name and Secure attribute stage 12 reads. nil is ClientInfoFrom.
	Client func(*http.Request) auth.ClientInfo
	// OwnerRequired is core.auth.owner.required, read per request; the same
	// function every other reader of the setting gets. nil means required.
	OwnerRequired func() bool
	// ResolveAtom is the app's atom table (Stage13Options.ResolveAtom).
	ResolveAtom AtomResolver
	// Catalog answers whether a refused scope is Sensitive.
	Catalog *rbac.Catalog
	// Audit records each 403 stage 13 writes as core.permission.deny (the
	// store coalesces them). nil records nothing.
	Audit audit.Logger
	// Log receives a refusal's write failure.
	Log *slog.Logger
}

// routerAuth is the wiring after SetAuth: stage 12 built once.
type routerAuth struct {
	stage12 func(http.Handler) http.Handler
	opts    Stage13Options
}

// SetAuth wires stages 12 and 13 into the routes Build registers: native and
// raw routes, Infra routes whose permission is not Public, and the routes of a
// LegacyAuthCore mount that do not authenticate themselves (OwnAuth). A
// LegacyAuthGin mount and the "/" owner are untouched. It also lets Build
// accept the non-Public routes and LegacyAuthCore mounts it refuses without
// it. It must be called before Build, once; it returns an error, and changes
// nothing, for a nil Service, a second call or a call after Build.
func (r *Router) SetAuth(w AuthWiring) error {
	if w.Service == nil {
		return errors.Join(ErrAuthWiring, errors.New("a nil Service"))
	}
	client := w.Client
	if client == nil {
		client = ClientInfoFrom
	}
	opts := Stage13Options{
		OwnerRequired: w.OwnerRequired, ResolveAtom: w.ResolveAtom, Catalog: w.Catalog,
		ClearCookie: func(req *http.Request) *http.Cookie {
			return w.Service.SessionCookie(client(req), "", time.Time{})
		},
		Audit: w.Audit, Log: w.Log,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.late("SetAuth"); err != nil {
		return err
	}
	if r.auth != nil {
		return errors.Join(ErrAuthWiring, errors.New("auth is already set"))
	}
	r.auth = &routerAuth{stage12: w.Service.Middleware(), opts: opts}
	return nil
}

// authWrap puts stage 12 and then stage 13 around h for the route named by
// route ("METHOD /pattern"); r.mu is held and r.auth is set. The stages run
// inside stages 10 and 11 (chain.Route wraps the result) and outside the
// handler, so a refused request never reaches it.
func (r *Router) authWrap(perm rbac.Requirement, guards Guards, route string, h http.Handler) http.Handler {
	o := r.auth.opts
	o.Route = route
	s13, err := Stage13(perm, guards, o)
	if err != nil {
		// Unreachable: SetAuth always sets ClearCookie. Refuse everything
		// rather than serve the route without stage 13.
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			WriteError(w, req, &Error{Status: http.StatusInternalServerError, Code: "internal_error"}, o.Log)
		})
	}
	return r.auth.stage12(s13(h))
}

// authRuns reports whether stages 12 and 13 wrap e: auth is set and the route
// is native or raw, or Infra with a permission other than Public (which keeps
// /livez, /readyz and /metrics unauthenticated when they are Public).
func (r *Router) authRuns(e *routeEntry) bool {
	if r.auth == nil {
		return false
	}
	switch e.info.Kind {
	case RouteKindNative, RouteKindRaw:
		return true
	case RouteKindInfra:
		return e.info.Perm.Kind() != rbac.KindPublic
	}
	return false
}

// ClientInfoFrom is the connection of r as stage 0 resolved it, for auth.Deps.Client
// and every package that reports a client. It is built on auth.DirectClient and
// only adds to it, so it can never report a request more local than auth's own
// fallback would:
//
//   - Peer is the unmapped TCP peer, never the attributed client address.
//   - Forwarded is true when any forwarding header is present, trusted peer or not.
//   - TrustedProxy is true when the peer is in the trusted-proxy set (stage 0),
//     whether or not it sent a usable client-IP header.
//   - Addr is the client the trusted proxy named in the configured client-IP
//     header (stage 0's attribution), else the peer.
//   - HTTPS is the connection's TLS, or, from a trusted proxy only, an
//     X-Forwarded-Proto or Forwarded proto of https. An untrusted peer's claim
//     is never believed.
//   - OnLink is a global IPv6 Addr inside a prefix of one of this host's
//     interfaces.
//
// ClientInfo.Local reads only Peer, Forwarded and TrustedProxy, the first two
// exactly as DirectClient sets them and the last only ever added.
func ClientInfoFrom(r *http.Request) auth.ClientInfo {
	return clientInfoFrom(r, interfaceAddrs)
}

func clientInfoFrom(r *http.Request, ifaces func() ([]net.Addr, error)) auth.ClientInfo {
	c := auth.DirectClient(r)
	ctx := r.Context()
	c.TrustedProxy = httpmw.PeerTrusted(ctx)
	if addr, attributed := httpmw.ClientAddr(ctx); attributed && addr.IsValid() {
		c.Addr = addr
	}
	if c.TrustedProxy && !c.HTTPS {
		c.HTTPS = forwardedHTTPS(r)
	}
	c.OnLink = onLink(c.Addr, ifaces)
	return c
}

// forwardedHTTPS reports that a trusted proxy said the client connection was
// HTTPS: X-Forwarded-Proto's last value (the proxy's own append), or the last
// Forwarded element's proto.
func forwardedHTTPS(r *http.Request) bool {
	if v := r.Header.Values("X-Forwarded-Proto"); len(v) > 0 {
		last := v[len(v)-1]
		if i := strings.LastIndexByte(last, ','); i >= 0 {
			last = last[i+1:]
		}
		return strings.EqualFold(strings.TrimSpace(last), "https")
	}
	if v := r.Header.Values("Forwarded"); len(v) > 0 {
		last := v[len(v)-1]
		if i := strings.LastIndexByte(last, ','); i >= 0 {
			last = last[i+1:]
		}
		for _, part := range strings.Split(last, ";") {
			k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok && strings.EqualFold(k, "proto") {
				return strings.EqualFold(strings.Trim(val, `"`), "https")
			}
		}
	}
	return false
}

// interfaceAddrs is the host's interface addresses.
func interfaceAddrs() ([]net.Addr, error) { return net.InterfaceAddrs() }

// onLink reports that a is a global IPv6 address inside a prefix one of this
// host's interfaces holds (a home LAN numbered from the ISP's /64). Private,
// link-local, loopback and IPv4 addresses are already throttle sources of
// their own, so only a global IPv6 address is looked up.
func onLink(a netip.Addr, ifaces func() ([]net.Addr, error)) bool {
	a = a.WithZone("").Unmap()
	if !a.IsValid() || !a.Is6() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLoopback() {
		return false
	}
	addrs, err := ifaces()
	if err != nil {
		return false
	}
	for _, x := range addrs {
		n, ok := x.(*net.IPNet)
		if !ok {
			continue
		}
		p, ok := netip.AddrFromSlice(n.IP)
		if !ok || p.Unmap().Is4() {
			continue
		}
		ones, _ := n.Mask.Size()
		if netip.PrefixFrom(p, ones).Masked().Contains(a) {
			return true
		}
	}
	return false
}

// routeLabel is the "METHOD /pattern" stage 13 records on a refusal.
func routeLabel(info RouteInfo) string {
	return strings.TrimSpace(info.Method + " " + info.Pattern)
}
