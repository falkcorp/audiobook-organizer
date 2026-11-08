// file: core/httpapi/router.go
// version: 1.0.4
// guid: 592b4b18-11e9-41ab-b2a5-23b0c23383c2
// last-edited: 2026-11-07

package httpapi

import (
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// Router is built by the kernel only (kernel/ imports it; modules never see
// it). The kernel registers from one goroutine, but a module reaches Handle
// through its *Group from any goroutine, so every method is safe for
// concurrent use (Router.mu). None panics: errors are recorded and returned
// by Build. After Build the returned handler and every RouteInfo are
// immutable (05-http-api §9).
type Router struct {
	opts Options
	deps Deps

	// mu guards the registration state below. The kernel registers from one
	// goroutine, but a module holds its *Group and can call Handle from a
	// goroutine it started in Register, concurrently with the kernel's P7
	// calls or Build (the late case errBuilt catches); every method that
	// reads or writes this state takes mu.
	mu     sync.Mutex
	routes []*routeEntry
	claims []claim
	groups map[string]*Group
	mounts []LegacyMount
	spa    http.Handler
	spaOpt SPAOptions // stage 8 reads BasicExemptAssets (R1-17)
	public map[string]map[string]bool
	errs   []error
	built  bool        // set by Build: a registration after it could never be served
	legacy legacyState // legacy.go: manifests, admissions and built infos (R1-14)

	// stage is a test seam standing in for the perimeter stages between
	// stage 2 and the mux (R1-17's chain takes its place); observe receives
	// the route carrier's value after the handler returned, which stage 2
	// will record (05-http-api §4.2 stage 2).
	stage   func(http.Handler) http.Handler
	observe func(route string)
}

// Options configure a Router. Fields with no consumer before R1-17's
// perimeter (TrustedProxies, ClientIPHeader, CORS, Limits) arrive with it.
type Options struct {
	CoreGroupBase string // "/api/core/v1" when empty
	OpenAPI       OpenAPIOptions
	// ServerName is server.address on every series and span stage 2 records:
	// a constant, never the request's Host (MOD10-2). The kernel fills it from
	// app.Options.ServiceName (06 §6.4.5), the value it also passes as
	// metrics.Options.ServiceName (ABO: "audiobook-organizer"; default: AppID).
	ServerName string
}

// OpenAPIOptions are the document-wide values of Router.OpenAPI.
type OpenAPIOptions struct {
	// Version is info.version of every document the router builds; the kernel
	// overrides it per owner (the module's Manifest.Version, version.Version
	// for core).
	Version string
	// CookieName is the session cookie the sessionCookie scheme names
	// (05-authn §4.3: core.auth.session.cookie_name, default "<AppID>_session").
	CookieName string
}

// Deps are the Router's dependencies. Auth, RBAC and Audit are R2 additions
// (05-http-api §3, COR2-9, COR3-3); until they exist Build refuses every
// non-Public native route (ErrNoAuthService).
type Deps struct {
	Log           *slog.Logger
	Metrics       *metrics.Scope       // "<namespace>.http": core's own http families (§11)
	MeterProvider metric.MeterProvider // the App's Provider.MeterProvider(); stage 2's instruments (R1-17)
}

// RouteKind is the kind of a registered route.
type RouteKind uint8

const (
	RouteKindNative RouteKind = iota + 1 // Handle
	RouteKindRaw                         // HandleRaw (with its first consumer)
	RouteKindLegacy                      // a legacy manifest route (R1-14)
	RouteKindInfra                       // Router.Infra
	RouteKindSPA                         // the "/" owner: Router.SPA or a ServesSPA legacy mount
)

// RouteInfo describes one registered route.
type RouteInfo struct {
	Owner      string // module ID or "core"
	Method     string
	Pattern    string
	Name       string
	Perm       rbac.Requirement
	Guards     Guards
	RateClass  RateClass
	Kind       RouteKind // Native, Raw, Legacy, Infra, SPA (Facade: deferred-build, §3.6)
	OwnAuth    bool      // legacy route authenticated by its own (ABS) authenticator (§8.2)
	AuthMethod string    // the auth method an OwnAuth route names (§8.2)
}

// SPAOptions is what the kernel passes with the shell handler at P8 (06
// §6.5.1). BasicExemptAssets is stage 8's static-asset exemption for the
// SPA's own paths; nil exempts nothing (FE5-10).
type SPAOptions struct {
	BasicExemptAssets func(path string) bool
}

// LegacyAuth says who authenticates a legacy mount (05-http-api §8.2).
type LegacyAuth uint8

const (
	LegacyAuthCore LegacyAuth = iota + 1 // core stages 12-13 run for the mount
	LegacyAuthGin                        // stages 12-13 off; only for app.Options.LegacyAuthGinUntil (R1)
)

// LegacyMount is an http.Handler mounted on a module's legacy prefixes
// (05-http-api §8.2). Router.MountLegacy and Build (legacy.go) register its
// manifest routes as ServeMux patterns under the route collision rule (§4.8),
// apply the LegacyAuthGin/OwnAuth admissions and answer its Redirects.
type LegacyMount struct {
	Owner             string                     // "books"; set by module.Registrar
	Handler           http.Handler               // *gin.Engine
	Routes            iter.Seq[LegacyRoute]      // manifest: every gin route; each its own mux pattern or a member of one generalised pattern (§4.8)
	Auth              LegacyAuth                 // LegacyAuthGin only while app.Options.LegacyAuthGinUntil names Owner (R1) | LegacyAuthCore
	ServesSPA         bool                       // owns "/" and serves the SPA through gin NoRoute (§4.10)
	DefaultCSP        func(*http.Request) string // stage 5 policy for this mount's responses, evaluated per response (FE2-1)
	CSPReport         string                     // Reporting-Endpoints URL for DefaultCSP (ABO: "/api/v1/csp-report")
	Redirects         []LegacyRedirect           // the /api/* -> /api/v1/* rule and its exclusions (§4.9 item 3)
	TopSegments       []string                   // top-level server paths of the mount: reserved IDs + NonSPA (03 §3.6)
	SPAShared         []string                   // top-level segments shared with SPA pages, excluded from NonSPA (§3.5, SEC3-16)
	BuildID           string                     // stage 5's X-App-Build until the shell flip (FE3-2, FE4-1, R5-7)
	BasicExemptAssets func(path string) bool     // stage 8 exempts these paths while the mount owns "/" (COR4-9)
}

// LegacyRoute is one entry of a legacy mount's manifest (05-http-api §8.2).
type LegacyRoute struct {
	Method, GinPath string
	Perm            rbac.Requirement // the native requirement books' atom table gives the route's atom (§8.3)
	Guards          Guards
	Public          bool      // on the reviewed list of 15 (raw/09 §2.4)
	OwnAuth         bool      // the route's gin chain runs the ABS authenticator; stages 8, 12, 13 skip it (SEC-14)
	BasicExempt     bool      // stage 8 skips it (COR4-9)
	AuthMethod      string    // required iff OwnAuth (SEC2-17)
	RateClass       RateClass // RateDefault iff the gin chain includes the API rate limiter today, else RateExempt
	NoCompress      bool      // stage 9 skips it (COR3-6)
}

// claim is one subtree (trailing "/") or exact path an owner occupies.
type claim struct{ owner, prefix string }

// NewRouter returns an empty router.
func NewRouter(opts Options, deps Deps) *Router {
	if opts.CoreGroupBase == "" {
		opts.CoreGroupBase = "/api/core/v1"
	}
	return &Router{opts: opts, deps: deps, groups: map[string]*Group{}, public: map[string]map[string]bool{}}
}

// record keeps err for Build; r.mu is held.
func (r *Router) record(err error) { r.errs = append(r.errs, err) }

// errBuilt is a registration after Build: it is refused and logged, because
// the handler Build returned can never serve it.
var errBuilt = errors.New("httpapi: registration after Router.Build")

// late reports, and logs at ERROR, a registration made after Build; r.mu
// is held.
func (r *Router) late(what string) error {
	if !r.built {
		return nil
	}
	err := fmt.Errorf("%w: %s ignored", errBuilt, what)
	if r.deps.Log != nil {
		r.deps.Log.Error("httpapi: registration after Router.Build ignored", "registration", what)
	}
	return err
}

func (r *Router) add(e *routeEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.late("route "+describe(e.info)) != nil {
		return
	}
	r.routes = append(r.routes, e)
}

// ModuleGroup returns the group of module moduleID at /api/<id>/v1 and claims
// /api/<id>/ for it. Every call for one ID returns the same group.
func (r *Router) ModuleGroup(moduleID string) *Group {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.groups[moduleID]; ok {
		return g
	}
	root := "/api/" + moduleID
	g := &Group{router: r, owner: moduleID, root: root, base: root + "/v1"}
	r.groups[moduleID] = g
	if err := r.late("group " + root); err != nil {
		g.err = err
		return g
	}
	if err := r.claim(moduleID, root+"/"); err != nil {
		r.record(err)
	}
	return g
}

// CoreGroup returns core's group at Options.CoreGroupBase (/api/core/v1),
// used only by core/internal/<capability>http (03 §3.3, COR2-19).
func (r *Router) CoreGroup() *Group {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.groups["core"]; ok {
		return g
	}
	base := r.opts.CoreGroupBase
	root := base[:strings.LastIndexByte(base, '/')]
	g := &Group{router: r, owner: "core", root: root, base: base}
	r.groups["core"] = g
	if err := r.late("group " + root); err != nil {
		g.err = err
		return g
	}
	if err := r.claim("core", root+"/"); err != nil {
		r.record(err)
	}
	return g
}

// AllowPublic records the names of owner's native routes that may use
// rbac.Public: the module's Manifest.PublicRoutes, handed over by the kernel
// (06 §6.4.2, COR3-3). Build refuses rbac.Public on any other module route
// (ErrPublicNotAllowed). Core's own Public routes are a reviewed golden
// instead (TestPublicRoutesGolden) and need no entry.
func (r *Router) AllowPublic(owner string, names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.late("AllowPublic "+owner) != nil {
		return
	}
	set := r.public[owner]
	if set == nil {
		set = map[string]bool{}
		r.public[owner] = set
	}
	for _, n := range names {
		set[n] = true
	}
}

// Infra registers a core infrastructure route (/livez, /readyz, /metrics),
// with an optional method ("GET /livez"). A non-Public perm is enforced
// like any route's (§4.2, SEC2-14), so before auth exists only Public infra
// routes build (ErrNoAuthService).
func (r *Router) Infra(pattern string, perm rbac.Requirement, h http.Handler) {
	method, p := splitPattern(pattern)
	e := &routeEntry{handler: h}
	e.info = RouteInfo{Owner: "core", Method: method, Pattern: p, Perm: perm, RateClass: RateExempt, Kind: RouteKindInfra}
	if h == nil {
		e.errs = append(e.errs, fmt.Errorf("%w: infra %s: nil handler", ErrSchema, pattern))
	}
	if err := cleanUnder(p, "/"); err != nil || p == "/" {
		e.errs = append(e.errs, fmt.Errorf("%w: infra %s: not a clean path below /", ErrOutsideClaim, pattern))
	}
	r.add(e)
}

// splitPattern splits "GET /x" into its method and path.
func splitPattern(p string) (method, path string) {
	if m, rest, ok := strings.Cut(p, " "); ok {
		return m, strings.TrimSpace(rest)
	}
	return "", p
}

// SPA makes h the owner of "/" (05-http-api §4.10). It returns
// ErrPrefixCollision while a legacy mount ServesSPA or another SPA is set.
// The "/" owner runs no rate limit, body limit, authentication or
// authorization and is recorded as RouteInfo{Kind: SPA, Perm: rbac.Public,
// RateClass: RateExempt}.
func (r *Router) SPA(h http.Handler, o SPAOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.late("SPA"); err != nil {
		return err
	}
	if h == nil {
		err := fmt.Errorf("%w: SPA handler is nil", ErrSchema)
		r.record(err)
		return err
	}
	if r.rootOwned() {
		err := fmt.Errorf("%w: \"/\" already has an owner (FE-1)", ErrPrefixCollision)
		r.record(err)
		return err
	}
	r.spa, r.spaOpt = h, o
	return nil
}

// rootOwned reports a "/" owner; r.mu is held.
func (r *Router) rootOwned() bool {
	if r.spa != nil {
		return true
	}
	return slices.ContainsFunc(r.mounts, func(m LegacyMount) bool { return m.ServesSPA })
}

// rootOwner returns the "/" owner's handler and owner name, or nil; r.mu is
// held.
func (r *Router) rootOwner() (http.Handler, string) {
	if r.spa != nil {
		return r.spa, "core"
	}
	for _, m := range r.mounts {
		if m.ServesSPA {
			return m.Handler, m.Owner
		}
	}
	return nil, ""
}

// Claim records a subtree ("/api/v1/", trailing slash) or exact path
// ("/ping") that owner occupies (05-http-api §4.9). Two owners whose claims
// overlap are ErrPrefixCollision, returned here and again by Build; one rule,
// no claim kinds and no yielding (SIMP-22). A claim must be a clean literal
// path below "/": no ".", ".." or empty segment and no wildcard, because the
// overlap test compares claims as strings.
func (r *Router) Claim(owner, prefix string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.late("Claim " + prefix); err != nil {
		return err
	}
	err := r.claim(owner, prefix)
	if err != nil {
		r.record(err)
	}
	return err
}

// claim is Claim without the recording; r.mu is held.
func (r *Router) claim(owner, prefix string) error {
	if owner == "" || !strings.HasPrefix(prefix, "/") || prefix == "/" || strings.Contains(prefix, "{") ||
		cleanUnder(prefix, "/") != nil {
		return fmt.Errorf("%w: %s: claim %q is not a clean literal path below /", ErrPrefixCollision, owner, prefix)
	}
	for _, o := range r.claims {
		if o.owner != owner && claimsOverlap(o.prefix, prefix) {
			return fmt.Errorf("%w: %q (%s) and %q (%s)", ErrPrefixCollision, o.prefix, o.owner, prefix, owner)
		}
	}
	r.claims = append(r.claims, claim{owner: owner, prefix: prefix})
	return nil
}

func claimsOverlap(a, b string) bool {
	if strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/") {
		return true
	}
	return (strings.HasSuffix(a, "/") && strings.HasPrefix(b, a)) ||
		(strings.HasSuffix(b, "/") && strings.HasPrefix(a, b))
}

// ReservedSegments returns the first path segments core itself serves: "api",
// "v<digits>", "livez", "readyz", "metrics", "health", "auth", "events",
// "assets", "static", "ui" (the SPA's hashed-asset and core UI paths,
// FE2-11). It is static: the kernel evaluates it at P1, before any
// MountLegacy (P6), so it carries no app segment (COR2-19). One input of
// module.ReservedIDs() (03 §3.6). A fresh slice per call (R5). "v<digits>"
// is a token for the pattern ^v[0-9]+$, not a literal segment.
func ReservedSegments() []string {
	return []string{"api", "v<digits>", "livez", "readyz", "metrics", "health", "auth", "events", "assets", "static", "ui"}
}

// nonSPAStatic are the core inputs of NonSPA (05-http-api §4.10): the claimed
// /api/ space, health and metrics explicitly (a case or slash variant of a
// probe path must never answer 200 text/html), auth and events.
func nonSPAStatic() []string { return []string{"api", "health", "metrics", "auth", "events"} }

// neverNonSPA are reserved segments that are the SPA handler's own path
// space and therefore never NonSPA.
func neverNonSPA() []string { return []string{"assets", "static", "ui"} }

// NonSPASegments returns the first segments behind NonSPA, sorted: data for
// the kernel's P8 check ProblemSPASegmentCollision (06 §6.10; §4.10, FE2-7,
// FE3-9). It is the union of the core inputs, the first segment of every
// Infra pattern and every mount's TopSegments, minus every mount's SPAShared
// and the SPA's own assets, static and ui.
func (r *Router) NonSPASegments() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nonSPASegments()
}

// nonSPASegments is NonSPASegments; r.mu is held.
func (r *Router) nonSPASegments() []string {
	set := map[string]bool{}
	for _, s := range nonSPAStatic() {
		set[s] = true
	}
	for _, e := range r.routes {
		if e.info.Kind == RouteKindInfra {
			if seg := firstSegment(e.info.Pattern); seg != "" {
				set[seg] = true
			}
		}
	}
	for _, m := range r.mounts {
		for _, s := range m.TopSegments {
			set[strings.ToLower(s)] = true
		}
	}
	for _, m := range r.mounts {
		for _, s := range m.SPAShared {
			delete(set, strings.ToLower(s))
		}
	}
	for _, s := range neverNonSPA() {
		delete(set, s)
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// NonSPA returns the predicate the "/" owner consults (05-http-api §4.10, for
// ui.Options; FE-3, FE2-6): true for a request that must never get the SPA's
// index.html, which is any method but GET and HEAD, and any path whose first
// segment, lower-cased and with trailing slashes ignored, is in
// NonSPASegments. The predicate is a snapshot of the segments at call time.
func (r *Router) NonSPA() func(*http.Request) bool {
	return nonSPAPredicate(r.NonSPASegments())
}

// nonSPAPredicate is NonSPA over a snapshot of the segments.
func nonSPAPredicate(segments []string) func(*http.Request) bool {
	set := map[string]bool{}
	for _, s := range segments {
		set[s] = true
	}
	return func(req *http.Request) bool {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			return true
		}
		return set[firstSegment(req.URL.Path)]
	}
}

// firstSegment is the lower-cased first segment of p ("/API/V1/x" -> "api").
func firstSegment(p string) string {
	p = strings.TrimLeft(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	return strings.ToLower(p)
}

// Routes returns every registered route: native and infra routes in
// registration order, then the legacy manifest routes of the last successful
// Build (legacy.go), then the "/" owner when one is set (Kind SPA).
// The sequence is a snapshot taken when iteration starts, so a yield that
// calls back into the router cannot deadlock.
func (r *Router) Routes() iter.Seq[RouteInfo] {
	return func(yield func(RouteInfo) bool) {
		r.mu.Lock()
		infos := make([]RouteInfo, 0, len(r.routes)+len(r.legacy.infos)+1)
		for _, e := range r.routes {
			infos = append(infos, e.info)
		}
		infos = append(infos, r.legacy.infos...)
		if _, owner := r.rootOwner(); owner != "" {
			infos = append(infos, RouteInfo{Owner: owner, Pattern: "/", Name: "spa", Perm: rbac.Public,
				RateClass: RateExempt, Kind: RouteKindSPA})
		}
		r.mu.Unlock()
		for _, ri := range infos {
			if !yield(ri) {
				return
			}
		}
	}
}

// routeSnapshot is the registered routes at the time of the call.
func (r *Router) routeSnapshot() []*routeEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.routes)
}

// Build validates every registration (05-http-api §5 step 3), registers the
// routes into a fresh ServeMux and returns the root handler, or errors.Join
// of every problem. Build registers inside recover(), so a ServeMux conflict
// is ErrPatternConflict, never a panic (§4.8).
func (r *Router) Build() (http.Handler, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.built = true
	errs := slices.Clone(r.errs)
	errs = append(errs, r.checkClaims()...)
	mux := http.NewServeMux()
	seen := map[string]string{}
	names := map[string]bool{}
	for _, e := range r.routes {
		errs = append(errs, e.errs...)
		errs = append(errs, r.checkPerm(e)...)
		key := strings.TrimSpace(e.info.Method + " " + e.info.Pattern)
		if prev, dup := seen[key]; dup {
			errs = append(errs, fmt.Errorf("%w: %s registered by %s and %s", ErrDuplicateRoute, key, prev, e.info.Owner))
			continue
		}
		seen[key] = e.info.Owner
		if e.info.Kind == RouteKindNative {
			n := e.info.Owner + "_" + e.info.Name
			if names[n] {
				errs = append(errs, fmt.Errorf("%w: %s: route name %q used twice", ErrDuplicateRoute, e.info.Owner, e.info.Name))
			}
			names[n] = true
		}
		if len(e.errs) > 0 || e.handler == nil {
			continue
		}
		if err := register(mux, key, wrapRoute(e)); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, r.buildLegacy(mux)...)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	root := r.rootHandler()
	d := &dispatcher{
		mux:          mux,
		root:         root,
		nonSPA:       nonSPAPredicate(r.nonSPASegments()),
		legacyClaims: r.legacyClaims(),
		redirect:     r.legacyRedirect(),
		log:          r.deps.Log,
	}
	var next http.Handler = d
	if r.stage != nil {
		next = r.stage(next)
	}
	observe := r.observe
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c := &routeCarrier{}
		next.ServeHTTP(w, req.WithContext(withCarrier(req.Context(), c)))
		if observe != nil {
			observe(c.get())
		}
	}), nil
}

// register adds one pattern to mux, turning ServeMux's panic (two
// conflicting patterns, or a pattern it cannot parse) into ErrPatternConflict.
func register(mux *http.ServeMux, pattern string, h http.Handler) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", ErrPatternConflict, p)
		}
	}()
	mux.Handle(pattern, h)
	return nil
}

// wrapRoute writes the route's pattern (method and host stripped) into the
// request's route carrier before the route's own handler runs (05-http-api
// §4.2 stage 2), so stage 2 reads it after the handler returns even when a
// stage in between cloned the request.
func wrapRoute(e *routeEntry) http.Handler {
	pattern, h := e.info.Pattern, e.handler
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if c := carrierFrom(req.Context()); c != nil {
			c.set(pattern)
		}
		h.ServeHTTP(w, req)
	})
}

// checkClaims reports every pair of overlapping claims of two owners; r.mu
// is held.
func (r *Router) checkClaims() []error {
	var errs []error
	for i, a := range r.claims {
		for _, b := range r.claims[i+1:] {
			if a.owner != b.owner && claimsOverlap(a.prefix, b.prefix) {
				errs = append(errs, fmt.Errorf("%w: %q (%s) and %q (%s)", ErrPrefixCollision, a.prefix, a.owner, b.prefix, b.owner))
			}
		}
	}
	return errs
}

// checkPerm applies R6 and the R1 permission rules to one route: a zero
// requirement is ErrNoPermission; a native route may require only its own
// namespace or core:admin, or Public/AnyAuthenticated (05-rbac §4.9,
// ErrForeignScope; the delegated and legacy kinds are legacy-route only); a
// module route's Public must be listed in its PublicRoutes
// (ErrPublicNotAllowed); and with no auth service every non-Public route is
// ErrNoAuthService. r.mu is held.
func (r *Router) checkPerm(e *routeEntry) []error {
	ri := e.info
	what := describe(ri)
	req := ri.Perm
	if req.IsZero() {
		return []error{fmt.Errorf("%w: %s", ErrNoPermission, what)}
	}
	var errs []error
	switch req.Kind() {
	case rbac.KindPublic:
		if ri.Kind == RouteKindNative && ri.Owner != "core" && !r.public[ri.Owner][ri.Name] {
			errs = append(errs, fmt.Errorf("%w: %s (%s)", ErrPublicNotAllowed, what, ri.Name))
		}
		return errs
	case rbac.KindAuthenticated:
	case rbac.KindAllOf, rbac.KindAnyOf:
		for _, p := range req.Permissions() {
			if p != rbac.CoreAdmin && p.Namespace() != ri.Owner {
				errs = append(errs, fmt.Errorf("%w: %s requires %s", ErrForeignScope, what, p))
			}
		}
	default:
		errs = append(errs, fmt.Errorf("%w: %s: requirement %s is for legacy routes only", ErrForeignScope, what, req))
	}
	errs = append(errs, fmt.Errorf("%w: %s requires %s", ErrNoAuthService, what, req))
	return errs
}

// legacyClaims returns the claims of every owner that has a legacy mount;
// r.mu is held.
func (r *Router) legacyClaims() []string {
	owners := map[string]bool{}
	for _, m := range r.mounts {
		owners[m.Owner] = true
	}
	var out []string
	for _, c := range r.claims {
		if owners[c.owner] {
			out = append(out, c.prefix)
		}
	}
	return out
}
