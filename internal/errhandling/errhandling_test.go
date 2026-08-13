// file: internal/errhandling/errhandling_test.go
// version: 1.0.0
// guid: 21b860a5-1a53-4ed1-8cc2-d62c8862bea9
// last-edited: 2026-08-11

package errhandling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// captureLogs installs a JSON logger over a buffer and returns a function that
// decodes whatever was written. Tests assert on the DECODED RECORDS, not on
// "it didn't panic" — a test that cannot see the output would pass with the
// function body deleted.
func captureLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	restore := SetLogger(slog.New(h))
	t.Cleanup(restore)

	return func() []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("undecodable log line %q: %v", line, err)
			}
			out = append(out, rec)
		}
		return out
	}
}

func TestMustLog_NilErrorIsNoOp(t *testing.T) {
	// Every wave will call MustLog unconditionally on a call that usually
	// succeeds. If nil produced a log line, the sweep would bury production
	// logs in noise — this is the most likely real-world bug in the helper.
	records := captureLogs(t)

	MustLog(nil, "should not appear", "key", "value")

	if got := records(); len(got) != 0 {
		t.Fatalf("MustLog(nil) emitted %d record(s), want 0: %v", len(got), got)
	}
}

func TestMustLog_LogsErrorAtWarnWithFields(t *testing.T) {
	records := captureLogs(t)
	sentinel := errors.New("disk on fire")

	MustLog(sentinel, "checkpoint not saved", "op_id", "op-123", "attempt", 2)

	got := records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want exactly 1: %v", len(got), got)
	}
	rec := got[0]

	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", rec["level"])
	}
	if rec["msg"] != "checkpoint not saved" {
		t.Errorf("msg = %v, want %q", rec["msg"], "checkpoint not saved")
	}
	// The error text must survive. This is the whole reason the helper exists:
	// a discard that loses the error is the defect being fixed.
	if rec["error"] != "disk on fire" {
		t.Errorf("error = %v, want %q", rec["error"], "disk on fire")
	}
	if rec["op_id"] != "op-123" {
		t.Errorf("op_id = %v, want op-123", rec["op_id"])
	}
	if rec["attempt"] != float64(2) {
		t.Errorf("attempt = %v, want 2", rec["attempt"])
	}
}

func TestMustLogContext_UsesContextAndLogs(t *testing.T) {
	records := captureLogs(t)

	MustLogContext(context.Background(), errors.New("boom"), "undo entry not written", "book_id", "b-9")

	got := records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0]["error"] != "boom" {
		t.Errorf("error = %v, want boom", got[0]["error"])
	}
	if got[0]["book_id"] != "b-9" {
		t.Errorf("book_id = %v, want b-9", got[0]["book_id"])
	}
}

func TestMustLog_DanglingKeyIsPreservedNotDropped(t *testing.T) {
	// A caller who miscounts arguments should still get their data. Silently
	// dropping the operand would be a silent failure inside the silent-failure
	// helper.
	records := captureLogs(t)

	MustLog(errors.New("x"), "msg", "lonely_key")

	got := records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0]["!BADKEY"] != "lonely_key" {
		t.Errorf("!BADKEY = %v, want lonely_key", got[0]["!BADKEY"])
	}
	// The error must survive a malformed kv list. If the dangling key were
	// paired with the error attr, the error would vanish — the exact defect
	// this package exists to prevent, reintroduced inside the fix.
	if got[0]["error"] != "x" {
		t.Errorf("error = %v, want %q — a dangling key swallowed the error", got[0]["error"], "x")
	}
}

func TestSetLogger_RestoresPrevious(t *testing.T) {
	var buf bytes.Buffer
	first := slog.New(slog.NewJSONHandler(&buf, nil))

	restore := SetLogger(first)
	if activeLogger() != first {
		t.Fatal("SetLogger did not install the logger")
	}
	restore()
	if activeLogger() == first {
		t.Fatal("restore() did not put the previous logger back")
	}
}

func TestMustLog_ConcurrentUseIsSafe(t *testing.T) {
	// Waves 4-13 will call this from inside bounded worker pools.
	records := captureLogs(t)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			MustLog(errors.New("concurrent"), "parallel discard")
		}()
	}
	wg.Wait()

	if n := len(records()); n != 50 {
		t.Fatalf("got %d records, want 50", n)
	}
}
