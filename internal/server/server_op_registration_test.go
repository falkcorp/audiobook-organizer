// file: internal/server/server_op_registration_test.go
// version: 1.0.0
// guid: 7f2a4c81-9d63-4e05-b8a7-1c30e95d6f24
// last-edited: 2026-08-20

// Regression test for the RootDir op-registration gate.
//
// Op registration used to be wrapped in `if config.AppConfig.RootDir != ""`,
// justified by test convenience. The consequence in production was that a
// server started without a root directory registered ZERO operations — no
// maintenance plugin, and none of the ~55 opRegistrars — while still reporting
// healthy. Nothing failed; the ops were simply absent.
//
// This test pins the fix from the direction that matters: RootDir empty, ops
// present anyway.
package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
)

// TestNewServer_RegistersOpsWithEmptyRootDir proves boot registers operations
// even with no RootDir configured.
func TestNewServer_RegistersOpsWithEmptyRootDir(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origCfg := config.AppConfig
	config.AppConfig.RootDir = ""
	t.Cleanup(func() { config.AppConfig = origCfg })

	store := mocks.NewMockStore(t)
	// Boot's own store calls, unrelated to what this test asserts.
	store.EXPECT().SetRootDir(mock.Anything).Return().Maybe()
	allowOpDefinitionUpserts(store)

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	srv := NewServer(store)
	t.Cleanup(func() {
		if srv.fileIOPool != nil {
			srv.fileIOPool.Stop()
		}
	})

	defs := srv.opRegistry.ActiveDefs()
	if len(defs) == 0 {
		t.Fatal("server registered ZERO operations with RootDir unset — the registration gate is back")
	}

	// Count by plugin so the failure message says WHICH family went missing:
	// the gate dropped the maintenance plugin and the opRegistrars loop
	// together, and either one silently vanishing is the same class of bug.
	byPlugin := map[string]int{}
	for _, d := range defs {
		byPlugin[d.Plugin]++
	}
	if byPlugin["maintenance"] == 0 {
		t.Errorf("no maintenance ops registered with RootDir unset (defs by plugin: %v)", byPlugin)
	}

	// A named def, so a registry that somehow registers only stubs still fails.
	if _, ok := srv.opRegistry.Def("maintenance.purge-deleted"); !ok {
		t.Errorf("maintenance.purge-deleted not registered with RootDir unset (defs by plugin: %v)", byPlugin)
	}

	t.Logf("registered %d op defs with RootDir unset: %v", len(defs), byPlugin)
}
