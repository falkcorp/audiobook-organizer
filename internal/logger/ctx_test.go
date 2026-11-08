// file: internal/logger/ctx_test.go
// version: 1.1.0
// guid: f985b834-583b-49a5-95a2-df71a0e16724
// last-edited: 2026-11-08

package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	corelog "github.com/falkcorp/mediacore/log"
)

// pipelineDefault makes a real core pipeline over buf the slog default, as
// app.InstallProcessDefaults does in serve, and puts the old default back.
// Not parallel-safe: it swaps slog.Default().
func pipelineDefault(t *testing.T, buf *bytes.Buffer, format corelog.Format, level slog.Level) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	l, err := corelog.New(corelog.Options{
		Output:    buf,
		Format:    format,
		Level:     level,
		AddSource: true,
		Redactor:  corelog.NewRedactor(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	slog.SetDefault(l.Root())
}

// TestCtxHelpers_AppendAttrsFrom: each helper writes one record through
// slog.Default(), the App's pipeline, that carries the context's attributes
// exactly once (the pipeline's handler appends them, so the helpers must
// not), with control characters in the message and string values escaped by
// sanitizeLogLine. Not parallel: it swaps slog.Default().
func TestCtxHelpers_AppendAttrsFrom(t *testing.T) {
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
			pipelineDefault(t, &buf, corelog.FormatJSON, slog.LevelDebug)
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
				// A context attribute is the handler's to quote: the JSON encoder
				// escaped the newline on the wire (one record, checked above).
				"who":   "pen\nguin",
				"count": float64(3),
			}
			for k, v := range want {
				if m[k] != v {
					t.Errorf("%s = %#v, want %#v", k, m[k], v)
				}
			}
			// json.Unmarshal keeps the last of a repeated key, so a doubled
			// attribute is only visible in the raw line.
			for _, k := range []string{"k", "who", "title", "count"} {
				if n := strings.Count(out, `"`+k+`":`); n != 1 {
					t.Errorf("attribute %q appears %d times, want once: %s", k, n, out)
				}
			}
			src, _ := m["source"].(map[string]any)
			if file, _ := src["file"].(string); !strings.HasSuffix(file, "ctx_test.go") {
				t.Errorf("source = %v, want the helper's caller", src)
			}
		})
	}

	// The same through the text format: the operation attribute is written
	// once, and a value that spells another opID stays inside its quotes, so
	// internal/activity's exact-key parser cannot read it as the row's op.
	var ordered bytes.Buffer
	pipelineDefault(t, &ordered, corelog.FormatText, slog.LevelInfo)
	opCtx := corelog.WithAttrs(context.Background(), slog.String("opID", "op-real"))
	InfoCtx(opCtx, "scan", "path", "/lib/x opID=op-forged")
	line := ordered.String()
	if n := strings.Count(line, " opID=op-real"); n != 1 {
		t.Errorf("opID=op-real written %d times, want once: %q", n, line)
	}
	if !strings.Contains(line, `path="/lib/x opID=op-forged"`) {
		t.Errorf("the forged opID is not quoted inside its value: %q", line)
	}

	// Below the pipeline's level nothing is written.
	var buf bytes.Buffer
	pipelineDefault(t, &buf, corelog.FormatText, slog.LevelWarn)
	InfoCtx(ctx, "hidden")
	if buf.Len() != 0 {
		t.Errorf("info written at level warn: %q", buf.String())
	}
}

func TestSanitizeLogErr(t *testing.T) {
	if got := SanitizeLogErr(nil); got != "" {
		t.Errorf("nil error = %q, want empty", got)
	}
	if got, want := SanitizeLogErr(errors.New("open /lib/a\nINFO forged\r\x1b")), `open /lib/a\nINFO forged\r\x1b`; got != want {
		t.Errorf("SanitizeLogErr = %q, want %q", got, want)
	}
}
