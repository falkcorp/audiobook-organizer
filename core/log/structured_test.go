// file: core/log/structured_test.go
// version: 1.0.1

package log

import (
	"context"
	"testing"
)

func TestWithOp(t *testing.T) {
	op := &OpContext{
		ID:     "op-123",
		Type:   "metadata-fetch",
		Status: "pending",
	}
	ctx := context.Background()
	ctx = WithOp(ctx, op)

	retrieved := OpFromContext(ctx)
	if retrieved == nil {
		t.Fatal("OpFromContext returned nil")
	}
	if retrieved.ID != op.ID {
		t.Errorf("ID mismatch: got %q, want %q", retrieved.ID, op.ID)
	}
	if retrieved.Type != op.Type {
		t.Errorf("Type mismatch: got %q, want %q", retrieved.Type, op.Type)
	}
	if retrieved.Status != op.Status {
		t.Errorf("Status mismatch: got %q, want %q", retrieved.Status, op.Status)
	}
}

func TestOpFromContextNil(t *testing.T) {
	ctx := context.Background()
	op := OpFromContext(ctx)
	if op != nil {
		t.Fatal("OpFromContext should return nil for context without operation")
	}
}

func TestAddEntity(t *testing.T) {
	op := &OpContext{
		ID:     "op-123",
		Type:   "metadata-fetch",
		Status: "pending",
	}
	op.AddEntity("books", "book-1", "book-2")
	op.AddEntity("genres", "rock")

	if len(op.Entities) != 2 {
		t.Errorf("Entities map size: got %d, want 2", len(op.Entities))
	}
	if len(op.Entities["books"]) != 2 {
		t.Errorf("books entities: got %d, want 2", len(op.Entities["books"]))
	}
	if len(op.Entities["genres"]) != 1 {
		t.Errorf("genres entities: got %d, want 1", len(op.Entities["genres"]))
	}
}

func TestSetStatus(t *testing.T) {
	op := &OpContext{
		ID:     "op-123",
		Type:   "metadata-fetch",
		Status: "pending",
	}
	result := op.SetStatus("success")
	if result != op {
		t.Fatal("SetStatus should return self for chaining")
	}
	if op.Status != "success" {
		t.Errorf("Status: got %q, want %q", op.Status, "success")
	}
}

func TestOpAttrsNil(t *testing.T) {
	attrs := opAttrs(nil)
	if len(attrs) != 0 {
		t.Errorf("opAttrs(nil) should return empty list, got %d attrs", len(attrs))
	}
}

func TestOpAttrs(t *testing.T) {
	op := &OpContext{
		ID:     "op-123",
		Type:   "metadata-fetch",
		Status: "success",
		Entities: map[string][]string{
			"books":  {"book-1"},
			"genres": {"rock"},
		},
	}
	attrs := opAttrs(op)

	// Should have 8 attrs: opID (2), opType (2), opStatus (2), entities (2)
	if len(attrs) != 8 {
		t.Errorf("opAttrs length: got %d, want 8", len(attrs))
	}

	// Verify keys are present
	keyFound := make(map[string]bool)
	for i := 0; i < len(attrs); i += 2 {
		key := attrs[i].(string)
		keyFound[key] = true
	}

	expectedKeys := []string{"opID", "opType", "opStatus", "entities"}
	for _, key := range expectedKeys {
		if !keyFound[key] {
			t.Errorf("Missing key: %q", key)
		}
	}
}
