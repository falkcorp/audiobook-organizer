// file: internal/transcribe/perfile_drain_test.go
// version: 1.0.0
// guid: a01da1c0-24b7-41ef-bbc4-8a0ed7b9025f
// last-edited: 2026-09-01

package transcribe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPerFileDrainsWorkersBeforeReturning pins the contract that
// transcribeRemotePerFile does not return while its workers are still running.
//
// The observable proxy is the in-flight slot. A worker holds its endpoint slot
// for the whole of transcribeOneRemote and releases it only on the way out, so
// if every slot is free the moment the function returns, no worker is still
// mid-request. Returning on the first error instead of draining leaves workers
// running: they keep calling acquireInFlight (reading global config) and
// sending real HTTP, so a dispatch that already reported failure goes on
// generating load, and under -race those goroutines outlive the test and
// collide with the next one's config write.
func TestPerFileDrainsWorkersBeforeReturning(t *testing.T) {
	resetInFlight(t, 0)

	const limit = 4

	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "fail" || r.Header.Get("X-Fail") != "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Everything else parks until the test releases it or the client's
		// context is cancelled, so a worker is demonstrably mid-request at the
		// moment the failing job reports its error.
		select {
		case <-unblock:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	defer close(unblock)

	dir := t.TempDir()
	jobs := map[string]string{}
	for _, id := range []string{"a", "b", "c"} {
		p := filepath.Join(dir, id+".wav")
		if err := os.WriteFile(p, []byte("RIFF....WAVE"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		jobs[id] = p
	}
	// One job whose wav does not exist fails immediately inside
	// transcribeOneRemote, before any HTTP, which is what triggers the early
	// return under the old code.
	jobs["missing"] = filepath.Join(dir, "does-not-exist.wav")

	_, err := transcribeRemotePerFile(context.Background(), srv.URL, limit, jobs, nil)
	if err == nil {
		t.Fatal("expected an error from the missing-file job")
	}

	// Every slot must be free the instant the call returns.
	//
	// Read the pool's channel length rather than trying to acquire: select
	// chooses UNIFORMLY among ready cases, so an "acquire with an already
	// cancelled context" is not a non-blocking probe -- the cancelled branch
	// wins about half the time even when a slot is free. len(pool.ch) is the
	// number of slots currently held, with no timing assumption at all.
	inflightMu.Lock()
	pool := inflightPools[srv.URL]
	inflightMu.Unlock()
	if pool == nil {
		t.Fatal("no in-flight pool was created for the endpoint; the test never exercised the slot path")
	}
	if held := len(pool.ch); held != 0 {
		t.Errorf("%d in-flight slot(s) still held when transcribeRemotePerFile "+
			"returned -- a worker is still mid-request; the function returned "+
			"without draining", held)
	}

	// NOTE: deliberately no assertion on server-side handler state. Go's
	// http.Server does not kill a handler when the client disconnects -- the
	// handler runs until it returns on its own, so its defer can lag the
	// client's return by an arbitrary amount. An earlier version of this test
	// asserted "no handler still executing" and failed in CI against correct
	// code. The slot count above is the actual contract: it is client-side
	// state, owned by the code under test.
}
