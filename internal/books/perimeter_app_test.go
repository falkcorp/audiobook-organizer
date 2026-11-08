// file: internal/books/perimeter_app_test.go
// version: 1.0.2
// guid: d50ddd31-9468-4c34-9994-a64e31f28a71
// last-edited: 2026-11-08

package books

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	r := runComposed(t)
	c, addr := r.composed, r.addr

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
				assert.Equal(t, readableBody(hw.Result().Header, hw.Body.Bytes()), readableBody(resp.Header, body), "body")
			}
			if !maps.EqualFunc(gh, kh, slices.Equal) {
				t.Errorf("headers differ\nHEAD: %v\nApp:  %v", gh, kh)
			}
		})
	}
}

// readableBody is a response body as the parity check compares and prints
// it (internal/server's perimeter_shim_test.go has the same helper; the two
// test packages cannot share it). A gzip-encoded body is decompressed, so the
// check compares what a client reads (Content-Encoding and Vary are compared
// with the headers) and a failure shows the text that differs, not two dumps
// of compressed bytes. A body that is not a readable gzip stream, or is not
// printable text, is shown as its length and SHA-256.
func readableBody(h http.Header, b []byte) string {
	opaque := func(what string, b []byte) string {
		return fmt.Sprintf("<%s: %d bytes, sha256 %x>", what, len(b), sha256.Sum256(b))
	}
	prefix := ""
	if h.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return opaque("unreadable gzip ("+err.Error()+")", b)
		}
		plain, err := io.ReadAll(zr)
		if err != nil {
			return opaque("unreadable gzip ("+err.Error()+")", b)
		}
		b, prefix = plain, "gzip: "
	}
	if !utf8.Valid(b) || strings.ContainsFunc(string(b), func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) {
		return opaque(prefix+"binary", b)
	}
	return prefix + string(b)
}

// TestStop_EventStreamsEndBeforeTheDrain (R1-15 review m7): an open event
// stream holds its connection busy, so the listener's graceful drain would
// wait on it until core.shutdown.http_drain (10s) ran out. The stop ends the
// streams first (R1Listener.Shutdown's pre-drain step: endStreamsOnShutdown
// for /api/events and the v2 operations stream, closeOpsV3Streams for v3).
// With the App running as serve runs it, each open stream must reach EOF
// well inside the drain once Run is cancelled.
func TestStop_EventStreamsEndBeforeTheDrain(t *testing.T) {
	r := runComposed(t)
	const drain = 10 * time.Second // core.shutdown.http_drain's default
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)

	type ended struct {
		path string
		at   time.Time
		err  error
	}
	paths := []string{"/api/events", "/api/v1/operations/events", "/api/v3/ops/events"}
	done := make(chan ended, len(paths))
	for _, p := range paths {
		ctx, cancel := context.WithTimeout(context.Background(), 2*drain)
		t.Cleanup(cancel)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+r.addr.String()+p, nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := client.Do(req)
		require.NoError(t, err, "open %s", p)
		require.Equal(t, http.StatusOK, resp.StatusCode, "open %s", p)
		go func() {
			_, err := io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			done <- ended{path: p, at: time.Now(), err: err}
		}()
	}
	// The streams are open and idle: none may have ended yet.
	select {
	case e := <-done:
		t.Fatalf("%s ended before the stop: %v", e.path, e.err)
	case <-time.After(200 * time.Millisecond):
	}

	stopAt := time.Now()
	go func() { _ = r.stop() }()
	for range paths {
		select {
		case e := <-done:
			assert.NoError(t, e.err, "%s ended with an error rather than its end", e.path)
			if took := e.at.Sub(stopAt); took >= drain/2 {
				t.Errorf("%s ended %s after the stop began: the drain waited on it", e.path, took)
			}
		case <-time.After(2 * drain):
			t.Fatal("an event stream never ended")
		}
	}
	require.NoError(t, r.stop())
}
