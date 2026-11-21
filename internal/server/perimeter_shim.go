// file: internal/server/perimeter_shim.go
// version: 2.0.0
// guid: 0c7f4e2a-9b13-4d58-a6e1-3f8d2c5b7a90
// last-edited: 2026-11-19

package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// The perimeter shim adds HEAD's Alt-Svc to the answers core's router writes
// itself.
//
// The Basic gate used to be gin's, so a request core answered without handing
// it to gin never met the gate, and this shim gated it (R1-15). Since F3-07
// e4 the gate is core's stage 8, around the whole router, and every request
// meets it before any answer is written; the shim gates nothing.
//
// What is left is HEAD's HTTP/3 advertisement. In HEAD the Alt-Svc
// middleware was added to the engine at listen time, so gin applied it to the
// NoRoute and NoMethod chains only: a routed handler's answer carried none,
// the engine's 404 and 405 did. Behind the kernel a request reaches gin only
// when core's router hands it to the legacy mount, and core answers some
// requests itself (05-http-api §4.3, §4.8, §4.9: a problem 404 or 405, a
// legacy-shape 404). The shim adds Alt-Svc to those 404s and 405s, and not to
// the /api -> /api/v1 redirect's 301, which HEAD's redirect middleware wrote
// before Alt-Svc ran.
//
//   - OPTIONS passes untouched: stage 6 answers every OPTIONS.
//   - Any other request goes to the kernel with a perimeterState in its
//     context. perimeterSeen, the engine's first middleware, marks it: when
//     the engine ran, the answer passes untouched (the engine's own late
//     middleware set Alt-Svc where HEAD's did).

// perimeterState records whether the gin engine ran for one request.
type perimeterState struct {
	gin atomic.Bool
}

type perimeterKey struct{}

// perimeterSeen is the gin engine's first middleware: it marks the request's
// perimeterState, so the shim knows the engine ran.
func perimeterSeen() gin.HandlerFunc {
	return func(c *gin.Context) {
		if st, ok := c.Request.Context().Value(perimeterKey{}).(*perimeterState); ok {
			st.gin.Store(true)
		}
		c.Next()
	}
}

// perimeterShim wraps next, the kernel's root handler, with HEAD's Alt-Svc
// for the 404s and 405s core writes itself. altSvc is the Alt-Svc value HEAD
// added at listen time when HTTP/3 is served, or "".
func (s *Server) perimeterShim(next http.Handler, altSvc string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || altSvc == "" {
			next.ServeHTTP(w, r)
			return
		}
		st := &perimeterState{}
		r = r.WithContext(context.WithValue(r.Context(), perimeterKey{}, st))
		next.ServeHTTP(&perimeterWriter{ResponseWriter: w, r: r, st: st, altSvc: altSvc}, r)
	})
}

// perimeterWriter adds Alt-Svc to a 404 or 405 core writes itself, on the
// answer's first WriteHeader or Write. It decides once.
type perimeterWriter struct {
	http.ResponseWriter
	r       *http.Request
	st      *perimeterState
	altSvc  string
	decided bool
}

func (w *perimeterWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	// An answer the engine wrote carries what the engine's own middleware
	// set; HEAD's late Alt-Svc middleware sat in the NoRoute and NoMethod
	// chains after the /api redirect middleware, so core's 404 and 405 carry
	// it and that 301 does not.
	if !w.st.gin.Load() && w.altSvc != "" && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) {
		w.ResponseWriter.Header().Set("Alt-Svc", w.altSvc)
	}
}

func (w *perimeterWriter) WriteHeader(status int) {
	w.decide(status)
	w.ResponseWriter.WriteHeader(status)
}

func (w *perimeterWriter) Write(b []byte) (int, error) {
	w.decide(http.StatusOK)
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher (gin's Flush asserts it on the writer it
// wraps); an event stream flushes through it.
func (w *perimeterWriter) Flush() {
	w.decide(http.StatusOK)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// FlushError is what http.ResponseController looks for first, so a
// handler's flush gets the connection's error through the shim; Flush has no
// result and would drop it.
func (w *perimeterWriter) FlushError() error {
	w.decide(http.StatusOK)
	return http.NewResponseController(w.ResponseWriter).Flush()
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
