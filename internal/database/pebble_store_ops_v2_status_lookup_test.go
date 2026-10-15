// file: internal/database/pebble_store_ops_v2_status_lookup_test.go
// version: 1.0.0
// guid: 1d7a3e85-9b2c-4f60-a4d8-5e0c6b9f3a27
// last-edited: 2026-10-15

package database

import "testing"

func TestHasOperationV2WithStatus(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	const def = "maintenance.example-op"
	if found, err := s.HasOperationV2WithStatus(def, "interrupted_ask"); err != nil || found {
		t.Fatalf("empty store: found=%t err=%v", found, err)
	}
	if err := s.InsertOperationV2(OperationV2Row{ID: "op-a", DefID: def, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateOperationV2Status("op-a", "interrupted_ask", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if found, err := s.HasOperationV2WithStatus(def, "interrupted_ask"); err != nil || !found {
		t.Fatalf("interrupted_ask row: found=%t err=%v, want found", found, err)
	}
	if found, _ := s.HasOperationV2WithStatus("maintenance.other-op", "interrupted_ask"); found {
		t.Error("matched a row of a different def")
	}
	if found, _ := s.HasOperationV2WithStatus(def, "canceled"); found {
		t.Error("matched a row with a different status")
	}
}
