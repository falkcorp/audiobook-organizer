// file: internal/server/middleware/actor_test.go
// version: 1.1.0
// guid: 2f6b9d14-7e83-4a05-b1c9-d40e5a8736f2
// last-edited: 2026-10-26

package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// An authenticated request that holds nothing (no permission set attached, as
// for an API key whose scopes intersect its account's roles to nothing) is
// recorded as an empty set, so the job it starts holds nothing; an unauthenticated
// request records no actor and no set at all.
func TestActorOptions_RecordsAnEmptyPermissionSetAsEmptyNotAbsent(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/v1/operations/v2", nil)
	ctx := auth.WithUser(c.Request.Context(), &database.User{ID: "01JALFREDPENNYWORTH00000B"})
	ctx = auth.WithPermissions(ctx, nil)
	c.Request = c.Request.WithContext(ctx)

	var eo opsregistry.EnqueueOptions
	for _, o := range ActorOptions(c) {
		o(&eo)
	}
	assert.Equal(t, "01JALFREDPENNYWORTH00000B", eo.ActorUserID)
	require.NotNil(t, eo.ActorPermissions, "an empty set is recorded, not left as no record")
	assert.Empty(t, eo.ActorPermissions)

	anon, _ := gin.CreateTestContext(httptest.NewRecorder())
	anon.Request = httptest.NewRequest("POST", "/api/v1/operations/v2", nil)
	var none opsregistry.EnqueueOptions
	for _, o := range ActorOptions(anon) {
		o(&none)
	}
	assert.Empty(t, none.ActorUserID)
	assert.Nil(t, none.ActorPermissions, "an unauthenticated request records no set")
}
