// file: core/internal/httpmw/state.go
// version: 1.0.0
// guid: 1519fbdb-d993-45c7-afd5-d1d3296554cd
// last-edited: 2026-11-08

// Package httpmw implements the core perimeter stages of 05-http-api §4.2
// for core/httpapi: the fixed chain every request the kernel serves runs
// through, outermost first (stages 0-7 and 9 around the router, 10-11 inside
// each route's wrapper). Stage 8, the Basic gate, stays in the legacy gin
// engine until the config flip F3 (§8.1). httpapi composes the chain;
// nothing else imports this package.
package httpmw

import (
	"context"
	"net/http"
	"net/netip"
	"sync/atomic"
)

// State is the one per-request carrier the stages share (05-http-api §4.2
// stage 2). The chain's entry puts it in the context before stage 0, so a
// stage or handler that clones the request (r.WithContext) cannot lose it:
// every clone carries the same pointer. Fields written once by an outer
// stage before calling the next one (client, request ID) are plain; fields
// written inside the router or a handler and read by an outer stage after
// it returns are atomic.
type State struct {
	client          netip.Addr
	attributed      bool
	requestID       string
	clientRequestID string

	route  atomic.Pointer[string] // the route wrapper's pattern; "" or unset = unrouted
	policy atomic.Pointer[Policy] // the response owner's policy; nil = core's
	event  atomic.Pointer[string] // stage 7's security_event for the access log
	// inflight is the owner the in-flight gauge counted the request under.
	inflight atomic.Pointer[string]

	compressed   atomic.Bool  // stage 9 compressed the body
	uncompressed atomic.Int64 // bytes the handler wrote through stage 9 when it compressed
}

// Policy is what the owner of a response decides for the stages outside the
// router: stage 5's CSP and Reporting-Endpoints, stage 9's exclusion, and
// the owner label of core's own counters. The router sets it when it hands
// the request to an owner (a route wrapper, the "/" owner, a legacy-shape
// answer) and before that owner writes.
type Policy struct {
	Owner string // "core" or the module ID
	// CSP is the owner's Content-Security-Policy, evaluated per response
	// (FE2-1); nil is core's default-src 'none'; frame-ancestors 'self'.
	CSP func(*http.Request) string
	// CSPReport is the Reporting-Endpoints URL written with CSP.
	CSPReport string
	// NoCompress skips stage 9 (LegacyRoute.NoCompress, COR3-6).
	NoCompress bool
}

type stateKey struct{}

// withState returns ctx carrying st.
func withState(ctx context.Context, st *State) context.Context {
	return context.WithValue(ctx, stateKey{}, st)
}

// From returns the request's State, or nil outside a request the chain's
// entry has seen.
func From(ctx context.Context) *State {
	st, _ := ctx.Value(stateKey{}).(*State)
	return st
}

// SetRoute records the matched route's pattern, method and host stripped
// (05-http-api §4.2 stage 2). "" records none.
func (s *State) SetRoute(p string) { s.route.Store(&p) }

// Route is the recorded pattern, "" for an unrouted request.
func (s *State) Route() string {
	if p := s.route.Load(); p != nil {
		return *p
	}
	return ""
}

// SetPolicy records the response owner's policy.
func (s *State) SetPolicy(p *Policy) { s.policy.Store(p) }

// Policy is the response owner's policy, nil when core owns it.
func (s *State) Policy() *Policy { return s.policy.Load() }

// owner is the owner label of core's counters: the policy's owner, "core"
// when none is set.
func (s *State) owner() string {
	if p := s.policy.Load(); p != nil && p.Owner != "" {
		return p.Owner
	}
	return "core"
}

// ClientAddr returns the client address stage 0 resolved and whether it was
// attributed through a trusted proxy's client-IP header (httpapi.
// ClientAddrFrom). ok-less: the zero Addr outside a request stage 0 saw.
func ClientAddr(ctx context.Context) (netip.Addr, bool) {
	st := From(ctx)
	if st == nil {
		return netip.Addr{}, false
	}
	return st.client, st.attributed
}

// RequestID returns the request ID stage 1 assigned, "" outside a request
// stage 1 saw (httpapi.RequestID, 05-http-api §4.7).
func RequestID(ctx context.Context) string {
	if st := From(ctx); st != nil {
		return st.requestID
	}
	return ""
}
