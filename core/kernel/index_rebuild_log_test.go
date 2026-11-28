// file: core/kernel/index_rebuild_log_test.go
// version: 0.1.0
// guid: 91c4e7b2-0a5d-4f36-8e1b-d3a6f2c80574
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// recHandler records every slog record: message, level and attrs in order.
type recHandler struct {
	mu   sync.Mutex
	recs []slogRec
}

type slogRec struct {
	msg   string
	level slog.Level
	attrs map[string]any
	keys  []string
}

func (h *recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *recHandler) WithGroup(string) slog.Handler            { return h }
func (h *recHandler) Handle(_ context.Context, r slog.Record) error {
	rec := slogRec{msg: r.Message, level: r.Level, attrs: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Any()
		rec.keys = append(rec.keys, a.Key)
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, rec)
	h.mu.Unlock()
	return nil
}

// TestIndexRebuildLogContract pins, literally, the three messages and their
// attributes that the F3-07 rehearsal gates on. The strings are spelled out
// here rather than through the constants, so a changed constant fails.
func TestIndexRebuildLogContract(t *testing.T) {
	gen := storage.Generation{15: 7}
	genText := "00000000000000000000000000000007"
	t.Run("a rebuild that finishes", func(t *testing.T) {
		h := &recHandler{}
		clock := []time.Time{time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 28, 1, 0, 2, 500e6, time.UTC)}
		now := func() time.Time { r := clock[0]; clock = clock[1:]; return r }
		err := logIndexRebuild(t.Context(), slog.New(h), gen, 1200, now, func(context.Context) (int64, error) { return 1187, nil })
		if err != nil {
			t.Fatal(err)
		}
		if len(h.recs) != 2 {
			t.Fatalf("%d lines, want started then finished: %+v", len(h.recs), h.recs)
		}
		s, f := h.recs[0], h.recs[1]
		if s.msg != "books: search index rebuild started" || s.level != slog.LevelInfo {
			t.Errorf("first line = %q at %v", s.msg, s.level)
		}
		if !slices.Equal(s.keys, []string{"generation", "books"}) || s.attrs["generation"] != genText || s.attrs["books"] != int64(1200) {
			t.Errorf("started attrs = %v %v", s.keys, s.attrs)
		}
		if f.msg != "books: search index rebuild finished" || f.level != slog.LevelInfo {
			t.Errorf("second line = %q at %v", f.msg, f.level)
		}
		if !slices.Equal(f.keys, []string{"generation", "docs", "elapsed_ms"}) || f.attrs["generation"] != genText || f.attrs["docs"] != int64(1187) || f.attrs["elapsed_ms"] != int64(2500) {
			t.Errorf("finished attrs = %v %v", f.keys, f.attrs)
		}
	})
	t.Run("a rebuild that fails", func(t *testing.T) {
		h := &recHandler{}
		boom := errors.New("bleve: disk full")
		err := logIndexRebuild(t.Context(), slog.New(h), gen, 40, nil, func(context.Context) (int64, error) { return 0, boom })
		if err != boom {
			t.Fatalf("returned %v, want the rebuild's own error", err)
		}
		if len(h.recs) != 2 || h.recs[0].msg != "books: search index rebuild started" {
			t.Fatalf("lines = %+v, want started then failed", h.recs)
		}
		f := h.recs[1]
		if f.msg != "books: search index rebuild failed" || f.level != slog.LevelError {
			t.Errorf("second line = %q at %v", f.msg, f.level)
		}
		if !slices.Equal(f.keys, []string{"generation", "err"}) || f.attrs["generation"] != genText || f.attrs["err"] != boom {
			t.Errorf("failed attrs = %v %v", f.keys, f.attrs)
		}
	})
	t.Run("the constants carry the same text", func(t *testing.T) {
		for got, want := range map[string]string{
			IndexRebuildStarted:  "books: search index rebuild started",
			IndexRebuildFinished: "books: search index rebuild finished",
			IndexRebuildFailed:   "books: search index rebuild failed",
		} {
			if got != want {
				t.Errorf("constant %q, want %q", got, want)
			}
		}
	})
	t.Run("a nil logger discards", func(t *testing.T) {
		if err := logIndexRebuild(t.Context(), nil, gen, 0, nil, func(context.Context) (int64, error) { return 0, nil }); err != nil {
			t.Fatal(err)
		}
	})
}
