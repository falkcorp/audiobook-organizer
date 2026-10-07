// file: internal/server/handlers/audiobooks/list_singleflight_test.go
// version: 1.1.0
// guid: c27e9a40-5b13-4d8f-9f61-0e4b7d2a3c86
// last-edited: 2026-10-06

package audiobookshandler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
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

// A caller that leaves while the shared build runs gets 499 (not a 500, not
// an empty 200), and with no caller left the build is cancelled.
func TestListAudiobooks_LeavingCallerGets499AndBuildIsCancelled(t *testing.T) {
	h, d := newHandler(t)
	started := make(chan struct{})
	release := make(chan struct{})
	d.rec.listHook = func() {
		close(started)
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	c, w := newCtx("GET", "/audiobooks?limit=50", nil, nil)
	c.Request = c.Request.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ListAudiobooks(c)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler kept waiting for the build after its caller left")
	}
	close(release)
	if w.Code != 499 && c.Writer.Status() != 499 {
		t.Fatalf("status %d / %d, want 499", w.Code, c.Writer.Status())
	}
}

// A *searchcache.PendingError cannot reach the shared path today (it needs
// Prefer: respond-async, which keeps a request off it), but if a build ever
// returns one there it must still be the 202 the unshared path answers, never
// a 500.
func TestListAudiobooks_SharedPathAnswersPendingWith202(t *testing.T) {
	h, d := newHandler(t)
	d.rec.listErr = &searchcache.PendingError{SearchID: "s-1", MatchesSoFar: 3}
	c, w := newCtx("GET", "/audiobooks?limit=50", nil, nil)
	h.ListAudiobooks(c)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202 (body %s)", w.Code, w.Body.String())
	}
}
