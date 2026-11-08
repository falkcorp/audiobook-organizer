// file: core/kernel/perimeter_test.go
// version: 1.0.0
// guid: 0d6a3f1c-8b2e-4f5a-9c7d-3e1b2a4c6f80
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
)

// TestPerimeterHonoursBlobOnlyValues: a perimeter value the app holds only
// in its saved configuration, which it loads in a module's Start hook (ABO
// reads its config blob there), is the value stages 0, 10 and 11 enforce
// from the first request the listener serves: the kernel reads R1Values
// after the hooks and hands the values to the router before the listener
// opens (05-config §1.12.1, 06 §6.5.2 start step 7).
func TestPerimeterHonoursBlobOnlyValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		var blob atomic.Pointer[R1Values] // filled only by the Start hook
		loader := mod("oracle", func(r *module.Registrar) error {
			r.Hook(module.Hook{Name: "oracle.load-config", Start: func(context.Context) error {
				blob.Store(&R1Values{PeerAllowlist: []string{"192.0.2.0/24"}})
				return nil
			}})
			return nil
		})
		c := supervised(t, rec, loader)
		c.R1Values = func() R1Values {
			v := blob.Load()
			if v == nil {
				t.Error("R1Values read before the config was loaded")
				return R1Values{}
			}
			return *v
		}
		var served atomic.Pointer[http.Handler]
		c.Listen = func(_ httpapi.ListenOptions, h http.Handler, _ *slog.Logger) (Server, error) {
			served.Store(&h)
			return &fakeServer{rec: rec, stopped: make(chan struct{})}, nil
		}
		k := mustNew(t, c)
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()
		hp := served.Load()
		if hp == nil {
			t.Fatal("the listener never opened")
		}
		h := *hp
		get := func(peer string) int {
			req := httptest.NewRequest(http.MethodGet, "/api/batcave/v1/gadgets", nil)
			req.RemoteAddr = peer
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			return w.Code
		}
		if got := get("198.51.100.4:1000"); got != http.StatusForbidden {
			t.Errorf("peer outside the saved allow-list = %d, want 403", got)
		}
		if got := get("192.0.2.4:1000"); got != http.StatusNotFound {
			t.Errorf("peer inside the saved allow-list = %d, want the router's 404", got)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
	})
}

// TestPerimeter_BadAllowlistEntryFailsBoot: an allow-list entry that is
// neither a prefix nor an address stops Run before the listener opens, as
// HEAD's ALLOWED_PEER_CIDRS check did; a bad trusted-proxy entry is logged
// and left out.
func TestPerimeter_BadAllowlistEntryFailsBoot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		c := supervised(t, rec)
		c.R1Values = func() R1Values { return R1Values{PeerAllowlist: []string{"192.0.2.0/24", "arkham-asylum"}} }
		k := mustNew(t, c)
		err := <-run(t.Context(), k)
		if err == nil || !strings.Contains(err.Error(), "core.http.peer_allowlist") || !strings.Contains(err.Error(), "arkham-asylum") {
			t.Fatalf("Run = %v, want the allow-list error", err)
		}
		if len(rec.matching("listen:")) != 0 {
			t.Fatalf("the listener opened despite the bad entry: %v", rec.list())
		}
	})
}
