// file: core/httpapi/basicgate.go
// version: 1.0.0
// guid: 49fa9648-7658-42df-8e5f-edbfdb58bbf8
// last-edited: 2026-11-10

package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
)

// Stage 8, the optional HTTP Basic perimeter gate (05-http-api §4.2 stage 8,
// F3-05). It is built and tested here but not inserted into any chain: F3-07,
// the config flip, puts it at position 8 of the outer chain for the legacy
// mount, binds its inputs and deletes gin's basicauth.go in the same PR.
//
// The gate reads three keys, each through core config, never by itself:
//   - core.http.basic_gate.enabled: the effective value. Core config resolves
//     it, fail-closed from F3-02 (§10); the gate only reads it.
//   - core.http.basic_gate.username: the effective value. An empty username
//     keeps HEAD's behaviour: ":<password>" passes (§10, SEC6-9).
//   - core.http.basic_gate.password: a secret, read through BasicGatePassword.
//     It is not fail-closed: an operator value locks it like any key, which is
//     also the recovery from a lock-out (05-config §1.3.5, OPS9-7).
//
// Exempt per route, never per mount (SEC-14), with HEAD's exempt set
// (internal/server/middleware/basicauth.go:34,40-46,69 at HEAD; COR4-9); see
// basicGateRoutes. No password, no entry (R5-1(c)): while the gate is enabled
// and the password is unset, cleared, empty or unreadable, every non-exempt
// request gets 401 and a presented empty password never matches.

// BasicGatePasswordKey is the password's config key, the name the startup log
// and the health detail give for an enabled gate with no usable password.
const BasicGatePasswordKey = "core.http.basic_gate.password"

// BasicGateNoPasswordToken is the health reason stage 8 reports, with
// health.StatusDegraded, while the gate is enabled with no usable password
// (05-health §5H.3.1). It is a posture token: it appears only on
// authenticated endpoints, never in /health's unauthenticated answer, so a
// caller the gate stops learns nothing through the exemption (§4.2 stage 8,
// SEC-7; 05-health §5H.3.3).
const BasicGateNoPasswordToken = "basic_gate_no_password"

// basicGateChallenge is the gate's one WWW-Authenticate value, HEAD's realm
// string unchanged (basicauth.go basicChallenge), so clients that remembered
// credentials for the realm keep them across the move.
const basicGateChallenge = `Basic realm="Audiobook Organizer"`

// BasicGateSettings are the effective core.http.basic_gate.enabled and
// .username, read together from one published config snapshot, so a request
// never mixes the fields of two saves.
type BasicGateSettings struct {
	Enabled  bool
	Username string
}

// BasicGatePassword is stage 8's password resolver seam (name coined by
// F3-05): it returns the effective core.http.basic_gate.password, or an
// error when it has none. F3-07 binds it to the secret store of F3-03 and
// maps secrets.ErrNotSet and secrets.ErrKeyUnavailable onto the error. The
// gate treats any error, and a zero Value, as "no usable password" and
// refuses (fail closed). An operator value is the effective value however
// the stored row reads; the resolver, not the gate, applies that lock.
type BasicGatePassword interface {
	BasicGatePassword(ctx context.Context) (secrets.Value, error)
}

// BasicGateOptions are stage 8's inputs other than the route facts, which
// the router derives itself (basicGateRoutes).
type BasicGateOptions struct {
	// Settings reads the effective enabled switch and username; required.
	Settings func() BasicGateSettings
	// Password resolves the effective password; required.
	Password BasicGatePassword
	// Health receives the gate's posture: StatusUp, or StatusDegraded with
	// BasicGateNoPasswordToken. nil reports nothing.
	Health health.Reporter
	// Log receives the posture transitions; nil discards them. No line ever
	// carries a credential: the gate logs the key's name, never a value.
	Log *slog.Logger
}

// basicGatePosture is what the gate last told health and the log.
type basicGatePosture uint32

const (
	postureUnknown    basicGatePosture = iota // nothing reported yet
	postureOff                                // the gate is disabled
	postureReady                              // enabled with a usable password
	postureNoPassword                         // enabled with no usable password
)

// basicGate is stage 8. It is safe for concurrent use: its inputs are the
// caller's concurrency-safe readers, and the posture is guarded by mu.
type basicGate struct {
	settings func() BasicGateSettings
	password BasicGatePassword
	exempt   func(*http.Request) bool
	health   health.Reporter
	log      *slog.Logger

	// posture is read without mu on every request; a change is made under
	// mu, so the report and the log line of each transition are in order.
	posture atomic.Uint32
	mu      sync.Mutex
}

// errBasicGateInput is a missing required input of the stage.
var errBasicGateInput = errors.New("httpapi: basic gate needs Settings, Password and the route facts")

// newBasicGate builds stage 8 from o and exempt, the router's route facts
// (basicGateRoutes). Each required input is checked here, so a gate that
// cannot read its switch or its password is never built.
func newBasicGate(o BasicGateOptions, exempt func(*http.Request) bool) (*basicGate, error) {
	if o.Settings == nil || o.Password == nil || exempt == nil {
		return nil, errBasicGateInput
	}
	logger := o.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &basicGate{settings: o.Settings, password: o.Password, exempt: exempt, health: o.Health, log: logger}, nil
}

// wrap is stage 8 around next: a refused request gets 401 with the gate's
// Basic challenge and no body, as HEAD's gate answered (WriteBasicChallenge).
func (g *basicGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.refuses(r) {
			w.Header().Set("WWW-Authenticate", basicGateChallenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refuses is the gate's verdict for r, in HEAD's order: a disabled gate
// passes everything; an exempt route passes; an enabled gate with no usable
// password refuses whatever the request presents (comparing two empty
// strings would admit "<username>:", 05-authn §11.1 item 5); a request
// without Basic credentials is refused; otherwise both the username and the
// password must match, each compared in constant time, both always computed.
func (g *basicGate) refuses(r *http.Request) bool {
	s := g.settings()
	if !s.Enabled {
		g.observe(postureOff, nil)
		return false
	}
	if g.exempt(r) {
		return false
	}
	want, ok := g.effectivePassword(r.Context())
	if !ok {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		return true
	}
	userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(s.Username)) == 1
	passMatch := subtle.ConstantTimeCompare([]byte(pass), want) == 1
	return !userMatch || !passMatch
}

// effectivePassword resolves the password of an enabled gate and records the
// posture it implies. ok is false for an error or an empty value: no usable
// password.
func (g *basicGate) effectivePassword(ctx context.Context) (want []byte, ok bool) {
	v, err := g.password.BasicGatePassword(ctx)
	if err != nil || v.IsZero() {
		g.observe(postureNoPassword, err)
		return nil, false
	}
	g.observe(postureReady, nil)
	return v.Bytes(), true
}

// checkPosture reads the gate's settings and password now and reports the
// posture to health and the log; it returns true while the gate is enabled
// with no usable password. F3-07 calls it at start, which is the startup line
// naming BasicGatePasswordKey, and after each change of the gate's keys, so
// the health token follows a saved or cleared password without a request.
func (g *basicGate) checkPosture(ctx context.Context) (noPassword bool) {
	if !g.settings().Enabled {
		g.observe(postureOff, nil)
		return false
	}
	_, ok := g.effectivePassword(ctx)
	return !ok
}

// observe records p as the gate's posture; on a change it reports p to
// health and logs a transition into or out of postureNoPassword. err is the
// resolver's error behind postureNoPassword, nil for an empty value; a
// resolver's error names the failure, never the secret.
func (g *basicGate) observe(p basicGatePosture, err error) {
	if basicGatePosture(g.posture.Load()) == p {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	prev := basicGatePosture(g.posture.Load())
	if prev == p {
		return
	}
	g.posture.Store(uint32(p))
	switch {
	case p == postureNoPassword:
		attrs := []any{"key", BasicGatePasswordKey}
		if err != nil {
			attrs = append(attrs, "error", err)
		}
		g.log.Error("httpapi: Basic gate enabled with no usable password; every non-exempt request answers 401", attrs...)
	case prev == postureNoPassword:
		g.log.Info("httpapi: Basic gate password usable again", "key", BasicGatePasswordKey)
	}
	if g.health == nil {
		return
	}
	if p == postureNoPassword {
		g.health.Report(health.Result{Status: health.StatusDegraded, Reason: BasicGateNoPasswordToken,
			Detail: "core.http.basic_gate.enabled is on and " + BasicGatePasswordKey + " is unset, empty or unreadable"})
		return
	}
	g.health.Report(health.Result{Status: health.StatusUp})
}

// basicGateFacts is what stage 8 knows of one registered pattern. It is
// registered into a facts-only ServeMux that is consulted with Handler and
// never serves a request.
type basicGateFacts struct {
	probe       bool       // a Public infra probe (basicGateProbe)
	basicExempt bool       // a BasicExempt legacy route
	ownAuth     []ginRoute // an OwnAuth legacy pattern's manifest entries, nil otherwise
	rootOwned   bool       // a legacy pattern of the mount that owns "/"
}

func (basicGateFacts) ServeHTTP(http.ResponseWriter, *http.Request) {}

// exempts reports a route stage 8 exempts. A probe or a BasicExempt route is
// exempt for every request its pattern matches. An OwnAuth pattern is exempt
// only for a request the legacy router routes to one of its entries, as HEAD
// decided by the handler chain gin matched (basicauth.go:69): the method is
// the entry's own (a GET pattern also matches HEAD in a ServeMux, never in
// gin) and the decoded path matches the entry's gin path, so a path that only
// the generalised pattern of a collision set matches, which gin routes to no
// member, stays gated as at HEAD.
func (f basicGateFacts) exempts(req *http.Request) bool {
	if f.probe || f.basicExempt {
		return true
	}
	for _, e := range f.ownAuth {
		if e.method == req.Method && ginPathMatches(e.path, req.URL.Path) {
			return true
		}
	}
	return false
}

// ginPathMatches reports that gin's route path gin (":name" one non-empty
// segment, "*name" the rest of the path) matches the decoded request path p,
// which is what gin routes on.
func ginPathMatches(gin, p string) bool {
	gs := strings.Split(strings.TrimPrefix(gin, "/"), "/")
	ps := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, g := range gs {
		if strings.HasPrefix(g, "*") {
			return i <= len(ps)
		}
		if i >= len(ps) {
			return false
		}
		if strings.HasPrefix(g, ":") {
			if ps[i] == "" {
				return false
			}
			continue
		}
		if g != ps[i] {
			return false
		}
	}
	return len(gs) == len(ps)
}

// basicGateProbe reports a Public infra probe, the infra routes stage 8
// exempts: /livez and /readyz. /metrics is infra too, but HEAD gates it, so
// "Public infra" alone is not the rule.
func basicGateProbe(ri RouteInfo) bool {
	if ri.Kind != RouteKindInfra || ri.Perm.Kind() != rbac.KindPublic {
		return false
	}
	return ri.Pattern == "/livez" || ri.Pattern == "/readyz"
}

// basicGateRoutes returns stage 8's exemption for a request, derived from the
// router's registrations; r.mu is held, after a successful Build. Before
// dispatch nothing has matched yet (the route wrapper records the pattern
// after stage 8), so the gate keeps its own facts-only ServeMux of the very
// patterns Build registered, built with Build's own derivation (the native
// keys, nativeNodes, legacyNode and collisionPlan), and matches a request as
// the dispatcher does (isClean, slashRedirect). HEAD's exempt set
// (basicauth.go:34,40-46,69):
//
//   - a matched pattern that is a Public infra probe (/livez, /readyz);
//   - a matched legacy pattern whose route is OwnAuth (the ABS routes whose
//     gin chain runs the ABS authenticator), for a request gin routes to one
//     of its entries (basicGateFacts.exempts). The decision follows the
//     matched route, never a path prefix, so no ".." or %2e%2e spelling can
//     widen it (an unclean path matches nothing);
//   - a matched legacy pattern whose route is BasicExempt (/api/health and
//     /api/v1/health). Per route: a method the manifest does not list for
//     the path matches no pattern and is gated, where HEAD's path test
//     exempted it into a 404;
//   - the "/" owner's static-asset predicate (LegacyMount.BasicExemptAssets
//     of the ServesSPA mount, or SPAOptions.BasicExemptAssets of Router.SPA;
//     nil exempts nothing) over r.URL.Path, for a request no pattern matches
//     and for a matched legacy pattern of the ServesSPA mount. HEAD's rule was
//     a path test on every request gin saw, the gin engine's wildcard routes
//     included (GET /api/v1/books/x.js was exempt); the mount that owns "/"
//     is that engine, so all its answers keep the rule. A native or infra
//     route, and another mount's route, never take it: a core route ending in
//     ".js" stays gated.
//
// Everything else is gated, the ABS routes without the ABS authenticator
// included; /ping, /status and /login stay gated.
func (r *Router) basicGateRoutes() (func(*http.Request) bool, error) {
	if !r.built {
		return nil, fmt.Errorf("%w: route facts before Build", errBasicGateInput)
	}
	mux := http.NewServeMux()
	seen := map[string]bool{}
	var errs []error
	for _, e := range r.routes {
		if len(e.errs) > 0 || e.handler == nil {
			continue
		}
		key := strings.TrimSpace(e.info.Method + " " + e.info.Pattern)
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := register(mux, key, basicGateFacts{probe: basicGateProbe(e.info)}); err != nil {
			errs = append(errs, err)
		}
	}
	nodes := r.nativeNodes()
	for mi, m := range r.mounts {
		if mi >= len(r.legacy.routes) {
			continue
		}
		for _, lr := range r.legacy.routes[mi] {
			n, nerrs := r.legacyNode(mi, m, lr)
			errs = append(errs, nerrs...)
			if n != nil {
				nodes = append(nodes, n)
			}
		}
	}
	plan, perrs := collisionPlan(nodes)
	errs = append(errs, perrs...)
	for _, reg := range plan {
		// The members of one pattern agree on OwnAuth and BasicExempt
		// (collision rule 4), so the first speaks for all.
		lr := reg.members[0].route
		facts := basicGateFacts{basicExempt: lr.BasicExempt, rootOwned: r.mounts[reg.mount].ServesSPA}
		if lr.OwnAuth {
			facts.ownAuth = reg.admits
		}
		if err := register(mux, reg.pat.String(), facts); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		// Fail closed: a router whose facts cannot be derived exempts nothing.
		return nil, fmt.Errorf("httpapi: basic gate route facts: %w", err)
	}
	assets := r.rootAssets()
	return func(req *http.Request) bool {
		rootOwned := true // no pattern matched: the "/" owner's rule applies
		if isClean(req.URL.Path) {
			if h, pattern := mux.Handler(req); pattern != "" && !slashRedirect(pattern, req) {
				f, _ := h.(basicGateFacts)
				if f.exempts(req) {
					return true
				}
				rootOwned = f.rootOwned
			}
		}
		return rootOwned && assets != nil && assets(req.URL.Path)
	}, nil
}

// rootAssets is the "/" owner's static-asset predicate, nil for none; r.mu is
// held.
func (r *Router) rootAssets() func(string) bool {
	if r.spa != nil {
		return r.spaOpt.BasicExemptAssets
	}
	for _, m := range r.mounts {
		if m.ServesSPA {
			return m.BasicExemptAssets
		}
	}
	return nil
}
