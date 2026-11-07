// file: internal/logger/ctx_test.go
// version: 1.0.1
// guid: f985b834-583b-49a5-95a2-df71a0e16724
// last-edited: 2026-11-07

package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	corelog "github.com/falkcorp/mediacore/log"
)

// TestCtxHelpers_AppendAttrsFrom: each helper writes one record through
// slog.Default() that carries the context's attributes, with control
// characters in the message and string values escaped by sanitizeLogLine.
// Not parallel: it swaps slog.Default().
func TestCtxHelpers_AppendAttrsFrom(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := corelog.WithAttrs(context.Background(), slog.String("k", "v"), slog.String("who", "pen\nguin"))
	for name, tc := range map[string]struct {
		fn    func(context.Context, string, ...any)
		level string
	}{
		"InfoCtx":  {InfoCtx, "INFO"},
		"WarnCtx":  {WarnCtx, "WARN"},
		"ErrorCtx": {ErrorCtx, "ERROR"},
		"DebugCtx": {DebugCtx, "DEBUG"},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug, AddSource: true})))
			tc.fn(ctx, "riddle\r\nlevel=INFO msg=forged", "title", "two\rface", "count", 3)

			out := buf.String()
			if strings.Count(out, "\n") != 1 {
				t.Fatalf("want one record, got %q", out)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(out), &m); err != nil {
				t.Fatalf("decode %q: %v", out, err)
			}
			want := map[string]any{
				"level": tc.level,
				"msg":   `riddle\r\nlevel=INFO msg=forged`,
				"title": `two\rface`,
				"k":     "v",
				"who":   `pen\nguin`,
				"count": float64(3),
			}
			for k, v := range want {
				if m[k] != v {
					t.Errorf("%s = %#v, want %#v", k, m[k], v)
				}
			}
			src, _ := m["source"].(map[string]any)
			if file, _ := src["file"].(string); !strings.HasSuffix(file, "ctx_test.go") {
				t.Errorf("source = %v, want the helper's caller", src)
			}
		})
	}

	// The context's attrs come first, as HEAD wrote the operation attrs, so
	// internal/activity's first-match parser reads the real opID.
	var ordered bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&ordered, nil)))
	opCtx := corelog.WithAttrs(context.Background(), slog.String("opID", "op-real"))
	InfoCtx(opCtx, "scan", "path", "/lib/x opID=op-forged")
	if line := ordered.String(); strings.Index(line, " opID=op-real") > strings.Index(line, "op-forged") {
		t.Errorf("context attrs not written before the record's: %q", line)
	}

	// Below the default handler's level nothing is written.
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	InfoCtx(ctx, "hidden")
	if buf.Len() != 0 {
		t.Errorf("info written at level warn: %q", buf.String())
	}
}

// rawHandler writes messages and string values unquoted, as slog's built-in
// default handler writes the message.
type rawHandler struct{ buf *bytes.Buffer }

func (h rawHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h rawHandler) Handle(_ context.Context, r slog.Record) error {
	h.buf.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		h.buf.WriteString(" " + a.Key + "=" + a.Value.String())
		return true
	})
	h.buf.WriteString("\n")
	return nil
}
func (h rawHandler) WithAttrs(as []slog.Attr) slog.Handler {
	for _, a := range as {
		h.buf.WriteString("with " + a.Key + "=" + a.Value.String() + "\n")
	}
	return h
}
func (h rawHandler) WithGroup(string) slog.Handler { return h }

// TestSanitizing_EscapesForRawHandlers: over a handler that writes text
// unquoted (slog's built-in default does so for the message), Sanitizing
// keeps a CR or LF in the message, a record value or a With value from
// forging a line.
func TestSanitizing_EscapesForRawHandlers(t *testing.T) {
	var buf bytes.Buffer
	l := Sanitizing(slog.New(rawHandler{buf: &buf})).With("dir", "/data\nWARN forged")
	l.Warn("close /lib/a\nINFO forged: denied", "err", "eof\r\nERROR forged", slog.Group("g", slog.String("v", "x\ny")))
	if got := buf.String(); strings.Count(got, "\n") != 2 || strings.ContainsRune(got, '\r') {
		t.Fatalf("a raw control character got through: %q", got)
	}
	if Sanitizing(nil) != nil {
		t.Error("Sanitizing(nil) != nil")
	}
}
