// file: internal/books/appconfig/unrep_test.go
// version: 0.1.0
// guid: d2f9d0fe-e3a4-4574-9996-277b6be7951d
// last-edited: 2026-11-21

package appconfig

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// lockedBuffer is a bytes.Buffer safe for a slog handler.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) lines(level string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for line := range strings.SplitSeq(l.b.String(), "\n") {
		if strings.Contains(line, "level="+level) {
			out = append(out, line)
		}
	}
	return out
}

// loggedEngine is an Engine over f whose log is captured, debug included.
func loggedEngine(f *fakeAccess) (*Engine, *lockedBuffer) {
	buf := &lockedBuffer{}
	e := New(f, nil)
	e.log = logger.FromSlog(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return e, buf
}

// swapRows puts row b before row a in the projection order, so a test proves
// the outcome does not depend on which of two rows is read first.
func swapRows(t *testing.T, e *Engine, a, b string) {
	t.Helper()
	i := slices.IndexFunc(e.rows, func(p projRow) bool { return p.row.Head == a })
	j := slices.IndexFunc(e.rows, func(p projRow) bool { return p.row.Head == b })
	if i < 0 || j < 0 {
		t.Fatalf("rows %s/%s not projected", a, b)
	}
	e.rows[i], e.rows[j] = e.rows[j], e.rows[i]
}

// TestEngine_SharedKeyHeldBySiblingIsNotUnrepresentable: bootstrap_key_ttl and
// bootstrap_key_ttl_days both name core.auth.bootstrap.key_ttl (a Shared
// pair). Core's default, 8h, is no whole number of days, so the _days row
// cannot hold it, but bootstrap_key_ttl does: the value reaches HEAD's Config
// and nothing is lost, so it is not reported unrepresentable and logs no
// error (a debug line at most), whichever row is read first. A value both
// rows hold fills both.
func TestEngine_SharedKeyHeldBySiblingIsNotUnrepresentable(t *testing.T) {
	for _, order := range []string{"table order", "days row first"} {
		t.Run(order, func(t *testing.T) {
			f := newFake()
			f.vals["core.auth.bootstrap.key_ttl"] = json.RawMessage(`"8h"`)
			e, logs := loggedEngine(f)
			if order == "days row first" {
				swapRows(t, e, "bootstrap_key_ttl", "bootstrap_key_ttl_days")
			}
			if err := e.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := e.Snapshot(); got.BootstrapKeyTTL != "8h" || got.BootstrapKeyTTLDays != 0 {
				t.Fatalf("ttl=%q days=%d, want 8h and the days row at its base", got.BootstrapKeyTTL, got.BootstrapKeyTTLDays)
			}
			if u := e.Unrepresentable(); len(u) != 0 {
				t.Errorf("Unrepresentable = %v, want none: bootstrap_key_ttl holds the value", u)
			}
			if errs := logs.lines("ERROR"); len(errs) != 0 {
				t.Errorf("logged errors for a value a sibling row holds: %v", errs)
			}
		})
	}

	f := newFake()
	f.vals["core.auth.bootstrap.key_ttl"] = json.RawMessage(`"24h"`)
	e, _ := loggedEngine(f)
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.Snapshot(); got.BootstrapKeyTTL != "24h" || got.BootstrapKeyTTLDays != 1 {
		t.Fatalf("ttl=%q days=%d, want 24h and 1", got.BootstrapKeyTTL, got.BootstrapKeyTTLDays)
	}
}

// TestEngine_UnrepresentableValueNoRowHoldsIsReported: a canonical value no
// HEAD row naming its path can hold (36h of task history, which only
// operation_log_retention_days, in whole days, names) keeps the setting at
// its base, is listed by Unrepresentable and is logged as an error.
func TestEngine_UnrepresentableValueNoRowHoldsIsReported(t *testing.T) {
	f := newFake()
	f.vals["core.tasks.history_retention"] = json.RawMessage(`"36h"`)
	e, logs := loggedEngine(f)
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if u := e.Unrepresentable(); !slices.Equal(u, []string{"operation_log_retention_days"}) {
		t.Fatalf("Unrepresentable = %v", u)
	}
	errs := logs.lines("ERROR")
	if len(errs) != 1 || !strings.Contains(errs[0], "operation_log_retention_days") {
		t.Fatalf("errors logged = %v, want one naming operation_log_retention_days", errs)
	}
}
