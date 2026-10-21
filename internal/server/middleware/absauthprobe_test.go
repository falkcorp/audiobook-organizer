// file: internal/server/middleware/absauthprobe_test.go
// version: 1.0.0
// guid: 94c7e0b3-6a1d-4f82-b5c9-2e8d1a7f3b60
// last-edited: 2026-10-20

package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// cf_cookie_len rolls up every CF_-prefixed cookie, cf_clearance included, so it
// cannot say whether the ACCESS cookie rode along. cf_authorization_len must count
// CF_Authorization alone.
func TestABSAuthProbe_LogsCFAuthorizationLengthSeparately(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ABSAuthProbe())
	router.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	tests := []struct {
		name    string
		cookies []*http.Cookie
		want    []string
	}{
		{"clearance only", []*http.Cookie{{Name: "cf_clearance", Value: strings.Repeat("c", 40)}},
			[]string{"cf_cookie_len=40", "cf_authorization=false", "cf_authorization_len=0"}},
		{"authorization plus clearance", []*http.Cookie{
			{Name: "cf_clearance", Value: strings.Repeat("c", 40)},
			{Name: "CF_Authorization", Value: strings.Repeat("a", 7)}},
			[]string{"cf_cookie_len=47", "cf_authorization=true", "cf_authorization_len=7"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			req := httptest.NewRequest(http.MethodGet, "/ping", nil)
			for _, ck := range tc.cookies {
				req.AddCookie(ck)
			}
			router.ServeHTTP(httptest.NewRecorder(), req)
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("probe line lacks %q:\n%s", w, out)
				}
			}
		})
	}
}
