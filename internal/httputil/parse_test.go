// file: internal/httputil/parse_test.go
// version: 1.0.0
// guid: 5e2a7c91-8b04-4d6f-a1c3-9f0e2d4b6a18
// last-edited: 2026-06-28

package httputil

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func paginationCtx(t *testing.T, query string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?"+query, nil)
	return c
}

func TestParsePaginationParams_Cap(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantLimit int
		wantOff   int
	}{
		{"default", "", 50, 0},
		{"explicit 500 honored", "limit=500", 500, 0},
		{"explicit 1000 honored", "limit=1000", 1000, 0},
		{"above cap clamped to 1000", "limit=5000", 1000, 0},
		{"below 1 falls to default", "limit=0", 50, 0},
		{"negative offset zeroed", "limit=20&offset=-5", 20, 0},
		{"offset preserved", "limit=20&offset=40", 20, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ParsePaginationParams(paginationCtx(t, tc.query))
			if p.Limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", p.Limit, tc.wantLimit)
			}
			if p.Offset != tc.wantOff {
				t.Errorf("offset = %d, want %d", p.Offset, tc.wantOff)
			}
		})
	}
}
