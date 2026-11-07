// file: internal/logger/ctx_test.go
// version: 1.0.0
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

	// Below the default handler's level nothing is written.
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	InfoCtx(ctx, "hidden")
	if buf.Len() != 0 {
		t.Errorf("info written at level warn: %q", buf.String())
	}
}
