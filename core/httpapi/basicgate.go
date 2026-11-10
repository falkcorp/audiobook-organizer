// file: core/httpapi/basicgate.go
// version: 1.1.0
// guid: 49fa9648-7658-42df-8e5f-edbfdb58bbf8
// last-edited: 2026-11-10

package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

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

// BasicGateDefaultRealm is the realm of a gate whose BasicGateOptions.Realm
// is empty. Core names no app: an app that wants clients to keep the
// credentials they remembered for its old realm passes that realm (F3-07
// passes HEAD's "Audiobook Organizer", basicauth.go basicChallenge).
const BasicGateDefaultRealm = "Restricted"

// BasicGateSettings are the effective core.http.basic_gate.enabled and
// .username, read together from one published config snapshot, so a request
// never mixes the fields of two saves.
//
// The password cannot join them: a published snapshot never carries a
// secret's value (config/store.go refuses a secret key in c/config/), so it is
// always a live read of the secret store, and no seam returns the pair at
// once. The gate reads the settings first and the password second, on
// purpose. Config's Apply commits the rows and the staged secrets in one batch
// and only then publishes the snapshot (config/apply.go), so a request that
// reads the new settings also reads the new password; the one mixed pair a
// save can produce is the previous settings with the new password. Both
// credentials must match, so that pair admits only a client holding the new
// password, never a holder of the replaced one, and its worst effect is a
// brief 401 for the saver presenting the new username. Reading the password
// first would let a request pair the old password with the new settings.
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
// the router's Build records (basicGateRoutes).
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
	// Realm is the realm of the gate's Basic challenge; empty means
	// BasicGateDefaultRealm. It may not hold a double quote, a backslash or
	// a control character, which would break the quoted-string.
	Realm string
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
	settings  func() BasicGateSettings
	password  BasicGatePassword
	exempt    func(*http.Request) bool
	health    health.Reporter
	log       *slog.Logger
	challenge string // the WWW-Authenticate value
	// equal compares two credential digests; subtle.ConstantTimeCompare,
	// a field only so a test can see what is compared.
	equal func(x, y []byte) int

	// mu serialises checkPosture, the only writer of posture and the only
	// caller of health and the posture log; see checkPosture.
	mu      sync.Mutex
	posture basicGatePosture
}

// errBasicGateInput is a missing or malformed input of the stage.
var errBasicGateInput = errors.New("httpapi: basic gate needs Settings, Password, a valid Realm and the route facts")

// newBasicGate builds stage 8 from o and exempt, the router's route facts
// (basicGateRoutes). Each required input is checked here, so a gate that
// cannot read its switch or its password is never built.
func newBasicGate(o BasicGateOptions, exempt func(*http.Request) bool) (*basicGate, error) {
	if o.Settings == nil || o.Password == nil || exempt == nil {
		return nil, errBasicGateInput
	}
	realm := o.Realm
	if realm == "" {
		realm = BasicGateDefaultRealm
	}
	if strings.ContainsFunc(realm, func(c rune) bool { return c == '"' || c == '\\' || c < 0x20 || c == 0x7f }) {
		return nil, fmt.Errorf("%w: realm %q", errBasicGateInput, realm)
	}
	logger := o.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &basicGate{settings: o.Settings, password: o.Password, exempt: exempt, health: o.Health, log: logger,
		challenge: `Basic realm="` + realm + `"`, equal: subtle.ConstantTimeCompare}, nil
}

// wrap is stage 8 around next: a refused request gets 401 with the gate's
// Basic challenge and no body, as HEAD's gate answered (WriteBasicChallenge).
func (g *basicGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.refuses(r) {
			w.Header().Set("WWW-Authenticate", g.challenge)
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
// password must match. The settings are read before the password (see
// BasicGateSettings). A request never reports posture: only checkPosture
// does, so a request that read the password before a change cannot report
// the old posture after the check that saw the change.
func (g *basicGate) refuses(r *http.Request) bool {
	s := g.settings()
	if !s.Enabled {
		return false
	}
	if g.exempt(r) {
		return false
	}
	want, err := g.usablePassword(r.Context())
	if err != nil {
		return true // no usable password: refused; checkPosture reports it
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		return true
	}
	return !g.credentialsMatch(user, pass, s.Username, want)
}

// credentialsMatch compares the presented credentials with the effective
// ones. Each side is hashed with SHA-256 first, so the constant-time
// compares always see two 32-byte digests and a length difference leaks
// nothing (subtle.ConstantTimeCompare returns at once on unequal lengths);
// both digests and both compares are always computed.
func (g *basicGate) credentialsMatch(user, pass, wantUser string, wantPass []byte) bool {
	gotU, wantU := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(wantUser))
	gotP, wantP := sha256.Sum256([]byte(pass)), sha256.Sum256(wantPass)
	userMatch := g.equal(gotU[:], wantU[:])
	passMatch := g.equal(gotP[:], wantP[:])
	return userMatch&passMatch == 1
}

// errBasicGateEmptyPassword is an empty effective password: no usable
// password, though the resolver did not err.
var errBasicGateEmptyPassword = errors.New("httpapi: basic gate password is empty")

// usablePassword resolves the password of an enabled gate. A resolver error
// or an empty value (errBasicGateEmptyPassword) is no usable password; a
// resolver error that also hands back bytes is still refused.
func (g *basicGate) usablePassword(ctx context.Context) ([]byte, error) {
	v, err := g.password.BasicGatePassword(ctx)
	if err != nil {
		return nil, err
	}
	if v.IsZero() {
		return nil, errBasicGateEmptyPassword
	}
	return v.Bytes(), nil
}

// checkPosture reads the gate's settings and password now and reports the
// posture to health and the log; it returns true while the gate is enabled
// with no usable password. F3-07 calls it at start, which is the startup line
// naming BasicGatePasswordKey, and after each change of the gate's keys, so
// the health token follows a saved or cleared password; a request refuses on
// what it reads but reports nothing.
//
// It holds mu across the reads and the report, so checks are totally
// ordered: each report comes from reads made after the previous report, and
// the last report always reflects the last check's reads. Without the lock a
// check that read the old password could report after one that read the new.
func (g *basicGate) checkPosture(ctx context.Context) (noPassword bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.settings().Enabled {
		g.report(postureOff, nil)
		return false
	}
	if _, err := g.usablePassword(ctx); err != nil {
		g.report(postureNoPassword, err)
		return true
	}
	g.report(postureReady, nil)
	return false
}

// report records p as the gate's posture; on a change it reports p to
// health and logs a transition into or out of postureNoPassword. err is the
// reason behind postureNoPassword (the resolver's error, or
// errBasicGateEmptyPassword); it names the failure, never the secret. g.mu
// is held.
func (g *basicGate) report(p basicGatePosture, err error) {
	prev := g.posture
	if prev == p {
		return
	}
	g.posture = p
	switch {
	case p == postureNoPassword:
		g.log.Error("httpapi: Basic gate enabled with no usable password; every non-exempt request answers 401",
			"key", BasicGatePasswordKey, "error", err)
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

// basicGateFacts is what stage 8 knows of one registered pattern. Build
// records them as it registers the pattern (gateRoute), in the same loops and
// after the same skips, dedups and refusals, so the gate never derives them a
// second time.
type basicGateFacts struct {
	probe       bool       // a Public infra probe (basicGateProbe)
	basicExempt []ginRoute // a BasicExempt legacy pattern's manifest entries, nil otherwise
	ownAuth     []ginRoute // an OwnAuth legacy pattern's manifest entries, nil otherwise
	rootOwned   bool       // a legacy pattern of the mount that owns "/"
}

// gateRoute is the handler Build registers for every pattern: the route's
// own handler, which serves the request, and stage 8's facts of the pattern,
// which the gate reads from the same ServeMux before dispatch.
type gateRoute struct {
	http.Handler
	facts basicGateFacts
}

// legacyGateFacts are stage 8's facts of one legacy registration of mount m.
// The members of one pattern agree on OwnAuth and BasicExempt (collision rule
// 4), so the first speaks for all; the entries are the ones gin routes the
// pattern to.
func legacyGateFacts(reg registration, m LegacyMount) basicGateFacts {
	lr := reg.members[0].route
	f := basicGateFacts{rootOwned: m.ServesSPA}
	if lr.BasicExempt {
		f.basicExempt = reg.admits
	}
	if lr.OwnAuth {
		f.ownAuth = reg.admits
	}
	return f
}

// exempts reports a route stage 8 exempts. A probe is exempt for every
// request its pattern matches. A BasicExempt or OwnAuth pattern is exempt
// only for a request the legacy router routes to one of its entries, as HEAD
// decided by the handler chain gin matched (basicauth.go:69): the method is
// the entry's own (a GET pattern also matches HEAD in a ServeMux, never in
// gin) and the decoded path matches the entry's gin path, so a path that only
// the generalised pattern of a collision set matches, which gin routes to no
// member, stays gated.
func (f basicGateFacts) exempts(req *http.Request) bool {
	return f.probe || routesToEntry(f.basicExempt, req) || routesToEntry(f.ownAuth, req)
}

// routesToEntry reports that gin routes req to one of entries.
func routesToEntry(entries []ginRoute, req *http.Request) bool {
	for _, e := range entries {
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
			// "*name" needs the "/" before it: gin routes /files/ and
			// /files/a to /files/*path, never /files (FullPath "").
			return i < len(ps)
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

// basicGateRoutes returns stage 8's exemption for a request, from the facts
// the last successful Build recorded; r.mu is held. Before dispatch nothing
// has matched yet (the route wrapper records the pattern after stage 8), so
// the gate asks the ServeMux that Build serves which pattern the request
// matches, as the dispatcher does (isClean, slashRedirect), and reads the
// facts Build attached to that pattern's handler (gateRoute). HEAD's exempt
// set (basicauth.go:34,40-46,69):
//
//   - a matched pattern that is a Public infra probe (/livez, /readyz);
//   - a matched legacy pattern whose route is OwnAuth (the ABS routes whose
//     gin chain runs the ABS authenticator), for a request gin routes to one
//     of its entries (basicGateFacts.exempts). The decision follows the
//     matched route, never a path prefix, so no ".." or %2e%2e spelling can
//     widen it (an unclean path matches nothing);
//   - a matched legacy pattern whose route is BasicExempt (/api/health and
//     /api/v1/health), on the same per-entry rule: a method the manifest does
//     not list for the path is gated, HEAD included, where HEAD's path test
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
		return nil, fmt.Errorf("%w: route facts before a successful Build", errBasicGateInput)
	}
	mux, assets := r.gateMux, r.gateAssets
	return func(req *http.Request) bool {
		rootOwned := true // no pattern matched: the "/" owner's rule applies
		if isClean(req.URL.Path) {
			if h, pattern := mux.Handler(req); pattern != "" && !slashRedirect(pattern, req) {
				// Every pattern Build registers is a gateRoute; any other
				// answer is the mux's own, whose zero facts exempt nothing
				// and take no asset rule (fail closed).
				gr, _ := h.(gateRoute)
				if gr.facts.exempts(req) {
					return true
				}
				rootOwned = gr.facts.rootOwned
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
