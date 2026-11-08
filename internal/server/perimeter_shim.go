// file: internal/server/perimeter_shim.go
// version: 1.0.3
// guid: 0c7f4e2a-9b13-4d58-a6e1-3f8d2c5b7a90
// last-edited: 2026-11-08

package server

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// The perimeter shim (R1-15) keeps HEAD's perimeter on every response while
// the kernel's router sits in front of the gin engine and before R1-17
// builds core's own perimeter stages.
//
// In HEAD every request entered gin, whose global middleware (security
// headers, CORS with its OPTIONS answer, the Basic gate) ran before any
// route or NoRoute handler. Behind the kernel, a request reaches gin only
// when core's router hands it to the legacy mount; core answers some
// requests itself (05-http-api §4.3, §4.8, §4.9: a problem 404 or 405, a
// legacy-shape 404, the /api -> /api/v1 redirect), and gin's middleware
// never sees those. The shim restores HEAD's answers for them:
//
//   - OPTIONS goes straight to the gin engine, whose CORS middleware answered
//     every OPTIONS in HEAD (204, or 403 for a foreign origin) before routing
//     and before the Basic gate.
//   - Any other request goes to the kernel with a perimeterState in its
//     context. perimeterSeen, the engine's first middleware, marks it. When
//     the response's first byte or status is written and the engine never
//     ran, core wrote it, and the shim applies what gin's chain would have:
//     the Basic gate as for a request gin routed to no route (path
//     exemptions only, BasicGateRefusesUnrouted), then the security and CORS
//     headers, the response compression (compresses: gin-contrib/gzip's
//     handling, R1-15 review m5), and Alt-Svc where HEAD's late NoRoute
//     middleware added it. It also writes gin's access line for such an
//     answer (review m6), as HEAD's logger, the engine's second middleware,
//     did for every request.
//
// R1-17 replaces the shim with core's perimeter stages; the parity test
// (perimeter_shim_test.go) pins what it must keep.

// perimeterState records whether the gin engine ran for one request.
type perimeterState struct{ gin atomic.Bool }

type perimeterKey struct{}

// perimeterSeen is the gin engine's first middleware: it marks the request's
// perimeterState, so the shim knows gin's own perimeter middleware ran.
func perimeterSeen() gin.HandlerFunc {
	return func(c *gin.Context) {
		if st, ok := c.Request.Context().Value(perimeterKey{}).(*perimeterState); ok {
			st.gin.Store(true)
		}
		c.Next()
	}
}

// perimeterShim wraps next, the kernel's root handler, with HEAD's perimeter
// for the responses core writes itself. altSvc is the Alt-Svc value HEAD
// added at listen time when HTTP/3 is served, or "".
//
// accessLog is where the access line of a core-written answer goes: what
// gin's logger writes to, gin.DefaultWriter, read here at construction as
// gin.LoggerWithConfig reads it when the engine is built.
func (s *Server) perimeterShim(next http.Handler, altSvc string) http.Handler {
	engine := s.router
	accessLog := gin.DefaultWriter
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			engine.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		st := &perimeterState{}
		r = r.WithContext(context.WithValue(r.Context(), perimeterKey{}, st))
		pw := &perimeterWriter{ResponseWriter: w, r: r, st: st, altSvc: altSvc, status: http.StatusOK}
		next.ServeHTTP(pw, r)
		pw.finish()
		if !st.gin.Load() {
			logCoreAccess(accessLog, r, pw.status, start)
		}
	})
}

// logCoreAccess writes gin's access line for an answer core wrote itself:
// HEAD's gin.LoggerWithConfig line, through the same redacting formatter, so
// ABS clients' ?token= stays masked. Its SkipPaths are event streams gin
// serves, so none applies here. ClientIP is the peer's address, as gin's is
// with SetTrustedProxies(nil). No colour: the engine's logger colours only a
// terminal, and the service's output is not one.
func logCoreAccess(out io.Writer, r *http.Request, status int, start time.Time) {
	path := r.URL.Path
	if raw := r.URL.RawQuery; raw != "" {
		path += "?" + raw
	}
	ip, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		ip = ""
	}
	_, _ = fmt.Fprint(out, servermiddleware.RedactingLogFormatter(gin.LogFormatterParams{
		Request: r, TimeStamp: time.Now(), StatusCode: status, Latency: time.Since(start),
		ClientIP: ip, Method: r.Method, Path: path,
	}))
}

// perimeterWriter applies the perimeter to a response core writes itself,
// on its first WriteHeader or Write. It decides once. A refused request gets
// the gate's 401 and core's own status, headers and body are dropped.
type perimeterWriter struct {
	http.ResponseWriter
	r       *http.Request
	st      *perimeterState
	altSvc  string
	decided bool
	refused bool
	// status is what the client got: core's status, or the gate's 401.
	status int
	// gz compresses a core-written answer below 400 for a client that
	// accepts gzip; wrote says a body byte went through it.
	gz    *gzip.Writer
	wrote bool
}

func (w *perimeterWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	w.status = status
	if w.st.gin.Load() {
		return
	}
	h := w.ResponseWriter.Header()
	if servermiddleware.BasicGateRefusesUnrouted(w.r) {
		w.status = http.StatusUnauthorized
		// HEAD's refusal carried the security and CORS headers (both ran
		// before the gate) and nothing a later handler would have set.
		w.refused = true
		clear(h)
		setSecurityHeaders(h, w.r)
		setCORSHeaders(h, w.r)
		servermiddleware.WriteBasicChallenge(w.ResponseWriter)
		return
	}
	setSecurityHeaders(h, w.r)
	setCORSHeaders(h, w.r)
	w.compress(status)
	// HEAD's Alt-Svc middleware was added to the engine at listen time, so it
	// sat in the NoRoute chain after the /api redirect middleware: every
	// unrouted answer carried it except that 301.
	if w.altSvc != "" && status != http.StatusMovedPermanently {
		h.Set("Alt-Svc", w.altSvc)
	}
}

// compress applies what HEAD's compressionMiddleware, which ran after the
// Basic gate and before any route or NoRoute handler, did to a response it
// compressed (gin-contrib/gzip v1.2.9). An error status gets no compression
// and loses Vary, Content-Encoding and ETag outright, so a same-origin 404
// carried no Vary: Origin either. Anything below 400 (core's /api redirect)
// is gzipped, with Content-Encoding: gzip and Vary: Accept-Encoding after
// CORS's Vary.
func (w *perimeterWriter) compress(status int) {
	if !compresses(w.r) {
		return
	}
	h := w.ResponseWriter.Header()
	if status >= http.StatusBadRequest {
		h.Del("Content-Encoding")
		h.Del("Vary")
		h.Del("ETag")
		return
	}
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	h.Del("Content-Length")
	w.gz = gzip.NewWriter(w.ResponseWriter)
}

// finish ends a compressed answer once core's handler has returned: the gzip
// footer goes out after a body, and nothing at all when there was none (a
// HEAD request), as gin-contrib/gzip does.
func (w *perimeterWriter) finish() {
	if w.gz == nil {
		return
	}
	if w.wrote {
		_ = w.gz.Close()
	}
	w.gz = nil
}

func (w *perimeterWriter) WriteHeader(status int) {
	w.decide(status)
	if w.refused {
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *perimeterWriter) Write(b []byte) (int, error) {
	w.decide(http.StatusOK)
	if w.refused {
		return len(b), nil
	}
	if w.gz != nil {
		if len(b) > 0 {
			w.wrote = true
		}
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher (gin's Flush asserts it on the writer it
// wraps); an event stream flushes through it.
func (w *perimeterWriter) Flush() {
	w.decide(http.StatusOK)
	if w.refused {
		return
	}
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker for the /socket.io upgrade, which gin
// serves (so the perimeter already ran).
func (w *perimeterWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

// CloseNotify implements http.CloseNotifier: gin's c.Stream (the audio
// sample route) asserts it on the writer without a check. Where the
// underlying writer has none (HTTP/3), the request's context stands in.
func (w *perimeterWriter) CloseNotify() <-chan bool {
	//lint:ignore SA1019 gin's Stream still calls CloseNotify
	if cn, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return cn.CloseNotify()
	}
	ch := make(chan bool, 1)
	go func() {
		<-w.r.Context().Done()
		ch <- true
	}()
	return ch
}

// Unwrap lets http.ResponseController reach the connection's own writer
// (deadlines, full duplex: captureRawResponseController).
func (w *perimeterWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
