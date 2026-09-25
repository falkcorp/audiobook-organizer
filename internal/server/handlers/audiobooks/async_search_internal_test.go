// file: internal/server/handlers/audiobooks/async_search_internal_test.go
// version: 1.0.0
// guid: 6a1c8e3f-2b7d-4d90-9e45-0f3b6a8c2d17
// last-edited: 2026-09-25

package audiobookshandler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Review finding 18: the 202 path is opt-in per request. Only an explicit
// Prefer: respond-async (the current web client's poller) enables it; a client
// that did not ask — an older bundle, a script — waits for its result.
func TestWantsAsyncSearch(t *testing.T) {
	cases := []struct {
		prefer []string
		want   bool
	}{
		{nil, false},
		{[]string{"return=minimal"}, false},
		{[]string{"respond-async"}, true},
		{[]string{"return=minimal, Respond-Async"}, true},
		{[]string{"wait=5", "respond-async"}, true},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/api/v1/audiobooks?search=x", nil)
		for _, v := range tc.prefer {
			c.Request.Header.Add("Prefer", v)
		}
		if got := wantsAsyncSearch(c); got != tc.want {
			t.Errorf("Prefer %v: wantsAsyncSearch = %v, want %v", tc.prefer, got, tc.want)
		}
	}
}
