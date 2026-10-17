// file: internal/server/handlers/opsv3/events.go
// version: 1.0.0
// guid: 4a1ad0ef-2930-46eb-bac3-35772875dfa4
// last-edited: 2026-10-17

package opsv3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// SSE tuning. Heartbeat is the comment line that keeps an idle connection
// open; the ring is how far back Last-Event-ID can resume; ClientBuffer is how
// many unsent events one slow client may hold before its oldest are dropped.
const (
	DefaultHeartbeat    = 15 * time.Second
	DefaultRingSize     = 512
	DefaultClientBuffer = 128
	defaultFlushEvery   = 250 * time.Millisecond
)

var eventsLog = logger.New("handlers.opsv3.events")

// Hub is the registry's event feed (opsregistry.EventHub).
type Hub interface {
	Subscribe() (<-chan opsregistry.Event, func())
}

// BrokerOptions tunes a Broker; zero values take the defaults above.
type BrokerOptions struct {
	Heartbeat    time.Duration
	RingSize     int
	ClientBuffer int
	// FlushEvery is how often changed runs are re-read and published. Progress
	// events arrive per item; reading a run per event would put a store read
	// on the progress path, so changes are coalesced per run instead.
	FlushEvery time.Duration
}

// message is one numbered event.
type message struct {
	id   uint64
	view RunView
}

// Broker turns the registry's event feed into numbered `run` events carrying a
// RunView, keeps the last RingSize of them for Last-Event-ID resume, and
// fans them out to clients. Publishing never blocks: the registry feed is
// drained by one goroutine that only marks runs dirty, and a client that does
// not keep up loses its oldest events and is told to resync.
type Broker struct {
	runs    RunReader
	hub     Hub
	zombies ZombieSource
	opt     BrokerOptions

	mu      sync.Mutex
	seq     uint64
	ring    []message // oldest first, at most opt.RingSize
	clients map[*client]struct{}
}

// NewBroker builds a Broker. runs reads the run behind each event; zombies may
// be nil.
func NewBroker(runs RunReader, hub Hub, zombies ZombieSource, opt BrokerOptions) *Broker {
	if opt.Heartbeat <= 0 {
		opt.Heartbeat = DefaultHeartbeat
	}
	if opt.RingSize <= 0 {
		opt.RingSize = DefaultRingSize
	}
	if opt.ClientBuffer <= 0 {
		opt.ClientBuffer = DefaultClientBuffer
	}
	if opt.FlushEvery <= 0 {
		opt.FlushEvery = defaultFlushEvery
	}
	return &Broker{runs: runs, hub: hub, zombies: zombies, opt: opt, clients: map[*client]struct{}{}}
}

// Run drains the feed until ctx ends. The feed's events name a run; each
// named run is re-read once per FlushEvery and published with its progress
// chunks and workers.
//
// This is one goroutine on purpose: the per-tick work is one store read per
// run that changed since the last tick, and a run that is busy changes
// constantly, so the cost is bounded by the number of active runs (a handful),
// not by the number of progress events.
func (b *Broker) Run(ctx context.Context) {
	if b.hub == nil || b.runs == nil {
		return
	}
	ch, unsubscribe := b.hub.Subscribe()
	defer unsubscribe()
	dirty := map[string]struct{}{}
	tick := time.NewTicker(b.opt.FlushEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if id := eventRunID(ev); id != "" {
				dirty[id] = struct{}{}
			}
		case <-tick.C:
			b.flush(dirty)
		}
	}
}

// eventRunID is the run an op.created / op.updated / op.terminal event names.
func eventRunID(ev opsregistry.Event) string {
	switch ev.Name {
	case "op.created", "op.updated", "op.terminal":
	default:
		return ""
	}
	m, ok := ev.Payload.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := m["op_id"].(string)
	return id
}

func (b *Broker) flush(dirty map[string]struct{}) {
	if len(dirty) == 0 {
		return
	}
	var zs map[string]bool
	if b.zombies != nil {
		for _, z := range b.zombies.Zombies() {
			if zs == nil {
				zs = map[string]bool{}
			}
			zs[z.OpID] = true
		}
	}
	for id := range dirty {
		delete(dirty, id)
		rec, err := b.runs.GetRunV3(id)
		if err != nil {
			eventsLog.Warn("read run %s for event failed: %v", logger.SanitizeLogValue(id), err)
			continue
		}
		if rec == nil {
			continue
		}
		b.publish(toView(*rec, zs))
	}
}

// publish numbers v, stores it in the ring and offers it to every client
// without blocking.
func (b *Broker) publish(v RunView) {
	b.mu.Lock()
	b.seq++
	m := message{id: b.seq, view: v}
	b.ring = append(b.ring, m)
	if len(b.ring) > b.opt.RingSize {
		b.ring = append(b.ring[:0], b.ring[len(b.ring)-b.opt.RingSize:]...)
	}
	for c := range b.clients {
		c.push(m)
	}
	b.mu.Unlock()
}

// client is one SSE connection's bounded queue.
type client struct {
	mu     sync.Mutex
	q      []message
	cap    int
	resync bool
	wake   chan struct{}
}

func newClient(capacity int) *client {
	return &client{cap: capacity, wake: make(chan struct{}, 1)}
}

// push queues m, dropping the oldest queued event when the queue is full and
// remembering that the client must resync. It never blocks.
func (c *client) push(m message) {
	c.mu.Lock()
	if len(c.q) >= c.cap {
		c.q = append(c.q[:0], c.q[1:]...)
		c.resync = true
	}
	c.q = append(c.q, m)
	c.mu.Unlock()
	c.signal()
}

func (c *client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *client) take() (msgs []message, resync bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	msgs, resync = c.q, c.resync
	c.q, c.resync = nil, false
	return msgs, resync
}

// subscribe registers a client. With lastID set it first queues the ring's
// events after lastID; when the ring no longer reaches back that far (or the
// id is from before a restart) the client is marked to resync instead.
func (b *Broker) subscribe(lastID uint64, hasLast bool) (*client, func()) {
	c := newClient(b.opt.ClientBuffer)
	b.mu.Lock()
	if hasLast {
		oldest := b.seq + 1
		if len(b.ring) > 0 {
			oldest = b.ring[0].id
		}
		switch {
		case lastID > b.seq, lastID+1 < oldest:
			c.resync = true
		default:
			for _, m := range b.ring {
				if m.id > lastID {
					c.push(m)
				}
			}
		}
	}
	b.clients[c] = struct{}{}
	b.mu.Unlock()
	return c, func() {
		b.mu.Lock()
		delete(b.clients, c)
		b.mu.Unlock()
	}
}

// Events implements GET /api/v3/ops/events: text/event-stream of
// `event: run` (a RunView, with progress.chunks and progress.workers), a
// `: heartbeat` comment every 15 s, and `event: resync` when events were
// missed (a client too slow for its buffer, or a Last-Event-ID the ring no
// longer holds): reload /runs, then keep reading.
func (h *Handler) Events(c *gin.Context) {
	b := h.d.Broker
	if b == nil {
		httputil.RespondWithServiceUnavailable(c, "operations event feed not initialized")
		return
	}
	var lastID uint64
	hasLast := false
	if raw := c.GetHeader("Last-Event-ID"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			httputil.RespondWithValidationError(c, "Last-Event-ID", "not an event id")
			return
		}
		lastID, hasLast = n, true
	}
	cl, unsubscribe := b.subscribe(lastID, hasLast)
	defer unsubscribe()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	fmt.Fprint(c.Writer, ": heartbeat\n\n")
	c.Writer.Flush()

	hb := time.NewTicker(b.opt.Heartbeat)
	defer hb.Stop()
	done := c.Request.Context().Done()
	// A subscribe that queued a replay or a resync has something to send now.
	cl.signal()
	for {
		select {
		case <-done:
			return
		case <-hb.C:
			fmt.Fprint(c.Writer, ": heartbeat\n\n")
			c.Writer.Flush()
		case <-cl.wake:
			msgs, resync := cl.take()
			if resync {
				fmt.Fprint(c.Writer, "event: resync\ndata: {\"reason\":\"events missed\"}\n\n")
			}
			for _, m := range msgs {
				data, err := json.Marshal(m.view)
				if err != nil {
					continue
				}
				fmt.Fprintf(c.Writer, "id: %d\nevent: run\ndata: %s\n\n", m.id, data)
			}
			c.Writer.Flush()
		}
	}
}
