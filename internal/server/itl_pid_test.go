// file: internal/server/itl_pid_test.go
// version: 1.0.0
// guid: 7c2e9f14-5a8b-4d36-b0c1-9e4f7a2d8b53
// last-edited: 2026-08-14

package server

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestPIDRepairDryRun pins that a dry-run request is honored no matter which
// transport carries it. The body form is the load-bearing case: it used to be
// silently ignored, so a caller asking for a preview got the APPLY path.
func TestPIDRepairDryRun(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name  string
		query string
		body  string
		want  bool
	}{
		{"query true", "?dry_run=true", "", true},
		{"json body true", "", `{"dry_run":true}`, true},
		{"json body false", "", `{"dry_run":false}`, false},
		{"no dry_run anywhere", "", "", false},
		{"malformed body does not force apply-with-preview-intent", "?dry_run=true", `{"dry_run"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/api/v1/itunes/pid-repair"+tc.query,
				bytes.NewBufferString(tc.body))
			if tc.body != "" {
				c.Request.Header.Set("Content-Type", "application/json")
			}
			if got := pidRepairDryRun(c); got != tc.want {
				t.Fatalf("%s: pidRepairDryRun = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
