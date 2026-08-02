// file: internal/server/metrics_gzip_test.go
// version: 1.0.0
// guid: 75ace827-acfe-4e59-8a02-2b989251a9cc
// last-edited: 2026-08-02

package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ginzip "github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus scrapes with `Accept-Encoding: gzip` and gunzips the response ONCE.
// promhttp.Handler() already compresses when asked, so any additional compression
// layer over /metrics produces a body that is still gzip after that single
// decompression, and every scrape fails with:
//
//	expected a valid start token, got "\x1f" ("INVALID") while parsing: "\x1f"
//
// which reads like a corrupt exposition format rather than a transport bug. That is
// exactly what production hit on 2026-08-02. These tests pin the transport contract
// rather than the middleware wiring, so they still fail if someone reintroduces
// double compression by another route.

// scrapeLikePrometheus performs the request a Prometheus scrape actually makes and
// returns the body after exactly one gzip decode, as Prometheus would see it.
func scrapeLikePrometheus(t *testing.T, r http.Handler, path string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d, want 200", path, rec.Code)
	}
	body := rec.Body.Bytes()
	if strings.EqualFold(rec.Header().Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("Content-Encoding says gzip but the body is not gzip: %v", err)
		}
		defer zr.Close()
		decoded, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("read gzip body: %v", err)
		}
		return decoded
	}
	return body
}

// isGzip reports whether b starts with the gzip magic number (0x1f 0x8b).
func isGzip(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

// metricsRouter builds a router wired exactly as setupRoutes wires the real one:
// the same global gzip middleware with the same exclusions, and promhttp on /metrics.
func metricsRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ginzip.Gzip(ginzip.DefaultCompression, ginzip.WithExcludedPaths([]string{"/api/events", "/metrics"})))
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	// A representative ordinary endpoint, to prove the exclusion is narrow.
	r.GET("/api/v1/books", func(c *gin.Context) {
		c.String(http.StatusOK, strings.Repeat("compress me. ", 200))
	})
	return r
}

// 🔴 TestMetrics_NotDoubleCompressed is the production regression: after ONE gzip
// decode the body must be parseable text, never more gzip.
func TestMetrics_NotDoubleCompressed(t *testing.T) {
	body := scrapeLikePrometheus(t, metricsRouter(), "/metrics")

	if isGzip(body) {
		t.Fatal("after one gzip decode the body is STILL gzip — /metrics is double-compressed, " +
			"and every Prometheus scrape fails with `got \"\\x1f\" (\"INVALID\")`")
	}
	// The Prometheus text format is line-oriented and starts with a comment or a
	// metric name; binary garbage here means the body is mangled some other way.
	if len(body) > 0 && !bytes.ContainsAny(body[:min(len(body), 200)], "#_abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("body does not look like Prometheus exposition text: %q", body[:min(len(body), 120)])
	}
}

// TestMetrics_ParsesAsExpositionFormat goes one step further than "not gzip": the
// decoded body must actually contain a HELP/TYPE header or a metric line, which is
// what the scrape parser requires.
func TestMetrics_ParsesAsExpositionFormat(t *testing.T) {
	body := string(scrapeLikePrometheus(t, metricsRouter(), "/metrics"))
	if !strings.Contains(body, "# HELP") && !strings.Contains(body, "# TYPE") && !strings.Contains(body, "go_") {
		t.Fatalf("no exposition-format content in the scraped body:\n%.300s", body)
	}
}

// TestGzipStillAppliesToOrdinaryEndpoints keeps the exclusion honest: it must cover
// /metrics only, not quietly disable compression for the whole API.
func TestGzipStillAppliesToOrdinaryEndpoints(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/books", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	metricsRouter().ServeHTTP(rec, req)

	if enc := rec.Header().Get("Content-Encoding"); !strings.EqualFold(enc, "gzip") {
		t.Fatalf("Content-Encoding = %q, want gzip — the exclusion is too broad", enc)
	}
	if !isGzip(rec.Body.Bytes()) {
		t.Fatal("ordinary endpoint was not compressed — the exclusion is too broad")
	}
}

// TestMetrics_UncompressedClientStillWorks: a client that does not ask for gzip must
// get plain text, not a gzip body it cannot read.
func TestMetrics_UncompressedClientStillWorks(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept-Encoding", "identity")
	rec := httptest.NewRecorder()
	metricsRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if isGzip(rec.Body.Bytes()) {
		t.Fatal("served gzip to a client that asked for identity encoding")
	}
}
