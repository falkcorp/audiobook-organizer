// file: internal/server/handlers/bridge_merge_actor_test.go
// version: 0.1.0
// guid: 25b33a80-64ec-4427-af76-6001289ef873
// last-edited: 2026-11-21

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// F4-08 B10b2: the merge actor under core identity is the admin role name AND
// core:admin, each alone refused (05-authn §11.1 item 4). A custom role that
// holds core:admin is not the admin role HEAD's gate names, and an admin whose
// principal lacks core:admin is not what core accepts; neither merges, on any
// of the six merge routes. The route set is the whole merge surface, so a route
// added without the gate fails here.
func TestMergeRoutes_ActorNeedsTheAdminRoleAndCoreAdmin(t *testing.T) {
	calls := func(r *mergeRig) []struct{ method, path string } {
		return []struct{ method, path string }{
			{http.MethodGet, "/users/" + r.absorbed.ID + "/footprint"},
			{http.MethodPost, r.mergePath("preview")},
			{http.MethodPost, r.mergePath("apply")},
			{http.MethodGet, "/users/merges"},
			{http.MethodPost, "/users/merges/j1/resume"},
			{http.MethodPost, "/users/merges/j1/undo"},
		}
	}
	for _, tc := range []struct {
		name    string
		role    string
		scopes  []rbac.Permission
		allowed bool
	}{
		{"the admin role holding core:admin", "admin", []rbac.Permission{rbac.CoreAdmin}, true},
		{"a custom role holding core:admin", "operator", []rbac.Permission{rbac.CoreAdmin}, false},
		{"the admin role without core:admin", "admin", nil, false},
		{"a custom role with neither", "operator", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRig(t)
			actor := *r.admin
			actor.Roles = []string{tc.role}
			r.actor = &actor
			e := gin.New()
			e.Use(func(c *gin.Context) {
				c.Set("auth_user", r.actor)
				c.Request = c.Request.WithContext(auth.WithMethod(c.Request.Context(), auth.MethodSession))
				bindCore(c, bridgeIdentity(t), coreauth.MethodSession, tc.scopes...)
				c.Next()
			})
			e.GET("/users/:id/footprint", r.h.UserFootprint)
			e.POST("/users/:id/merge-into/:target/preview", r.h.PreviewUserMerge)
			e.POST("/users/:id/merge-into/:target/apply", r.h.ApplyUserMerge)
			e.GET("/users/merges", r.h.ListUserMerges)
			e.POST("/users/merges/:jid/resume", r.h.ResumeUserMerge)
			e.POST("/users/merges/:jid/undo", r.h.UndoUserMerge)
			r.router = e
			for _, c := range calls(r) {
				w, _ := r.do(t, c.method, c.path, map[string]any{"digest": "x"})
				if refused := w.Code == http.StatusForbidden; refused == tc.allowed {
					t.Errorf("%s %s = %d, allowed=%v", c.method, c.path, w.Code, tc.allowed)
				}
			}
			if !tc.allowed && len(r.enq.calls) != 0 {
				t.Errorf("a refused actor enqueued %d operation(s)", len(r.enq.calls))
			}
		})
	}
}
