// file: core/internal/libraryhttp/routes.go
// version: 1.0.0
// guid: 40eef632-2b59-493d-b892-c4c759f7412a
// last-edited: 2026-11-11

// Package libraryhttp is the native HTTP surface of core's library: one route,
// GET /api/core/v1/library/browse, a directory picker over the registered
// roots (05-library). It mirrors taskhttp: the routes are returned as data
// (Route) and the package is dark until the kernel's start rule mounts it:
// nothing imports it yet.
//
// Every handler checks its route's permission itself (core:library.browse
// against the caller's Principal.Perms), so a Route mounted bare enforces what
// the table says.
//
// The route lists directories only, never files, and every filesystem access
// goes through the library's confined RootFS, never a host path. Dot-prefixed
// directories and each root's AppDir and Quarantine area are neither listed
// nor browsable. Symlinks are never followed: a symlink to a directory is not
// a directory here, so it is not listed and cannot be browsed through.
//
// Out of scope: the spec's "a path an admin is registering" case, browsing a
// host path that is not yet under any registered root, which needs a different
// permission and a host-path allow-list. This route only browses roots that
// are already registered.
package libraryhttp

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// BasePath is where the routes mount.
const BasePath = "/api/core/v1/library"

// scopeBrowse is the route's permission (05-rbac's fixed core:* catalog).
const scopeBrowse rbac.Permission = "core:library.browse"

// Route is one native route. Path is relative to BasePath ("/browse"). Perm is
// the route floor; Handler checks it too.
type Route struct {
	Method  string
	Path    string
	Name    string // operationId, "<verb><Noun>"
	Summary string
	Perm    rbac.Requirement
	Handler http.Handler
}

// Pattern is the route as a ServeMux pattern under BasePath.
func (r Route) Pattern() string { return r.Method + " " + BasePath + r.Path }

// Deps are what the routes call. Library is required; New refuses it missing
// rather than serving a route that fails open.
type Deps struct {
	// Library is the registry the picker reads. library.Manager satisfies it.
	Library Library
	// Log receives internal errors; nil discards.
	Log *slog.Logger
}

// errNoLibrary is what New returns for a missing Library.
var errNoLibrary = errors.New("libraryhttp: Deps.Library is required")

// Server holds the routes' dependencies.
type Server struct {
	lib    Library
	log    *slog.Logger
	routes []Route
}

// New validates d and builds the routes.
func New(d Deps) (*Server, error) {
	if d.Library == nil {
		return nil, errNoLibrary
	}
	s := &Server{lib: d.Library, log: d.Log}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	r := Route{
		Method: "GET", Path: "/browse", Name: "browseLibrary",
		Summary: "List the library roots, or the directories under one root path",
		Perm:    rbac.Need(scopeBrowse),
	}
	r.Handler = s.guard(r, s.browse)
	s.routes = []Route{r}
	return s, nil
}

// Routes returns the route table, in table order.
func (s *Server) Routes() []Route { return append([]Route(nil), s.routes...) }

// Handler serves every route under BasePath on its own ServeMux, for tests and
// for a bare mount. A request must already carry the caller's principal
// (auth.WithPrincipal); without one every route answers 401.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.routes {
		mux.Handle(r.Pattern(), r.Handler)
	}
	return mux
}

// guard is every route's front door: the caller's principal must exist and
// satisfy the route floor. h runs only after that.
func (s *Server) guard(rt Route, h func(http.ResponseWriter, *http.Request)) http.Handler {
	perm := rt.Perm
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok {
			s.write(w, r, problem{Status: http.StatusUnauthorized, Code: "unauthenticated", Detail: "sign in to use the library API"})
			return
		}
		if p.Perms == nil || !p.Perms.Satisfies(perm) {
			s.write(w, r, forbidden(perm))
			return
		}
		h(w, r)
	})
}
