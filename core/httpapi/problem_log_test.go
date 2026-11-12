// file: core/httpapi/problem_log_test.go
// version: 1.0.0
// guid: e3a8d5c1-7f20-4b96-a4d8-1c06b92e7f35
// last-edited: 2026-11-12

package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// attrLog is a slog handler that keeps the raw attribute values of the records
// it receives, so a test can see what the sink was handed rather than what a
// text or JSON handler makes of it (both quote a value that holds a newline).
type attrLog struct{ attrs map[string]string }

func (h *attrLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *attrLog) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		h.attrs[a.Key] = a.Value.String()
		return true
	})
	return nil
}
func (h *attrLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *attrLog) WithGroup(string) slog.Handler      { return h }

// A handler error and a request path are logged on one line each: the cause can
// quote what the request carried, and a decoded path can hold a newline.
func TestWriteErrorLogsTheCauseAndPathOnOneLine(t *testing.T) {
	h := &attrLog{attrs: map[string]string{}}
	cause := errors.New("import: entry \"db/x\rINFO forged by the entry name\nERROR forged again\" is refused")
	r := httptest.NewRequest(http.MethodPost, "/api/core/v1/backups/import", nil)
	r.URL.Path = "/api/core/v1/backups/import\nINFO forged by the path"
	w := httptest.NewRecorder()
	WriteError(w, r, cause, slog.New(h))

	for _, k := range []string{"error", "path"} {
		v, ok := h.attrs[k]
		if !ok {
			t.Fatalf("no %q attribute was logged: %v", k, h.attrs)
		}
		if strings.ContainsAny(v, "\r\n") {
			t.Errorf("%s = %q holds a line break", k, v)
		}
	}
	if !strings.Contains(h.attrs["error"], "is refused") || !strings.Contains(h.attrs["error"], "db/x") {
		t.Errorf("the cause was not kept readable: %q", h.attrs["error"])
	}
	if !strings.Contains(h.attrs["path"], "forged by the path") {
		t.Errorf("the path was not kept readable: %q", h.attrs["path"])
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "is refused") || strings.Contains(w.Body.String(), "db/x") {
		t.Errorf("the body carries the cause: %s", w.Body.String())
	}
}

// A nil error is still a 500 with a record, as before; it must not panic the
// sink that now reads its text.
func TestWriteErrorNilCauseStillLogs(t *testing.T) {
	h := &attrLog{attrs: map[string]string{}}
	r := httptest.NewRequest(http.MethodGet, "/api/core/v1/x", nil)
	w := httptest.NewRecorder()
	WriteError(w, r, nil, slog.New(h))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if h.attrs["error"] != "<nil>" {
		t.Fatalf("error attribute = %q, want <nil>", h.attrs["error"])
	}
}

// A mapped error (an *Error) is not logged as internal at all.
func TestWriteErrorMappedErrorIsNotLogged(t *testing.T) {
	h := &attrLog{attrs: map[string]string{}}
	r := httptest.NewRequest(http.MethodGet, "/api/core/v1/x", nil)
	WriteError(httptest.NewRecorder(), r, Conflict("name_taken", "pick another"), slog.New(h))
	if len(h.attrs) != 0 {
		t.Fatalf("a mapped error was logged: %v", h.attrs)
	}
}
