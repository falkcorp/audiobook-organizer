// file: internal/server/vector_backend_warn_test.go
// version: 1.0.0
// guid: 8d1c4b6e-2a37-4f95-b0e8-6c3f9a25d7b1
// last-edited: 2026-08-30

package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// captureWarnLogs swaps the default slog handler for one writing into a buffer
// at WARN level and restores it afterwards. Matches the pattern already used by
// internal/server/handlers/abs/browse_unsupported_sort_test.go.
func captureWarnLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestResolveVectorBackend_ChromemIsAudible is the point of the whole change.
//
// chromem is kept as a deliberate fallback (coder/hnsw had a SIGSEGV crash loop
// in June 2026), but until now selecting it produced no error, no unusual log
// line, and a query cost two orders of magnitude higher than HNSW. A fallback
// nobody can tell they are running is a trap, not a fallback. The WARN must
// name the backend and say the cost scales with the corpus.
func TestResolveVectorBackend_ChromemIsAudible(t *testing.T) {
	buf := captureWarnLogs(t)

	got := resolveVectorBackend("chromem")
	if got != "chromem" {
		t.Fatalf("an explicit chromem choice must still be honoured, got %q", got)
	}

	out := buf.String()
	for _, want := range []string{"chromem", "BRUTE-FORCE", "linearly"} {
		if !strings.Contains(out, want) {
			t.Errorf("chromem WARN must mention %q; log was:\n%s", want, out)
		}
	}
}

// TestResolveVectorBackend_HNSWIsSilent is the other half of the instrument: a
// warning that fires for every backend carries no information. Without this the
// chromem test above would pass against an unconditional log line.
func TestResolveVectorBackend_HNSWIsSilent(t *testing.T) {
	buf := captureWarnLogs(t)

	if got := resolveVectorBackend("hnsw"); got != "hnsw" {
		t.Fatalf("resolveVectorBackend(\"hnsw\") = %q, want \"hnsw\"", got)
	}

	if out := buf.String(); strings.TrimSpace(out) != "" {
		t.Errorf("hnsw must not warn; log was:\n%s", out)
	}
}

// TestResolveVectorBackend_EmptyResolvesToHNSW guards the upgrade path at the
// one site that actually picks an implementation. An upgraded install whose
// stored config_blob predates the field carries "" out of migrateEmbeddingBlob,
// and viper.SetDefault never runs on that path — so the pre-fix `== "hnsw"`
// comparison sent every such install to the brute-force scan.
func TestResolveVectorBackend_EmptyResolvesToHNSW(t *testing.T) {
	buf := captureWarnLogs(t)

	if got := resolveVectorBackend(""); got != "hnsw" {
		t.Fatalf("an unset backend must resolve to hnsw, got %q", got)
	}
	if out := buf.String(); strings.TrimSpace(out) != "" {
		t.Errorf("an unset backend is the normal upgrade case and must not warn; log was:\n%s", out)
	}
}

// TestResolveVectorBackend_UnknownWarnsAndUsesDefault covers the typo case.
// Config.Validate() rejects an unknown value outright, but Validate is
// non-fatal on one of its two call sites (cmd/root.go prints a warning and
// continues), so the selection site must still land somewhere sane and say so.
func TestResolveVectorBackend_UnknownWarnsAndUsesDefault(t *testing.T) {
	buf := captureWarnLogs(t)

	if got := resolveVectorBackend("hnws"); got != "hnsw" {
		t.Fatalf("an unknown backend must fall back to the default, got %q", got)
	}

	out := buf.String()
	if !strings.Contains(out, "hnws") {
		t.Errorf("the WARN must echo the configured value so the typo is findable; log was:\n%s", out)
	}
}
