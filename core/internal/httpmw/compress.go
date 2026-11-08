// file: core/internal/httpmw/compress.go
// version: 1.0.1
// guid: 1dcc991f-5931-42a7-be22-5dc86cf39509
// last-edited: 2026-11-08

package httpmw

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
)

// newGzipPool returns a pool of compressors at gin-contrib/gzip's level, the
// default.
func newGzipPool() *sync.Pool {
	return &sync.Pool{New: func() any {
		return gzip.NewWriter(io.Discard) // gzip.DefaultCompression
	}}
}

// acceptsGzip is the request half of stage 9's decision, HEAD's
// gin-contrib/gzip rule: the client accepts gzip, the connection is not an
// upgrade, and the path does not end in an already-compressed image type.
func acceptsGzip(r *http.Request) bool {
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
		strings.Contains(r.Header.Get("Connection"), "Upgrade") {
		return false
	}
	switch filepath.Ext(r.URL.Path) {
	case ".png", ".gif", ".jpeg", ".jpg":
		return false
	}
	return true
}

// compress is stage 9 (05-http-api §4.2): gzip for a client that accepts
// it, decided when the response's first byte (or its end, for an empty
// one) is written, so the route's NoCompress and the handler's own headers
// are known. Skipped: the owner's NoCompress (HEAD's path exclusions, now
// LegacyRoute.NoCompress, COR3-6; /metrics; HandleRaw streams) and every
// text/event-stream response. The response headers otherwise follow HEAD's
// gin-contrib/gzip v1.2.9 exactly: a compressed answer gets
// Content-Encoding: gzip, Vary: Accept-Encoding added and no
// Content-Length; an error status (>= 400) or a handler-set non-gzip
// Content-Encoding is sent uncompressed with Vary, ETag and
// Content-Encoding deleted; an answer with no body is sent with Vary
// deleted. The writer implements Unwrap, Flush and FlushError, so
// http.ResponseController reaches the connection and gets its error.
func (c *Chain) compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w, r: r, st: From(r.Context()), pool: c.gzip, log: c.deps.Log}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

// gzipWriter holds the status back until the first byte, so the decision
// sees the final headers, as gin's own writer (which delays WriteHeader)
// let gin-contrib/gzip.
type gzipWriter struct {
	http.ResponseWriter
	r        *http.Request
	st       *State
	status   int
	sent     bool // the status went down
	decided  bool
	gz       *gzip.Writer // non-nil while compressing
	pool     *sync.Pool
	log      *slog.Logger
	hijacked bool
	flushErr error // a failed Flush, returned by the next Write
}

func (w *gzipWriter) WriteHeader(code int) {
	if w.sent || w.hijacked {
		return
	}
	if code >= 100 && code < 200 {
		if code == http.StatusSwitchingProtocols {
			w.decided = true
			w.sendHeader(code)
			return
		}
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
	}
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}
	if w.flushErr != nil {
		return 0, w.flushErr
	}
	if !w.decided {
		w.decide(true)
	}
	if w.gz != nil {
		n, err := w.gz.Write(b)
		if w.st != nil {
			w.st.uncompressed.Add(int64(n))
		}
		return n, err
	}
	return w.ResponseWriter.Write(b)
}

// Flush flushes the compressor and the connection (an event stream that is
// not text/event-stream still arrives incrementally). http.Flusher has no
// result, so a failure is kept and returned by the next Write, as net/http's
// own writer reports one; a writer that cannot flush is not a failure.
func (w *gzipWriter) Flush() {
	if err := w.FlushError(); err != nil && !errors.Is(err, http.ErrNotSupported) && w.flushErr == nil {
		w.flushErr = err
	}
}

// FlushError is what http.ResponseController.Flush calls: Flush with the
// compressor's or the connection's error returned.
func (w *gzipWriter) FlushError() error {
	if w.hijacked {
		return nil
	}
	if !w.decided {
		w.decide(true)
	}
	if w.gz != nil {
		if err := w.gz.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.hijacked, w.decided = true, true
	}
	return conn, rw, err
}

func (w *gzipWriter) CloseNotify() <-chan bool { return closeNotify(w.ResponseWriter, w.r) }

func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// decide settles compression once; body reports that a body follows.
func (w *gzipWriter) decide(body bool) {
	w.decided = true
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	h := w.Header()
	switch {
	case w.st != nil && w.st.Policy() != nil && w.st.Policy().NoCompress, eventStream(h):
		// Not this stage's response: untouched.
	case status >= http.StatusBadRequest:
		removeGzipHeaders(h)
	case !body:
		// gin-contrib/gzip's "nothing was compressed" branch.
		h.Del("Vary")
	case h.Get("Content-Encoding") == "gzip":
		// Already gzip: passed through as the handler encoded it.
	case h.Get("Content-Encoding") != "":
		removeGzipHeaders(h)
	default:
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		h.Del("Content-Length")
		gz := w.pool.Get().(*gzip.Writer)
		gz.Reset(w.ResponseWriter)
		w.gz = gz
		if w.st != nil {
			w.st.compressed.Store(true)
		}
	}
	w.sendHeader(status)
}

// sendHeader passes the status down once.
func (w *gzipWriter) sendHeader(status int) {
	if w.sent {
		return
	}
	w.sent = true
	w.ResponseWriter.WriteHeader(status)
}

// finish runs when the handler returned: an empty answer is decided now,
// and a compressed one gets its gzip footer.
func (w *gzipWriter) finish() {
	if w.hijacked {
		return
	}
	if !w.decided {
		if w.status == 0 {
			// Nothing written and no status: net/http's implicit 200 (or
			// stage 5's) goes out with the headers as they are, which
			// gin-contrib/gzip's empty branch would also have stripped of Vary.
			w.decided = true
			if w.st == nil || w.st.Policy() == nil || !w.st.Policy().NoCompress {
				w.Header().Del("Vary")
			}
			return
		}
		w.decide(false)
	}
	if w.gz != nil {
		if err := w.gz.Close(); err != nil {
			// The trailer did not reach the client, which went away; the
			// handler has returned, so there is no one else to tell.
			w.log.DebugContext(w.r.Context(), "httpapi: finish gzip response", "error", err)
		}
		w.gz.Reset(io.Discard)
		w.pool.Put(w.gz)
		w.gz = nil
	}
}

// removeGzipHeaders is gin-contrib/gzip's error-response cleanup: it deletes
// every Vary value, not only its own, and the ETag.
func removeGzipHeaders(h http.Header) {
	h.Del("Content-Encoding")
	h.Del("Vary")
	h.Del("ETag")
}

// eventStream reports a text/event-stream response.
func eventStream(h http.Header) bool {
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && mt == "text/event-stream"
}
