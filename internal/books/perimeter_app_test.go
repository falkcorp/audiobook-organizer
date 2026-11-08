// file: internal/books/perimeter_app_test.go
// version: 1.0.0
// guid: d50ddd31-9468-4c34-9994-a64e31f28a71
// last-edited: 2026-11-08

package books

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/httpapi"
)

// TestPerimeter_ThroughTheRunningApp (R1-15 review m5): internal/server's
// parity table runs the perimeter shim over a router the test builds. This
// runs cases from it through what serve listens with: the App running, its
// listener opened by books' R1Listen, so the shim wraps app.Handler(), core's
// router with its stages and books' legacy mount. Each answer is compared
// with HEAD's, the gin engine on its own. The cases are the kinds core
// answers itself (the /api redirect, the legacy-shape 404, core's group)
// with gzip and a same origin, the case review m5 found different, plus a
// routed one.
func TestPerimeter_ThroughTheRunningApp(t *testing.T) {
	addrs := make(chan net.Addr, 1)
	c := bootComposedWith(t, false, nil, nil, func(o *app.Options) {
		listen := o.R1Listen
		o.R1Listen = func(lo httpapi.ListenOptions, h http.Handler, l *slog.Logger) (app.R1Server, error) {
			srv, err := listen(lo, h, l)
			if err == nil {
				addrs <- srv.Addr()
			}
			return srv, err
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- c.app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-ran:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(60 * time.Second):
			t.Error("Run did not return after its context was cancelled")
		}
	})
	var addr net.Addr
	select {
	case addr = <-addrs:
	case err := <-ran:
		t.Fatalf("Run returned before the listener opened: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the App never opened its listener")
	}

	client := &http.Client{
		// The client must not ask for or undo the compression itself.
		Transport:     &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	t.Cleanup(client.CloseIdleConnections)
	gzipSame := map[string]string{"Accept-Encoding": "gzip", "Origin": "https://gotham.example"}
	for _, tc := range []struct {
		name, path string
		header     map[string]string
		coreBody   bool // core's own problem body: compare status and headers only
	}{
		{name: "/api redirect, gzip, same origin", path: "/api/audiobooks?batman=1", header: gzipSame},
		{name: "/api redirect", path: "/api/audiobooks"},
		{name: "legacy 404, gzip, same origin", path: "/api/v1/riddler-unrouted", header: gzipSame},
		{name: "core group, gzip, same origin", path: "/api/core/v1/riddler", header: gzipSame, coreBody: true},
		{name: "routed, gzip, same origin", path: "/api/v1/health", header: gzipSame},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hreq := httptest.NewRequest(http.MethodGet, tc.path, nil)
			hreq.Host = "gotham.example"
			hreq.RemoteAddr = "127.0.0.1:41000"
			req, err := http.NewRequest(http.MethodGet, "http://"+addr.String()+tc.path, nil)
			require.NoError(t, err)
			req.Host = "gotham.example"
			for k, v := range tc.header {
				hreq.Header.Set(k, v)
				req.Header.Set(k, v)
			}
			hw := httptest.NewRecorder()
			c.srv.LegacyHandler().ServeHTTP(hw, hreq)
			resp, err := client.Do(req)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, resp.Body.Close())
			require.NoError(t, err)

			assert.Equal(t, hw.Code, resp.StatusCode, "status")
			gh, kh := hw.Result().Header.Clone(), resp.Header.Clone()
			for _, v := range []string{"Date", "X-Request-Id", "Content-Length"} {
				gh.Del(v)
				kh.Del(v)
			}
			if tc.coreBody {
				gh.Del("Content-Type")
				kh.Del("Content-Type")
			} else {
				assert.Equal(t, hw.Body.Bytes(), body, "body")
			}
			if !maps.EqualFunc(gh, kh, slices.Equal) {
				t.Errorf("headers differ\nHEAD: %v\nApp:  %v", gh, kh)
			}
		})
	}
}
