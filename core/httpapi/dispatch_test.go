// file: core/httpapi/dispatch_test.go
// version: 1.0.0
// guid: 10f67bda-3d92-4798-b55c-741cfba03165
// last-edited: 2026-11-07

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	err := r.MountLegacy(LegacyMount{Owner: "batcomputer", Handler: http.NotFoundHandler(), ServesSPA: true})
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
	if err := r.MountLegacy(LegacyMount{Owner: "batcomputer", Handler: http.NotFoundHandler(),
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
