// file: core/internal/httpmw/writer_chain_test.go
// version: 0.1.0
// guid: f90bb1f1-3b0b-41d4-b715-67e14d03ca02
// last-edited: 2026-11-20

package httpmw

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The realtime hub's stream (core/internal/sse) sets a write deadline before
// each frame, writes, flushes and clears the deadline, all through
// http.NewResponseController. Every writer the chain wraps a response in must
// let those calls reach the connection (Unwrap, Flush, FlushError): a wrapper
// that hides them makes SetWriteDeadline answer http.ErrNotSupported, which
// the stream tolerates silently, so a client that stops reading would hold a
// stream's goroutine until the server's own WriteTimeout and a shutdown could
// no longer interrupt a stuck write (seams.d/F6-02.md). These tests pin the
// chain on a real listener, HTTP/1.1 and HTTP/2, with gzip negotiated so
// stage 9's writer is in the chain too.

// streamPath is the route the stream is mounted at (05-realtime §3).
const streamPath = "/api/core/v1/events"

// opaqueWriter hides the writer below it: no Unwrap, Flush or deadline.
type opaqueWriter struct{ w http.ResponseWriter }

func (o opaqueWriter) Header() http.Header         { return o.w.Header() }
func (o opaqueWriter) Write(b []byte) (int, error) { return o.w.Write(b) }
func (o opaqueWriter) WriteHeader(code int)        { o.w.WriteHeader(code) }

// frameProbe is what the stream handler saw.
type frameProbe struct {
	proto                 int
	hooks                 int  // hookWriter wrappers between the handler and the connection
	gzip                  bool // stage 9's writer was among them
	setDeadline, flush    error
	clearDeadline, expire error // expire: the write after a deadline passed
}

// wrappers counts the chain's writers below w by following Unwrap.
func wrappers(w http.ResponseWriter) (hooks int, gz bool) {
	for {
		switch w.(type) {
		case *hookWriter:
			hooks++
		case *gzipWriter:
			gz = true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return hooks, gz
		}
		w = u.Unwrap()
	}
}

// serveStream mounts a stream-shaped handler behind the full chain (Outer
// and Route, as httpapi mounts a raw core route) and opens one stream with
// Accept-Encoding: gzip. The handler writes a frame under a deadline and
// flushes it, then blocks until the client has read the frame, so the frame
// can only have arrived through the flush. With expire it then lets a short
// deadline pass and writes again, which must fail if the deadline reached
// the connection.
func serveStream(t *testing.T, http2, expire bool, inner func(http.Handler) http.Handler) frameProbe {
	t.Helper()
	c, err := New(Deps{Inner: inner}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	probes := make(chan frameProbe, 1)
	read := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("GET "+streamPath, c.Route(RouteSpec{Pattern: streamPath, Policy: &Policy{Owner: "core"}, Class: "exempt"},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var p frameProbe
			defer func() { probes <- p }()
			p.proto = r.ProtoMajor
			p.hooks, p.gzip = wrappers(w)
			rc := http.NewResponseController(w)
			w.Header().Set("Content-Type", "text/event-stream")
			p.setDeadline = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.WriteString(w, "data: {\"topic\":\"batcave.signal.lit.v1\"}\n\n"); err != nil {
				p.flush = err
				return
			}
			p.flush = rc.Flush()
			p.clearDeadline = rc.SetWriteDeadline(time.Time{})
			select {
			case <-read:
			case <-r.Context().Done():
				return
			}
			if !expire {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
			time.Sleep(100 * time.Millisecond)
			chunk := strings.Repeat("data: {\"topic\":\"batcave.signal.lit.v1\"}\n\n", 4096)
			if _, err := io.WriteString(w, chunk); err != nil {
				p.expire = err
				return
			}
			p.expire = rc.Flush()
		})))
	srv := httptest.NewUnstartedServer(c.Outer(mux))
	srv.EnableHTTP2 = http2
	if http2 {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+streamPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream answered %d", resp.StatusCode)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Errorf("an event stream was sent with Content-Encoding %q", ce)
	}
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if !strings.HasPrefix(l, "data: ") {
			t.Errorf("first line = %q, want the frame", l)
		}
	case <-time.After(10 * time.Second):
		t.Error("the frame did not reach the client while the handler held the stream open: Flush did not reach the connection")
	}
	// The client reads nothing more: the write after the deadline can only
	// fail if the deadline reached the connection (the transports' buffers
	// and HTTP/2 windows would otherwise take it).
	close(read)
	select {
	case p := <-probes:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("the handler did not return")
		return frameProbe{}
	}
}

// TestWriterChain_DeadlineAndFlushReachTheConnection: through every wrapper
// of the chain (four hookWriters: stages 2, 3, 4, 5; stage 9's gzipWriter),
// SetWriteDeadline, Flush and clearing the deadline all reach the
// connection, on HTTP/1.1 and HTTP/2, and an event stream is never gzipped.
func TestWriterChain_DeadlineAndFlushReachTheConnection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		http2 bool
		proto int
	}{{"HTTP/1.1", false, 1}, {"HTTP/2", true, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			p := serveStream(t, tc.http2, false, nil)
			if p.proto != tc.proto {
				t.Fatalf("served over HTTP/%d, want HTTP/%d", p.proto, tc.proto)
			}
			if p.hooks != 4 || !p.gzip {
				t.Errorf("the handler sat under %d hookWriters, gzipWriter %v; want 4 and true (every wrapper of the chain)", p.hooks, p.gzip)
			}
			for name, err := range map[string]error{"SetWriteDeadline": p.setDeadline, "Flush": p.flush, "SetWriteDeadline(zero)": p.clearDeadline} {
				if err != nil {
					t.Errorf("%s through the chain = %v, want nil", name, err)
				}
			}
		})
	}
}

// TestWriterChain_DeadlineIsEnforced: a deadline set through the chain is the
// connection's own: a write after it passes fails, on HTTP/1.1 and HTTP/2. A
// wrapper that answered SetWriteDeadline without passing it down would let
// the write succeed.
func TestWriterChain_DeadlineIsEnforced(t *testing.T) {
	for _, tc := range []struct {
		name  string
		http2 bool
	}{{"HTTP/1.1", false}, {"HTTP/2", true}} {
		t.Run(tc.name, func(t *testing.T) {
			p := serveStream(t, tc.http2, true, nil)
			if p.setDeadline != nil {
				t.Fatalf("SetWriteDeadline = %v", p.setDeadline)
			}
			if p.expire == nil {
				t.Error("a write after the deadline passed succeeded: the deadline did not reach the connection")
			}
		})
	}
}

// TestWriterChain_OpaqueWrapperIsDetected is the control for the two tests
// above: a writer with no Unwrap inside the chain hides the connection, and
// the same probe sees http.ErrNotSupported. If this ever passes with nil, the
// probe no longer tests anything.
func TestWriterChain_OpaqueWrapperIsDetected(t *testing.T) {
	opaque := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(opaqueWriter{w}, r) })
	}
	for _, tc := range []struct {
		name  string
		http2 bool
	}{{"HTTP/1.1", false}, {"HTTP/2", true}} {
		t.Run(tc.name, func(t *testing.T) {
			p := serveStreamNoFlushCheck(t, tc.http2, opaque)
			if !errors.Is(p.setDeadline, http.ErrNotSupported) || !errors.Is(p.flush, http.ErrNotSupported) {
				t.Errorf("behind an opaque writer: SetWriteDeadline = %v, Flush = %v; want http.ErrNotSupported for both", p.setDeadline, p.flush)
			}
		})
	}
}

// serveStreamNoFlushCheck is serveStream for the control: behind an opaque
// writer nothing is flushed until the handler returns, so the handler does
// not wait for the client to read the frame.
func serveStreamNoFlushCheck(t *testing.T, http2 bool, inner func(http.Handler) http.Handler) frameProbe {
	t.Helper()
	c, err := New(Deps{Inner: inner}, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	probes := make(chan frameProbe, 1)
	mux := http.NewServeMux()
	mux.Handle("GET "+streamPath, c.Route(RouteSpec{Pattern: streamPath, Policy: &Policy{Owner: "core"}, Class: "exempt"},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			var p frameProbe
			rc := http.NewResponseController(w)
			w.Header().Set("Content-Type", "text/event-stream")
			p.setDeadline = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, _ = io.WriteString(w, "data: {}\n\n")
			p.flush = rc.Flush()
			probes <- p
		})))
	srv := httptest.NewUnstartedServer(c.Outer(mux))
	srv.EnableHTTP2 = http2
	if http2 {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	defer srv.Close()
	req, err := http.NewRequest(http.MethodGet, srv.URL+streamPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	select {
	case p := <-probes:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("the handler did not run")
		return frameProbe{}
	}
}
