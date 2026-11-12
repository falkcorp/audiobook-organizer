// file: core/internal/modulehttp/openapi_test.go
// version: 1.0.1
// guid: 6fd872bb-8fed-44b9-a7e6-3f551b71aacf
// last-edited: 2026-11-11

package modulehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
)

// batcave is a Batman-named settings section with one config.Duration field,
// the shape a core settings section has.
type batcave struct {
	PatrolEvery config.Duration `json:"patrol_every"`
	Callsign    string          `json:"callsign"`
}

type batcaveGet struct{}

// patrolDoc is a router whose one native route answers a batcave, and the
// document httpapi builds for it. A route registered here is not Build-ed: the
// document is built from the registrations, as the kernel's is.
func patrolDoc(t testing.TB) []byte {
	t.Helper()
	r := newRouter()
	httpapi.Handle(r.CoreGroup(), httpapi.Route{
		Method: http.MethodGet, Path: "/batcave", Name: "getBatcave", Perm: anyAuthenticated(),
	}, func(context.Context, *httpapi.In[batcaveGet]) (batcave, error) { return batcave{}, nil })
	doc, err := r.OpenAPI("core")
	if err != nil {
		t.Fatal(err)
	}
	b, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// served builds a router with only the document route, over docs and the given
// access flag, and returns its handler; the one Public route builds without an
// auth service, so the whole chain runs.
func served(t testing.TB, docs Documents, public func() bool) http.Handler {
	t.Helper()
	r := newRouter()
	registerOpenAPI(r.CoreGroup(), &api{docs: docs, public: public, log: discard()})
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

const docPath = "/api/core/v1/openapi/core.json"

func yes() bool { return true }
func no() bool  { return false }

// TestOpenAPI_UsesConfigOverrides (05-http-api §12, SIMP3-1): a config.Duration
// field renders as string/duration in the served document, the same schema as
// in the settings schema (config.SchemaFor). The served bytes are the ones
// Router.OpenAPI(...).JSON() produced, so this also proves the route adds
// nothing.
func TestOpenAPI_UsesConfigOverrides(t *testing.T) {
	built := patrolDoc(t)
	h := served(t, DocumentMap{"core": built}, yes)
	rec := get(h, docPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", docPath, rec.Code, rec.Body)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	served, ok := doc.Components.Schemas["core.batcave"].Properties["patrol_every"]
	if !ok {
		t.Fatalf("the served document has no core.batcave.patrol_every: %s", rec.Body)
	}

	sch, err := config.SchemaFor[batcave]()
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(sch.JSON(), &settings); err != nil {
		t.Fatal(err)
	}
	want, ok := settings.Properties["patrol_every"]
	if !ok {
		t.Fatalf("the settings schema has no patrol_every: %s", sch.JSON())
	}
	var gotDur, wantDur map[string]any
	if err := json.Unmarshal(served, &gotDur); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantDur); err != nil {
		t.Fatal(err)
	}
	if gotDur["type"] != "string" || gotDur["format"] != "duration" {
		t.Errorf("the served document renders a Duration as %s, want string/duration", served)
	}
	if !reflect.DeepEqual(gotDur, wantDur) {
		t.Errorf("the served schema %s differs from the settings schema %s", served, want)
	}
}

// TestOpenAPI_ServedBytesAreTheBuiltDocument: the response body is, byte for
// byte, what Document.JSON() returned, with a JSON content type, its length,
// and a no-store cache directive; nothing is re-encoded or indented.
func TestOpenAPI_ServedBytesAreTheBuiltDocument(t *testing.T) {
	built := patrolDoc(t)
	other := []byte(`{"openapi":"3.1.0","info":{"title":"robin","version":"1"}}`)
	h := served(t, DocumentMap{"core": built, "robin": other}, yes)

	for owner, want := range map[string][]byte{"core": built, "robin": other} {
		rec := get(h, "/api/core/v1/openapi/"+owner+".json")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("%s: %d, body differs from the built document (%d bytes, want %d)", owner, rec.Code, rec.Body.Len(), len(want))
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type = %q", owner, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", owner, cc)
		}
		if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(want)) {
			t.Errorf("%s: Content-Length = %q, want %d", owner, cl, len(want))
		}
	}
}

// TestOpenAPI_AuthenticatedCallerGetsTheDocument: a signed-in caller gets the
// owner's document whether or not core.http.openapi.public is set, whatever
// they hold.
func TestOpenAPI_AuthenticatedCallerGetsTheDocument(t *testing.T) {
	built := patrolDoc(t)
	for _, public := range []bool{false, true} {
		h := served(t, DocumentMap{"core": built}, func() bool { return public })
		for name, p := range map[string]*auth.Principal{"user": user(), "admin": admin()} {
			rec := get(asPrincipal(h, p), docPath)
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), built) {
				t.Errorf("public=%v %s: %d, want the document", public, name, rec.Code)
			}
		}
	}
}

// TestOpenAPI_AnonymousIs401UnlessPublic: an anonymous caller gets 401
// authentication_required, and no byte of the document, while the flag is
// false; true serves them. The flag is read at each request (it is
// hot-reloadable), so one handler answers both ways as it changes.
func TestOpenAPI_AnonymousIs401UnlessPublic(t *testing.T) {
	built := patrolDoc(t)
	flag := false
	h := served(t, DocumentMap{"core": built}, func() bool { return flag })

	rec := get(h, docPath)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"authentication_required"`) {
		t.Fatalf("anonymous, not public: %d %s, want 401 authentication_required", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"paths"`) || strings.Contains(rec.Body.String(), "patrol_every") {
		t.Errorf("the 401 body carries the document: %s", rec.Body)
	}
	// A request carrying a nil principal binding (a stale cookie arrives
	// anonymous, 05-authn §4.3) is the same anonymous caller.
	if rec := get(asPrincipal(h, nil), docPath); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous with an empty principal binding: %d, want 401", rec.Code)
	}

	flag = true
	rec = get(h, docPath)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), built) {
		t.Errorf("anonymous, public: %d, want the document", rec.Code)
	}
	flag = false
	if rec := get(h, docPath); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous after the flag was switched off: %d, want 401", rec.Code)
	}
}

// TestOpenAPI_NilFlagIsNotPublic: Deps with no OpenAPIPublic func means false,
// never public.
func TestOpenAPI_NilFlagIsNotPublic(t *testing.T) {
	a, err := newAPI(Deps{Modules: &fakeView{}, Documents: DocumentMap{"core": []byte(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if a.public() {
		t.Fatal("a nil OpenAPIPublic reads as public")
	}
	r := newRouter()
	registerOpenAPI(r.CoreGroup(), a)
	h, err := r.Build()
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(h, docPath); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous with no flag = %d, want 401", rec.Code)
	}
}

// TestOpenAPI_ConfinedCallerIsRefusedUnlessPublic: stage 13 would apply
// AnyAuthenticated to a non-Public route, so the handler does: a principal that
// does not satisfy it (a confined key) or has no permission set gets 403 while
// the route is not public, and the document once it is.
func TestOpenAPI_ConfinedCallerIsRefusedUnlessPublic(t *testing.T) {
	built := patrolDoc(t)
	for name, p := range map[string]*auth.Principal{
		"confined key":  confinedKey(),
		"no permission": {Method: auth.MethodSession},
	} {
		rec := get(asPrincipal(served(t, DocumentMap{"core": built}, no), p), docPath)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"permission_denied"`) {
			t.Errorf("%s, not public: %d %s, want 403 permission_denied", name, rec.Code, rec.Body)
		}
		if rec := get(asPrincipal(served(t, DocumentMap{"core": built}, yes), p), docPath); rec.Code != http.StatusOK {
			t.Errorf("%s, public: %d, want 200", name, rec.Code)
		}
	}
}

// TestOpenAPI_UnknownOwnerIs404: an owner with no document, a name without the
// .json suffix and an empty name are the one 404, which does not echo the
// request. It answers only a caller allowed to read documents: an anonymous
// caller is told 401 for every name, so they cannot list the owners.
func TestOpenAPI_UnknownOwnerIs404(t *testing.T) {
	h := served(t, DocumentMap{"core": []byte(`{}`)}, no)
	authed := asPrincipal(h, user())
	for _, name := range []string{"scarecrow.json", "core", "core.json.json", ".json", "core.JSON", "%2e%2e.json"} {
		rec := get(authed, "/api/core/v1/openapi/"+name)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("%q: %d %s, want 404 not_found", name, rec.Code, rec.Body)
		}
		var prob struct{ Detail string }
		if err := json.Unmarshal(rec.Body.Bytes(), &prob); err != nil || prob.Detail != "no such document" {
			t.Errorf("%q: the 404 detail = %q (%v), want the fixed text", name, prob.Detail, err)
		}
		if anon := get(h, "/api/core/v1/openapi/"+name); anon.Code != http.StatusUnauthorized {
			t.Errorf("%q anonymous: %d, want 401 (the same for every name)", name, anon.Code)
		}
	}
}

// TestOpenAPI_BuiltOverARealRouterDocument: the Documents the kernel passes can
// be filled from a router's own OpenAPI output; this is the adapter, and that a
// document built after registration is the one served for core.
func TestOpenAPI_BuiltOverARealRouterDocument(t *testing.T) {
	r := newRouter()
	if err := Register(r.CoreGroup(), Deps{Modules: &fakeView{}, Documents: DocumentMap{}}); err != nil {
		t.Fatal(err)
	}
	doc, err := r.OpenAPI("core")
	if err != nil {
		t.Fatal(err)
	}
	b, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"/api/core/v1/modules"`, `"/api/core/v1/openapi/{file}"`, `"core_listModules"`, `"core_getOpenAPIDocument"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the core document does not describe %s", want)
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	// The document route is the one Public operation, and says so.
	var public int
	for _, item := range parsed["paths"].(map[string]any) {
		for _, op := range item.(map[string]any) {
			if op.(map[string]any)["x-mediacore-permission"] == "public" {
				public++
			}
		}
	}
	if public != 1 {
		t.Errorf("%d operations are marked public, want 1 (the document route)", public)
	}
}

func TestDocumentMap(t *testing.T) {
	m := DocumentMap{"core": []byte("x")}
	if b, ok := m.OpenAPI("core"); !ok || string(b) != "x" {
		t.Errorf("OpenAPI(core) = %q, %v", b, ok)
	}
	if _, ok := m.OpenAPI("joker"); ok {
		t.Error("OpenAPI(joker) found a document")
	}
}

// brokenBody is a ResponseWriter whose body write fails.
type brokenBody struct{ h http.Header }

func (b *brokenBody) Header() http.Header       { return b.h }
func (b *brokenBody) WriteHeader(int)           {}
func (b *brokenBody) Write([]byte) (int, error) { return 0, errors.New("client went away") }

// TestOpenAPI_ALostClientIsLoggedNotSilent: a document that cannot be
// delivered is logged at debug, without the document in the log.
func TestOpenAPI_ALostClientIsLoggedNotSilent(t *testing.T) {
	var logs strings.Builder
	a := &api{docs: DocumentMap{"core": []byte(`{"openapi":"3.1.0"}`)}, public: yes,
		log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	r := httptest.NewRequest(http.MethodGet, docPath, nil)
	r.SetPathValue(openAPIFile, "core.json")
	a.serveOpenAPI(&brokenBody{h: http.Header{}}, r)
	if !strings.Contains(logs.String(), "not delivered") || strings.Contains(logs.String(), "3.1.0") {
		t.Fatalf("log = %q", logs.String())
	}
}
