// file: core/kernel/perimeter_test.go
// version: 1.0.1
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
	"github.com/falkcorp/mediacore/rbac"
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

// TestPerimeter_RateLimitSwitchEndToEnd: R1Values' rate-limit switch
// reaches stage 10 through the kernel, on a RateDefault route of a legacy
// mount: on at 1 a minute, the request after the burst (10, the floor) is
// 429; off, no request is.
func TestPerimeter_RateLimitSwitchEndToEnd(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			rec := &recorder{}
			gcpd := mod("gcpd", func(r *module.Registrar) error {
				r.MountLegacy(httpapi.LegacyMount{
					Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Gordon")) }),
					Auth:    httpapi.LegacyAuthGin,
					Routes: func(yield func(httpapi.LegacyRoute) bool) {
						yield(httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/cases/:id", Perm: rbac.AnyAuthenticated, RateClass: httpapi.RateDefault})
					},
				})
				return nil
			}).with(func(m *module.Manifest) { m.LegacyPrefixes = []string{"/api/v1/"} })
			c := supervised(t, rec, gcpd)
			c.LegacyAuthGinUntil = "gcpd"
			c.R1Values = func() R1Values { return R1Values{RateLimitEnabled: enabled, RateLimitPerMinute: 1} }
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
			limited := 0
			for range 11 {
				req := httptest.NewRequest(http.MethodGet, "/api/v1/cases/42", nil)
				req.RemoteAddr = "192.0.2.4:1000"
				w := httptest.NewRecorder()
				(*hp).ServeHTTP(w, req)
				if w.Code == http.StatusTooManyRequests {
					limited++
				} else if w.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 or 429", w.Code)
				}
			}
			if want := map[bool]int{true: 1, false: 0}[enabled]; limited != want {
				t.Errorf("enabled=%v: %d of 11 requests limited, want %d", enabled, limited, want)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run = %v", err)
			}
		})
	}
}
