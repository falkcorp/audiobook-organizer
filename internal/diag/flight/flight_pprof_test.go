// file: internal/diag/flight/flight_pprof_test.go
// version: 1.0.0
// guid: 293bf75e-c047-4e1c-80e3-e74ee8c9328c
// last-edited: 2026-10-14

//go:build pprof

package flight

import (
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"testing"
)

// The recorder runs always; the pprof build (06-P3) also serves
// /debug/pprof/trace, which calls trace.Start. Both must work at once. 06-P3's
// mux is not on main yet, so this drives the standard library handler that
// mux mounts. A failure here is a finding about runtime/trace, not a flake.
func TestRecorderCoexistsWithPprofTrace(t *testing.T) {
	r, _ := startRecorder(t, Config{})

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/trace?seconds=1", nil)
	w := httptest.NewRecorder()
	pprof.Trace(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/debug/pprof/trace with the recorder running: %d %s", w.Code, w.Body.String())
	}
	if w.Body.Len() == 0 {
		t.Fatal("/debug/pprof/trace returned an empty trace")
	}

	r.Snapshot("pprof-coexist")
	r.inflight.Wait()
	if got := len(traceFiles(t, r.dir)); got != 1 {
		t.Fatalf("snapshot after a pprof trace: want 1 file, got %d", got)
	}
	if !r.fr.Enabled() {
		t.Fatal("the pprof trace stopped the flight recorder")
	}
}
