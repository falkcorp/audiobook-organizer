// file: core/library/closelog_test.go
// version: 1.0.0
// guid: bd3c4dd1-917d-4962-8f45-066b1837cd43
// last-edited: 2026-11-12

package library

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// attrRecorder keeps the attribute values a record carried, before any handler
// formats them: what the logging call itself handed over.
type attrRecorder struct {
	mu   sync.Mutex
	vals map[string]string
}

func (h *attrRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (h *attrRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r.Attrs(func(a slog.Attr) bool {
		h.vals[a.Key] = a.Value.String()
		return true
	})
	return nil
}
func (h *attrRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *attrRecorder) WithGroup(string) slog.Handler      { return h }

type failingCloser struct{ err error }

func (c failingCloser) Close() error { return c.err }

// A name with a line break in it, and the error that repeats it, reach the log as
// one line each: a forged line cannot follow.
func TestCloseLoggedKeepsANameOnOneLine(t *testing.T) {
	h := &attrRecorder{vals: map[string]string{}}
	closeLogged(slog.New(h), failingCloser{errors.New("close Arkham\r\nforged: input/output error")}, "the directory holding Arkham\r\nforged")
	for key, want := range map[string]string{
		"what": `the directory holding Arkham\r\nforged`,
		"err":  `close Arkham\r\nforged: input/output error`,
	} {
		got := h.vals[key]
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("%s = %q holds a raw line break", key, got)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// A close that worked logs nothing.
	quiet := &attrRecorder{vals: map[string]string{}}
	closeLogged(slog.New(quiet), failingCloser{}, "Arkham")
	if len(quiet.vals) != 0 {
		t.Errorf("a clean close logged %v", quiet.vals)
	}
}
