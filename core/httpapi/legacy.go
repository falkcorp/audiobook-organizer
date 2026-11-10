// file: core/httpapi/legacy.go
// version: 1.1.1
// guid: 8bdc5e36-9eb7-41f3-9bef-2c0c9e0c68c5
// last-edited: 2026-11-10

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/internal/httpmw"
	"github.com/falkcorp/mediacore/rbac"
)

// This file is the legacy half of the Router (05-http-api §8.2, §4.8): a
// legacy mount's manifest routes become ServeMux patterns, the route collision
// rule folds each conflicting same-owner set into one generalised pattern,
// the wrapper hands the matched pattern to the legacy handler, and the
// mount's Redirects answer the unmatched paths they cover.
//
// The legacy handler is a gin engine, and core never imports gin (N7). The
// post-match check of §4.8 therefore runs in the app: the wrapper puts the
// matched pattern and the manifest entries it admits in the request context,
// and the app's first gin middleware asks LegacyMatchAgrees with gin's own
// match (c.Request.Method, c.FullPath()) before any gin handler runs.

// LegacyRedirect is one redirect rule of a legacy mount (05-http-api §4.9
// item 3; ABO's /api/* -> /api/v1/* 301). It covers every unmatched request
// whose path lies under Prefix and under no claim of an owner without a
// legacy mount: Location returns the 301 target, or false when the rule
// excludes the request, which is then answered 404 in the legacy error shape.
// Every ServeMux pattern and every core or module claim is matched before it,
// so a claimed prefix (/api/core/, /api/<module>/) is never redirected.
type LegacyRedirect struct {
	Prefix   string                                           // a subtree, "/api/"
	Location func(r *http.Request) (location string, ok bool) // the 301 target; false = excluded (legacy 404)
}

// legacyState is what MountLegacy and the kernel's admissions record for
// Build (one per Router).
type legacyState struct {
	routes      [][]LegacyRoute            // per r.mounts index, the manifest as handed over
	authMethods map[string]map[string]bool // owner -> auth methods it registered (Registrar.AuthMethod)
	infos       []RouteInfo                // one per manifest route, after Build
}

// AllowAuthMethods records the auth methods owner registered with
// Registrar.AuthMethod (05-authn §3.3), handed over by the kernel at P7. An
// OwnAuth manifest route is admitted only when its AuthMethod is one of its
// owner's (SEC2-17, ErrOwnAuthMethod).
func (r *Router) AllowAuthMethods(owner string, methods ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.late("AllowAuthMethods "+owner) != nil {
		return
	}
	if r.legacy.authMethods == nil {
		r.legacy.authMethods = map[string]map[string]bool{}
	}
	set := r.legacy.authMethods[owner]
	if set == nil {
		set = map[string]bool{}
		r.legacy.authMethods[owner] = set
	}
	for _, m := range methods {
		set[m] = true
	}
}

// MountLegacy records a legacy mount (05-http-api §8.2) and returns the
// errors it can see at once: a missing Owner or Handler, an Auth that is
// neither LegacyAuthCore nor LegacyAuthGin, a malformed Redirects entry, and
// a second owner of "/" (FE-1). Routes is read here, once, before r.mu is
// taken, so an iterator that reads router state cannot deadlock. Every manifest
// rule (the pattern translation, the claims, the OwnAuth admission, the
// collision rule) is checked by Build, so the order of the kernel's P7 calls
// does not matter. Which module may use LegacyAuthGin is the Registrar's
// check (ProblemLegacyAuth, SEC2-17), made before the mount reaches here.
func (r *Router) MountLegacy(m LegacyMount) error {
	var routes []LegacyRoute
	if m.Routes != nil {
		for lr := range m.Routes {
			routes = append(routes, lr)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.late("MountLegacy " + m.Owner); err != nil {
		return err
	}
	var errs []error
	if m.Owner == "" || m.Handler == nil {
		errs = append(errs, fmt.Errorf("%w: legacy mount needs an Owner and a Handler", ErrLegacyManifest))
	}
	if m.Auth != LegacyAuthCore && m.Auth != LegacyAuthGin {
		errs = append(errs, fmt.Errorf("%w: %s: legacy mount Auth %d is neither LegacyAuthCore nor LegacyAuthGin", ErrLegacyManifest, m.Owner, m.Auth))
	}
	for _, rd := range m.Redirects {
		if rd.Location == nil || !strings.HasPrefix(rd.Prefix, "/") || !strings.HasSuffix(rd.Prefix, "/") || !isClean(rd.Prefix) {
			errs = append(errs, fmt.Errorf("%w: %s: redirect %q needs a clean subtree Prefix and a Location", ErrLegacyManifest, m.Owner, rd.Prefix))
		}
	}
	if m.ServesSPA && r.rootOwned() {
		errs = append(errs, fmt.Errorf("%w: %s: \"/\" already has an owner (FE-1)", ErrPrefixCollision, m.Owner))
	}
	if err := errors.Join(errs...); err != nil {
		r.record(err)
		return err
	}
	m.Routes = nil // read once; Build works from the copy
	m.TopSegments = slices.Clone(m.TopSegments)
	m.SPAShared = slices.Clone(m.SPAShared)
	m.Redirects = slices.Clone(m.Redirects)
	r.mounts = append(r.mounts, m)
	r.legacy.routes = append(r.legacy.routes, routes)
	return nil
}

// legacyMatch is what the wrapper puts in the request context.
type legacyMatch struct {
	pattern string     // as registered, method included
	admits  []ginRoute // the manifest entries gin may run for it
}

type ginRoute struct{ method, path string }

type legacyMatchKey struct{}

// MatchedPattern returns the ServeMux pattern the legacy mount's wrapper
// matched for this request, as registered (method included), and false
// outside a request the wrapper hands to the legacy handler. Its one reader
// is the SEC2-22 post-match check, which runs inside gin before any handler
// (05-http-api §3.5, §8.2).
func MatchedPattern(ctx context.Context) (pattern string, ok bool) {
	m, ok := ctx.Value(legacyMatchKey{}).(*legacyMatch)
	if !ok {
		return "", false
	}
	return m.pattern, true
}

// LegacyMatchAgrees is the post-match check of 05-http-api §4.8 (SEC2-22),
// for the legacy handler to call once it has routed: method is the request's
// method and ginPath the route the legacy router matched, in its own syntax
// (gin's c.FullPath(), "" when it matched nothing). agrees reports that
// (method, ginPath) is the manifest entry of the pattern the mux matched or,
// for a generalised pattern, one of its members; the caller answers 404 in
// the legacy error shape otherwise, before any handler runs, so the route
// stages 8, 10 and 13 judged is the route the legacy router runs. A ServesSPA
// mount's "/" fallback is wrapped the same way with GET / and HEAD / as its
// only entries (rootHandler). ok is false outside a request a wrapper handed
// over (the legacy handler served directly), where there is nothing to
// compare.
func LegacyMatchAgrees(ctx context.Context, method, ginPath string) (agrees, ok bool) {
	m, ok := ctx.Value(legacyMatchKey{}).(*legacyMatch)
	if !ok {
		return false, false
	}
	return slices.Contains(m.admits, ginRoute{method: method, path: ginPath}), true
}

// legacyHandler is the wrapper of one registered legacy pattern: through
// the chain's route wrapper it records the pattern (spec.Pattern) and the
// mount's policy in the request's State like every route wrapper (§4.2
// stage 2) and runs stages 10-11, then puts the match in the context and
// calls the mount's handler. r.mu is held.
func (r *Router) legacyHandler(pattern string, admits []ginRoute, h http.Handler, spec httpmw.RouteSpec) http.Handler {
	m := &legacyMatch{pattern: pattern, admits: admits}
	return r.chain.Route(spec, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), legacyMatchKey{}, m)))
	}))
}

// ---------------------------------------------------------------------------
// Patterns: the gin -> ServeMux translation and the structural relations the
// collision rule needs. Go's own ServeMux stays the conflict oracle.

// segKind is the kind of one pattern segment.
type segKind uint8

const (
	segLit   segKind = iota // a literal; "" is the {$} end of a path that ends in "/"
	segWild                 // {name}: one non-empty segment
	segMulti                // {name...}: the rest of the path, last segment only
)

type seg struct {
	kind segKind
	text string // the literal, or the wildcard as written ("{id}")
}

// muxPat is one pattern of the conflict graph.
type muxPat struct {
	method string // "" for any method
	segs   []seg
}

func (p muxPat) String() string {
	var b strings.Builder
	if p.method != "" {
		b.WriteString(p.method + " ")
	}
	for _, s := range p.segs {
		b.WriteByte('/')
		if s.kind == segLit && s.text == "" {
			b.WriteString("{$}")
			continue
		}
		b.WriteString(s.text)
	}
	if len(p.segs) == 0 {
		b.WriteByte('/')
	}
	return b.String()
}

// path is the pattern without its method, as stage 2 records it.
func (p muxPat) path() string {
	s := p.String()
	if p.method != "" {
		s = strings.TrimPrefix(s, p.method+" ")
	}
	return s
}

// translateGinPath is §4.8's translation: ":name" -> "{name}", "*name" (last
// segment only) -> "{name...}", and a path ending in "/" ("/" included) ends
// in "{$}", so every legacy pattern is exact.
func translateGinPath(method, gin string) (muxPat, error) {
	p := muxPat{method: method}
	if !strings.HasPrefix(gin, "/") || strings.ContainsAny(gin, "{} \t?#") {
		return p, fmt.Errorf("gin path %q cannot be a ServeMux pattern", gin)
	}
	parts := strings.Split(gin[1:], "/")
	for i, s := range parts {
		last := i == len(parts)-1
		switch {
		case s == "" && last:
			p.segs = append(p.segs, seg{kind: segLit})
		case s == "" || s == "." || s == "..":
			return p, fmt.Errorf("gin path %q is not clean", gin)
		case s[0] == ':' && len(s) > 1:
			p.segs = append(p.segs, seg{kind: segWild, text: "{" + s[1:] + "}"})
		case s[0] == '*' && len(s) > 1 && last:
			p.segs = append(p.segs, seg{kind: segMulti, text: "{" + s[1:] + "...}"})
		case strings.ContainsAny(s, ":*"):
			return p, fmt.Errorf("gin path %q has a wildcard ServeMux cannot express", gin)
		default:
			p.segs = append(p.segs, seg{kind: segLit, text: s})
		}
	}
	return p, nil
}

// parseMuxPattern reads a registered native or infra pattern ("GET /a/{id}")
// into the same model. ok is false for a shape the model does not cover (a
// host, a subtree pattern), which the conflict graph then leaves to Go's mux.
func parseMuxPattern(method, path string) (muxPat, bool) {
	p := muxPat{method: method}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "{$}/") {
		return p, false
	}
	if path == "/{$}" {
		p.segs = []seg{{kind: segLit}}
		return p, true
	}
	if strings.HasSuffix(path, "/") {
		return p, false // a subtree pattern
	}
	parts := strings.Split(path[1:], "/")
	for i, s := range parts {
		switch {
		case s == "{$}" && i == len(parts)-1:
			p.segs = append(p.segs, seg{kind: segLit})
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "...}") && i == len(parts)-1:
			p.segs = append(p.segs, seg{kind: segMulti, text: s})
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
			p.segs = append(p.segs, seg{kind: segWild, text: s})
		case strings.ContainsAny(s, "{}"):
			return p, false
		default:
			p.segs = append(p.segs, seg{kind: segLit, text: s})
		}
	}
	return p, true
}

// methodSet is the request methods a pattern method matches: "" every method,
// GET also HEAD (ServeMux's rule).
func methodSet(m string) []string {
	switch m {
	case "":
		return nil
	case http.MethodGet:
		return []string{http.MethodGet, http.MethodHead}
	default:
		return []string{m}
	}
}

func methodsIntersect(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	sb := methodSet(b)
	for _, m := range methodSet(a) {
		if slices.Contains(sb, m) {
			return true
		}
	}
	return false
}

// methodsWithin reports that every method a matches, b matches.
func methodsWithin(a, b string) bool {
	if b == "" {
		return true
	}
	if a == "" {
		return false
	}
	sb := methodSet(b)
	for _, m := range methodSet(a) {
		if !slices.Contains(sb, m) {
			return false
		}
	}
	return true
}

// segsOverlap reports that some path matches both segment lists.
func segsOverlap(a, b []seg) bool {
	for i := 0; ; i++ {
		switch {
		case i == len(a) && i == len(b):
			return true
		case i == len(a) || i == len(b):
			return false
		}
		x, y := a[i], b[i]
		if x.kind == segMulti || y.kind == segMulti {
			return true // each matches one or more remaining segments
		}
		switch {
		case x.kind == segLit && y.kind == segLit:
			if x.text != y.text {
				return false
			}
		case x.kind == segLit: // y is a wildcard: it never matches the empty end
			if x.text == "" {
				return false
			}
		case y.kind == segLit:
			if y.text == "" {
				return false
			}
		}
	}
}

// segsWithin reports that every path a matches, b matches.
func segsWithin(a, b []seg) bool {
	for i := 0; ; i++ {
		switch {
		case i == len(a) && i == len(b):
			return true
		case i == len(b):
			return false
		case b[i].kind == segMulti:
			return i < len(a)
		case i == len(a):
			return false
		}
		x, y := a[i], b[i]
		switch y.kind {
		case segLit:
			if x.kind != segLit || x.text != y.text {
				return false
			}
		case segWild:
			if x.kind == segMulti || (x.kind == segLit && x.text == "") {
				return false
			}
		}
	}
}

func overlaps(a, b muxPat) bool {
	return methodsIntersect(a.method, b.method) && segsOverlap(a.segs, b.segs)
}

func within(a, b muxPat) bool { return methodsWithin(a.method, b.method) && segsWithin(a.segs, b.segs) }

// strictlyMoreSpecific reports that a matches a strict subset of b's requests.
func strictlyMoreSpecific(a, b muxPat) bool { return within(a, b) && !within(b, a) }

// muxConflict asks Go's ServeMux whether two patterns conflict.
func muxConflict(a, b string) (conflict bool) {
	defer func() {
		if recover() != nil {
			conflict = true
		}
	}()
	m := http.NewServeMux()
	m.Handle(a, http.NotFoundHandler())
	m.Handle(b, http.NotFoundHandler())
	return false
}

// ginPrefers reports that gin's tree routes every request both a and b match
// to a: at the first segment where they differ in kind, a has the literal and
// b the parameter (gin routes a static segment before a parameter at the same
// depth).
func ginPrefers(a, b muxPat) bool {
	if len(a.segs) != len(b.segs) {
		return false
	}
	for i := range a.segs {
		x, y := a.segs[i], b.segs[i]
		switch {
		case x.kind == segLit && y.kind == segLit:
			if x.text != y.text {
				return false
			}
		case x.kind == segLit:
			return true
		case y.kind == segLit:
			return false
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Build

// node is one pattern of the conflict graph: a native or infra route, or one
// manifest route.
type node struct {
	owner  string
	legacy bool
	pat    muxPat
	key    string // method + " " + path, as registered
	mount  int    // r.mounts index when legacy
	route  LegacyRoute
}

func (n *node) name() string { return n.owner + " " + n.key }

// buildLegacy validates every mount's manifest, applies the collision rule
// and registers the result into mux (05-http-api §4.8, §8.2). seen holds the
// native and infra patterns Build registered, by key.
func (r *Router) buildLegacy(mux *http.ServeMux) []error {
	var errs []error
	nodes := r.nativeNodes()
	seen := map[string]string{}
	for _, n := range nodes {
		seen[n.key] = n.owner
	}
	r.legacy.infos = nil
	var infos []RouteInfo
	for mi, m := range r.mounts {
		var routes []LegacyRoute
		if mi < len(r.legacy.routes) {
			routes = r.legacy.routes[mi]
		}
		errs = append(errs, r.checkMount(m, routes)...)
		for _, lr := range routes {
			n, rerrs := r.legacyNode(mi, m, lr)
			errs = append(errs, rerrs...)
			if n == nil {
				continue
			}
			if prev, dup := seen[n.key]; dup {
				errs = append(errs, fmt.Errorf("%w: %s registered by %s and %s", ErrDuplicateRoute, n.key, prev, n.owner))
				continue
			}
			seen[n.key] = n.owner
			nodes = append(nodes, n)
		}
	}
	plan, perrs := collisionPlan(nodes)
	errs = append(errs, perrs...)
	if len(errs) > 0 {
		return errs
	}
	for _, reg := range plan {
		m := r.mounts[reg.mount]
		// The members of a generalised pattern share their RateClass and
		// NoCompress (sameRequirements), so the first speaks for all.
		first := reg.members[0].route
		spec := httpmw.RouteSpec{Pattern: reg.pat.path(), Policy: mountPolicy(m, first.NoCompress),
			Class: rateClassName(first.RateClass), MaxBody: -1}
		if err := register(mux, reg.pat.String(), r.legacyHandler(reg.pat.String(), reg.admits, m.Handler, spec)); err != nil {
			errs = append(errs, err)
		}
		for _, mem := range reg.members {
			lr := mem.route
			infos = append(infos, RouteInfo{Owner: m.Owner, Method: lr.Method, Pattern: reg.pat.path(),
				Name: lr.GinPath, Perm: lr.Perm, Guards: lr.Guards, RateClass: lr.RateClass, Kind: RouteKindLegacy,
				OwnAuth: lr.OwnAuth, AuthMethod: lr.AuthMethod})
		}
	}
	if len(errs) == 0 {
		// Routes() describes a built router only: a failed register leaves
		// no partial list behind.
		r.legacy.infos = infos
	}
	return errs
}

// rootHandler is the "/" owner Build hands the dispatcher. A ServesSPA legacy
// mount's handler is wrapped like a manifest pattern whose only entries are
// GET / and HEAD /, so the fallback reaches the legacy router's NoRoute
// chain and its "/" page only (05-http-api §8.2): an unclean path or a
// SPAShared GET that the legacy router would route to any other handler
// gets the post-match check's 404, because the mux never admitted that
// route. r.mu is held.
func (r *Router) rootHandler() http.Handler {
	h, owner := r.rootOwner()
	if h == nil {
		return nil
	}
	// Pattern "": the "/" owner's pages record no http.route (05-http-api
	// §4.2 stage 2), and the "/" owner runs no rate or body limit.
	spec := httpmw.RouteSpec{Class: rateClassName(RateExempt), MaxBody: -1}
	if owner == "core" {
		spec.Policy = &httpmw.Policy{Owner: "core"}
		return r.chain.Route(spec, h)
	}
	for _, m := range r.mounts {
		if m.ServesSPA {
			spec.Policy = mountPolicy(m, false)
		}
	}
	return r.legacyHandler("", []ginRoute{{method: http.MethodGet, path: "/"}, {method: http.MethodHead, path: "/"}}, h, spec)
}

// nativeNodes are the routes Build registered itself, as graph nodes.
func (r *Router) nativeNodes() []*node {
	var out []*node
	bases := r.fixedBases()
	for _, e := range r.routes {
		if len(e.errs) > 0 || e.handler == nil || isCoreExact(e, bases) {
			// A CoreExactPatterns() route sits inside the legacy owner's claim
			// by declaration (§4.9): checkCoreFixed refuses every manifest
			// route over it, so it never shadows one (ErrShadow) here.
			continue
		}
		key := strings.TrimSpace(e.info.Method + " " + e.info.Pattern)
		p, ok := parseMuxPattern(e.info.Method, e.info.Pattern)
		if !ok {
			continue // Build's own registration still catches its conflicts
		}
		out = append(out, &node{owner: e.info.Owner, pat: p, key: key})
	}
	return out
}

// checkMount applies the mount-wide rule of 05-http-api §8.2 that needs the
// manifest: a LegacyAuthCore mount with non-Public routes waits for the auth
// service.
func (r *Router) checkMount(m LegacyMount, routes []LegacyRoute) []error {
	var errs []error
	if m.Auth == LegacyAuthCore {
		// Stage 13 enforces a LegacyAuthCore mount's Perm and Guards; until
		// the auth service exists (R2) nothing could, so the mount is refused
		// exactly as a non-Public native route is (fail closed).
		n := 0
		for _, lr := range routes {
			if lr.Perm.Kind() != rbac.KindPublic {
				n++
			}
		}
		if n > 0 {
			errs = append(errs, fmt.Errorf("%w: %s: LegacyAuthCore mount with %d non-Public routes", ErrNoAuthService, m.Owner, n))
		}
	}
	return errs
}

// legacyNode validates one manifest route and returns its graph node, or nil.
func (r *Router) legacyNode(mi int, m LegacyMount, lr LegacyRoute) (*node, []error) {
	what := m.Owner + " " + lr.Method + " " + lr.GinPath
	var errs []error
	if !validMethod(lr.Method) {
		errs = append(errs, fmt.Errorf("%w: %s: method %q", ErrLegacyManifest, what, lr.Method))
	}
	p, err := translateGinPath(lr.Method, lr.GinPath)
	if err != nil {
		errs = append(errs, fmt.Errorf("%w: %s: %v", ErrLegacyManifest, what, err))
	}
	errs = append(errs, r.checkLegacyPerm(m.Owner, what, lr)...)
	if err == nil {
		errs = append(errs, r.checkLegacyPlace(m, what, lr.GinPath)...)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return &node{owner: m.Owner, legacy: true, pat: p, key: p.String(), mount: mi, route: lr}, nil
}

func validMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return true
	}
	return false
}

// checkLegacyPerm is R6 and the admissions for one manifest route: a
// requirement is present; Public is exactly rbac.Public; an OwnAuth route
// names an auth method its owner registered (ErrOwnAuthMethod) and only an
// OwnAuth route may be rbac.Delegated; a LegacyCheck value is an in-handler
// check's, never a route's.
func (r *Router) checkLegacyPerm(owner, what string, lr LegacyRoute) []error {
	req := lr.Perm
	if req.IsZero() {
		return []error{fmt.Errorf("%w: %s", ErrNoPermission, what)}
	}
	var errs []error
	if lr.Public != (req.Kind() == rbac.KindPublic) {
		errs = append(errs, fmt.Errorf("%w: %s: Public is %t but the requirement is %s", ErrLegacyManifest, what, lr.Public, req))
	}
	switch {
	case lr.OwnAuth && lr.AuthMethod == "":
		errs = append(errs, fmt.Errorf("%w: %s: OwnAuth route names no AuthMethod", ErrOwnAuthMethod, what))
	case lr.OwnAuth && !r.legacy.authMethods[owner][lr.AuthMethod]:
		errs = append(errs, fmt.Errorf("%w: %s: auth method %q is not registered by %s", ErrOwnAuthMethod, what, lr.AuthMethod, owner))
	case !lr.OwnAuth && lr.AuthMethod != "":
		errs = append(errs, fmt.Errorf("%w: %s: AuthMethod %q on a route that is not OwnAuth", ErrLegacyManifest, what, lr.AuthMethod))
	}
	switch req.Kind() {
	case rbac.KindDelegated:
		if !lr.OwnAuth {
			errs = append(errs, fmt.Errorf("%w: %s: rbac.Delegated on a route that is not OwnAuth (SEC2-17)", ErrLegacyManifest, what))
		}
	case rbac.KindPublic:
		if lr.OwnAuth {
			errs = append(errs, fmt.Errorf("%w: %s: an OwnAuth route is authenticated, not Public", ErrLegacyManifest, what))
		}
	case rbac.KindLegacyCheck:
		errs = append(errs, fmt.Errorf("%w: %s: %s is an in-handler check, not a route requirement", ErrLegacyManifest, what, req))
	}
	return errs
}

// checkLegacyPlace keeps a manifest route inside its owner's claims (SEC3-8,
// ErrOutsideClaim; "/" is the "/" owner's, never a manifest route) and a
// route outside /api/ on a first segment that is a TopSegments or SPAShared
// entry of its mount (ErrPrefixCollision, §3.5). A reserved segment that is
// core's own path space ("v<digits>", livez, readyz) or the SPA's (assets,
// static, ui) is refused even when listed; auth, health, metrics and events
// pass only when the mount lists them.
func (r *Router) checkLegacyPlace(m LegacyMount, what, gin string) []error {
	var errs []error
	if !r.insideClaims(m.Owner, gin) {
		errs = append(errs, fmt.Errorf("%w: %s", ErrOutsideClaim, what))
	}
	first := firstSegment(gin)
	switch {
	case first == "" || first == "api":
	case coreOnlySegment(first):
		errs = append(errs, fmt.Errorf("%w: %s: top-level segment %q is reserved for core or the SPA", ErrPrefixCollision, what, first))
	case !slices.Contains(m.TopSegments, first) && !slices.Contains(m.SPAShared, first):
		errs = append(errs, fmt.Errorf("%w: %s: top-level segment %q is not in the mount's TopSegments or SPAShared", ErrPrefixCollision, what, first))
	}
	return errs
}

// coreOnlySegment reports a reserved first segment no legacy route may use:
// "v<digits>" (versioned core paths), livez and readyz (core's probes), and
// assets, static and ui (the SPA's own paths, FE2-11).
func coreOnlySegment(s string) bool {
	return (s[0] == 'v' && reservedSegment(s)) || slices.Contains([]string{"livez", "readyz", "assets", "static", "ui"}, s)
}

// insideClaims reports that every path gin matches lies under one claim of
// owner: its literal prefix (up to the first wildcard) is under a subtree
// claim "/x/" (which also covers "/x"), or the path is wildcard-free and
// equals an exact claim.
func (r *Router) insideClaims(owner, gin string) bool {
	lit := gin
	if i := strings.IndexAny(gin, ":*"); i >= 0 {
		lit = gin[:i]
	}
	wild := lit != gin
	for _, c := range r.claims {
		if c.owner != owner {
			continue
		}
		if before, ok := strings.CutSuffix(c.prefix, "/"); ok {
			if strings.HasPrefix(lit, c.prefix) || (!wild && gin == before) {
				return true
			}
			continue
		}
		if !wild && gin == c.prefix {
			return true
		}
	}
	return false
}

// reservedSegment reports a first segment ReservedSegments names, reading
// its "v<digits>" token as the pattern ^v[0-9]+$.
func reservedSegment(s string) bool {
	if len(s) > 1 && s[0] == 'v' {
		if _, err := strconv.ParseUint(s[1:], 10, 64); err == nil && !strings.ContainsAny(s[1:], "+-") {
			return true
		}
	}
	return slices.Contains(ReservedSegments(), s)
}

// registration is one pattern the collision plan registers: a route of its
// own, or one generalised pattern with its folded members.
type registration struct {
	mount   int
	pat     muxPat
	members []*node
	admits  []ginRoute
}

// collisionPlan is the route collision rule (05-http-api §4.8, resolution
// A-1) over every pattern of the build. It returns the registrations of the
// legacy routes, or the refusals.
func collisionPlan(nodes []*node) ([]registration, []error) {
	var errs []error
	parent := make([]int, len(nodes))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	partners := make([][]int, len(nodes))
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			a, b := nodes[i], nodes[j]
			if !overlaps(a.pat, b.pat) {
				continue
			}
			if muxConflict(a.key, b.key) {
				partners[i] = append(partners[i], j)
				partners[j] = append(partners[j], i)
				parent[find(i)] = find(j)
				continue
			}
			if a.owner != b.owner && (a.legacy || b.legacy) {
				errs = append(errs, shadowError(a, b))
			}
		}
	}
	comps := map[int][]int{}
	for i := range nodes {
		root := find(i)
		comps[root] = append(comps[root], i)
	}
	roots := make([]int, 0, len(comps))
	for root := range comps {
		roots = append(roots, root)
	}
	sort.Ints(roots)
	var plan []registration
	for _, root := range roots {
		comp := comps[root]
		if len(comp) == 1 {
			n := nodes[comp[0]]
			if n.legacy {
				plan = append(plan, registration{mount: n.mount, pat: n.pat, members: []*node{n},
					admits: []ginRoute{{method: n.route.Method, path: n.route.GinPath}}})
			}
			continue
		}
		regs, cerrs := foldComponent(nodes, comp, partners)
		errs = append(errs, cerrs...)
		plan = append(plan, regs...)
	}
	return plan, errs
}

func shadowError(a, b *node) error {
	if strictlyMoreSpecific(b.pat, a.pat) {
		a, b = b, a
	}
	return fmt.Errorf("%w: %s is strictly more specific than %s (SEC3-8)", ErrShadow, a.name(), b.name())
}

// foldComponent applies rules 1-4 to one connected component of the
// "conflicts with" relation.
func foldComponent(nodes []*node, comp []int, partners [][]int) ([]registration, []error) {
	names := make([]string, 0, len(comp))
	for _, i := range comp {
		names = append(names, nodes[i].name())
	}
	sort.Strings(names)
	refuse := func(why string) []error {
		return []error{fmt.Errorf("%w: %s: %s", ErrPatternConflict, why, strings.Join(names, " | "))}
	}
	first := nodes[comp[0]]
	for _, i := range comp {
		n := nodes[i]
		if !n.legacy || n.owner != first.owner || n.mount != first.mount {
			return nil, refuse("a conflicting set must be one legacy mount's routes (rule 1)")
		}
	}
	var kept, members []*node
	for _, i := range comp {
		k := true
		for _, j := range partners[i] {
			if !ginPrefers(nodes[i].pat, nodes[j].pat) {
				k = false
				break
			}
		}
		if k {
			kept = append(kept, nodes[i])
		} else {
			members = append(members, nodes[i])
		}
	}
	if len(members) == 0 {
		return nil, refuse("no route of the set can be folded (rule 2)")
	}
	ref := members[0]
	for _, n := range append(slices.Clone(members), kept...) {
		if len(n.pat.segs) != len(ref.pat.segs) || slices.ContainsFunc(n.pat.segs, func(s seg) bool { return s.kind == segMulti }) {
			return nil, refuse("members of different segment counts or with a {name...} wildcard cannot share a pattern (rule 3)")
		}
	}
	for _, n := range members[1:] {
		if n.pat.method != ref.pat.method {
			return nil, refuse("members with different methods cannot share a pattern (rule 3)")
		}
		if why := sameRequirements(ref.route, n.route); why != "" {
			return nil, refuse("members differ in " + why + " (rule 4)")
		}
	}
	gen := muxPat{method: ref.pat.method}
	all := append(slices.Clone(members), kept...)
	for i, s := range ref.pat.segs {
		same := true
		for _, n := range all {
			if n.pat.segs[i] != s {
				same = false
				break
			}
		}
		if same {
			gen.segs = append(gen.segs, s)
			continue
		}
		gen.segs = append(gen.segs, seg{kind: segWild, text: "{p" + strconv.Itoa(i+1) + "}"})
	}
	for _, k := range kept {
		if !strictlyMoreSpecific(k.pat, gen) {
			return nil, refuse("kept route " + k.key + " is not strictly more specific than the generalised pattern " + gen.String() + " (rule 3)")
		}
	}
	for _, n := range nodes {
		if slices.Contains(all, n) {
			continue
		}
		if overlaps(gen, n.pat) && muxConflict(gen.String(), n.key) {
			return nil, refuse("the generalised pattern " + gen.String() + " conflicts with " + n.name() + " (rule 3)")
		}
	}
	regs := []registration{{mount: ref.mount, pat: gen, members: members}}
	for _, n := range members {
		regs[0].admits = append(regs[0].admits, ginRoute{method: n.route.Method, path: n.route.GinPath})
	}
	for _, k := range kept {
		regs = append(regs, registration{mount: k.mount, pat: k.pat, members: []*node{k},
			admits: []ginRoute{{method: k.route.Method, path: k.route.GinPath}}})
	}
	return regs, nil
}

// sameRequirements names the first field a stage before the legacy handler
// reads in which a and b differ (rule 4), or "".
func sameRequirements(a, b LegacyRoute) string {
	switch {
	case a.Perm.String() != b.Perm.String():
		return "Perm"
	case a.Guards != b.Guards:
		return "Guards"
	case a.Public != b.Public:
		return "Public"
	case a.RateClass != b.RateClass:
		return "RateClass"
	case a.OwnAuth != b.OwnAuth || a.AuthMethod != b.AuthMethod:
		return "OwnAuth"
	case a.BasicExempt != b.BasicExempt:
		return "BasicExempt"
	case a.NoCompress != b.NoCompress:
		return "NoCompress"
	}
	return ""
}

// legacyRedirect returns the dispatcher's redirect consult for an unmatched
// request (05-http-api §4.9 item 3): covered reports a path under a redirect
// Prefix and under no claim of an owner without a legacy mount; location is
// the 301 target, "" when the rule excludes the request.
func (r *Router) legacyRedirect() func(*http.Request) (location string, covered bool) {
	type rule struct {
		prefix string
		loc    func(*http.Request) (string, bool)
	}
	var rules []rule
	legacyOwners := map[string]bool{}
	for _, m := range r.mounts {
		legacyOwners[m.Owner] = true
		for _, rd := range m.Redirects {
			rules = append(rules, rule{prefix: rd.Prefix, loc: rd.Location})
		}
	}
	if len(rules) == 0 {
		return nil
	}
	var native []string
	for _, c := range r.claims {
		if !legacyOwners[c.owner] {
			native = append(native, c.prefix)
		}
	}
	return func(req *http.Request) (string, bool) {
		p := req.URL.Path
		for _, c := range native {
			if p == strings.TrimSuffix(c, "/") || (strings.HasSuffix(c, "/") && strings.HasPrefix(p, c)) {
				return "", false
			}
		}
		for _, rl := range rules {
			if strings.HasPrefix(p, rl.prefix) {
				if loc, ok := rl.loc(req); ok {
					return loc, true
				}
				return "", true
			}
		}
		return "", false
	}
}
