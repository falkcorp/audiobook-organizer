// file: internal/server/handlers/opsv3/events_test.go
// version: 1.4.0
// guid: 38f6d698-9487-485c-acea-303202e6d6eb
// last-edited: 2026-10-18

package opsv3

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// sseConn reads an event stream from a live test server.
type sseConn struct {
	resp   *http.Response
	sc     *bufio.Scanner
	cancel context.CancelFunc
}

func openSSE(t testing.TB, srv *httptest.Server, lastID string) *sseConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v3/ops/events", nil)
	req.Header.Set(hPerms, auth.PermLibraryView)
	req.Header.Set(hUser, "u")
	req.Header.Set(hMethod, string(auth.MethodSession))
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	return &sseConn{resp: resp, sc: bufio.NewScanner(resp.Body), cancel: cancel}
}

type sseFrame struct {
	id, event, data string
	comment         bool
}

// next reads one frame (an event or a comment line).
func (c *sseConn) next(t testing.TB) sseFrame {
	t.Helper()
	var f sseFrame
	done := make(chan bool, 1)
	go func() {
		for c.sc.Scan() {
			line := c.sc.Text()
			switch {
			case line == "":
				if f != (sseFrame{}) {
					done <- true
					return
				}
			case strings.HasPrefix(line, ":"):
				f.comment = true
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.data = line[6:]
			}
		}
		done <- false
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("event stream ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return f
}

// fakeHub lets a test feed registry events.
type fakeHub struct {
	ch chan opsregistry.Event
}

func newFakeHub() *fakeHub                                       { return &fakeHub{ch: make(chan opsregistry.Event, 64)} }
func (h *fakeHub) Subscribe() (<-chan opsregistry.Event, func()) { return h.ch, func() {} }
func (h *fakeHub) emit(name, id string) {
	h.ch <- opsregistry.Event{Name: name, Payload: map[string]any{"op_id": id}}
}

func batchRecord(id string, done, chunksDone int) database.RunRecord {
	total := int64(100)
	return database.RunRecord{
		ID: id, DefID: "test.batch", State: state.V3Running,
		Progress: database.ProgressSnapshot{
			Done: int64(done), Total: &total,
			Chunks:  &database.ChunkProgress{Done: chunksDone, Total: 10, Size: 10},
			Workers: []database.WorkerProgress{{Worker: 0, Chunk: chunksDone, Item: "item-x"}},
		},
	}
}

func TestSSEProgressCarriesChunks(t *testing.T) {
	mem := &memRuns{runs: map[string]database.RunRecord{"b1": batchRecord("b1", 10, 1)}}
	hub := newFakeHub()
	br := NewBroker(mem, hub, nil, BrokerOptions{FlushEvery: 5 * time.Millisecond})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go br.Run(ctx)

	srv := httptest.NewServer(newRouter(New(Deps{Runs: mem, Broker: br})))
	t.Cleanup(srv.Close)
	c := openSSE(t, srv, "")
	if f := c.next(t); !f.comment {
		t.Fatalf("first frame should be the heartbeat comment: %+v", f)
	}

	hub.emit("op.updated", "b1")
	f := c.next(t)
	if f.event != "run" || f.id == "" {
		t.Fatalf("frame = %+v, want an id'd run event", f)
	}
	v := unmarshalView(t, f.data)
	if v.ID != "b1" || v.Progress.Chunks == nil || v.Progress.Chunks.Done != 1 || v.Progress.Chunks.Total != 10 {
		t.Fatalf("run event lacks chunks: %+v", v)
	}
	if len(v.Progress.Workers) != 1 || v.Progress.Workers[0].Item != "item-x" {
		t.Fatalf("run event lacks workers: %+v", v.Progress.Workers)
	}
	if strings.Contains(f.data, `"progress_current"`) {
		t.Error("v3 events must not carry the v2 progress shape")
	}

	// A later delta shows the new chunk count.
	mem.set("b1", batchRecord("b1", 40, 4))
	hub.emit("op.updated", "b1")
	if v := unmarshalView(t, c.next(t).data); v.Progress.Chunks.Done != 4 || v.Progress.Done != 40 {
		t.Fatalf("delta = %+v", v.Progress)
	}
	// Events that name no run (logs) publish nothing.
	hub.emit("op.log", "b1")
}

func TestSSEHeartbeatAndResumeFromLastEventID(t *testing.T) {
	mem := &memRuns{runs: map[string]database.RunRecord{}}
	// Six streams of one caller stay open below: lift the per-user cap,
	// which TestSSEPerUserCap covers.
	br := NewBroker(mem, nil, nil, BrokerOptions{Heartbeat: 20 * time.Millisecond, RingSize: 4, MaxClientsPerUser: 16})
	for i := range 6 {
		br.publish(RunView{ID: string(rune('a' + i)), State: "running"})
	}
	srv := httptest.NewServer(newRouter(New(Deps{Runs: mem, Broker: br})))
	t.Cleanup(srv.Close)

	// Ring holds ids 3..6. Resuming after 4 replays 5 and 6, no resync.
	c := openSSE(t, srv, br.eventID(4))
	var ids []string
	for len(ids) < 2 {
		f := c.next(t)
		switch {
		case f.event == "resync":
			t.Fatal("a resume inside the ring must not resync")
		case f.event == "run":
			ids = append(ids, f.id)
		}
	}
	if ids[0] != br.eventID(5) || ids[1] != br.eventID(6) {
		t.Fatalf("replayed %v, want [5 6]", ids)
	}
	// Idle: heartbeats keep coming.
	if f := c.next(t); !f.comment {
		t.Fatalf("idle stream frame = %+v, want a heartbeat comment", f)
	}

	// An id older than the ring, one past this broker's sequence, one from
	// a previous process (another nonce, or the bare number a restarted
	// server used to send), and one that does not parse: all resync.
	for _, last := range []string{br.eventID(1), br.eventID(999), "0a0a0a0a0a0a-5", "5", "abc"} {
		c := openSSE(t, srv, last)
		seen := false
		for range 3 {
			if c.next(t).event == "resync" {
				seen = true
				break
			}
		}
		if !seen {
			t.Errorf("Last-Event-ID %s should be told to resync", last)
		}
	}
}

func TestSSENoBrokerIs503(t *testing.T) {
	nobroker := newRouter(New(Deps{Runs: &memRuns{}}))
	if w := do(t, nobroker, viewer, http.MethodGet, "/api/v3/ops/events", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no broker = %d, want 503", w.Code)
	}
}

// TestSSEIDsCarryTheBootNonce: ids are "<nonce>-<seq>", and a broker built
// later (a restarted server) has another nonce, so it cannot mistake an
// earlier process's id for one of its own.
func TestSSEIDsCarryTheBootNonce(t *testing.T) {
	a := NewBroker(&memRuns{}, nil, nil, BrokerOptions{})
	b := NewBroker(&memRuns{}, nil, nil, BrokerOptions{})
	if a.nonce == "" || a.nonce == b.nonce {
		t.Fatalf("nonces %q and %q must be set and differ", a.nonce, b.nonce)
	}
	if n, ok := a.parseEventID(a.eventID(42)); !ok || n != 42 {
		t.Fatalf("own id = %d %v", n, ok)
	}
	if _, ok := b.parseEventID(a.eventID(42)); ok {
		t.Fatal("an id from another boot parsed as this broker's")
	}
}

// TestSSEClientCap: past MaxClients open streams the answer is 503, and a
// closed stream frees its place.
func TestSSEClientCap(t *testing.T) {
	br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{MaxClients: 2})
	_, u1, err := br.subscribe("", 0, resumeNone)
	if err != nil {
		t.Fatal(err)
	}
	_, u2, err := br.subscribe("", 0, resumeNone)
	if err != nil {
		t.Fatal(err)
	}
	defer u2()
	r := newRouter(New(Deps{Runs: &memRuns{}, Broker: br}))
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/events", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("third stream = %d, want 503", w.Code)
	}
	u1()
	if _, u3, err := br.subscribe("", 0, resumeNone); err != nil {
		t.Fatalf("after one closed: %v", err)
	} else {
		u3()
	}
}

// TestSSEPerUserCap: one signed-in user may hold MaxClientsPerUser streams;
// one more is 429 while other users still get in, closing one frees a place,
// and requests with no user are bounded only by MaxClients.
func TestSSEPerUserCap(t *testing.T) {
	viewerBucket := sessionBucket(viewer.user)
	br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{MaxClients: 10, MaxClientsPerUser: 2})
	_, a1, err := br.subscribe(viewerBucket, 0, resumeNone)
	if err != nil {
		t.Fatal(err)
	}
	_, a2, err := br.subscribe(viewerBucket, 0, resumeNone)
	if err != nil {
		t.Fatal(err)
	}
	defer a2()
	r := newRouter(New(Deps{Runs: &memRuns{}, Broker: br}))
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/events", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("third stream of one user = %d, want 429", w.Code)
	}
	if _, u, err := br.subscribe("someone-else", 0, resumeNone); err != nil {
		t.Fatalf("another user refused: %v", err)
	} else {
		u()
	}
	for range 5 {
		_, u, err := br.subscribe("", 0, resumeNone)
		if err != nil {
			t.Fatalf("no-user stream refused under MaxClients: %v", err)
		}
		defer u()
	}
	a1()
	a1() // idempotent: must not free a second place
	if _, u, err := br.subscribe(viewerBucket, 0, resumeNone); err != nil {
		t.Fatalf("after one closed: %v", err)
	} else {
		defer u()
	}
	if _, _, err := br.subscribe(viewerBucket, 0, resumeNone); !errors.Is(err, ErrTooManyUserClients) {
		t.Fatalf("a double unsubscribe freed an extra place: %v", err)
	}
}

// TestSSEStreamsEndOnServerClosing: closing Deps.Closing ends an open stream
// at once, so the HTTP shutdown is not held for its timeout.
func TestSSEStreamsEndOnServerClosing(t *testing.T) {
	br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{Heartbeat: time.Hour})
	closing := make(chan struct{})
	srv := httptest.NewServer(newRouter(New(Deps{Runs: &memRuns{}, Broker: br, Closing: closing})))
	t.Cleanup(srv.Close)
	c := openSSE(t, srv, "")
	_ = c.next(t) // the opening heartbeat
	ended := make(chan struct{})
	go func() {
		for c.sc.Scan() {
		}
		close(ended)
	}()
	close(closing)
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream stayed open after the server started closing")
	}
}

// TestSSEBrokerStartsOnFirstSubscriber: with StartOnFirstSubscriber the loop
// does not run until a client subscribes, starts exactly once, and does not
// start once its context is done.
func TestSSEBrokerStartsOnFirstSubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br := NewBroker(&memRuns{}, newFakeHub(), nil, BrokerOptions{})
	var mu sync.Mutex
	spawned := 0
	br.StartOnFirstSubscriber(ctx, func(fn func()) {
		mu.Lock()
		spawned++
		mu.Unlock()
		go fn()
	})
	mu.Lock()
	if spawned != 0 {
		t.Fatal("started before any subscriber")
	}
	mu.Unlock()
	for range 3 {
		_, u, err := br.subscribe("", 0, resumeNone)
		if err != nil {
			t.Fatal(err)
		}
		u()
	}
	mu.Lock()
	if spawned != 1 {
		t.Fatalf("spawned %d times, want 1", spawned)
	}
	mu.Unlock()

	done, stop := context.WithCancel(context.Background())
	stop()
	late := NewBroker(&memRuns{}, newFakeHub(), nil, BrokerOptions{})
	late.StartOnFirstSubscriber(done, func(func()) { t.Error("spawned after shutdown") })
	_, u, _ := late.subscribe("", 0, resumeNone)
	u()
}

// TestSSETerminalSurvivesADroppedEvent: the registry feed drops events for a
// slow subscriber. A run published while running is re-read each tick until
// it settles, so its terminal state reaches clients even when op.terminal
// never arrived; once settled it is no longer re-read.
func TestSSETerminalSurvivesADroppedEvent(t *testing.T) {
	mem := &memRuns{runs: map[string]database.RunRecord{"r1": {ID: "r1", DefID: "test.batch", State: state.V3Running}}}
	hub := newFakeHub()
	br := NewBroker(mem, hub, nil, BrokerOptions{FlushEvery: 5 * time.Millisecond})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go br.Run(ctx)
	srv := httptest.NewServer(newRouter(New(Deps{Runs: mem, Broker: br})))
	t.Cleanup(srv.Close)
	c := openSSE(t, srv, "")
	_ = c.next(t) // heartbeat

	hub.emit("op.updated", "r1")
	if v := unmarshalView(t, c.next(t).data); v.State != "running" {
		t.Fatalf("first event = %+v", v)
	}
	// The run finishes; its op.terminal is lost (never emitted).
	mem.set("r1", database.RunRecord{ID: "r1", DefID: "test.batch", State: state.V3Succeeded})
	f := c.next(t)
	if f.event != "run" {
		t.Fatalf("frame = %+v, want the run's terminal event", f)
	}
	if v := unmarshalView(t, f.data); v.State != "succeeded" {
		t.Fatalf("re-read published %+v, want succeeded", v)
	}
	// Settled: no longer watched, so nothing more is published for it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		br.mu.Lock()
		seq := br.seq
		br.mu.Unlock()
		if seq == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	br.mu.Lock()
	seq := br.seq
	br.mu.Unlock()
	if seq != 2 {
		t.Fatalf("published %d events, want 2 (running, succeeded)", seq)
	}
}

// TestBrokerWatchSetIsBounded: no more than MaxWatched unsettled runs are
// re-read per tick.
func TestBrokerWatchSetIsBounded(t *testing.T) {
	mem := &memRuns{runs: map[string]database.RunRecord{}}
	for i := range 10 {
		id := "r" + string(rune('a'+i))
		mem.runs[id] = database.RunRecord{ID: id, State: state.V3Running}
	}
	br := NewBroker(mem, nil, nil, BrokerOptions{MaxWatched: 3})
	dirty := map[string]struct{}{}
	for id := range mem.runs {
		dirty[id] = struct{}{}
	}
	br.flush(dirty)
	if len(br.watch) != 3 {
		t.Fatalf("watching %d runs, want the cap of 3", len(br.watch))
	}
}

func TestSSESlowClientGetsResyncNotBlocking(t *testing.T) {
	br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{ClientBuffer: 4, RingSize: 1000})
	cl, unsub, err := br.subscribe("", 0, resumeNone)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	// A client that reads nothing while 500 events are published: publishing
	// returns promptly every time (it never waits on the client).
	finished := make(chan struct{})
	go func() {
		for i := range 500 {
			br.publish(RunView{ID: "r", State: "running", Progress: ProgressView{Done: int64(i)}})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a client that is not reading")
	}
	msgs, resync := cl.take()
	if !resync {
		t.Fatal("a client that lost events must be told to resync")
	}
	if len(msgs) != 4 {
		t.Fatalf("slow client holds %d events, want its buffer of 4", len(msgs))
	}
	if msgs[len(msgs)-1].view.Progress.Done != 499 {
		t.Errorf("the newest event must be kept; last has done=%d", msgs[len(msgs)-1].view.Progress.Done)
	}
	if msgs[0].view.Progress.Done != 496 {
		t.Errorf("oldest are dropped first; first kept has done=%d, want 496", msgs[0].view.Progress.Done)
	}

	// End to end: the writer turns that state into an `event: resync`.
	srv := httptest.NewServer(newRouter(New(Deps{Runs: &memRuns{}, Broker: br})))
	t.Cleanup(srv.Close)
	c := openSSE(t, srv, "")
	_ = c.next(t) // heartbeat
	// Wedge this connection's queue past its capacity before it drains.
	br.mu.Lock()
	var live *client
	for k := range br.clients {
		if k != cl {
			live = k
		}
	}
	br.mu.Unlock()
	if live == nil {
		t.Fatal("connection did not register")
	}
	for i := range 20 {
		br.publish(RunView{ID: "w", State: "running", Progress: ProgressView{Done: int64(i)}})
	}
	sawResync := false
	for range 25 {
		f := c.next(t)
		if f.event == "resync" {
			sawResync = true
			break
		}
	}
	if !sawResync {
		// The writer may have kept up with 20 events; force an overflow with
		// a burst larger than the buffer under the lock so it cannot drain.
		br.mu.Lock()
		for i := range 10 {
			br.seq++
			live.push(message{id: br.seq, view: RunView{ID: "w", Progress: ProgressView{Done: int64(i)}}})
		}
		br.mu.Unlock()
		for range 15 {
			if c.next(t).event == "resync" {
				sawResync = true
				break
			}
		}
	}
	if !sawResync {
		t.Fatal("the stream never sent event: resync to a client that overflowed")
	}
}

func unmarshalView(t testing.TB, data string) RunView {
	t.Helper()
	var v RunView
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		t.Fatalf("event data is not a RunView: %s", data)
	}
	return v
}

// failWriter is a ResponseWriter whose Write fails after okWrites writes, like
// a connection the client has dropped.
type failWriter struct {
	hdr      http.Header
	okWrites int
	writes   int
}

func (w *failWriter) Header() http.Header { return w.hdr }
func (w *failWriter) WriteHeader(int)     {}
func (w *failWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > w.okWrites {
		return 0, errors.New("write: broken pipe")
	}
	return len(p), nil
}

// TestSSEStreamEndsWhenAWriteFails: a failed write to the client ends Events
// at once (it does not keep looping until the request context ends) and frees
// the stream's place, both for the opening heartbeat and for a later event.
func TestSSEStreamEndsWhenAWriteFails(t *testing.T) {
	for _, okWrites := range []int{0, 1} {
		t.Run("ok_writes_"+strconv.Itoa(okWrites), func(t *testing.T) {
			br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{Heartbeat: time.Hour})
			h := New(Deps{Runs: &memRuns{}, Broker: br})
			fw := &failWriter{hdr: http.Header{}, okWrites: okWrites}
			c, _ := gin.CreateTestContext(fw)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v3/ops/events", nil)
			returned := make(chan struct{})
			go func() { h.Events(c); close(returned) }()
			if okWrites > 0 {
				// Wait for the stream to subscribe, then give it an event to
				// write into the broken connection.
				deadline := time.Now().Add(5 * time.Second)
				for {
					br.mu.Lock()
					n := len(br.clients)
					br.mu.Unlock()
					if n > 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("stream never subscribed")
					}
					time.Sleep(time.Millisecond)
				}
				br.publish(RunView{ID: "r1"})
			}
			select {
			case <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("Events kept running after a write to the client failed")
			}
			br.mu.Lock()
			defer br.mu.Unlock()
			if len(br.clients) != 0 {
				t.Fatalf("%d clients still registered after the stream ended", len(br.clients))
			}
		})
	}
}

// TestSSETooManyStreamsHasRetryAfter: the 429 names how long to wait.
func TestSSETooManyStreamsHasRetryAfter(t *testing.T) {
	br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{MaxClientsPerUser: 1})
	_, u, err := br.subscribe(sessionBucket(viewer.user), 0, resumeNone)
	if err != nil {
		t.Fatal(err)
	}
	defer u()
	r := newRouter(New(Deps{Runs: &memRuns{}, Broker: br}))
	w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/events", nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second stream = %d, want 429", w.Code)
	}
	if got, want := w.Header().Get("Retry-After"), strconv.Itoa(tooManyStreamsRetryAfterSeconds); got != want {
		t.Fatalf("Retry-After = %q, want %q", got, want)
	}
}

// TestSSEPerUserCapIsPerCredential: one user's session streams and API-key
// streams are counted apart, so a script on a key cannot use up the browser
// session's places; all of the user's keys share one bucket, so minting keys
// cannot raise the user's total above 2 x MaxClientsPerUser.
func TestSSEPerUserCapIsPerCredential(t *testing.T) {
	const perUser = 2
	br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{MaxClients: 20, MaxClientsPerUser: perUser})
	h := New(Deps{Runs: &memRuns{}, Broker: br})
	r := gin.New()
	g := r.Group("/api/v3", func(c *gin.Context) {
		ctx := auth.WithUser(c.Request.Context(), &database.User{ID: "u-same"})
		ctx = auth.WithPermissions(ctx, []string{auth.PermLibraryView})
		c.Request = c.Request.WithContext(ctx)
		// The key the API-key middleware records (middleware.CurrentAPIKey
		// reads this gin key).
		if id := c.GetHeader("X-Test-Key"); id != "" {
			c.Set("auth_api_key", &database.APIKey{ID: id})
		}
		c.Next()
	})
	h.Register(g, testPerm)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	open := func(keyID string) *http.Response {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v3/ops/events", nil)
		if keyID != "" {
			req.Header.Set("X-Test-Key", keyID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
		return resp
	}
	for _, cred := range []string{"", "key-1"} {
		for i := range perUser {
			if resp := open(cred); resp.StatusCode != http.StatusOK {
				t.Fatalf("credential %q stream %d = %d, want 200", cred, i+1, resp.StatusCode)
			}
		}
		if resp := open(cred); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("credential %q past the cap = %d, want 429", cred, resp.StatusCode)
		}
	}
	// More keys buy no more streams: every key of the user shares the one
	// API-key bucket, so the user's total stays 2 x MaxClientsPerUser.
	for _, key := range []string{"key-2", "key-3", "key-4"} {
		if resp := open(key); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("another key of a user whose keys are at the cap = %d, want 429", resp.StatusCode)
		}
	}
	br.mu.Lock()
	got := br.perUser[sessionBucket("u-same")] + br.perUser[apiKeyBucket("u-same")]
	br.mu.Unlock()
	if got != 2*perUser {
		t.Fatalf("user holds %d streams, want the total 2 x %d", got, perUser)
	}
}
