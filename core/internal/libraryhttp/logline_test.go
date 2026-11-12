// file: core/internal/libraryhttp/logline_test.go
// version: 1.0.0
// guid: e865b84b-b151-4d5b-a1e6-16414c415429
// last-edited: 2026-11-12

package libraryhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// A request path with a line break in it, and the error that repeats it, reach the
// log as one line each: a forged line cannot follow.
func TestFailLogsWhatAClientSentOnOneLine(t *testing.T) {
	h := &attrRecorder{vals: map[string]string{}}
	s := &Server{log: slog.New(h)}
	req := httptest.NewRequest(http.MethodGet, "/browse%0D%0Aforged", nil)
	s.fail(httptest.NewRecorder(), req, errors.New("cannot read /browse\r\nforged"))
	for key, want := range map[string]string{
		"method": "GET",
		"path":   `/browse\r\nforged`,
		"err":    `cannot read /browse\r\nforged`,
	} {
		got := h.vals[key]
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("%s = %q holds a raw line break", key, got)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestCloseLoggedKeepsTheDirectoryOnOneLine(t *testing.T) {
	h := &attrRecorder{vals: map[string]string{}}
	s := &Server{log: slog.New(h)}
	s.closeLogged(failingCloser{errors.New("close Arkham\nforged: input/output error")}, "Arkham\nforged")
	for key, want := range map[string]string{
		"dir": `Arkham\nforged`,
		"err": `close Arkham\nforged: input/output error`,
	} {
		if got := h.vals[key]; strings.ContainsAny(got, "\r\n") || got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
