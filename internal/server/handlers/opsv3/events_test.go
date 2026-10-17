// file: internal/server/handlers/opsv3/events_test.go
// version: 1.0.0
// guid: 38f6d698-9487-485c-acea-303202e6d6eb
// last-edited: 2026-10-17

package opsv3

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	br := NewBroker(mem, nil, nil, BrokerOptions{Heartbeat: 20 * time.Millisecond, RingSize: 4})
	for i := range 6 {
		br.publish(RunView{ID: string(rune('a' + i)), State: "running"})
	}
	srv := httptest.NewServer(newRouter(New(Deps{Runs: mem, Broker: br})))
	t.Cleanup(srv.Close)

	// Ring holds ids 3..6. Resuming after 4 replays 5 and 6, no resync.
	c := openSSE(t, srv, "4")
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
	if ids[0] != "5" || ids[1] != "6" {
		t.Fatalf("replayed %v, want [5 6]", ids)
	}
	// Idle: heartbeats keep coming.
	if f := c.next(t); !f.comment {
		t.Fatalf("idle stream frame = %+v, want a heartbeat comment", f)
	}

	// An id older than the ring, and one from a previous process, resync.
	for _, last := range []string{"1", "999"} {
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

func TestSSEBadLastEventID(t *testing.T) {
	br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{})
	r := newRouter(New(Deps{Runs: &memRuns{}, Broker: br}))
	req := httptest.NewRequest(http.MethodGet, "/api/v3/ops/events", nil)
	req.Header.Set(hPerms, auth.PermLibraryView)
	req.Header.Set("Last-Event-ID", "abc")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Last-Event-ID abc = %d, want 400", w.Code)
	}
	nobroker := newRouter(New(Deps{Runs: &memRuns{}}))
	if w := do(t, nobroker, viewer, http.MethodGet, "/api/v3/ops/events", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no broker = %d, want 503", w.Code)
	}
}

func TestSSESlowClientGetsResyncNotBlocking(t *testing.T) {
	br := NewBroker(&memRuns{}, nil, nil, BrokerOptions{ClientBuffer: 4, RingSize: 1000})
	cl, unsub := br.subscribe(0, false)
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
