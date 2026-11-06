// file: core/log/integration_test.go
// version: 1.0.1

package log

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestEndToEndLoggingFlow verifies the full flow: OpContext attached to ctx,
// downstream logging.Info call emits an slog record carrying opID, opType,
// opStatus, and entities attributes — the contract the UI relies on for
// grouping logs by operation.
func TestEndToEndLoggingFlow(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	op := &OpContext{
		ID:     "op-end-to-end-123",
		Type:   "metadata-fetch",
		Status: "pending",
	}
	op.AddEntity("books", "book-1", "book-2")

	ctx := WithOp(context.Background(), op)
	Info(ctx, "starting work", "totalBooks", 2)

	op.SetStatus("success")
	Info(ctx, "finished work", "found", 2)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d:\n%s", len(lines), buf.String())
	}

	// First line: pending status
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line invalid JSON: %v\n%s", err, lines[0])
	}
	assertEqual(t, first, "opID", "op-end-to-end-123")
	assertEqual(t, first, "opType", "metadata-fetch")
	assertEqual(t, first, "opStatus", "pending")
	assertEqual(t, first, "msg", "starting work")
	assertEqual(t, first, "totalBooks", float64(2)) // JSON numbers decode as float64
	if !strings.Contains(first["entities"].(string), "book-1") {
		t.Errorf("entities attribute should mention book-1, got %v", first["entities"])
	}

	// Second line: success status (mutated through pointer)
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second line invalid JSON: %v\n%s", err, lines[1])
	}
	assertEqual(t, second, "opStatus", "success")
	assertEqual(t, second, "found", float64(2))
}

// TestLoggingWithoutOpContext verifies log calls work normally when no
// operation context is attached — no panics, no spurious empty op attrs.
func TestLoggingWithoutOpContext(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	Info(context.Background(), "no op", "foo", "bar")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if _, ok := rec["opID"]; ok {
		t.Errorf("opID should be absent when no OpContext, got %v", rec["opID"])
	}
	assertEqual(t, rec, "foo", "bar")
}

func assertEqual(t *testing.T, m map[string]any, key string, want any) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("missing key %q in %v", key, m)
		return
	}
	if got != want {
		t.Errorf("key %q: got %v (%T), want %v (%T)", key, got, got, want, want)
	}
}
