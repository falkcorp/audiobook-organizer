// file: core/httpapi/raw_test.go
// version: 1.0.0
// guid: 9b1d7c04-62a5-4e8f-a3c9-17f4d0e58b26
// last-edited: 2026-11-10

package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/openapi/openapitest"
	"github.com/falkcorp/mediacore/rbac"
)

// loginBody and sessionBody are the fixture's JSON shapes.
type loginBody struct {
	Username string `json:"username"`
}

type sessionBody struct {
	PublicID string `json:"public_id"`
}

// camelRawBody breaks the one wire-name rule (05-http-api §3.4).
type camelRawBody struct {
	CamelCase string `json:"camelCase"`
}

// cookieHandler is a raw handler that sets a cookie, the case typed Handle
// cannot serve.
func cookieHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "batcave_session", Value: "alfred", Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"public_id":"` + r.PathValue("who") + `"}`))
	})
}

func TestHandleRaw_ServesThroughTheChainAndSetsACookie(t *testing.T) {
	r, g := newRouter(t, "signIn")
	HandleRaw(g, Route{Method: http.MethodPost, Path: "/sessions/{who}", Name: "signIn", Perm: rbac.Public, RateClass: RateAuth},
		RawDoc{Request: BodyOf[loginBody](), Success: BodyOf[sessionBody](), Status: http.StatusCreated, Query: []string{"remember"}},
		cookieHandler())
	h := mustBuild(t, r)
	rec := serve(h, http.MethodPost, "/api/batcave/v1/sessions/dick", `{"username":"dick"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"public_id":"dick"`) {
		t.Fatalf("POST = %d %s, want the handler's own answer", rec.Code, rec.Body)
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != "batcave_session" || !c[0].HttpOnly {
		t.Fatalf("cookies = %v, want the handler's Set-Cookie to reach the client", c)
	}
	if len(rec.Header().Get("X-Request-ID")) != 26 {
		t.Fatalf("X-Request-ID = %q: the raw route did not run the core chain", rec.Header().Get("X-Request-ID"))
	}
	var info RouteInfo
	for ri := range r.Routes() {
		info = ri
	}
	if info.Kind != RouteKindRaw || info.RateClass != RateAuth || info.Owner != "batcave" {
		t.Fatalf("RouteInfo = %+v, want a RouteKindRaw record with its RateClass", info)
	}
}

func TestHandleRaw_OpenAPIEntry(t *testing.T) {
	r, g := newRouter(t, "signIn")
	HandleRaw(g, Route{Method: http.MethodPost, Path: "/sessions/{who}", Name: "signIn", Perm: rbac.Public},
		RawDoc{Request: BodyOf[loginBody](), Success: BodyOf[sessionBody](), Status: http.StatusCreated, Query: []string{"remember"}},
		cookieHandler())
	mustBuild(t, r)
	doc, err := r.OpenAPI("batcave")
	if err != nil {
		t.Fatal(err)
	}
	b, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := openapitest.Validate(b); err != nil {
		t.Fatalf("document is not valid OpenAPI 3.1: %v\n%s", err, b)
	}
	var d struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Parameters  []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
			Responses map[string]json.RawMessage `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	op := d.Paths["/api/batcave/v1/sessions/{who}"]["post"]
	if op.OperationID != "batcave_signIn" {
		t.Fatalf("operationId = %q, want batcave_signIn", op.OperationID)
	}
	got := map[string]string{}
	for _, p := range op.Parameters {
		got[p.Name] = p.In
	}
	if got["who"] != "path" || got["remember"] != "query" {
		t.Fatalf("parameters = %v, want who in path and remember in query", got)
	}
	if _, ok := op.Responses["201"]; !ok {
		t.Fatalf("responses = %v, want the documented 201", op.Responses)
	}
	for _, name := range []string{"batcave.loginBody", "batcave.sessionBody"} {
		if _, ok := d.Components.Schemas[name]; !ok {
			t.Errorf("component %s missing", name)
		}
	}
}

func TestHandleRaw_RefusedRegistrations(t *testing.T) {
	cases := []struct {
		name string
		rt   Route
		doc  RawDoc
		h    http.Handler
		want error
	}{
		{"no permission", Route{Method: http.MethodGet, Path: "/x", Name: "x"}, RawDoc{}, cookieHandler(), ErrNoPermission},
		{"nil handler", Route{Method: http.MethodGet, Path: "/x", Name: "x", Perm: rbac.Public}, RawDoc{}, nil, ErrSchema},
		{"a body on GET", Route{Method: http.MethodGet, Path: "/x", Name: "x", Perm: rbac.Public}, RawDoc{Request: BodyOf[loginBody]()}, cookieHandler(), ErrBadBinding},
		{"not lowerCamel", Route{Method: http.MethodGet, Path: "/x", Name: "Bad_Name", Perm: rbac.Public}, RawDoc{}, cookieHandler(), ErrRouteShape},
		{"RateExempt", Route{Method: http.MethodGet, Path: "/x", Name: "x", Perm: rbac.Public, RateClass: RateExempt}, RawDoc{}, cookieHandler(), ErrRouteShape},
		{"trailing slash", Route{Method: http.MethodGet, Path: "/x/", Name: "x", Perm: rbac.Public}, RawDoc{}, cookieHandler(), ErrRouteShape},
		{"camelCase body", Route{Method: http.MethodPost, Path: "/x", Name: "x", Perm: rbac.Public}, RawDoc{Request: BodyOf[camelRawBody]()}, cookieHandler(), ErrJSONName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, g := newRouter(t, "x")
			HandleRaw(g, tc.rt, tc.doc, tc.h)
			_, err := r.Build()
			if err == nil {
				t.Fatal("Build accepted the registration")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Build = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestHandleRaw_NonPublicFailsClosedUntilAuthExists(t *testing.T) {
	r, g := newRouter(t)
	HandleRaw(g, Route{Method: http.MethodPost, Path: "/sessions/{who}/revoke", Name: "revoke", Perm: rbac.AnyAuthenticated,
		Guards: Guards{Interactive: true}}, RawDoc{}, cookieHandler())
	if _, err := r.Build(); !errors.Is(err, ErrNoAuthService) {
		t.Fatalf("Build = %v, want ErrNoAuthService for a non-Public raw route", err)
	}
}

func TestHandleRaw_NameUniqueAcrossKinds(t *testing.T) {
	r, g := newRouter(t, "same")
	Handle(g, Route{Method: http.MethodGet, Path: "/a/{id}", Name: "same", Perm: rbac.Public}, getGadget)
	HandleRaw(g, Route{Method: http.MethodGet, Path: "/b", Name: "same", Perm: rbac.Public}, RawDoc{}, cookieHandler())
	if _, err := r.Build(); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("Build = %v, want ErrDuplicateRoute for one operation name on a typed and a raw route", err)
	}
}

func TestHandleRaw_FixedGroupAcceptsOnlyTheExactPatterns(t *testing.T) {
	const base = "/api/v1/auth/oauth"
	build := func(paths ...string) error {
		r := NewRouter(Options{}, Deps{Log: slog.New(slog.DiscardHandler)})
		g := r.CoreFixedGroup(base)
		for i, p := range paths {
			HandleRaw(g, Route{Method: http.MethodGet, Path: p, Name: "route" + string(rune('A'+i)), Perm: rbac.Public, RateClass: RateAuth}, RawDoc{}, cookieHandler())
		}
		_, err := r.Build()
		return err
	}
	if err := build("/{provider}/callback", "/{provider}/start"); err != nil {
		t.Fatalf("the two exact patterns as raw routes: %v", err)
	}
	if err := build("/{provider}/callback", "/{provider}/unlink"); !errors.Is(err, ErrPrefixCollision) {
		t.Fatalf("a third raw route on the fixed group: %v, want ErrPrefixCollision", err)
	}
}

func TestWriteError_IsHandlesProblem(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, Conflict("email_in_use", "sign in and link from your profile"), nil)
	})
	rec := serve(h, http.MethodGet, "/x", "")
	if rec.Code != http.StatusConflict || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("WriteError = %d %s, want a 409 problem+json", rec.Code, rec.Header().Get("Content-Type"))
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != "email_in_use" {
		t.Fatalf("body = %s (%v), want code email_in_use", rec.Body, err)
	}
	h = func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, errors.New("pebble: secret path /var/lib/x"), nil)
	}
	rec = serve(h, http.MethodGet, "/x", "")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "pebble") {
		t.Fatalf("a plain error = %d %s, want a 500 with the cause withheld", rec.Code, rec.Body)
	}
}

func TestError_WithRetryAfterAndThrottled(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, Throttled(90*time.Second), nil)
	})
	rec := serve(h, http.MethodGet, "/x", "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "90" {
		t.Fatalf("Throttled = %d Retry-After %q, want 429 and 90", rec.Code, rec.Header().Get("Retry-After"))
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != "throttled" {
		t.Fatalf("body = %s (%v), want code throttled", rec.Body, err)
	}
	// A sub-second wait still tells the client to wait a whole second.
	rec = serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, Throttled(0), nil)
	}), http.MethodGet, "/x", "")
	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Throttled(0) Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
	// Any other code can carry one, and 0 omits the header.
	rec = serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, (&Error{Status: http.StatusServiceUnavailable, Code: "busy"}).WithRetryAfter(2*time.Second), nil)
	}), http.MethodGet, "/x", "")
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("busy = %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	rec = serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, (&Error{Status: http.StatusServiceUnavailable, Code: "busy"}).WithRetryAfter(0), nil)
	}), http.MethodGet, "/x", "")
	if rec.Header().Get("Retry-After") != "" {
		t.Fatalf("WithRetryAfter(0) set Retry-After %q", rec.Header().Get("Retry-After"))
	}
}

func TestDecodeJSON(t *testing.T) {
	do := func(ct, body string, limit int64) (loginBody, *Error) {
		var got loginBody
		var out *Error
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := DecodeJSON(w, r, limit, &got); err != nil {
				if !errors.As(err, &out) {
					t.Fatalf("DecodeJSON error %T is not an *Error: %v", err, err)
				}
			}
		})
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		return got, out
	}
	if got, err := do("application/json", `{"username":"dick"}`, 0); err != nil || got.Username != "dick" {
		t.Fatalf("a good body: %+v %v", got, err)
	}
	for _, tc := range []struct {
		name, ct, body string
		limit          int64
		status         int
		code           string
	}{
		{"wrong content type", "text/plain", `{"username":"dick"}`, 0, 415, "unsupported_media_type"},
		{"no content type", "", `{"username":"dick"}`, 0, 415, "unsupported_media_type"},
		{"unknown field", "application/json", `{"username":"dick","admin":true}`, 0, 400, "unknown_field"},
		{"trailing data", "application/json", `{"username":"dick"} {}`, 0, 400, "malformed_json"},
		{"too big", "application/json", `{"username":"` + strings.Repeat("a", 64) + `"}`, 16, 413, "body_too_large"},
		{"wrong type", "application/json", `{"username":7}`, 0, 422, "validation_failed"},
		{"empty", "application/json", ``, 0, 400, "malformed_json"},
	} {
		if _, err := do(tc.ct, tc.body, tc.limit); err == nil || err.Status != tc.status || err.Code != tc.code {
			t.Errorf("%s: got %+v, want %d %s", tc.name, err, tc.status, tc.code)
		}
	}
	if err := DecodeJSON(nil, nil, 0, loginBody{}); err == nil {
		t.Error("a non-pointer destination was accepted")
	}
}
