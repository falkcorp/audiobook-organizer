// file: core/internal/httpmw/writer.go
// version: 1.0.1
// guid: 1ea9c2e4-9eb0-4d35-b6e3-3e1f2eab0555
// last-edited: 2026-11-08

package httpmw

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// hookWriter is the writer the outer stages wrap a response in: it records
// the status and the bytes written, and calls before once, just before the
// final status line goes out, so a stage can complete the headers then
// (stage 5's CSP needs the owner the router picked). It implements Flush,
// FlushError, Hijack, CloseNotify and Unwrap, so http.ResponseController and the legacy
// gin engine reach the connection through it: gin's Flush asserts
// http.Flusher, its Hijack http.Hijacker, and its c.Stream (the audio sample
// route) http.CloseNotifier, each without a check.
type hookWriter struct {
	http.ResponseWriter
	r        *http.Request
	before   func(status int)
	status   int
	wrote    bool
	hijacked bool
	n        int64
	flushErr error // a failed Flush, returned by the next Write
}

func newHookWriter(w http.ResponseWriter, r *http.Request, before func(int)) *hookWriter {
	return &hookWriter{ResponseWriter: w, r: r, before: before}
}

func (w *hookWriter) WriteHeader(code int) {
	if w.wrote || w.hijacked {
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		// An informational answer (103 Early Hints) passes; the final
		// status is still to come.
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.wrote, w.status = true, code
	if w.before != nil {
		w.before(code)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *hookWriter) Write(b []byte) (int, error) {
	if w.flushErr != nil {
		return 0, w.flushErr
	}
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.n += int64(n)
	return n, err
}

// Flush implements http.Flusher. http.Flusher has no result, so a failure
// is kept and returned by the next Write, as net/http's own writer reports
// one; a writer below that cannot flush is not a failure.
func (w *hookWriter) Flush() {
	if err := w.FlushError(); err != nil && !errors.Is(err, http.ErrNotSupported) && w.flushErr == nil {
		w.flushErr = err
	}
}

// FlushError is what http.ResponseController.Flush calls, so a handler's
// flush error crosses the stages: a flush before any write sends the 200
// headers, as net/http's does, then the connection is flushed.
func (w *hookWriter) FlushError() error {
	if !w.wrote && !w.hijacked {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack implements http.Hijacker (the /socket.io upgrade).
func (w *hookWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.hijacked = true
		if !w.wrote {
			w.status = http.StatusSwitchingProtocols
		}
	}
	return conn, rw, err
}

// CloseNotify implements http.CloseNotifier; where the writer below has
// none (HTTP/3), the request's context stands in.
func (w *hookWriter) CloseNotify() <-chan bool { return closeNotify(w.ResponseWriter, w.r) }

// Unwrap lets http.ResponseController reach the connection's own writer.
func (w *hookWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Status is the final status written, 200 when the handler wrote nothing
// (net/http's implicit answer), 101 after a hijack.
func (w *hookWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// closeNotify is http.CloseNotifier for a wrapper over w.
func closeNotify(w http.ResponseWriter, r *http.Request) <-chan bool {
	//lint:ignore SA1019 gin's Stream still calls CloseNotify
	if cn, ok := w.(http.CloseNotifier); ok {
		return cn.CloseNotify()
	}
	ch := make(chan bool, 1)
	if r == nil {
		return ch
	}
	go func() {
		<-r.Context().Done()
		ch <- true
	}()
	return ch
}
