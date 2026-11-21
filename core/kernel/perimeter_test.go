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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// TestPerimeterHonoursConfiguredValues: a perimeter value core.http holds is
// the value stages 0, 10 and 11 enforce from the first request the listener
// serves: the kernel reads core.http from the layered config after the hooks
// and hands the values to the router before the listener opens (06 §6.5.2
// start step 7). The allow-list here arrives as a flag layer, as an
// operator's environment would carry it.
func TestPerimeterHonoursConfiguredValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		c := supervised(t, rec)
		withCore(&c, "core.http.peer_allowlist", `["192.0.2.0/24"]`)
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
			t.Errorf("peer outside the allow-list = %d, want 403", got)
		}
		if got := get("192.0.2.4:1000"); got != http.StatusNotFound {
			t.Errorf("peer inside the allow-list = %d, want the router's 404", got)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
	})
}

// TestPerimeter_BadValuesAreRefusedWhenConfigLoads: an allow-list entry that is
// neither a prefix nor an address, a listen address that is not host:port and a
// TLS pair with one file missing each stop the boot at the config load, naming
// the key and not the value (core.http holds a secret, so config replaces a
// validator's own text with a fixed one), before any hook runs or listener
// opens, as HEAD's ALLOWED_PEER_CIDRS check refused before it served.
func TestPerimeter_BadValuesAreRefusedWhenConfigLoads(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"allow-list", "core.http.peer_allowlist", `["192.0.2.0/24","arkham-asylum"]`},
		{"listen", "core.http.listen", "arkham-asylum"},
		{"tls pair", "core.http.tls.cert_file", "/etc/batcave.crt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			c := supervised(t, rec, hooked("alfred", rec))
			withCore(&c, tc.key, tc.value)
			_, err := New(t.Context(), c)
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("New = %v, want a refusal naming %s", err, tc.key)
			}
			if strings.Contains(err.Error(), "arkham-asylum") {
				t.Fatalf("the refusal prints the value: %v", err)
			}
			if len(rec.matching("start:")) != 0 || len(rec.matching("listen:")) != 0 {
				t.Fatalf("a hook started or the listener opened despite the bad value: %v", rec.list())
			}
		})
	}
}

// TestPerimeter_RateLimitSwitchEndToEnd: core.http.rate_limit's switch
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
			withCore(&c, "core.http.rate_limit.enabled", strconv.FormatBool(enabled), "core.http.rate_limit.per_minute", "1")
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
