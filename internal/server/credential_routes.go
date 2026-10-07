// file: internal/server/credential_routes.go
// version: 1.0.0
// guid: 0b7e3f5a-9c21-4d84-a6f3-5e8d2c1b7a90
// last-edited: 2026-10-07

package server

import (
	"bytes"
	"errors"
	"io"
	"path"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Credential-changing routes are registered ONLY through credRoute and
// credRouteWhen, which attach the one shared guard
// (servermiddleware.RequireCredentialChangeMethod / ...When) and record the
// route. credential_routes_test.go walks the router's full route list and
// fails when a route that is not recorded here is neither on its reviewed
// exempt list, so a new sibling path (a second way to reset a password, mint
// a session, install a program) cannot ship unguarded by omission — the
// 2026-10-07 security review's "sibling-path gate parity" finding.
//
// What counts as credential-changing, and why each route is or is not one:
// docs/plans/2026-10-07-apikey-expiry-and-privilege.md.

// credRouteKind says how a recorded route is guarded.
type credRouteKind string

const (
	credRouteAlways      credRouteKind = "always"
	credRouteConditional credRouteKind = "conditional"
)

var credRoutesMu sync.Mutex

// recordCredRoute notes "METHOD /full/path" as guarded.
func (s *Server) recordCredRoute(g *gin.RouterGroup, method, rel string, kind credRouteKind) {
	credRoutesMu.Lock()
	defer credRoutesMu.Unlock()
	if s.credRoutes == nil {
		s.credRoutes = make(map[string]credRouteKind)
	}
	full := path.Join(g.BasePath(), rel)
	if rel != "" && rel[len(rel)-1] == '/' && full[len(full)-1] != '/' {
		full += "/"
	}
	s.credRoutes[method+" "+full] = kind
}

// credRoute registers a route that changes credentials, identity, the
// programs the server runs or the paths it opens: an API key (any key, any
// scope), an ABS token or an unknown method is refused before any other
// handler runs. A no-op guard when auth is disabled, exactly like s.perm:
// every request is then anonymous with full access and there is no API key
// to tell apart. The route is recorded either way.
func (s *Server) credRoute(g *gin.RouterGroup, method, rel string, h ...gin.HandlerFunc) {
	s.recordCredRoute(g, method, rel, credRouteAlways)
	g.Handle(method, rel, append([]gin.HandlerFunc{s.credGuardMiddleware(nil)}, h...)...)
}

// credRouteWhen is credRoute for a route that is a credential change only
// when guarded reports true (a key for ANOTHER user). guarded must fail
// closed.
func (s *Server) credRouteWhen(g *gin.RouterGroup, method, rel string, guarded func(*gin.Context) bool, h ...gin.HandlerFunc) {
	s.recordCredRoute(g, method, rel, credRouteConditional)
	g.Handle(method, rel, append([]gin.HandlerFunc{s.credGuardMiddleware(guarded)}, h...)...)
}

func (s *Server) credGuardMiddleware(guarded func(*gin.Context) bool) gin.HandlerFunc {
	if !config.AppConfig.EnableAuth {
		return func(c *gin.Context) { c.Next() }
	}
	if guarded == nil {
		return servermiddleware.RequireCredentialChangeMethod()
	}
	return servermiddleware.RequireCredentialChangeMethodWhen(guarded)
}

// maxGuardBodyBytes bounds what the guard reads to decide; a larger body is
// treated as guarded (fail closed) rather than read whole.
const maxGuardBodyBytes = 1 << 20

// apiKeyCreateForAnotherUser reports whether a POST /auth/api-keys asks for a
// key owned by someone other than the caller. It decodes the body with the
// SAME struct and the SAME binding the handler uses, so the guard and the
// handler cannot read different user_id values out of one body (a
// case-sensitive map lookup here would have missed "User_ID", which
// encoding/json binds onto UserID). The body is put back for the handler.
func apiKeyCreateForAnotherUser(c *gin.Context) bool {
	caller, ok := servermiddleware.CurrentUser(c)
	if !ok || caller == nil || c.Request.Body == nil {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxGuardBodyBytes+1))
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil || len(raw) > maxGuardBodyBytes {
		return true
	}
	var req handlers.CreateAPIKeyRequest
	if err := binding.JSON.BindBody(raw, &req); err != nil {
		// A body that decoded but failed a binding rule (no name) is
		// still readable; the handler answers 400 for it. Anything that
		// did not decode is refused here.
		var ve validator.ValidationErrors
		if !errors.As(err, &ve) {
			return true
		}
	}
	return req.UserID != "" && req.UserID != caller.ID
}

// apiKeyOwnedByAnotherUser reports whether the key named by :id belongs to
// someone other than the caller. A lookup error or a missing key is treated
// as guarded: the handler would answer 404/500 anyway, and the guard must not
// open on a failure.
func (s *Server) apiKeyOwnedByAnotherUser(c *gin.Context) bool {
	caller, ok := servermiddleware.CurrentUser(c)
	if !ok || caller == nil {
		return true
	}
	key, err := s.storeForWiring().GetAPIKey(c.Param("id"))
	if err != nil || key == nil {
		return true
	}
	return key.UserID != caller.ID
}
