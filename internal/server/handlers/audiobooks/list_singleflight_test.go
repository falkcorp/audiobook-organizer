// file: internal/server/handlers/audiobooks/list_singleflight_test.go
// version: 1.0.0
// guid: c27e9a40-5b13-4d8f-9f61-0e4b7d2a3c86
// last-edited: 2026-10-06

package audiobookshandler_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestListAudiobooks_IdenticalMissesShareOneBuild: identical list requests
// that miss the cache together run ONE build and all get its response. After a
// prod restart, 14 identical requests arrived in one second and each built the
// same page for 44-160 s.
func TestListAudiobooks_IdenticalMissesShareOneBuild(t *testing.T) {
	h, d := newHandler(t)
	var builds atomic.Int32
	release := make(chan struct{})
	d.rec.listResp = gin.H{"items": []any{}, "count": 7, "limit": 50, "offset": 0}
	d.rec.listHook = func() {
		builds.Add(1)
		<-release
	}

	const callers = 8
	var wg sync.WaitGroup
	codes := make([]int, callers)
	bodies := make([]map[string]any, callers)
	for i := range callers {
		wg.Go(func() {
			c, w := newCtx("GET", "/audiobooks?limit=50&sort_by=title", nil, nil)
			h.ListAudiobooks(c)
			codes[i] = w.Code
			_ = json.Unmarshal(w.Body.Bytes(), &bodies[i])
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for builds.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let the other callers reach the shared build
	close(release)
	wg.Wait()

	if n := builds.Load(); n != 1 {
		t.Fatalf("%d builds for %d identical concurrent misses, want 1", n, callers)
	}
	for i := range callers {
		if codes[i] != http.StatusOK {
			t.Fatalf("caller %d: status %d", i, codes[i])
		}
		data, _ := bodies[i]["data"].(map[string]any)
		if data == nil {
			data = bodies[i]
		}
		if _, ok := data["applied_filters"]; !ok {
			t.Fatalf("caller %d: response lacks applied_filters: %v", i, bodies[i])
		}
	}
}
