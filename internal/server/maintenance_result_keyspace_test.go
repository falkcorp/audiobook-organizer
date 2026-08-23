// file: internal/server/maintenance_result_keyspace_test.go
// version: 1.0.0
// guid: b654171b-40ad-4ba9-88df-841c4d39c5cc
// last-edited: 2026-08-23

package server

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
)

// The two maintenance result routes are the only readers left that have to span
// BOTH operations keyspaces. Retiring the v1 minter means a run started after
// the deploy has a v2 row and no v1 row, while every run started before it has a
// v1 row and no v2 row — and the results table, keyed by an operation id string
// with no foreign key to either, holds both eras' rows side by side.
//
// These tests pin that both eras resolve. Without the v1 arm, the retirement
// would 404 every historical run while its results sat intact in the store; the
// routes have no frontend consumer, so nothing else would have caught it.
func TestLookupMaintenanceResultOp_ResolvesBothKeyspaces(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("v2 row minted by the current dispatcher", func(t *testing.T) {
		opID := ulid.Make().String()
		if err := server.storeForWiring().InsertOperationV2(database.OperationV2Row{
			ID:              opID,
			DefID:           maintenanceOpID("scan-composer-tags"),
			Plugin:          "maintenance",
			Status:          "completed",
			ProgressCurrent: 7,
			ProgressTotal:   9,
			QueuedAt:        time.Now().UTC(),
		}); err != nil {
			t.Fatalf("InsertOperationV2: %v", err)
		}

		got, err := server.lookupMaintenanceResultOp(opID, "scan-composer-tags",
			"composer_tag_scan", "maintenance:scan-composer-tags")
		if err != nil {
			t.Fatalf("lookup of a v2 maintenance row failed: %v", err)
		}
		if got.Status != "completed" || got.Progress != 7 || got.Total != 9 {
			t.Fatalf("v2 row projected as %+v, want {completed 7 9}", got)
		}
	})

	// Both v1 names, because the v1 dispatcher renamed the type once already:
	// the pre-ASYNC-CLEAN-1 name and the "maintenance:<job>" name that replaced
	// it. Rows carrying either are still in the store.
	for _, legacyType := range []string{"composer_tag_scan", "maintenance:scan-composer-tags"} {
		t.Run("v1 row typed "+legacyType, func(t *testing.T) {
			opID := ulid.Make().String()
			if _, err := server.storeForWiring().CreateOperation(opID, legacyType, nil); err != nil {
				t.Fatalf("CreateOperation: %v", err)
			}
			if err := server.storeForWiring().UpdateOperationStatus(opID, "completed", 3, 4, "done"); err != nil {
				t.Fatalf("UpdateOperationStatus: %v", err)
			}

			got, err := server.lookupMaintenanceResultOp(opID, "scan-composer-tags",
				"composer_tag_scan", "maintenance:scan-composer-tags")
			if err != nil {
				t.Fatalf("lookup of a historical v1 row failed: %v", err)
			}
			if got.Status != "completed" || got.Progress != 3 || got.Total != 4 {
				t.Fatalf("v1 row projected as %+v, want {completed 3 4}", got)
			}
		})
	}
}

// A wrong-job id must be distinguishable from an unknown one: the routes answer
// 400 for the first and 404 for the second, and collapsing them would tell an
// operator their completed run had vanished.
func TestLookupMaintenanceResultOp_WrongJobIsNotNotFound(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	t.Run("v2 row for a different job", func(t *testing.T) {
		opID := ulid.Make().String()
		if err := server.storeForWiring().InsertOperationV2(database.OperationV2Row{
			ID:       opID,
			DefID:    maintenanceOpID("repair-missing-files"),
			Plugin:   "maintenance",
			Status:   "completed",
			QueuedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("InsertOperationV2: %v", err)
		}
		_, err := server.lookupMaintenanceResultOp(opID, "scan-composer-tags",
			"composer_tag_scan", "maintenance:scan-composer-tags")
		if !errors.Is(err, errMaintenanceOpWrongJob) {
			t.Fatalf("a v2 row for another job returned %v, want errMaintenanceOpWrongJob", err)
		}
	})

	t.Run("v1 row for a different job", func(t *testing.T) {
		opID := ulid.Make().String()
		if _, err := server.storeForWiring().CreateOperation(opID, "maintenance:repair-missing-files", nil); err != nil {
			t.Fatalf("CreateOperation: %v", err)
		}
		_, err := server.lookupMaintenanceResultOp(opID, "scan-composer-tags",
			"composer_tag_scan", "maintenance:scan-composer-tags")
		if !errors.Is(err, errMaintenanceOpWrongJob) {
			t.Fatalf("a v1 row for another job returned %v, want errMaintenanceOpWrongJob", err)
		}
	})

	t.Run("an id in neither keyspace", func(t *testing.T) {
		_, err := server.lookupMaintenanceResultOp(ulid.Make().String(), "scan-composer-tags",
			"composer_tag_scan", "maintenance:scan-composer-tags")
		if err == nil || errors.Is(err, errMaintenanceOpWrongJob) {
			t.Fatalf("an unknown id returned %v, want a not-found error", err)
		}
	})
}
