// file: core/coretest/logcapture.go
// version: 1.0.0
// guid: 155a2aa9-906e-487e-8f5d-2be349726953
// last-edited: 2026-11-07

// Package coretest holds test support for core's own tests.
package coretest

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
)

// LogCapture records the slog records written through Logger. It never touches
// slog.Default (03 R5): the logger it returns is handed to the code under test.
type LogCapture struct {
	mu    sync.Mutex
	recs  []slog.Record
	level slog.Level
}

// NewLogCapture returns an empty capture at level Debug. It holds no global
// state, so it registers no cleanup.
func NewLogCapture(tb testing.TB) *LogCapture {
	tb.Helper()
	return &LogCapture{level: slog.LevelDebug}
}

// Logger returns a logger whose records are captured by c.
func (c *LogCapture) Logger() *slog.Logger {
	return slog.New(&captureHandler{c: c})
}

// Records returns copies of the captured records, in write order.
func (c *LogCapture) Records() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]slog.Record, len(c.recs))
	for i, r := range c.recs {
		out[i] = r.Clone()
	}
	return out
}

// Messages returns the message of each captured record, in write order.
func (c *LogCapture) Messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.recs))
	for i, r := range c.recs {
		out[i] = r.Message
	}
	return out
}

// captureHandler writes into its LogCapture. Handlers derived with WithAttrs
// or WithGroup share the same capture; the attrs and groups they add are
// carried onto each record they write, nested as slog's contract describes.
type captureHandler struct {
	c      *LogCapture
	attrs  []slog.Attr // already nested under the groups open when they were added
	groups []string    // open groups, outermost first
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.c.level
}

// nest wraps attrs in the handler's open groups, innermost last.
func (h *captureHandler) nest(attrs []slog.Attr) []slog.Attr {
	for _, v := range slices.Backward(h.groups) {
		if len(attrs) == 0 {
			return nil
		}
		attrs = []slog.Attr{{Key: v, Value: slog.GroupValue(attrs...)}}
	}
	return attrs
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	out.AddAttrs(h.attrs...)
	var own []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		own = append(own, a)
		return true
	})
	out.AddAttrs(h.nest(own)...)
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	h.c.recs = append(h.c.recs, out)
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = append(append([]slog.Attr(nil), h.attrs...), h.nest(attrs)...)
	return &nh
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.groups = append(append([]string(nil), h.groups...), name)
	return &nh
}
