// file: core/internal/modulehttp/openapi.go
// version: 1.0.2
// guid: 2608b4b5-3c38-43b4-9f93-421aecbf9687
// last-edited: 2026-11-11

package modulehttp

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Documents is the kernel's read-only view of the OpenAPI documents it built,
// one per owner ("core" and each module ID), as the exact JSON the
// document's JSON method produced (httpapi.Router.OpenAPI(owner), 05-http-api
// §7). The route writes those bytes unchanged: nothing here re-encodes them.
type Documents interface {
	// OpenAPI returns the document of owner, or false when there is none.
	OpenAPI(owner string) ([]byte, bool)
}

// DocumentMap is the simplest Documents: the built documents by owner. The
// kernel builds it once, after Router.Build, from each owner's
// Router.OpenAPI(owner).JSON(); routes are immutable after Build, so the
// documents are too.
type DocumentMap map[string][]byte

// OpenAPI implements Documents.
func (m DocumentMap) OpenAPI(owner string) ([]byte, bool) {
	b, ok := m[owner]
	return b, ok
}

// openAPIFile is the path wildcard of the document route. 05-http-api §7 spells
// the path /openapi/{owner}.json, which ServeMux cannot register (a wildcard
// must be a whole segment: "bad wildcard segment"), so the route takes the
// whole last segment and the handler requires the ".json" suffix.
const openAPIFile = "file"

// openAPIRoute is GET /openapi/{file}.
//
// Its Perm is rbac.Public, and the handler decides. 05-http-api §7 gives the
// route rbac.AnyAuthenticated, and "core.http.openapi.public=true makes it
// Public"; but Route.Perm is fixed when the route is registered and the key is
// hot-reloadable (§10), so no static requirement can mean "anyone while the key
// is true, an authenticated caller otherwise". A Public route still passes stage
// 12, so a credential that was sent is bound (and an invalid Bearer or API key
// is still a 401, a stale cookie arrives anonymous, 05-authn §4.3); the handler
// then applies the rule itself. The consequence for the document: this one
// operation carries x-mediacore-permission "public", and the generated client
// does not redirect a 401 from it to sign-in.
func openAPIRoute() httpapi.Route {
	return httpapi.Route{
		Method: http.MethodGet, Path: "/openapi/{" + openAPIFile + "}", Name: "getOpenAPIDocument", Perm: rbac.Public,
		Tags: []string{"modules"}, MaxBody: maxBody,
		Summary: "The OpenAPI 3.1 document of one owner, as <owner>.json",
		Doc:     "Takes <owner>.json, the owner being core or a module ID. A caller who is not signed in gets 401 unless core.http.openapi.public is true; an unknown owner is 404. The body is the document the build produced, byte for byte.",
	}
}

func registerOpenAPI(g *httpapi.Group, a *api) {
	httpapi.HandleRaw(g, openAPIRoute(), httpapi.RawDoc{}, http.HandlerFunc(a.serveOpenAPI))
}

// serveOpenAPI applies, in this order: the access rule, then the lookup. The
// order is deliberate: an owner that does not exist answers 404 only to a
// caller allowed to read documents, so a caller who is not cannot list the
// owners by probing.
func (a *api) serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	if err := a.openAPIAccess(r); err != nil {
		httpapi.WriteError(w, r, err, a.log)
		return
	}
	owner, ok := strings.CutSuffix(r.PathValue(openAPIFile), ".json")
	if !ok || owner == "" {
		httpapi.WriteError(w, r, errNoDocument(), a.log)
		return
	}
	body, ok := a.docs.OpenAPI(owner)
	if !ok {
		httpapi.WriteError(w, r, errNoDocument(), a.log)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	// The document is the same for everyone, but whether a caller may have it
	// depends on a flag and a credential, so no shared cache keeps it.
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		// The status line is out; the client went away mid-body.
		a.log.DebugContext(r.Context(), "modulehttp: the OpenAPI document was not delivered", slog.Any("error", err))
	}
}

// openAPIAccess is the rule of 05-http-api §7: core.http.openapi.public makes
// the document available to anyone; otherwise the route is rbac.AnyAuthenticated,
// which stage 13 would apply for a non-Public route and which a Public route
// must therefore apply itself. A principal with no compiled permission set is
// refused (core always builds one; a nil set is a defect, so it fails closed).
func (a *api) openAPIAccess(r *http.Request) error {
	if a.public() {
		return nil
	}
	p, _ := auth.PrincipalFrom(r.Context())
	return signedIn(p)
}

// errNoDocument is the one 404 of the route: an owner with no document, a name
// without the .json suffix and an empty name look the same, and the path value
// is not echoed.
func errNoDocument() error {
	return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such document"}
}
