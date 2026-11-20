//go:build embed_frontend

// file: core/examples/hello/ui_embed_test.go
// version: 0.1.0
// guid: 0f4b8e62-7c19-4a53-b2d6-9e1a3c5f7d80
// last-edited: 2026-11-20

package hello_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/examples/hello"
	"github.com/falkcorp/mediacore/ui"
)

// TestUIBundle_RealDist runs ui.FromFS over the SPA Vite really built
// (make -C core hello-embed-test builds it first): the three preset metas
// parse, the build ID is a release one, and the CSP ui.Handler serves
// carries the hash of the exact inline boot script in the built
// index.html, so the browser runs it (07 §7.6.2, §7.10).
func TestUIBundle_RealDist(t *testing.T) {
	b := hello.UIBundle()
	if b.IsZero() || b.AppID() != "hello" {
		t.Fatalf("bundle zero=%v app=%q", b.IsZero(), b.AppID())
	}
	if id := b.BuildID(); id == "" || id == "dev" {
		t.Fatalf("BuildID = %q, want the build's own ID", id)
	}
	raw, err := os.ReadFile("web/dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	inline := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(string(raw), -1)
	if len(inline) != 1 {
		t.Fatalf("%d inline scripts in the built index.html, want the one boot script", len(inline))
	}
	sum := sha256.Sum256([]byte(inline[0][1]))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	rec := httptest.NewRecorder()
	ui.Handler(b, ui.Options{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/greeting", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != string(raw) {
		t.Fatalf("GET /greeting = %d, body is not the built index.html", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self' "+hash) {
		t.Fatalf("CSP %q does not allow the boot script %s", csp, hash)
	}
}
