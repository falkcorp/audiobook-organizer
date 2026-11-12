// file: core/internal/authhttp/authhttp.go
// version: 1.0.0
// guid: 5d7c1e0a-3b42-4f6d-8a19-c2e47b903f51
// last-edited: 2026-11-10

// Package authhttp is the HTTP surface of core's identity: every row of
// 05-authn §4.13 (sign-in and sign-out, setup and bootstrap, the provider list,
// the OIDC start and callback with the native-app hand-off of §4.7, the
// caller's own profile, password, sessions and API keys, every user's keys and
// sessions, the user list and the account actions, invites and temp-login
// links). It registers on httpapi's core group (/api/core/v1) and, when the app
// supplied an OIDC callback base, on the CoreFixedGroup for the two exact core
// patterns HEAD's identity providers are registered against.
//
// The package is a thin mapping layer. Every rule that protects an account
// lives in auth.Service (dominance, CanAssign, the interactive requirement,
// the throttle, epochs, audit); a handler binds the request, calls the
// Service with the caller's principal and maps its sentinel errors to the
// status and code of 05-authn §6. Stage 13 will enforce each route's Perm and
// Guards before a handler runs (F4-08); because no stage 13 exists at this
// release, every handler also checks its own route's requirement, so a route
// mounted bare refuses what its table row says.
//
// The invariants the tests pin:
//
//   - Tokens travel in request bodies, never in a path or a query (the one
//     exception is the OIDC provider's own redirect, which carries state and
//     code in the query as OAuth defines).
//   - A response never carries a stored secret, hash or mask. The credentials
//     a creation call mints (an API key, a bootstrap key, an invite, temp-login
//     or reset link) are returned once by that call and never by a list or a
//     read. A sign-in sets the session cookie and returns no token in its body.
//   - A sign-in, a link consumption or a lookup answers the same whether the
//     account exists or not.
//   - The auth-disabled principal has no user: every route that lists or reads
//     credential metadata answers 401 authentication_required for it.
//
// The package is dark until F4-08 mounts it: nothing imports it before then. It
// is layer L6 (03 R4, internal/<capability>http) and imported only by kernel.
package authhttp

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Deps are what the routes call. Every field but Profiles, Flow, Audit, Client
// and Log is required; Register refuses a missing one rather than serve a
// route that fails open. *auth.Service satisfies every identity interface
// below, and FromService fills them from one.
type Deps struct {
	Entry     Entry
	Cookies   Cookies
	Self      Self
	Accounts  Accounts
	Invites   Invites
	Users     UserLookup
	Dominance Dominance

	// Flow is the OIDC sign-in (auth/oidc's *Flow). nil serves an empty
	// provider list and answers the start and callback routes 404, as a
	// provider that does not exist.
	Flow Flow
	// Profiles writes PATCH /me. *auth.Service has no profile writer at this
	// release (seams.d/F4-05.md): nil answers 501 profile_edit_unavailable.
	Profiles Profiles
	// Audit records the 403 dominance_required refusals this package writes
	// (05-rbac §8 core.permission.deny, "recorded where the 403 is written").
	// nil records nothing.
	Audit audit.Logger
	// Client resolves a request's connection as httpapi resolved it
	// (trusted-proxy aware). nil uses the TCP peer, and HTTPS only when the
	// connection itself is TLS; the kernel passes auth.Deps.Client's value.
	Client func(*http.Request) auth.ClientInfo
	// OIDCCallbackBase is auth.Options.OIDCCallbackBase: the path prefix of
	// HEAD's callback and start (ABO: /api/v1/auth/oauth). Non-empty
	// registers those two routes on Router.CoreFixedGroup(base); empty (hello)
	// serves the callback at /api/core/v1/auth/oidc/{provider}/callback.
	OIDCCallbackBase string
	// Log receives internal errors; nil discards.
	Log *slog.Logger
}

// Errors Register returns.
var (
	errNoRouter = errors.New("authhttp: a nil router")
	errNoDep    = errors.New("authhttp: a required dependency is nil")
)

// api is what the handlers share. Its fields are fixed at Register.
type api struct {
	Deps
	log *slog.Logger
}

// Register registers every route of 05-authn §4.13 on r's core group and, when
// d.OIDCCallbackBase is set, the two exact OIDC patterns on its fixed group.
// It never panics; a bad registration is returned by r.Build.
func Register(r *httpapi.Router, d Deps) error {
	if r == nil {
		return errNoRouter
	}
	var missing []string
	for name, nilDep := range map[string]bool{
		"Entry": d.Entry == nil, "Cookies": d.Cookies == nil, "Self": d.Self == nil,
		"Accounts": d.Accounts == nil, "Invites": d.Invites == nil, "Users": d.Users == nil, "Dominance": d.Dominance == nil,
	} {
		if nilDep {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("%w: Deps.%s", errNoDep, strings.Join(missing, ", Deps."))
	}
	a := &api{Deps: d, log: d.Log}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	a.registerAll(r, a.routes())
	return nil
}

// registerAll registers rts on r's core group, and those marked fixed on the
// fixed group of the OIDC callback base.
func (a *api) registerAll(r *httpapi.Router, rts []route) {
	core := r.CoreGroup()
	var fixed *httpapi.Group
	for _, rt := range rts {
		g := core
		if rt.fixed {
			if fixed == nil {
				fixed = r.CoreFixedGroup(a.OIDCCallbackBase)
			}
			g = fixed
		}
		rt.reg(g)
	}
}

// The scopes the table names (05-rbac's fixed core:* catalog).
const (
	scopeSelf        rbac.Permission = "core:self"
	scopeUsersView   rbac.Permission = "core:users.view"
	scopeUsersManage rbac.Permission = "core:users.manage"
)

// maxBody bounds an identity body: a username, a password, a token, a handful
// of fields. httpapi's 1 MiB default is far more than any of them needs.
const maxBody = 16 << 10

// clientOf is the caller's connection as auth knows it.
func (a *api) clientOf(r *http.Request) auth.ClientInfo {
	if a.Client != nil {
		return a.Client(r)
	}
	peer := peerAddr(r)
	c := auth.ClientInfo{Peer: peer, UserAgent: r.UserAgent(), HTTPS: r.TLS != nil}
	if addr, attributed := httpapi.ClientAddrFrom(r.Context()); addr.IsValid() {
		c.Addr, c.TrustedProxy = addr, attributed
	} else {
		c.Addr = peer
	}
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			c.Forwarded = true
		}
	}
	return c
}

// peerAddr is the TCP peer of r, or the zero Addr when RemoteAddr does not
// parse.
func peerAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}
