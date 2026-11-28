// file: core/kernel/integrations_routes.go
// version: 0.1.0
// guid: 43c9385c-3162-45ae-843e-51328f17ca6d
// last-edited: 2026-11-22

package kernel

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/integrationhttp"
	"github.com/falkcorp/mediacore/internal/libraryhttp"
	"github.com/falkcorp/mediacore/library"
)

// The integrations and library routes the F7 flip mounts on the router's core
// group (/api/core/v1). Both are dark: only tests call them. The permission of
// every route is the one integrationhttp and libraryhttp declare, and no route
// sets Guards (the access table of the F7-09 B3 brief, section 3); this file
// adds no permission of its own and no gate of its own.

// mountIntegrationRoutes registers the ten integration routes on g, which must
// be the router's CoreGroup(). The route package nil-checks its holds and audit
// arguments to fail closed (503), and an interface holding a nil pointer is not
// nil, so a nil *providerhttp.Registry and a nil audit logger are passed as
// untyped nils (seams.d/F7-10.md "Typed nil at Register").
//
// TODO(W-2): a Put that writes a secret needs an interactive core:admin
// (coordinator STOP W-2). The check belongs in core/integrations/write.go, the
// one place every secret write passes; this mount deliberately adds no
// kernel-side gate so the follow-up is that one edit.
func mountIntegrationRoutes(g *httpapi.Group, s *integrationsServices) {
	holds, rec := s.routeCollaborators()
	integrationhttp.Register(g, s.registry, holds, rec)
}

// routeCollaborators are the hold releaser and audit logger the integration
// routes take, each an untyped nil when its service is absent (a nil pointer
// inside the interface would pass the route package's nil check).
func (s *integrationsServices) routeCollaborators() (integrationhttp.HoldReleaser, audit.Logger) {
	var holds integrationhttp.HoldReleaser
	if !isNil(s.providerhttp) {
		holds = s.providerhttp
	}
	var rec audit.Logger
	if !isNil(s.in.Audit) {
		rec = s.in.Audit
	}
	return holds, rec
}

// mountLibraryRoutes registers GET /api/core/v1/library/browse through
// httpapi.HandleRaw: libraryhttp's handler is raw (its own ServeMux handler),
// and its route table carries the permission, core:library.browse. The handler
// guards the permission itself as well (401 unauthenticated without a
// principal), so a mounting mistake still answers closed. g must be the
// router's CoreGroup(): the route's path is /library/browse under it, and the
// mount refuses a group whose base does not put it where libraryhttp.BasePath
// says.
func mountLibraryRoutes(g *httpapi.Group, m library.Manager, log *slog.Logger) error {
	if isNil(m) {
		return errors.New("kernel: library routes: no library manager")
	}
	return mountLibraryBrowse(g, m, log)
}

// mountLibraryBrowse is mountLibraryRoutes over the part of the Manager the
// routes read, so Describe can mount it over a stub.
func mountLibraryBrowse(g *httpapi.Group, lib libraryhttp.Library, log *slog.Logger) error {
	srv, err := libraryhttp.New(libraryhttp.Deps{Library: lib, Log: log})
	if err != nil {
		return fmt.Errorf("kernel: library routes: %w", err)
	}
	for _, rt := range srv.Routes() {
		path := strings.TrimPrefix(libraryhttp.BasePath, g.Base()) + rt.Path
		if g.Base()+path != libraryhttp.BasePath+rt.Path {
			return fmt.Errorf("kernel: library routes: group %q does not serve %s", g.Base(), libraryhttp.BasePath)
		}
		httpapi.HandleRaw(g, httpapi.Route{
			Method: rt.Method, Path: path, Name: rt.Name, Perm: rt.Perm, Summary: rt.Summary,
			Doc: "Without root, the roots the caller may read; with root and rel, the directories directly under that path. Directories only; symlinks are never followed.",
		}, httpapi.RawDoc{Query: []string{"root", "rel"}}, rt.Handler)
	}
	return nil
}

// emptyLibrary is a libraryhttp.Library with no roots, for Describe.
type emptyLibrary struct{}

func (emptyLibrary) Root(library.RootID) (*library.Root, error) { return nil, library.ErrUnknownRoot }
func (emptyLibrary) Roots(library.RootFilter) []*library.Root   { return nil }

// describeIntegrationRoutes is Describe's counterpart of the two mounts: the
// same routes over inputs that cannot serve, so the OpenAPI document carries
// them. Nothing it mounts is ever called.
func describeIntegrationRoutes(r *httpapi.Router) error {
	mountIntegrationRoutes(r.CoreGroup(), &integrationsServices{})
	return mountLibraryBrowse(r.CoreGroup(), emptyLibrary{}, nil)
}
