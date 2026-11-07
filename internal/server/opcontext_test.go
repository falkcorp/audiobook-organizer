// file: internal/server/opcontext_test.go
// version: 1.0.0
// guid: 14942c06-75c3-4b74-a4c2-ad3a8105aa5e
// last-edited: 2026-11-07

package server

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	logging "github.com/falkcorp/mediacore/log"
)

// TestOpContextAttrsResolvedAtLogTime: carried in the context with an empty
// key, the operation's attributes are read when each record is logged, so a
// status set later shows on later records, and the keys stay flat.
func TestOpContextAttrsResolvedAtLogTime(t *testing.T) {
	var out bytes.Buffer
	l, err := logging.New(logging.Options{Output: &out, Format: logging.FormatText, Level: logging.LevelInfo,
		Redactor: logging.NewRedactor(nil)})
	if err != nil {
		t.Fatal(err)
	}
	op := &opContext{ID: "op-arkham-1", Type: "dedup.book-scan", Status: "pending"}
	ctx := logging.WithAttrs(context.Background(), slog.Any("", op))
	log := l.Module("books")

	log.InfoContext(ctx, "scan starting")
	op.AddEntity("books", "book-joker", "book-bane")
	op.SetStatus("failed")
	log.InfoContext(ctx, "scan failed")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 records, got %q", out.String())
	}
	for _, want := range []string{"opID=op-arkham-1", "opType=dedup.book-scan", "opStatus=pending"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("first record lacks %s: %s", want, lines[0])
		}
	}
	if strings.Contains(lines[0], "entities=") {
		t.Errorf("entities written before any were added: %s", lines[0])
	}
	for _, want := range []string{" opStatus=failed", ` entities="{\"books\":[\"book-joker\",\"book-bane\"]}"`} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("second record lacks %s: %s", want, lines[1])
		}
	}
	if strings.Contains(out.String(), ".opID") {
		t.Errorf("op keys nested under a group: %s", out.String())
	}

	// Nil-safe, and LogValue is safe against concurrent updates (-race).
	var nilOp *opContext
	nilOp.AddEntity("books", "x")
	if nilOp.SetStatus("x") != nil {
		t.Error("nil SetStatus returned non-nil")
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			op.AddEntity("series", "s")
			op.SetStatus("success")
			log.InfoContext(ctx, "worker", "i", i)
		})
	}
	wg.Wait()
}
