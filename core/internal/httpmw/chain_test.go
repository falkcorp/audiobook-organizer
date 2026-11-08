// file: core/internal/httpmw/chain_test.go
// version: 1.0.0
// guid: 6f0b8d2e-5a43-4c71-9e1d-2b7c8a4f3e10
// last-edited: 2026-11-08

package httpmw

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeIfaces is a host whose interfaces a test changes.
type fakeIfaces struct {
	mu  sync.Mutex
	ifs []net.Interface
}

func (f *fakeIfaces) set(ifs ...net.Interface) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ifs = ifs
}

func (f *fakeIfaces) list() ([]net.Interface, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]net.Interface(nil), f.ifs...), nil
}

// clientOf serves one request from peer through c and returns the client
// address stage 0 published.
func clientOf(t *testing.T, c *Chain, peer, header string) (netip.Addr, bool) {
	t.Helper()
	var got netip.Addr
	var attributed bool
	h := c.Outer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, attributed = ClientAddr(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cases", nil)
	req.RemoteAddr = peer
	if header != "" {
		req.Header.Set("Cf-Connecting-Ip", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("peer %s answered %d", peer, rec.Code)
	}
	return got, attributed
}

// TestClientIP_ZonedConnectorTrusted: a connector configured as an IPv6
// link-local address with a zone is trusted only on that interface; a zone
// naming no interface yet is resolved again after the retry interval, so a
// connector whose interface comes up after start becomes trusted without
// a restart; a numeric zone is the interface's index (client_trust.go's
// zone rules, now stage 0's).
func TestClientIP_ZonedConnectorTrusted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &fakeIfaces{}
		c, err := New(Deps{}, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		c.ifaces = host.list
		s := Settings{
			TrustedProxies: []Peer{{Addr: netip.MustParseAddr("fe80::5%cloudflared0")}},
			ClientIPHeader: "CF-Connecting-IP",
		}
		if err := c.Set(s); err == nil || !strings.Contains(err.Error(), "zone names no interface") {
			t.Fatalf("Set with an absent interface = %v, want the pending-zone error", err)
		}
		const visitor = "203.0.113.9"
		if a, attr := clientOf(t, c, "[fe80::5%cloudflared0]:4000", visitor); attr || a != netip.MustParseAddr("fe80::5") {
			t.Fatalf("before the interface exists: client %s attributed %v, want the peer itself", a, attr)
		}
		host.set(net.Interface{Index: 7, Name: "cloudflared0"}, net.Interface{Index: 8, Name: "eth8"})
		time.Sleep(zoneRetryAfter - time.Second)
		if _, attr := clientOf(t, c, "[fe80::5%cloudflared0]:4000", visitor); attr {
			t.Fatal("zones re-resolved before the retry interval")
		}
		time.Sleep(2 * time.Second)
		if a, attr := clientOf(t, c, "[fe80::5%cloudflared0]:4000", visitor); !attr || a != netip.MustParseAddr(visitor) {
			t.Fatalf("after the interface came up: client %s attributed %v, want %s from the header", a, attr, visitor)
		}
		// The same address on another interface is another host.
		if a, attr := clientOf(t, c, "[fe80::5%eth8]:4000", visitor); attr || a != netip.MustParseAddr("fe80::5") {
			t.Fatalf("on eth8: client %s attributed %v, want the peer itself", a, attr)
		}
		// A numeric zone names the interface by index.
		s.TrustedProxies = []Peer{{Addr: netip.MustParseAddr("fe80::5%8")}}
		if err := c.Set(s); err != nil {
			t.Fatal(err)
		}
		if _, attr := clientOf(t, c, "[fe80::5%eth8]:4000", visitor); !attr {
			t.Fatal("zone 8 did not resolve to eth8")
		}
		// A link-local entry without a zone is refused, never matched.
		s.TrustedProxies = []Peer{{Addr: netip.MustParseAddr("fe80::5")}}
		if err := c.Set(s); err == nil || !strings.Contains(err.Error(), "needs a zone") {
			t.Fatalf("zone-less link-local entry = %v, want refused", err)
		}
		// The header is never believed from an untrusted peer, nor a zoned
		// header value from a trusted one.
		s.TrustedProxies = []Peer{{Prefix: netip.MustParsePrefix("192.0.2.0/24")}}
		if err := c.Set(s); err != nil {
			t.Fatal(err)
		}
		if a, attr := clientOf(t, c, "198.51.100.4:1000", visitor); attr || a != netip.MustParseAddr("198.51.100.4") {
			t.Fatalf("untrusted peer: client %s attributed %v", a, attr)
		}
		if a, attr := clientOf(t, c, "192.0.2.9:1000", "fe80::1%eth0"); attr || a != netip.MustParseAddr("192.0.2.9") {
			t.Fatalf("zoned header value: client %s attributed %v", a, attr)
		}
		if a, attr := clientOf(t, c, "192.0.2.9:1000", "::ffff:"+visitor); !attr || a != netip.MustParseAddr(visitor) {
			t.Fatalf("mapped header value: client %s attributed %v, want it unmapped", a, attr)
		}
	})
}

// recordingHandler keeps the warnings a chain logs.
type recordingHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) count(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.HasPrefix(m, prefix) {
			n++
		}
	}
	return n
}

// TestPeerAllowlist_RefusesOutsiders: with an allow-list, loopback, the
// list and the trusted proxies pass; any other peer is 403 peer_refused,
// logged once per peer (an IPv6 peer once per /64).
func TestPeerAllowlist_RefusesOutsiders(t *testing.T) {
	logs := &recordingHandler{}
	c, err := New(Deps{Log: slog.New(logs)}, Settings{
		PeerAllowlist:  []Peer{{Prefix: netip.MustParsePrefix("192.0.2.0/24")}, {Prefix: netip.MustParsePrefix("::ffff:198.51.100.0/120")}},
		TrustedProxies: []Peer{{Addr: netip.MustParseAddr("203.0.113.53")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := c.Outer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Alfred")) }))
	for peer, want := range map[string]int{
		"127.0.0.1:1": 200, "[::1]:1": 200, "192.0.2.40:1": 200, "198.51.100.7:1": 200,
		"[::ffff:198.51.100.8]:1": 200, "203.0.113.53:1": 200,
		"203.0.113.54:1": 403, "[2001:db8:1:2::1]:1": 403, "[2001:db8:1:2::2]:1": 403, "garbage": 403,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = peer
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("peer %s = %d, want %d", peer, rec.Code, want)
		}
		if want == 403 && !strings.Contains(rec.Body.String(), "peer_refused") {
			t.Errorf("peer %s body = %q, want peer_refused", peer, rec.Body.String())
		}
	}
	// 203.0.113.54, one /64 for both 2001:db8:1:2::, and "garbage".
	if n := logs.count("httpapi: refused a connection"); n != 3 {
		t.Fatalf("refusal log lines = %d, want 3", n)
	}
	// An IPv4-mapped prefix shorter than /96 is refused at compile.
	if err := c.Set(Settings{PeerAllowlist: []Peer{{Prefix: netip.MustParsePrefix("::ffff:0:0/80")}}}); err == nil {
		t.Fatal("a mapped /80 was accepted")
	}
}

// TestRecovery_PanicAnswers500OrAborts: a panic before the response
// started is a 500 problem with the handler's representation headers
// dropped; after it started the connection is aborted (ErrAbortHandler),
// since a second status cannot be sent.
func TestRecovery_PanicAnswers500OrAborts(t *testing.T) {
	logs := &recordingHandler{}
	c, err := New(Deps{Log: slog.New(logs)}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	h := c.Outer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename=joker.txt")
		w.Header().Set("Etag", `"harley"`)
		if r.URL.Path == "/after" {
			_, _ = w.Write([]byte("partial"))
		}
		panic("Bane broke the bat")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/before", nil))
	if rec.Code != 500 || rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("Etag") != "" ||
		!strings.Contains(rec.Body.String(), "internal") {
		t.Fatalf("panic before writing = %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	func() {
		defer func() {
			if v := recover(); v != http.ErrAbortHandler {
				t.Fatalf("panic after writing re-panicked with %v, want http.ErrAbortHandler", v)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/after", nil))
	}()
	if n := logs.count("httpapi: panic recovered"); n != 2 {
		t.Fatalf("panic log lines = %d, want 2", n)
	}
}

// TestBodyLimit_DeclaredAndStreamed: stage 11 answers 413 for a declared
// length over the class's limit and caps a streamed body; MaxBody < 0 is
// the legacy mount's no-limit (gin's own groups still limit, until F3).
func TestBodyLimit_DeclaredAndStreamed(t *testing.T) {
	c, err := New(Deps{}, Settings{JSONMaxBytes: 8, UploadMaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	var readErr error
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	serve := func(max int64, ctype string, body string, chunked bool) int {
		h := c.Outer(c.Route(RouteSpec{Pattern: "/x", Class: "exempt", MaxBody: max}, echo))
		var rd io.Reader = strings.NewReader(body)
		if chunked {
			rd = io.MultiReader(bytes.NewBufferString(body)) // no length known
		}
		req := httptest.NewRequest(http.MethodPost, "/x", rd)
		if chunked {
			req.ContentLength = -1
		}
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		readErr = nil
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := serve(0, "application/json", "0123456789", false); got != 413 {
		t.Errorf("JSON over 8 declared = %d, want 413", got)
	}
	if got := serve(0, "multipart/form-data; boundary=x", "0123456789", false); got != 204 {
		t.Errorf("upload under 16 = %d, want 204", got)
	}
	serve(0, "application/json", "0123456789", true)
	var mbe *http.MaxBytesError
	if !errors.As(readErr, &mbe) {
		t.Errorf("streamed JSON over 8: read error %v, want *http.MaxBytesError", readErr)
	}
	if got := serve(-1, "application/json", strings.Repeat("x", 100), false); got != 204 || readErr != nil {
		t.Errorf("MaxBody -1 = %d (%v), want no limit", got, readErr)
	}
}

// TestEncodeULID_Vectors: the Crockford encoding of the extremes.
func TestEncodeULID_Vectors(t *testing.T) {
	var zero, ones [16]byte
	for i := range ones {
		ones[i] = 0xff
	}
	if got := encodeULID(zero); got != "00000000000000000000000000" {
		t.Errorf("zero = %s", got)
	}
	if got := encodeULID(ones); got != "7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Errorf("ones = %s", got)
	}
	c, _ := New(Deps{}, Settings{})
	c.rand = func([]byte) (int, error) { return 0, errors.New("no entropy in the Batcave") }
	if a, b := c.newRequestID(), c.newRequestID(); a != "local-1" || b != "local-2" {
		t.Errorf("fallback IDs = %s, %s", a, b)
	}
}
