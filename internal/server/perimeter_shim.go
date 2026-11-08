// file: internal/server/perimeter_shim.go
// version: 1.0.0
// guid: 0c7f4e2a-9b13-4d58-a6e1-3f8d2c5b7a90
// last-edited: 2026-11-07

package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sync/atomic"

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
//     headers, and Alt-Svc where HEAD's late NoRoute middleware added it.
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
func (s *Server) perimeterShim(next http.Handler, altSvc string) http.Handler {
	engine := s.router
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			engine.ServeHTTP(w, r)
			return
		}
		st := &perimeterState{}
		r = r.WithContext(context.WithValue(r.Context(), perimeterKey{}, st))
		next.ServeHTTP(&perimeterWriter{ResponseWriter: w, r: r, st: st, altSvc: altSvc}, r)
	})
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
}

func (w *perimeterWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	if w.st.gin.Load() {
		return
	}
	h := w.ResponseWriter.Header()
	if servermiddleware.BasicGateRefusesUnrouted(w.r) {
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
	// HEAD's Alt-Svc middleware was added to the engine at listen time, so it
	// sat in the NoRoute chain after the /api redirect middleware: every
	// unrouted answer carried it except that 301.
	if w.altSvc != "" && status != http.StatusMovedPermanently {
		h.Set("Alt-Svc", w.altSvc)
	}
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
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher (gin's Flush asserts it on the writer it
// wraps); an event stream flushes through it.
func (w *perimeterWriter) Flush() {
	w.decide(http.StatusOK)
	if w.refused {
		return
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
	//nolint:staticcheck // SA1019: gin's Stream still calls CloseNotify.
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
