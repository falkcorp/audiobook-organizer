// file: internal/books/cutover/registered_harness_eventstream_test.go
// version: 0.1.0
// guid: 8a4f1c76-3e59-4d02-b9a7-5c1e0d6f2b83
// last-edited: 2026-11-22

package cutover_test

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// deadlineSpy is a ResponseWriter at the outermost end of the served stack,
// next to net/http's own writer. The realtime hub asks for each write's
// deadline through http.ResponseController, which walks the router's wrappers
// down to the first writer that implements SetWriteDeadline: this one. So a
// call that arrives here has crossed every wrapper the router put on the
// stream, and a wrapper that hid the method would leave this spy unreached and
// the hub's calls answering http.ErrNotSupported.
type deadlineSpy struct {
	http.ResponseWriter
	mu   *sync.Mutex
	sets *[]time.Time
	past *atomic.Bool
}

func (s deadlineSpy) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// SetWriteDeadline records the deadline and forwards it to the transport. With
// past set it forwards an instant already gone instead, which the transport
// must enforce on the next write.
func (s deadlineSpy) SetWriteDeadline(d time.Time) error {
	if !d.IsZero() {
		s.mu.Lock()
		*s.sets = append(*s.sets, d)
		s.mu.Unlock()
		if s.past.Load() {
			d = time.Now().Add(-time.Second)
		}
	}
	return http.NewResponseController(s.ResponseWriter).SetWriteDeadline(d)
}

// TestRegisteredHarness_EventStreamWriterChain (F6-03 obligation 7): GET
// /api/core/v1/events is opened over a real connection, HTTP/1.1 and HTTP/2,
// through the registered root handler, and a per-write deadline is both set and
// enforced. Set: after the stream has opened and one book change has crossed
// it, the spy at the end of the writer chain has seen deadlines about
// core.realtime.write_timeout ahead of now, so every wrapper between the hub and
// the transport passes SetWriteDeadline on. Enforced: with the spy then
// forwarding an instant already past, the next write fails at the transport and
// the stream ends from the server's side, instead of staying open.
func TestRegisteredHarness_EventStreamWriterChain(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	admin := h.Principal("key:admin:every")
	if admin == nil || admin.bearer == "" {
		t.Fatal("the harness has no admin key to open the stream with")
	}
	for _, proto := range []string{"h1", "h2"} {
		t.Run(proto, func(t *testing.T) {
			var mu sync.Mutex
			var sets []time.Time
			var past atomic.Bool
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/core/v1/events" {
					w = deadlineSpy{ResponseWriter: w, mu: &mu, sets: &sets, past: &past}
				}
				h.Handler.ServeHTTP(w, r)
			}))
			if proto == "h2" {
				srv.EnableHTTP2 = true
				srv.StartTLS()
			} else {
				srv.Start()
			}
			t.Cleanup(srv.Close)
			client := srv.Client()

			req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/core/v1/events", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+admin.bearer)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			if want := map[string]int{"h1": 1, "h2": 2}[proto]; resp.ProtoMajor != want {
				t.Fatalf("negotiated HTTP/%d, want HTTP/%d", resp.ProtoMajor, want)
			}

			// A reader goroutine: the lines of the stream, and its end.
			lines := make(chan string, 64)
			ended := make(chan struct{})
			go func() {
				defer close(ended)
				sc := bufio.NewScanner(resp.Body)
				sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
				for sc.Scan() {
					lines <- sc.Text()
				}
			}()
			waitFor := func(what string, match func(string) bool) {
				t.Helper()
				deadline := time.After(20 * time.Second)
				for {
					select {
					case l := <-lines:
						if match(l) {
							return
						}
					case <-ended:
						t.Fatalf("the stream ended before %s", what)
					case <-deadline:
						t.Fatalf("no %s within 20s", what)
					}
				}
			}
			waitFor("the opened frame", func(l string) bool { return strings.Contains(l, "core.stream.opened.v1") })

			// A book write publishes books.book.changed.v1, which crosses the stream.
			b, err := store.GetGlobalStore().CreateBook(&store.Book{Title: "The Killing Joke", FilePath: "/tmp/killing-joke.m4b", Format: "m4b"})
			if err != nil {
				t.Fatal(err)
			}
			waitFor("the book frame", func(l string) bool { return strings.Contains(l, "books.book.changed.v1") && strings.Contains(l, b.ID) })

			mu.Lock()
			got := append([]time.Time(nil), sets...)
			mu.Unlock()
			if len(got) == 0 {
				t.Fatal("no write deadline reached the end of the writer chain: a wrapper hides SetWriteDeadline")
			}
			if ahead := time.Until(got[len(got)-1]); ahead <= 0 || ahead > time.Minute {
				t.Errorf("the last deadline is %v ahead of now, want about core.realtime.write_timeout (10s by default)", ahead)
			}

			// Enforced: the transport refuses the next write once its deadline is past.
			past.Store(true)
			if _, err := store.GetGlobalStore().CreateBook(&store.Book{Title: "Arkham Asylum", FilePath: "/tmp/arkham-asylum.m4b", Format: "m4b"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ended:
			case <-time.After(20 * time.Second):
				t.Fatal("the stream stayed open after the transport's deadline had passed: it is not enforced")
			}
		})
	}
}
