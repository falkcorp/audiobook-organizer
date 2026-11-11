// file: core/httpapi/raw.go
// version: 1.0.0
// guid: 3f0c6a52-8d7e-4c1b-9a64-5e2b17d0c3a8
// last-edited: 2026-11-10

package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/falkcorp/mediacore/internal/jsonschemax"
)

// RawDoc describes a HandleRaw route's OpenAPI entry (05-http-api §3.1): the
// handler writes its own response, so the reflector cannot read the shapes
// from a handler signature. A zero RawDoc is a route with no JSON body in
// either direction, answering 200.
type RawDoc struct {
	// Request is the type of the JSON request body, nil for none. A route
	// whose body is not JSON leaves it nil.
	Request reflect.Type
	// Success is the type of the JSON success body, nil for none (a redirect,
	// a 204, a stream).
	Success reflect.Type
	// Status is the success status the document lists; 0 is 200.
	Status int
	// Query names the query parameters the handler reads, for the document.
	// They are optional strings: a raw handler validates them itself.
	Query []string
}

// BodyOf is the RawDoc type of a body whose Go type is T.
func BodyOf[T any]() reflect.Type { return reflect.TypeFor[T]() }

// HandleRaw registers an operation that writes its own response: a redirect, a
// response that sets a cookie, a download, a stream (05-http-api §3.1). Perm,
// Guards, RateClass and MaxBody apply exactly as for Handle, because the
// route runs the same core chain; what Handle adds on top (binding, strict
// decoding, the success writer) is the handler's own. A handler reads path
// wildcards with r.PathValue, takes the caller from auth.PrincipalFrom, and
// reports an error through WriteError so a failure is the same problem+json
// as a typed route's. Like Handle it never panics: a bad registration is
// returned by Build. The route is recorded as RouteKindRaw.
func HandleRaw(g *Group, rt Route, doc RawDoc, h http.Handler) {
	e := &routeEntry{route: rt, handler: h}
	e.info = RouteInfo{Owner: g.owner, Method: rt.Method, Pattern: g.base + rt.Path, Name: rt.Name,
		Perm: rt.Perm, Guards: rt.Guards, RateClass: rt.RateClass, Kind: RouteKindRaw}
	if g.err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", describe(e.info), g.err))
	}
	e.errs = append(e.errs, checkRouteShape(g, rt)...)
	if h == nil {
		e.errs = append(e.errs, fmt.Errorf("%w: %s: nil handler", ErrSchema, describe(e.info)))
	}
	e.status = statusOr(doc.Status, http.StatusOK)
	for _, n := range pathNames(e.info.Pattern) {
		e.rawParams = append(e.rawParams, paramDesc{name: n, in: "path", schema: paramSchema(kindString)})
	}
	for _, q := range doc.Query {
		e.rawParams = append(e.rawParams, paramDesc{name: q, in: "query", schema: paramSchema(kindString)})
	}
	if doc.Request != nil {
		if rt.Method == http.MethodGet || rt.Method == http.MethodHead {
			e.errs = append(e.errs, fmt.Errorf("%w: %s: a request body on a %s route", ErrBadBinding, describe(e.info), rt.Method))
		} else if name, sch, err := rawSchema(g.owner, doc.Request, "/body"); err != nil {
			e.errs = append(e.errs, fmt.Errorf("%s: request body: %w", describe(e.info), err))
		} else {
			e.bodyName, e.bodyType, e.bodySch = name, doc.Request, sch
		}
	}
	if doc.Success != nil {
		if name, sch, err := rawSchema(g.owner, doc.Success, ""); err != nil {
			e.errs = append(e.errs, fmt.Errorf("%s: response: %w", describe(e.info), err))
		} else {
			e.respName, e.respType, e.respSch = name, doc.Success, sch
		}
	}
	g.router.add(e)
}

// rawSchema reflects a RawDoc body type and applies the snake_case rule to it,
// as Handle does for its own bodies.
func rawSchema(owner string, t reflect.Type, at string) (string, *jsonschema.Schema, error) {
	name, err := componentName(owner, t)
	if err != nil {
		return "", nil, err
	}
	sch, err := jsonschemax.ForType(t, schemaOverrides())
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrSchema, err)
	}
	if err := checkSnake(sch, at); err != nil {
		return "", nil, err
	}
	return name, sch, nil
}

// WriteError writes err as a HandleRaw handler's failure: the problem+json
// Handle would write for the same error (an *Error keeps its status and code,
// a plain error is a 500 whose cause is logged to log and withheld from the
// body). log may be nil.
func WriteError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	writeError(r.Context(), w, r, err, log)
}
