// file: core/httpapi/dispatch_test.go
// version: 1.0.2
// guid: 10f67bda-3d92-4798-b55c-741cfba03165
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
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

// spaHandler stands in for the "/" owner and counts what reaches it.
type spaHandler struct{ hits atomic.Int64 }

func (s *spaHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.hits.Add(1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><title>Gotham</title>"))
}

func TestProblem_ShapeGolden(t *testing.T) {
	r, g := newRouter(t, "getGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	h := mustBuild(t, r)

	req := httptest.NewRequest(http.MethodGet, "/api/batcave/v1/gadgets/joker-gas", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey{}, "req-0001"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	const want = `{"type":"https://mediacore.dev/problems/not_found","title":"Not Found","status":404,` +
		`"detail":"gadget \"joker-gas\" not found","instance":"/api/batcave/v1/gadgets/joker-gas",` +
		`"code":"not_found","request_id":"req-0001"}` + "\n"
	if rec.Code != http.StatusNotFound || rec.Body.String() != want {
		t.Fatalf("got %d\n%s\nwant 404\n%s", rec.Code, rec.Body.String(), want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestProblem_ErrorMapping(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		code   string
		check  func(t *testing.T, p map[string]any, h http.Header)
	}{
		"forbidden names the scope": {Forbidden("batcave:write"), 403, "permission_denied",
			func(t *testing.T, p map[string]any, _ http.Header) {
				if p["required_permission"] != "batcave:write" {
					t.Errorf("required_permission = %v", p["required_permission"])
				}
			}},
		"invalid carries fields": {Invalid(Field("/body/name", "too_long", "1..64 characters")), 422, "validation_failed",
			func(t *testing.T, p map[string]any, _ http.Header) {
				errs, _ := p["errors"].([]any)
				if len(errs) != 1 {
					t.Errorf("errors = %v", p["errors"])
				}
			}},
		"unavailable sets Retry-After": {Unavailable("cave flooded", 1500*time.Millisecond), 503, "unavailable",
			func(t *testing.T, _ map[string]any, h http.Header) {
				if h.Get("Retry-After") != "2" {
					t.Errorf("Retry-After = %q, want 2 (rounded up)", h.Get("Retry-After"))
				}
			}},
		"wrapped sentinel":   {errors.Join(errors.New("ctx"), ErrConflict), 409, "conflict", nil},
		"precondition":       {ErrPrecondition, 412, "precondition_failed", nil},
		"conflict with code": {Conflict("gadget_exists", "already built"), 409, "gadget_exists", nil},
		"plain error withholds detail": {errors.New("the Riddler's secret"), 500, "internal",
			func(t *testing.T, p map[string]any, _ http.Header) {
				if _, ok := p["detail"]; ok {
					t.Errorf("detail leaked: %v", p["detail"])
				}
			}},
		"out-of-range status is internal": {&Error{Status: 200, Code: "ok"}, 500, "internal", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, g := newRouter(t, "fail")
			Handle(g, Route{Method: http.MethodGet, Path: "/fail", Name: "fail", Perm: rbac.Public},
				func(context.Context, *In[struct{}]) (Gadget, error) { return Gadget{}, tc.err })
			rec := serve(mustBuild(t, r), http.MethodGet, "/api/batcave/v1/fail", "")
			var p map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("body %q: %v", rec.Body.String(), err)
			}
			if rec.Code != tc.status || p["code"] != tc.code || p["status"] != float64(tc.status) {
				t.Fatalf("got %d %v, want %d %s", rec.Code, p, tc.status, tc.code)
			}
			if tc.check != nil {
				tc.check(t, p, rec.Header())
			}
		})
	}
}

// legacyRouter returns a router whose stand-in legacy mount (R1-14's real
// MountLegacy re-runs these cells) claims /api/v1/.
func legacyRouter(t *testing.T, servesSPA bool, h http.Handler) *Router {
	t.Helper()
	r := NewRouter(Options{}, Deps{})
	if err := r.Claim("batcomputer", "/api/v1/"); err != nil {
		t.Fatal(err)
	}
	if err := r.MountLegacy(LegacyMount{Owner: "batcomputer", Handler: h, ServesSPA: servesSPA, Auth: LegacyAuthCore}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLegacyError_ShapeGolden(t *testing.T) {
	h := mustBuild(t, legacyRouter(t, false, http.NotFoundHandler()))
	rec := serve(h, http.MethodGet, "/api/v1/riddles/unsolved", "")
	// HEAD's gin NoRoute body for a server-side path, byte for byte
	// (httputil.RespondWithNotFound(c, "endpoint", "") through c.JSON).
	const want = `{"error":"endpoint not found","code":"NOT_FOUND","status":404}`
	if rec.Code != http.StatusNotFound || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 404 %q", rec.Code, rec.Body.String(), want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestUnmatched_APIPathIsProblemNotHTML(t *testing.T) {
	r, g := newRouter(t, "getGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	spa := &spaHandler{}
	if err := r.SPA(spa, SPAOptions{}); err != nil {
		t.Fatal(err)
	}
	h := mustBuild(t, r)
	for _, p := range []string{"/api/nope", "/api/batcave/v1/unknown", "/api/batcave/v9/gadgets/x", "/API/batcave"} {
		rec := serve(h, http.MethodGet, p, "")
		if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("GET %s = %d %q %q", p, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
	if spa.hits.Load() != 0 {
		t.Fatalf("the SPA answered %d /api/ request(s)", spa.hits.Load())
	}
	// With no "/" owner every unmatched path is a problem 404.
	r, _ = newRouter(t)
	rec := serve(mustBuild(t, r), http.MethodGet, "/library", "")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("GET /library without a root owner = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestMethodNotAllowed_HasAllow(t *testing.T) {
	r, g := newRouter(t, "getGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	if err := r.SPA(&spaHandler{}, SPAOptions{}); err != nil {
		t.Fatal(err)
	}
	rec := serve(mustBuild(t, r), http.MethodPut, "/api/batcave/v1/gadgets/batarang", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d %q", rec.Code, rec.Body.String())
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow = %q, want %q", allow, "GET, HEAD")
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != "method_not_allowed" {
		t.Fatalf("body %q (%v)", rec.Body.String(), err)
	}
}

func TestDecode_StrictUnknownField(t *testing.T) {
	r, g := newRouter(t, "createGadget", "importGadget")
	create := func(_ context.Context, in *In[createGadgetReq]) (Gadget, error) { return in.Req.Body, nil }
	Handle(g, Route{Method: http.MethodPost, Path: "/gadgets", Name: "createGadget", Perm: rbac.Public}, create)
	Handle(g, Route{Method: http.MethodPost, Path: "/imports", Name: "importGadget", Perm: rbac.Public, Lenient: true}, create)
	h := mustBuild(t, r)

	const body = `{"name":"batarang","owner":"Alfred Pennyworth","color":"black"}`
	rec := serve(h, http.MethodPost, "/api/batcave/v1/gadgets", body)
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest || p.Code != "unknown_field" || len(p.Errors) != 1 || p.Errors[0].Field != "/body/color" {
		t.Fatalf("strict route = %d %+v", rec.Code, p)
	}
	if rec = serve(h, http.MethodPost, "/api/batcave/v1/imports", body); rec.Code != http.StatusOK {
		t.Fatalf("lenient route = %d %q", rec.Code, rec.Body.String())
	}

	cases := map[string]struct {
		ct, body string
		status   int
		code     string
	}{
		"wrong content type": {"text/plain", `{}`, 415, "unsupported_media_type"},
		"trailing data":      {"application/json", `{"name":"a"} {}`, 400, "malformed_json"},
		"empty body":         {"application/json", ``, 400, "malformed_json"},
		"too large":          {"application/json", `{"name":"` + strings.Repeat("a", defaultJSONMaxBytes) + `"}`, 413, "body_too_large"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/batcave/v1/gadgets", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.ct)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			var p Problem
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if rec.Code != tc.status || p.Code != tc.code {
				t.Fatalf("got %d %q, want %d %s", rec.Code, p.Code, tc.status, tc.code)
			}
		})
	}
}

func TestUnclean_PathNotRedirectedByMux(t *testing.T) {
	r, _ := newRouter(t)
	r.Infra("GET /ping", rbac.Public, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	spa := &spaHandler{}
	if err := r.SPA(spa, SPAOptions{}); err != nil {
		t.Fatal(err)
	}
	h := mustBuild(t, r)
	for _, p := range []string{"//ping", "/a/../ping", "/library/./x"} {
		rec := serve(h, http.MethodGet, p, "")
		if rec.Code == http.StatusMovedPermanently || rec.Code == http.StatusTeapot {
			t.Errorf("GET %s = %d (Location %q): the mux decided, not the / owner", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if spa.hits.Load() != 3 {
		t.Fatalf("the / owner saw %d of 3 unclean paths", spa.hits.Load())
	}
	rec := serve(h, http.MethodGet, "//api/batcave", "")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("GET //api/batcave = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSPA_NotRateLimitedAndPublic(t *testing.T) {
	r, _ := newRouter(t)
	spa := &spaHandler{}
	if err := r.SPA(spa, SPAOptions{}); err != nil {
		t.Fatal(err)
	}
	h := mustBuild(t, r)
	for i := range 200 {
		p := "/"
		if i%2 == 1 {
			p = "/library/shelf"
		}
		if rec := serve(h, http.MethodGet, p, ""); rec.Code != http.StatusOK {
			t.Fatalf("cold load %d of %s = %d", i, p, rec.Code)
		}
	}
	// The rate limit is R1-17's stage 10; what it keys on is recorded here.
	var root []RouteInfo
	for ri := range r.Routes() {
		if ri.Kind == RouteKindSPA {
			root = append(root, ri)
		}
	}
	if len(root) != 1 || root[0].Pattern != "/" || root[0].Perm.Kind() != rbac.KindPublic || root[0].RateClass != RateExempt {
		t.Fatalf("SPA routes = %+v, want one / route, Public, RateExempt", root)
	}
}

func TestSPA_SingleOwnerOfRoot(t *testing.T) {
	r := legacyRouter(t, true, http.NotFoundHandler())
	if err := r.SPA(&spaHandler{}, SPAOptions{}); !errors.Is(err, ErrPrefixCollision) {
		t.Fatalf("SPA after a ServesSPA mount = %v, want ErrPrefixCollision", err)
	}
	if _, err := r.Build(); !errors.Is(err, ErrPrefixCollision) {
		t.Fatalf("Build = %v, want ErrPrefixCollision", err)
	}

	r = NewRouter(Options{}, Deps{})
	if err := r.SPA(&spaHandler{}, SPAOptions{}); err != nil {
		t.Fatal(err)
	}
	err := r.MountLegacy(LegacyMount{Owner: "batcomputer", Handler: http.NotFoundHandler(), Auth: LegacyAuthCore, ServesSPA: true})
	if !errors.Is(err, ErrPrefixCollision) {
		t.Fatalf("ServesSPA mount after SPA = %v, want ErrPrefixCollision", err)
	}
	if err := r.SPA(&spaHandler{}, SPAOptions{}); !errors.Is(err, ErrPrefixCollision) {
		t.Fatalf("second SPA = %v, want ErrPrefixCollision", err)
	}
}

func TestNonSPA_PredicateCoversClaims(t *testing.T) {
	r, _ := newRouter(t)
	for _, p := range []string{"GET /livez", "GET /readyz", "GET /metrics"} {
		r.Infra(p, rbac.Public, http.NotFoundHandler())
	}
	if err := r.MountLegacy(LegacyMount{Owner: "batcomputer", Handler: http.NotFoundHandler(), Auth: LegacyAuthCore,
		TopSegments: []string{"socket.io", "login"}, SPAShared: []string{"login"}}); err != nil {
		t.Fatal(err)
	}
	spa := &spaHandler{}
	if err := r.SPA(spa, SPAOptions{}); err != nil {
		t.Fatal(err)
	}
	h := mustBuild(t, r)
	never := []struct{ method, path string }{
		{http.MethodGet, "/socket.io"}, {http.MethodGet, "/API/V1/x"}, {http.MethodPost, "/library"},
		{http.MethodGet, "/health/"}, {http.MethodGet, "/HEALTH"}, {http.MethodGet, "/metrics/"},
		{http.MethodGet, "/livez/"},
	}
	for _, c := range never {
		before := spa.hits.Load()
		rec := serve(h, c.method, c.path, "")
		if spa.hits.Load() != before {
			t.Errorf("%s %s reached the SPA handler (%d)", c.method, c.path, rec.Code)
		}
	}
	for _, p := range []string{"/assets/x.js", "/login"} {
		before := spa.hits.Load()
		if rec := serve(h, http.MethodGet, p, ""); rec.Code != http.StatusOK || spa.hits.Load() != before+1 {
			t.Errorf("GET %s = %d, want the SPA", p, rec.Code)
		}
	}
	got := strings.Join(r.NonSPASegments(), ",")
	if got != "api,auth,events,health,livez,metrics,readyz,socket.io" {
		t.Fatalf("NonSPASegments = %s", got)
	}
}

func TestNonSPA_LegacyFallbackConsultsPredicate(t *testing.T) {
	gin := &spaHandler{} // the stand-in gin engine's NoRoute page
	r := legacyRouter(t, true, gin)
	r.Infra("GET /livez", rbac.Public, http.NotFoundHandler())
	r.Infra("GET /readyz", rbac.Public, http.NotFoundHandler())
	h := mustBuild(t, r)
	for _, p := range []string{"/livez/", "/READYZ"} {
		rec := serve(h, http.MethodGet, p, "")
		if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("GET %s = %d %q, want problem 404", p, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if gin.hits.Load() != 0 {
		t.Fatalf("gin's NoRoute answered %d probe variant(s)", gin.hits.Load())
	}
	if rec := serve(h, http.MethodGet, "/library", ""); rec.Code != http.StatusOK || gin.hits.Load() != 1 {
		t.Fatalf("GET /library = %d, want the legacy / owner", rec.Code)
	}
}

func TestStage2_RouteSurvivesRequestClone(t *testing.T) {
	r, g := newRouter(t, "getGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	type cloneKey struct{}
	// A stage between stage 2 and the mux that clones the request.
	r.stage = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), cloneKey{}, "Alfred")))
		})
	}
	var routes []string
	r.observe = func(route string) { routes = append(routes, route) }
	h := mustBuild(t, r)
	serve(h, http.MethodGet, "/api/batcave/v1/gadgets/batarang", "")
	serve(h, http.MethodGet, "/api/batcave/v1/nope", "")
	// The span name is "GET " + route (stage 2, R1-17); both read the carrier.
	if len(routes) != 2 || routes[0] != "/api/batcave/v1/gadgets/{id}" || routes[1] != "" {
		t.Fatalf("observed routes = %q, want the pattern, then none for an unmatched path", routes)
	}
}

// utilityBelt is a nested body whose wrong-typed and unknown properties must
// be reported by JSON pointer, in JSON's vocabulary.
type utilityBelt struct {
	Owner   string   `json:"owner"`
	Gadgets []Gadget `json:"gadgets"`
	Charges int      `json:"charges"`
}

type packBeltReq struct {
	Body utilityBelt `body:"json"`
}

// TestDecode_ErrorsNameNoGoTypes is the review's M3: a body error's detail
// never carries encoding/json's text (Go type and struct field names); a
// type mismatch is 422 invalid_type at the property's pointer; a syntax
// error names only its offset; an unknown property is located by pointer,
// nested ones included, without parsing encoding/json's wording.
func TestDecode_ErrorsNameNoGoTypes(t *testing.T) {
	r, g := newRouter(t, "packBelt")
	Handle(g, Route{Method: http.MethodPost, Path: "/belts", Name: "packBelt", Perm: rbac.Public},
		func(_ context.Context, in *In[packBeltReq]) (Gadget, error) {
			return Gadget{Owner: in.Req.Body.Owner}, nil
		})
	h := mustBuild(t, r)
	cases := map[string]struct {
		body          string
		status        int
		code, field   string
		fieldCode     string
		detailContain string
	}{
		"wrong type":        {`{"owner":"Bruce Wayne","charges":"three"}`, 422, "validation_failed", "/body/charges", "invalid_type", ""},
		"nested wrong type": {`{"gadgets":[{"name":7}]}`, 422, "validation_failed", "/body/gadgets/0/name", "invalid_type", ""},
		"top-level type":    {`["batarang"]`, 422, "validation_failed", "/body", "invalid_type", ""},
		"out of range":      {`{"charges":1e400}`, 422, "validation_failed", "/body/charges", "invalid_type", ""},
		"syntax":            {`{"owner":"Bruce Wayne",}`, 400, "malformed_json", "", "", "byte offset"},
		"truncated":         {`{"owner":"Bru`, 400, "malformed_json", "", "", "ends inside"},
		"unknown":           {`{"owner":"Bruce Wayne","cape":"black"}`, 400, "unknown_field", "/body/cape", "unknown_field", ""},
		"nested unknown":    {`{"gadgets":[{"name":"batarang"},{"name":"grapple","range":30}]}`, 400, "unknown_field", "/body/gadgets/1/range", "unknown_field", ""},
		"case-folded known": {`{"OWNER":"Bruce Wayne"}`, 200, "", "", "", ""},
		"pointer escape":    {`{"a/b~c":1}`, 400, "unknown_field", "/body/a~1b~0c", "unknown_field", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := serve(h, http.MethodPost, "/api/batcave/v1/belts", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d %s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
			raw := rec.Body.String()
			for _, leak := range []string{"Go struct", "Go value", "struct field", "utilityBelt", "json: ", "unmarshal", "of type", "Gadget"} {
				if strings.Contains(raw, leak) {
					t.Fatalf("body leaks %q: %s", leak, raw)
				}
			}
			if tc.status == 200 {
				return
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Code != tc.code || !strings.Contains(p.Detail, tc.detailContain) {
				t.Fatalf("problem = %+v, want code %s with detail containing %q", p, tc.code, tc.detailContain)
			}
			if tc.field != "" && (len(p.Errors) != 1 || p.Errors[0].Field != tc.field || p.Errors[0].Code != tc.fieldCode) {
				t.Fatalf("errors = %+v, want one %s at %s", p.Errors, tc.fieldCode, tc.field)
			}
		})
	}
}

// TestRouter_RegistrationAfterBuildRefused is the review's m4: the handler
// Build returned can never serve a later registration, so each one is
// refused, logged at ERROR and absent from Routes.
func TestRouter_RegistrationAfterBuildRefused(t *testing.T) {
	var logged bytes.Buffer
	r := NewRouter(Options{}, Deps{Log: slog.New(slog.NewTextHandler(&logged, nil))})
	g := r.ModuleGroup("batcave")
	r.AllowPublic("batcave", "getGadget", "lateGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	mustBuild(t, r)

	Handle(g, Route{Method: http.MethodGet, Path: "/late", Name: "lateGadget", Perm: rbac.Public}, noop)
	if err := r.Claim("batcave", "/batcave/"); !errors.Is(err, errBuilt) {
		t.Fatalf("Claim after Build = %v, want errBuilt", err)
	}
	if err := r.MountLegacy(LegacyMount{Owner: "batcave", Handler: http.NotFoundHandler(), Auth: LegacyAuthCore}); !errors.Is(err, errBuilt) {
		t.Fatalf("MountLegacy after Build = %v, want errBuilt", err)
	}
	if err := r.SPA(http.NotFoundHandler(), SPAOptions{}); !errors.Is(err, errBuilt) {
		t.Fatalf("SPA after Build = %v, want errBuilt", err)
	}
	for ri := range r.Routes() {
		if ri.Name == "lateGadget" {
			t.Fatal("a route registered after Build is listed")
		}
	}
	if n := strings.Count(logged.String(), "registration after Router.Build ignored"); n != 4 {
		t.Fatalf("logged %d late registrations, want 4:\n%s", n, logged.String())
	}
}

// TestRouter_ConcurrentLateRegistration is the verification review's
// MINOR-1: a module goroutine started in Register may call Handle while the
// kernel runs Build. Under -race this was a data race on the built flag and
// the route list; every call now either lands before Build or is refused.
func TestRouter_ConcurrentLateRegistration(t *testing.T) {
	r := NewRouter(Options{}, Deps{Log: slog.New(slog.DiscardHandler)})
	g := r.ModuleGroup("batcave")
	names := make([]string, 50)
	for i := range names {
		names[i] = fmt.Sprintf("gadget%d", i)
	}
	r.AllowPublic("batcave", names...)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Go(func() {
			<-start
			Handle(g, Route{Method: http.MethodGet, Path: fmt.Sprintf("/gadgets/n%d", i), Name: n, Perm: rbac.Public}, noop)
			r.AllowPublic("batcave", n)
			if err := r.Claim("batcave", fmt.Sprintf("/batcave/n%d/", i)); err != nil && !errors.Is(err, errBuilt) {
				t.Errorf("Claim: %v", err)
			}
		})
	}
	close(start)
	h, err := r.Build()
	wg.Wait()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Every route Build saw is served; every other one was refused.
	served := 0
	for ri := range r.Routes() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ri.Pattern, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("listed route %s = %d, want 200", ri.Pattern, rec.Code)
		}
		served++
	}
	if served > len(names) {
		t.Fatalf("%d routes listed, want at most %d", served, len(names))
	}
}

// TestClaim_RefusesUncleanPrefix is the review's m5: the overlap test
// compares claims as strings, so a claim must be a clean literal path.
func TestClaim_RefusesUncleanPrefix(t *testing.T) {
	for _, p := range []string{"/x/../api/hello/", "/{w}/", "//api/hello/", "/api/./hello/", "/api/hello?x/", "relative/", "/"} {
		r := NewRouter(Options{}, Deps{})
		if err := r.Claim("joker", p); !errors.Is(err, ErrPrefixCollision) {
			t.Errorf("Claim(%q) = %v, want ErrPrefixCollision", p, err)
		}
	}
	r := NewRouter(Options{}, Deps{})
	if err := r.Claim("joker", "/api/joker/"); err != nil {
		t.Fatalf("a clean claim was refused: %v", err)
	}
}

// TestMountLegacy_RequiresAuth is the review's m2 at the router: a zero or
// unknown LegacyAuth is refused (the registrar reports it as
// ProblemLegacyAuth before the router sees it).
func TestMountLegacy_RequiresAuth(t *testing.T) {
	for _, a := range []LegacyAuth{0, LegacyAuthGin + 1} {
		r := NewRouter(Options{}, Deps{})
		if err := r.MountLegacy(LegacyMount{Owner: "batcave", Handler: http.NotFoundHandler(), Auth: a}); !errors.Is(err, ErrLegacyManifest) {
			t.Errorf("MountLegacy(Auth %d) = %v, want ErrLegacyManifest", a, err)
		}
	}
}
