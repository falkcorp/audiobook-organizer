// file: internal/server/perimeter_shim.go
// version: 1.2.3
// guid: 0c7f4e2a-9b13-4d58-a6e1-3f8d2c5b7a90
// last-edited: 2026-11-08

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

// The perimeter shim keeps HEAD's Basic gate on the answers core's router
// writes itself, until F3 moves the gate into core's stage 8.
//
// R1-15 added the shim to give those answers HEAD's whole perimeter; since
// R1-17 core's stages 0-11 run that perimeter on every request (security
// headers, CORS with its OPTIONS answer, compression), and only the Basic
// gate is still gin's (05-http-api §4.2, §8.1 R1 row). In HEAD every request
// entered gin, whose BasicAuth middleware ran before any route or NoRoute
// handler. Behind the kernel a request reaches gin only when core's router
// hands it to the legacy mount; core answers some requests itself (05-http-api
// §4.3, §4.8, §4.9: a problem 404 or 405, a legacy-shape 404, the /api ->
// /api/v1 redirect), and gin's gate never sees those. The shim gates them:
//
//   - OPTIONS passes untouched: stage 6 answers every OPTIONS, as HEAD's CORS
//     middleware did before the gate.
//   - Any other request goes to the kernel with a perimeterState in its
//     context. perimeterSeen, the engine's first middleware, marks it. When
//     the engine never ran, every answer core wrote itself is gated (fail
//     closed: a native route added later cannot skip the gate) as for a
//     request gin routed to no route (path exemptions only,
//     BasicGateRefusesUnrouted). A refusal keeps the headers core's stages
//     set (HEAD's 401 carried the security and CORS headers, which ran
//     before the gate), drops what describes core's body, and writes the
//     gate's 401. The shim also adds Alt-Svc to core's 404 and 405, where
//     HEAD's late NoRoute middleware did.
//
// The stages' own refusals are not gated (stageRefusal): stage 0's 403 and
// its 425 for 0-RTT early data, stage 7's 403, stage 10's 429, stage 11's
// 413 and stage 3's 500. In HEAD the peer gate ran outside gin, stage 7 did
// not exist, and the rate and body limits now run before gin (stage 8 sits
// inside the legacy mount until F3).
//
// Core's access log and HTTP metrics record the status core wrote, not the
// gate's 401, for a refused unrouted request: they run inside the kernel's
// handler, outside which the shim sits. F3 removes the shim with the gate.

// perimeterState records whether the gin engine ran for one request.
type perimeterState struct{ gin atomic.Bool }

type perimeterKey struct{}

// perimeterSeen is the gin engine's first middleware: it marks the request's
// perimeterState, so the shim knows gin's own Basic gate ran.
func perimeterSeen() gin.HandlerFunc {
	return func(c *gin.Context) {
		if st, ok := c.Request.Context().Value(perimeterKey{}).(*perimeterState); ok {
			st.gin.Store(true)
		}
		c.Next()
	}
}

// perimeterShim wraps next, the kernel's root handler, with HEAD's Basic
// gate for the unrouted answers core writes itself. altSvc is the Alt-Svc
// value HEAD added at listen time when HTTP/3 is served, or "".
func (s *Server) perimeterShim(next http.Handler, altSvc string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		st := &perimeterState{}
		r = r.WithContext(context.WithValue(r.Context(), perimeterKey{}, st))
		next.ServeHTTP(&perimeterWriter{ResponseWriter: w, r: r, st: st, altSvc: altSvc}, r)
	})
}

// perimeterWriter applies the gate to a response core writes itself, on its
// first WriteHeader or Write. It decides once. A refused request gets the
// gate's 401 and core's own status and body are dropped.
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
	if w.st.gin.Load() || stageRefusal(status) {
		return
	}
	h := w.ResponseWriter.Header()
	if servermiddleware.BasicGateRefusesUnrouted(w.r) {
		w.refused = true
		// What core's answer set about its own body goes.
		for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Location", "Cache-Control"} {
			h.Del(k)
		}
		// HEAD's gate ran after its CORS middleware and before its gzip
		// middleware, so its 401 carried exactly CORS's Vary: Origin, set
		// with Access-Control-Allow-Origin. Stage 9 has already run on
		// core's answer: it added Vary: Accept-Encoding to a 301, and
		// deleted every Vary value, Origin included, from a status of 400
		// or more (gin-contrib/gzip's rule). So the refusal's Vary is
		// rebuilt as HEAD's. gin's own gate 401 passes the shim untouched
		// (gin ran), so it loses Vary: Origin under gzip (deviation 16).
		h.Del("Vary")
		if h.Get("Access-Control-Allow-Origin") != "" {
			h.Set("Vary", "Origin")
		}
		servermiddleware.WriteBasicChallenge(w.ResponseWriter)
		return
	}
	// HEAD's Alt-Svc middleware was added to the engine at listen time, so it
	// sat in the NoRoute and NoMethod chains after the /api redirect
	// middleware: a 404 or 405 carried it, that 301 did not.
	if w.altSvc != "" && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) {
		h.Set("Alt-Svc", w.altSvc)
	}
}

// stageRefusal reports a status only a core stage writes as a refusal. The
// pass-through codes are exactly the stage refusals: stage 0's 403 (peer)
// and 425 (0-RTT early data), stage 7's 403 (cross-origin), stage 10's 429,
// stage 11's 413 and stage 3's 500. The shim lets these through ungated, as
// HEAD's peer gate and limits answered before its Basic gate; every other
// answer core writes itself is gated.
//
// The check is by status alone: a core-native route that answers one of
// these codes itself (a handler's own 403 or 500) would bypass the Basic
// gate. ABO serves no native route in R1; re-examine this before F3, or
// before any native route ships while the shim is still in place.
func stageRefusal(status int) bool {
	switch status {
	case http.StatusForbidden, http.StatusRequestEntityTooLarge, http.StatusTooEarly,
		http.StatusTooManyRequests, http.StatusInternalServerError:
		return true
	}
	return false
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

// FlushError is what http.ResponseController looks for first, so a
// handler's flush gets the connection's error through the shim; Flush has no
// result and would drop it. A refused answer has nothing to flush.
func (w *perimeterWriter) FlushError() error {
	w.decide(http.StatusOK)
	if w.refused {
		return nil
	}
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
