// file: internal/server/handlers/operations_v2_permissions_test.go
// version: 1.3.0
// guid: 61966351-b637-469e-8483-4e3819bf56f6
// last-edited: 2026-10-10

// Covers TriggerOperationV2's per-def permission gate.
//
// Before this gate, OperationDef.Permissions was written to op_definitions_v2 and
// read by nothing: the only guard on POST /operations/v2 is a single blanket
// scan.trigger for every op. The seeded editor role holds scan.trigger but NOT
// settings.manage, so ops were reachable by a role the v1 maintenance route
// rejects. A def that declares no Permissions is treated as settings.manage.
// These tests pin that closed.
//
// Each test asserts the mock's call log, not just the status code: in the deny
// cases no EnqueueOp expectation is registered, so mockery fails the test if the
// handler enqueues anyway. That is what makes these tests fail when the
// enforcement block is deleted rather than merely passing alongside it.

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// withCallerPerms attaches a permission set to the request context the same way
// the auth middleware does, so auth.Can resolves against it.
func withCallerPerms(c *gin.Context, perms ...auth.Permission) {
	c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), perms))
}

// maintenanceDef mirrors what RegisterMaintenanceJobOps registers for every one
// of the 37 maintenance jobs: settings.manage.
func maintenanceDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:          "maintenance.cleanup-backups",
		Permissions: []auth.Permission{auth.PermSettingsManage},
	}
}

// The headline case: the seeded `editor` role. It holds scan.trigger, which is
// all the route-level guard requires, but not settings.manage. Before the gate
// this returned 202 and ran a maintenance job.
func TestTriggerOperationV2_EditorCannotRunAMaintenanceOp(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Def("maintenance.cleanup-backups").Return(maintenanceDef(), true)
	// Deliberately NO EnqueueOp expectation: reaching it is the failure.

	h := handlers.NewOperationsV2Handler(nil, registry, nil, true)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"maintenance.cleanup-backups"}`, nil)
	withCallerPerms(c, auth.PermScanTrigger)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), string(auth.PermSettingsManage))
	registry.AssertNotCalled(t, "EnqueueOp", mock.Anything, mock.Anything, mock.Anything)
}

// An admin holds settings.manage and is let through.
func TestTriggerOperationV2_AdminCanRunAMaintenanceOp(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Def("maintenance.cleanup-backups").Return(maintenanceDef(), true)
	registry.EXPECT().EnqueueOp(mock.Anything, "maintenance.cleanup-backups", mock.Anything).Return("op7", nil)

	h := handlers.NewOperationsV2Handler(nil, registry, nil, true)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"maintenance.cleanup-backups"}`, nil)
	withCallerPerms(c, auth.PermScanTrigger, auth.PermSettingsManage)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "op7")
}

// The outage guard. auth.Can returns false for a caller with no permission set,
// so enforcing unconditionally would 403 every trigger in a deployment running
// with EnableAuth=false. enforcePerms=false must skip the check entirely — and
// must not even consult the registry for the def.
func TestTriggerOperationV2_SkipsEnforcementWhenAuthDisabled(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().EnqueueOp(mock.Anything, "maintenance.cleanup-backups", mock.Anything).Return("op8", nil)
	// No Def expectation: with auth disabled the gate must not run at all.

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"maintenance.cleanup-backups"}`, nil)
	// No caller permissions at all, exactly as when auth is disabled.
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "op8")
}

// A def that declares no permissions is treated as requiring settings.manage:
// the route-level scan.trigger guard alone would let the seeded editor start it.
// The def literal mirrors a plugin op (acoustid.scan) that declares nothing.
func TestTriggerOperationV2_DefWithNoPermissionsRequiresSettingsManage(t *testing.T) {
	t.Run("editor is refused", func(t *testing.T) {
		registry := handlersmocks.NewMockOperationsRegistry(t)
		registry.EXPECT().Def("acoustid.scan").Return(opsregistry.OperationDef{ID: "acoustid.scan"}, true)
		// Deliberately NO EnqueueOp expectation: reaching it is the failure.

		h := handlers.NewOperationsV2Handler(nil, registry, nil, true)
		c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"acoustid.scan"}`, nil)
		withCallerPerms(c, auth.PermScanTrigger)
		h.TriggerOperationV2(c)

		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "permission denied: settings.manage")
		registry.AssertNotCalled(t, "EnqueueOp", mock.Anything, mock.Anything, mock.Anything)
	})
	t.Run("admin is let through", func(t *testing.T) {
		registry := handlersmocks.NewMockOperationsRegistry(t)
		registry.EXPECT().Def("acoustid.scan").Return(opsregistry.OperationDef{ID: "acoustid.scan"}, true)
		registry.EXPECT().EnqueueOp(mock.Anything, "acoustid.scan", mock.Anything).Return("op9", nil)

		h := handlers.NewOperationsV2Handler(nil, registry, nil, true)
		c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"acoustid.scan"}`, nil)
		withCallerPerms(c, auth.PermScanTrigger, auth.PermSettingsManage)
		h.TriggerOperationV2(c)

		assert.Equal(t, http.StatusAccepted, w.Code)
		assert.Contains(t, w.Body.String(), "op9")
	})
}

// With auth disabled the default must not apply: auth.Can is false for a caller
// with no permission set, so enforcing would 403 every trigger. The registry's
// Def must not even be consulted.
func TestTriggerOperationV2_NoPermissionsDefAllowedWhenAuthDisabled(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().EnqueueOp(mock.Anything, "acoustid.scan", mock.Anything).Return("op11", nil)
	// No Def expectation: with auth disabled the gate must not run at all.

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"acoustid.scan"}`, nil)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "op11")
}

// Retry shares defPermissionsHeld with trigger, so an editor retrying a failed
// run of a def that declares nothing is refused at the handler layer too (the
// route itself already requires settings.manage).
func TestRetryOperationV2_NoPermissionsDefRequiresSettingsManage(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().GetOperationV2("op-old").Return(&database.OperationV2Row{
		ID: "op-old", DefID: "acoustid.scan", Status: "failed", Params: "{}",
	}, nil)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Def("acoustid.scan").Return(opsregistry.OperationDef{ID: "acoustid.scan"}, true)
	// Deliberately NO EnqueueOp expectation: reaching it is the failure.

	h := handlers.NewOperationsV2Handler(store, registry, nil, true)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2/op-old/retry", "", gin.Params{{Key: "id", Value: "op-old"}})
	withCallerPerms(c, auth.PermScanTrigger)
	h.RetryOperationV2(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "permission denied: settings.manage")
	registry.AssertNotCalled(t, "EnqueueOp", mock.Anything, mock.Anything, mock.Anything)
}

// An unknown def_id falls through to EnqueueOp so the existing error response is
// preserved; the gate must not turn it into a 403 or a 404.
func TestTriggerOperationV2_UnknownDefFallsThroughUnchanged(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Def("nope.missing").Return(opsregistry.OperationDef{}, false)
	registry.EXPECT().EnqueueOp(mock.Anything, "nope.missing", mock.Anything).Return("opX", nil)

	h := handlers.NewOperationsV2Handler(nil, registry, nil, true)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"nope.missing"}`, nil)
	withCallerPerms(c, auth.PermScanTrigger)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
}

// A def declaring a permission other than settings.manage is enforced the same
// way. bulk-fetch-metadata is the real instance: it is the one job implementing
// PermissionAware, and as of the PermissionAware threading its def declares
// library.edit_metadata rather than settings.manage.
//
// This test only proves the HANDLER honours whatever the def declares -- it
// builds the def as a literal behind a mock registry, so nothing here reaches
// registerMaintenanceJobOp. What the def is actually registered with is pinned
// by TestBulkFetchMetadataOpRequiresEditMetadata in internal/server, which
// exercises the real registration path. Both are needed: this one would keep
// passing if the registration regressed.
func TestTriggerOperationV2_EnforcesANonDefaultPermission(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Def("maintenance.bulk-fetch-metadata").Return(opsregistry.OperationDef{
		ID:          "maintenance.bulk-fetch-metadata",
		Permissions: []auth.Permission{auth.PermLibraryEditMetadata},
	}, true)
	registry.EXPECT().EnqueueOp(mock.Anything, "maintenance.bulk-fetch-metadata", mock.Anything).Return("op10", nil)

	h := handlers.NewOperationsV2Handler(nil, registry, nil, true)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"maintenance.bulk-fetch-metadata"}`, nil)
	// Holds exactly what the def asks for, and nothing else beyond the route guard.
	withCallerPerms(c, auth.PermScanTrigger, auth.PermLibraryEditMetadata)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "op10")
}

// The mirror image: settings.manage does NOT unlock a def that asks for
// library.edit_metadata. Without this, the test above would pass for a handler
// that had simply stopped checking.
func TestTriggerOperationV2_SettingsManageDoesNotUnlockEditMetadata(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Def("maintenance.bulk-fetch-metadata").Return(opsregistry.OperationDef{
		ID:          "maintenance.bulk-fetch-metadata",
		Permissions: []auth.Permission{auth.PermLibraryEditMetadata},
	}, true)
	// No EnqueueOp expectation: reaching it is the failure.

	h := handlers.NewOperationsV2Handler(nil, registry, nil, true)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"maintenance.bulk-fetch-metadata"}`, nil)
	withCallerPerms(c, auth.PermScanTrigger, auth.PermSettingsManage)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), string(auth.PermLibraryEditMetadata))
	registry.AssertNotCalled(t, "EnqueueOp", mock.Anything, mock.Anything, mock.Anything)
}

// The two scheduled report-only checks (maintenance.file-integrity-check and
// maintenance.orphan-book-files-cleanup) declare settings.manage; a caller with
// only scan.trigger (the seeded editor) must be refused on both. The real defs'
// Permissions are pinned in internal/plugins/maintenance so this mock cannot drift.
func TestTriggerOperationV2_EditorCannotRunScheduledIntegrityChecks(t *testing.T) {
	for _, id := range []string{"maintenance.file-integrity-check", "maintenance.orphan-book-files-cleanup"} {
		t.Run(id, func(t *testing.T) {
			registry := handlersmocks.NewMockOperationsRegistry(t)
			registry.EXPECT().Def(id).Return(opsregistry.OperationDef{
				ID:          id,
				Permissions: []auth.Permission{auth.PermSettingsManage},
			}, true)
			// Deliberately NO EnqueueOp expectation: reaching it is the failure.

			h := handlers.NewOperationsV2Handler(nil, registry, nil, true)
			c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"`+id+`"}`, nil)
			withCallerPerms(c, auth.PermScanTrigger)
			h.TriggerOperationV2(c)

			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.Contains(t, w.Body.String(), string(auth.PermSettingsManage))
			registry.AssertNotCalled(t, "EnqueueOp", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}
