// file: core/httpapi/problem.go
// version: 1.0.0
// guid: 58d791ab-c222-4ab1-b523-0444ce052274
// last-edited: 2026-11-07

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

// Problem is the native error body, RFC 9457 application/problem+json with
// extension members (05-http-api §3.3). Every native route and every core
// middleware emits it; legacy-mount handlers keep their own bodies (§4.3).
type Problem struct {
	Type      string       `json:"type"` // "https://mediacore.dev/problems/<code>" stable URI, or "about:blank"
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"` // request path
	Code      string       `json:"code"`               // machine code, snake_case, stable: "not_found", "permission_denied", ...
	RequestID string       `json:"request_id"`
	Errors    []FieldError `json:"errors,omitempty"`              // validation details
	Required  string       `json:"required_permission,omitempty"` // on 403, the missing scope
}

// FieldError is one validation detail of a Problem.
type FieldError struct {
	Field   string `json:"field"` // JSON pointer, "/body/title" or "/query/limit"
	Code    string `json:"code"`  // "required", "too_long", "invalid_enum", ...
	Message string `json:"message"`
}

// Error is what handlers return; core maps it to a Problem. Plain errors map
// to 500 "internal" with Detail withheld (logged with the request id).
type Error struct {
	Status int
	Code   string
	Detail string
	Fields []FieldError
	Err    error // wrapped cause, never serialized

	required   rbac.Permission // Forbidden's scope, written as required_permission
	retryAfter time.Duration   // Unavailable's Retry-After
}

// Error implements error.
func (e *Error) Error() string {
	msg := fmt.Sprintf("httpapi: %d %s", e.Status, e.Code)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the wrapped cause.
func (e *Error) Unwrap() error { return e.Err }

// NotFound is a 404 not_found naming the kind and ID that were not found.
func NotFound(kind, id string) *Error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Detail: fmt.Sprintf("%s %q not found", kind, id)}
}

// Conflict is a 409 with a caller-chosen snake_case code.
func Conflict(code, detail string) *Error {
	return &Error{Status: http.StatusConflict, Code: code, Detail: detail}
}

// Invalid is a 422 validation_failed carrying the field details.
func Invalid(fields ...FieldError) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Fields: fields}
}

// Field builds one FieldError: Invalid(Field("/body/name", "too_long", "must be 1..64 characters")).
func Field(pointer, code, message string) FieldError {
	return FieldError{Field: pointer, Code: code, Message: message}
}

// Forbidden is a 403 permission_denied naming the missing scope.
func Forbidden(scope rbac.Permission) *Error {
	return &Error{Status: http.StatusForbidden, Code: "permission_denied", required: scope}
}

// Unavailable is a 503 unavailable with a Retry-After header (0 omits it).
func Unavailable(detail string, retryAfter time.Duration) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Detail: detail, retryAfter: retryAfter}
}

// Sentinel causes that modules may wrap; core maps them without a constructor.
var (
	ErrNotFound     = errors.New("httpapi: not found")    // 404 not_found
	ErrConflict     = errors.New("httpapi: conflict")     // 409 conflict
	ErrPrecondition = errors.New("httpapi: precondition") // 412 precondition_failed (If-Match)
	ErrUnavailable  = errors.New("httpapi: unavailable")  // 503 unavailable
)

// Registration-time errors returned (joined with errors.Join) by Router.Build.
var (
	ErrNoPermission     = errors.New("httpapi: route has no permission requirement")
	ErrUnknownScope     = errors.New("httpapi: route permission is not in the catalog")
	ErrForeignScope     = errors.New("httpapi: route permission is outside the module namespace")
	ErrDuplicateRoute   = errors.New("httpapi: duplicate method+pattern")
	ErrPatternConflict  = errors.New("httpapi: ServeMux patterns conflict")
	ErrPrefixCollision  = errors.New("httpapi: path prefix claimed by two owners")
	ErrBadBinding       = errors.New("httpapi: invalid request binding tags")
	ErrSchema           = errors.New("httpapi: type cannot be described in OpenAPI")
	ErrJSONName         = errors.New("httpapi: JSON property name is not snake_case")
	ErrLegacyManifest   = errors.New("httpapi: legacy route missing from or inconsistent with the manifest")
	ErrLegacyAuthGin    = errors.New("httpapi: LegacyAuthGin mount not named by app.Options.LegacyAuthGinUntil")
	ErrOwnAuthMethod    = errors.New("httpapi: OwnAuth route names no auth method registered by its module")
	ErrNoAuthService    = errors.New("httpapi: non-Public native route registered before auth/rbac exist")
	ErrPublicNotAllowed = errors.New("httpapi: module native route uses rbac.Public but is not in its Manifest.PublicRoutes")
	ErrOutsideClaim     = errors.New("httpapi: legacy route pattern outside its owner's claims")
	ErrShadow           = errors.New("httpapi: a route of one owner is strictly more specific than a route of another owner over the same paths")
)

// problemTypeBase is the stable URI prefix of every problem type.
const problemTypeBase = "https://mediacore.dev/problems/"

// problemFor maps a handler error to its Problem and the extra headers it
// needs. internal reports a plain error, whose cause is logged and withheld.
func problemFor(err error) (p Problem, header http.Header, internal bool) {
	var he *Error
	switch {
	case errors.As(err, &he):
		p = Problem{Status: he.Status, Code: he.Code, Detail: he.Detail, Errors: he.Fields,
			Required: string(he.required)}
		if he.retryAfter > 0 {
			header = http.Header{"Retry-After": {strconv.Itoa(int((he.retryAfter + time.Second - 1) / time.Second))}}
		}
	case errors.Is(err, ErrNotFound):
		p = Problem{Status: http.StatusNotFound, Code: "not_found"}
	case errors.Is(err, ErrConflict):
		p = Problem{Status: http.StatusConflict, Code: "conflict"}
	case errors.Is(err, ErrPrecondition):
		p = Problem{Status: http.StatusPreconditionFailed, Code: "precondition_failed"}
	case errors.Is(err, ErrUnavailable):
		p = Problem{Status: http.StatusServiceUnavailable, Code: "unavailable"}
	default:
		return Problem{Status: http.StatusInternalServerError, Code: "internal"}, nil, true
	}
	if p.Status < 400 || p.Status > 599 {
		return Problem{Status: http.StatusInternalServerError, Code: "internal"}, nil, true
	}
	return p, header, false
}

// writeProblem completes p (type, title, instance, request ID) and writes it.
func writeProblem(ctx context.Context, w http.ResponseWriter, r *http.Request, p Problem, header http.Header, log *slog.Logger) {
	if p.Code == "" {
		p.Code = "error"
	}
	if p.Type == "" {
		p.Type = problemTypeBase + p.Code
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	if p.Instance == "" && r != nil {
		p.Instance = r.URL.Path
	}
	p.RequestID = RequestID(ctx)
	for k, vs := range header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	writeJSON(ctx, w, p.Status, "application/problem+json", p, log)
}

// writeError maps err with problemFor and writes it; a plain error is logged
// with its cause and answered 500 with the detail withheld.
func writeError(ctx context.Context, w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	p, header, internal := problemFor(err)
	if internal && log != nil {
		log.ErrorContext(ctx, "httpapi: handler error", "error", err, "path", r.URL.Path, "request_id", RequestID(ctx))
	}
	writeProblem(ctx, w, r, p, header, log)
}

// legacyError is the legacy error envelope ({error, code, status}), the shape
// of ABO's internal/httputil ErrorResponse that legacy-mount handlers keep
// (05-http-api §4.3). The router writes it for an unmatched path under a
// legacy prefix (§4.3, §8.2).
type legacyError struct {
	Error  string `json:"error"`
	Code   string `json:"code,omitempty"`
	Status int    `json:"status"`
}

// writeLegacyNotFound answers 404 with the bytes HEAD's gin NoRoute writes for
// a server-side path today (httputil.RespondWithNotFound(c, "endpoint", ""),
// written by gin's c.JSON: json.Marshal, no trailing newline), so a legacy
// client sees the same body whether gin or the router answers (§12,
// TestLegacyError_ShapeGolden).
func writeLegacyNotFound(ctx context.Context, w http.ResponseWriter, log *slog.Logger) {
	b, err := json.Marshal(legacyError{Error: "endpoint not found", Code: "NOT_FOUND", Status: http.StatusNotFound})
	if err != nil { // a fixed struct of strings and an int: unreachable
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	if _, err := w.Write(b); err != nil && log != nil {
		log.DebugContext(ctx, "httpapi: write response", "error", err)
	}
}

// writeJSON encodes v before writing any header, so an encode failure is still
// a clean 500, then writes status and body. A failed write (the client went
// away) is logged at debug.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, contentType string, v any, log *slog.Logger) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		if log != nil {
			log.ErrorContext(ctx, "httpapi: encode response", "error", err)
		}
		http.Error(w, `{"code":"internal","status":500}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil && log != nil {
		log.DebugContext(ctx, "httpapi: write response", "error", err)
	}
}

// requestIDKey carries the request ID stage 1 (R1-17) resolves.
type requestIDKey struct{}

// RequestID returns the request ID of ctx (05-http-api §4.7), "" outside a
// request that stage 1 has seen.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
