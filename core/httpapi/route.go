// file: core/httpapi/route.go
// version: 1.0.4
// guid: 0e02f805-1af9-41bb-87b2-82fc71cc65f0
// last-edited: 2026-11-10

// Package httpapi is core's HTTP surface (05-http-api): routes and groups,
// typed handlers with a required permission, the problem+json error shape and
// the legacy error envelope, pages and cursors, and the kernel-facing Router
// that builds one stdlib ServeMux with pattern-conflict detection, the SPA
// claim and the NonSPA predicate (§3, §4.8, §4.10). It is layer L6.
//
// This is the R1 skeleton (06 §6.4.10): the perimeter stages 0-13 are R1-17's
// (core/internal/httpmw), the legacy manifest and its collision rule are
// R1-14's (legacy.go), and the auth, rbac and audit Deps arrive in R2. While
// they are absent Build refuses every native route that is not Public
// (ErrNoAuthService, §3).
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/jsonschemax"
	"github.com/falkcorp/mediacore/rbac"
)

// Route is the declarative description of one operation. It is data: the
// kernel reads it for permission coverage (R6), OpenAPI, metrics labels and
// audit.
type Route struct {
	Method  string           // http.MethodGet, ...; required
	Path    string           // ServeMux path relative to the Group base, e.g. "/books/{id}"; use "{$}" for exact
	Name    string           // operation name, lowerCamel, unique per module; OpenAPI operationId = <module>_<Name>
	Perm    rbac.Requirement // REQUIRED: zero value is rejected at registration (R6)
	Summary string
	Doc     string
	Tags    []string

	Guards Guards // extra identity requirements (interactive-only, owner step-up)
	// MaxBody: 0 = core.http.body.json_max_bytes; -1 = streaming upload,
	// handler enforces. A negative value is only for a raw body the handler
	// reads itself: a body:"json" body is decoded into memory and always read
	// through http.MaxBytesReader (05-http-api §3.4), so Build refuses a
	// negative MaxBody on a route with one (ErrBadBinding).
	MaxBody   int64
	RateClass RateClass // RateDefault, RateAuth, RateExpensive, RateExempt
	ETag      bool      // opt-in: GET returns ETag from the entity Version, writes honour If-Match (§4.3)
	Success   int       // success status; 0 = 200 (201 if Resp is Created[T], 204 if NoContent)
	Lenient   bool      // accept unknown JSON fields; zero value = strict (§3.4, COR3-13)
}

// Guards are identity requirements beyond the permission.
type Guards struct {
	Interactive bool // refuse API keys, ABS tokens and auth-disabled (HEAD's credGuard, auth.Method.MayChangeCredentials)
	Owner       bool // require an owner-assurance principal (HEAD's RequireOwner; 05-authn §4.8)
}

// RateClass is a route's rate-limit bucket class (stage 10, R1-17).
type RateClass uint8

const (
	RateDefault   RateClass = iota
	RateAuth                // login/setup/bootstrap/invite: tighter per-source buckets (05-authn §4.9)
	RateExpensive           // search, exports
	RateExempt              // infra probes and the SPA handler only (Handle refuses it, and any unknown class)
)

// Group is a mount point whose whole core chain is already applied. A module
// receives exactly one, rooted at /api/<moduleID>/v1, from
// module.Registrar.HTTP(). There is no API that yields an unauthenticated or
// chain-less group (G8).
type Group struct {
	router *Router
	owner  string
	root   string // "/api/books"
	base   string // "/api/books/v1"
	err    error  // a bad Sub or Version: every route under the group is refused with it
}

// Base returns the group's path prefix, e.g. "/api/books/v1".
func (g *Group) Base() string { return g.base }

// Module returns the owner of the group, e.g. "books" ("core" for CoreGroup).
func (g *Group) Module() string { return g.owner }

// Sub returns the group at base+prefix with the same owner and chain. A
// prefix that is not a clean "/segment..." path (no wildcard, no trailing
// slash) makes every route under the group fail Build with ErrOutsideClaim.
func (g *Group) Sub(prefix string) *Group {
	sub := &Group{router: g.router, owner: g.owner, root: g.root, base: g.base + prefix, err: g.err}
	if sub.err == nil && (!strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") ||
		strings.Contains(prefix, "{") || cleanUnder(sub.base, g.base+"/") != nil) {
		sub.err = fmt.Errorf("%w: %s: Sub(%q) is not a clean /segment path", ErrOutsideClaim, g.owner, prefix)
	}
	return sub
}

// Version returns the group of a later major, e.g. "/api/books/v2"; major
// must be >= 2 (05-http-api §4.6), else every route under it fails Build
// with ErrOutsideClaim.
func (g *Group) Version(major int) *Group {
	v := &Group{router: g.router, owner: g.owner, root: g.root, base: fmt.Sprintf("%s/v%d", g.root, major)}
	if major < 2 {
		v.err = fmt.Errorf("%w: %s: Version(%d): a later major is >= 2", ErrOutsideClaim, g.owner, major)
	}
	return v
}

// In is the decoded request. Req's fields bind by tag:
//
//	path:"id"      -> r.PathValue("id")
//	query:"limit"  -> URL query (string, ints, bools, []string, time.Time RFC 3339, encoding.TextUnmarshaler)
//	header:"If-Match"
//	body:"json"    -> exactly one field: JSON body (methods with a body only)
//
// Binding is compiled once per Req type at registration (reflection cached),
// so a bad tag is a boot error, not a request-time 500.
type In[Req any] struct {
	Req       Req
	Principal *auth.Principal // never nil on non-Public routes
	RequestID string
	Request   *http.Request // escape hatch; handlers must not write to a ResponseWriter
}

// Created and NoContent are response wrappers recognised by Handle.
type Created[T any] struct {
	Location string
	Body     T
}

func (c Created[T]) createdLocation() string { return c.Location }
func (c Created[T]) createdBody() any        { return c.Body }

// createdResponse is how Handle recognises any Created[T].
type createdResponse interface {
	createdLocation() string
	createdBody() any
}

// NoContent answers 204 with no body.
type NoContent struct{}

// Validator is implemented by request types that need more than JSON
// decoding. Return Invalid(...) or an *Error.
type Validator interface{ Validate() error }

// routeEntry is one registered route of any kind.
type routeEntry struct {
	info    RouteInfo
	route   Route
	handler http.Handler
	errs    []error // registration errors found at Handle time

	// native routes only
	binding   *binding
	rawParams []paramDesc // HandleRaw routes: the path wildcards and RawDoc.Query
	bodyName  string      // component name of the JSON request body
	bodyType reflect.Type
	bodySch  *jsonschema.Schema
	respName string // component name of the success body, "" for no body
	respType reflect.Type
	respSch  *jsonschema.Schema
	status   int
}

// routeName is the lowerCamel rule for Route.Name.
func routeName() *regexp.Regexp { return regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`) }

// Handle registers a typed JSON operation. It never panics: errors are
// collected and returned by the kernel from app.New (fail-closed boot, §5).
func Handle[Req, Resp any](g *Group, rt Route, fn func(ctx context.Context, in *In[Req]) (Resp, error)) {
	e := &routeEntry{route: rt}
	e.info = RouteInfo{Owner: g.owner, Method: rt.Method, Pattern: g.base + rt.Path, Name: rt.Name,
		Perm: rt.Perm, Guards: rt.Guards, RateClass: rt.RateClass, Kind: RouteKindNative}
	if g.err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", describe(e.info), g.err))
	}
	e.errs = append(e.errs, checkRouteShape(g, rt)...)
	if fn == nil {
		e.errs = append(e.errs, fmt.Errorf("%w: %s: nil handler", ErrSchema, describe(e.info)))
	}

	b, err := compileBinding(reflect.TypeFor[Req](), rt.Method)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", describe(e.info), err))
	}
	if b != nil {
		if err := b.checkPath(e.info.Pattern); err != nil {
			e.errs = append(e.errs, err)
		}
		e.binding = b
		if b.body != nil && rt.MaxBody < 0 {
			e.errs = append(e.errs, fmt.Errorf("%w: %s: MaxBody %d with a body:\"json\" field: a decoded body is always bounded",
				ErrBadBinding, describe(e.info), rt.MaxBody))
		}
		if b.body != nil {
			reqSch, reqErr := jsonschemax.For[Req](schemaOverrides())
			e.reflectBody(g.owner, b, reqSch, reqErr)
		}
	}
	respSch, respErr := jsonschemax.For[Resp](schemaOverrides())
	e.reflectResponse(g.owner, reflect.TypeFor[Resp](), respSch, respErr)

	log := g.router.deps.Log
	e.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		v, err := e.binding.decode(w, r, rt)
		if err != nil {
			writeError(ctx, w, r, err, log)
			return
		}
		req := v.Interface().(Req)
		if err := validate(&req); err != nil {
			writeError(ctx, w, r, err, log)
			return
		}
		p, _ := auth.PrincipalFrom(ctx)
		resp, err := fn(ctx, &In[Req]{Req: req, Principal: p, RequestID: RequestID(ctx), Request: r})
		if err != nil {
			writeError(ctx, w, r, err, log)
			return
		}
		writeSuccess(ctx, w, rt, any(resp), log)
	})
	g.router.add(e)
}

func validate[Req any](req *Req) error {
	switch v := any(req).(type) {
	case Validator:
		return v.Validate()
	}
	if v, ok := any(*req).(Validator); ok {
		return v.Validate()
	}
	return nil
}

func writeSuccess(ctx context.Context, w http.ResponseWriter, rt Route, resp any, log *slog.Logger) {
	switch v := resp.(type) {
	case NoContent:
		w.WriteHeader(http.StatusNoContent)
		return
	case createdResponse:
		if loc := v.createdLocation(); loc != "" {
			w.Header().Set("Location", loc)
		}
		writeJSON(ctx, w, statusOr(rt.Success, http.StatusCreated), "application/json", v.createdBody(), log)
		return
	}
	writeJSON(ctx, w, statusOr(rt.Success, http.StatusOK), "application/json", resp, log)
}

func statusOr(s, def int) int {
	if s == 0 {
		return def
	}
	return s
}

// checkRouteShape reports a malformed method or name, a trailing-slash
// path and a RateClass Handle does not accept (ErrRouteShape), and a path
// that is not a clean "/..." path under the group's base (ErrOutsideClaim:
// the route would leave its owner's claim).
func checkRouteShape(g *Group, rt Route) []error {
	var errs []error
	what := g.owner + " " + rt.Method + " " + g.base + rt.Path
	if rt.Method == "" || strings.ToUpper(rt.Method) != rt.Method || strings.ContainsAny(rt.Method, " /") {
		errs = append(errs, fmt.Errorf("%w: %s: method %q", ErrRouteShape, what, rt.Method))
	}
	if !routeName().MatchString(rt.Name) {
		errs = append(errs, fmt.Errorf("%w: %s: name %q is not lowerCamel", ErrRouteShape, what, rt.Name))
	}
	if rt.Path != "" && !strings.HasPrefix(rt.Path, "/") {
		errs = append(errs, fmt.Errorf("%w: %s: path %q does not start with /", ErrOutsideClaim, what, rt.Path))
	} else if err := cleanUnder(g.base+rt.Path, g.root+"/"); err != nil {
		errs = append(errs, fmt.Errorf("%w: %s: %v", ErrOutsideClaim, what, err))
	} else if strings.HasSuffix(rt.Path, "/") {
		// ServeMux serves a pattern ending in "/" as a subtree and redirects
		// the slashless path to it; native patterns are exact (05-http-api
		// §4.8), so the one trailing-slash form is ".../{$}".
		errs = append(errs, fmt.Errorf("%w: %s: path %q ends in \"/\", which ServeMux serves as a subtree; drop the slash or end the path in \"/{$}\"",
			ErrRouteShape, what, rt.Path))
	}
	if rt.RateClass > RateExempt {
		errs = append(errs, fmt.Errorf("%w: %s: unknown RateClass %d", ErrRouteShape, what, rt.RateClass))
	} else if rt.RateClass == RateExempt {
		errs = append(errs, fmt.Errorf("%w: %s: RateExempt is for infra probes and the SPA handler only", ErrRouteShape, what))
	}
	return errs
}

// cleanUnder reports a path that is not clean or does not start with prefix.
// "{$}" and a trailing slash are allowed; ".", ".." and empty segments are not.
func cleanUnder(p, prefix string) error {
	if !strings.HasPrefix(p, prefix) || strings.ContainsAny(p, " \t?#") {
		return fmt.Errorf("pattern %q is not under %q", p, prefix)
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		if s == "." || s == ".." || (s == "" && i != len(segs)-1) {
			return fmt.Errorf("pattern %q is not a clean path", p)
		}
	}
	return nil
}

func describe(ri RouteInfo) string { return ri.Owner + " " + ri.Method + " " + ri.Pattern }

// reflectBody takes the body:"json" field's schema out of the request type's
// schema (jsonschemax.For is generic, so Handle reflects Req and the body is
// one of its properties).
func (e *routeEntry) reflectBody(owner string, b *binding, reqSch *jsonschema.Schema, reqErr error) {
	f := b.typ.FieldByIndex(b.body.index)
	e.bodyType = f.Type
	name, err := componentName(owner, f.Type)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: request body: %w", describe(e.info), err))
		return
	}
	if reqErr != nil {
		e.errs = append(e.errs, fmt.Errorf("%w: %s: request: %v", ErrSchema, describe(e.info), reqErr))
		return
	}
	sch := reqSch.Properties[jsonName(f)]
	if sch == nil {
		e.errs = append(e.errs, fmt.Errorf("%w: %s: body field %s has no JSON schema (json:\"-\"?)", ErrSchema, describe(e.info), f.Name))
		return
	}
	if err := checkSnake(sch, "/body"); err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", describe(e.info), err))
	}
	e.bodyName, e.bodySch = name, sch
}

// jsonName is the property name encoding/json gives a struct field.
func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name
	}
	if n, _, _ := strings.Cut(tag, ","); n != "" {
		return n
	}
	return f.Name
}

// reflectResponse records the success body: T of Created[T] (the Body
// property of Resp's schema), nothing for NoContent, Resp otherwise.
func (e *routeEntry) reflectResponse(owner string, t reflect.Type, sch *jsonschema.Schema, err error) {
	e.status = statusOr(e.route.Success, http.StatusOK)
	if t == reflect.TypeFor[NoContent]() {
		e.status = statusOr(e.route.Success, http.StatusNoContent)
		return
	}
	if t.Kind() == reflect.Pointer && t.Implements(reflect.TypeFor[createdResponse]()) {
		e.errs = append(e.errs, fmt.Errorf("%w: %s: response %s: return Created[T] by value, not a pointer", ErrSchema, describe(e.info), t))
		return
	}
	if t.Implements(reflect.TypeFor[createdResponse]()) {
		e.status = statusOr(e.route.Success, http.StatusCreated)
		f, _ := t.FieldByName("Body")
		t = f.Type
		if sch != nil {
			sch = sch.Properties["Body"]
		}
	}
	e.respType = t
	name, nerr := componentName(owner, t)
	if nerr != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: response: %w", describe(e.info), nerr))
		return
	}
	if err != nil || sch == nil {
		e.errs = append(e.errs, fmt.Errorf("%w: %s: response: %v", ErrSchema, describe(e.info), err))
		return
	}
	if err := checkSnake(sch, ""); err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", describe(e.info), err))
	}
	e.respName, e.respSch = name, sch
}

// schemaOverrides are httpapi's reflector entries on top of jsonschemax.Base
// (05-http-api §7): rbac.Permission and json.RawMessage. config.Overrides
// joins when config lands (R2, F3).
func schemaOverrides() jsonschemax.Overrides {
	return jsonschemax.Base().With(jsonschemax.Overrides{
		reflect.TypeFor[rbac.Permission](): {Type: "string",
			Pattern: `^[a-z][a-z0-9]{1,15}:[a-z][a-z0-9_]{0,31}(\.[a-z][a-z0-9_]{0,31})?$`},
		reflect.TypeFor[json.RawMessage](): {},
	})
}

// componentName is <owner>.<GoType> (05-http-api §7); a generic type
// Page[x/y.T] becomes Page_T. An unnamed type cannot name a component.
func componentName(owner string, t reflect.Type) (string, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	n := t.Name()
	if n == "" {
		return "", fmt.Errorf("%w: unnamed type %s cannot name a component", ErrSchema, t)
	}
	if i := strings.IndexByte(n, '['); i >= 0 {
		args := strings.Split(strings.TrimSuffix(n[i+1:], "]"), ",")
		for j, a := range args {
			a = strings.TrimSpace(a)
			if k := strings.LastIndexAny(a, "./"); k >= 0 {
				a = a[k+1:]
			}
			args[j] = a
		}
		n = n[:i] + "_" + strings.Join(args, "_")
	}
	return owner + "." + n, nil
}

// checkSnake walks a schema and reports every property name that is not
// snake_case (ErrJSONName), naming its JSON pointer.
func checkSnake(s *jsonschema.Schema, at string) error {
	snake := snakeCase()
	var bad []string
	var walk func(s *jsonschema.Schema, at string)
	walk = func(s *jsonschema.Schema, at string) {
		if s == nil {
			return
		}
		for name, p := range s.Properties {
			if !snake.MatchString(name) {
				bad = append(bad, at+"/"+name)
			}
			walk(p, at+"/"+name)
		}
		walk(s.Items, at+"/items")
		walk(s.AdditionalProperties, at+"/additionalProperties")
		for _, x := range s.AnyOf {
			walk(x, at)
		}
		for _, x := range s.OneOf {
			walk(x, at)
		}
		for _, x := range s.AllOf {
			walk(x, at)
		}
	}
	walk(s, at)
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s", ErrJSONName, strings.Join(bad, ", "))
	}
	return nil
}
