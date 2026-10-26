// file: internal/server/middleware/actor_test.go
// version: 1.0.0
// guid: 2f6b9d14-7e83-4a05-b1c9-d40e5a8736f2
// last-edited: 2026-10-26

package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// ActorOptions records who started an op, how they signed in and what the
// request could do, for every start route that takes caller params. It never
// marks the op as an owner apply: that mark (opsregistry.WithOwnerApply) is the
// Repairs owner-apply handler's alone, and a Cloudflare Access session on a
// generic start or retry route records the same actor and cf_access method the
// handler does, so the mark is all that tells them apart.
func TestActorOptions_RecordsTheRequestAndNeverMarksAnOwnerApply(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/v1/operations/v2", nil)
	ctx := auth.WithUser(c.Request.Context(), &database.User{ID: "01JBRUCEWAYNE00000000000A"})
	ctx = auth.WithMethod(ctx, auth.MethodCFAccess)
	ctx = auth.WithPermissions(ctx, []auth.Permission{auth.PermSettingsManage, auth.PermLibraryView})
	c.Request = c.Request.WithContext(ctx)

	var eo opsregistry.EnqueueOptions
	for _, o := range ActorOptions(c) {
		o(&eo)
	}
	assert.Equal(t, "01JBRUCEWAYNE00000000000A", eo.ActorUserID)
	assert.Equal(t, string(auth.MethodCFAccess), eo.ActorAuthMethod)
	assert.Equal(t, []string{string(auth.PermLibraryView), string(auth.PermSettingsManage)}, eo.ActorPermissions)
	assert.False(t, eo.OwnerApply, "only the owner-apply handler marks an op as an owner apply")
}
