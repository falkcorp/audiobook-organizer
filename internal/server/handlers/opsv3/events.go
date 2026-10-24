// file: internal/server/handlers/opsv3/events.go
// version: 1.6.0
// guid: 4a1ad0ef-2930-46eb-bac3-35772875dfa4
// last-edited: 2026-10-24

package opsv3

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	"github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// SSE tuning. Heartbeat is the comment line that keeps an idle connection
// open; the ring is how far back Last-Event-ID can resume; ClientBuffer is how
// many unsent events one slow client may hold before its oldest are dropped.
// MaxClients bounds the open streams (each holds a goroutine and a queue);
// one more is answered 503 with Retry-After. MaxClientsPerUser bounds the open streams of one
// bucket: a signed-in user's browser session is one bucket and all of their
// API keys together are another (see streamBucket), so a script using an API
// key cannot use up the session's places and more keys buy no more streams;
// one more is answered 429 with Retry-After. A request with no user (auth disabled, first run) is
// bounded only by MaxClients: there is no account to tell apart, and keying on
// the client address would put every caller behind one reverse proxy into the
// same small bucket. MaxWatched bounds the runs the broker re-reads until they
// settle (see Run).
const (
	DefaultHeartbeat    = 15 * time.Second
	DefaultRingSize     = 512
	DefaultClientBuffer = 128
	DefaultMaxClients   = 64
	DefaultMaxPerUser   = 4
	DefaultMaxWatched   = 256
	defaultFlushEvery   = 250 * time.Millisecond

	// streamRetryAfterSeconds is the Retry-After of both stream refusals,
	// the per-bucket 429 TOO_MANY_STREAMS and the server-wide 503. It is for
	// clients that read it: fetch-based stream readers and non-browser
	// clients (scripts, the SDK). A browser EventSource never sees it: on a
	// non-200 answer it fails the connection and does not reconnect, so the
	// page's own code decides when to open a new one. Long enough that a
	// retry loop backs off, short enough that a reload after closing a tab
	// (or after another client's stream ends) is not locked out.
	streamRetryAfterSeconds = 5
)

// subscribe's refusals: past MaxClients, and past MaxClientsPerUser for one
// bucket.
var (
	ErrTooManyClients     = errors.New("opsv3: too many event stream clients")
	ErrTooManyUserClients = errors.New("opsv3: too many event streams for this user")
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
	FlushEvery        time.Duration
	MaxClients        int
	MaxClientsPerUser int
	MaxWatched        int
}

// message is one numbered event. Its SSE id is "<nonce>-<id>" (eventID).
type message struct {
	id   uint64
	view RunView
	// repairs and fixerID say which Repairs fixer the run belongs to, so each
	// stream can leave out the runs its caller may not read. They are not part of
	// the event body: the broker computes them once, from the record it just
	// read, and each Events loop applies its own caller's verdict.
	repairs bool
	defID   string
	fixerID string
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

	// nonce is this broker's boot id. seq restarts at 0 with the process,
	// so a bare number from before a restart would resume at the wrong
	// place; an id carrying another nonce is told to resync instead.
	nonce string

	mu      sync.Mutex
	seq     uint64
	ring    []message // oldest first, at most opt.RingSize
	clients map[*client]struct{}
	// perUser counts the open streams of each bucket (streamBucket): a
	// signed-in user's session, or all of that user's API keys together.
	perUser map[string]int

	// Lazy start (StartOnFirstSubscriber): Run begins with the first client.
	startCtx  context.Context
	spawn     func(func())
	startOnce sync.Once

	// watch is the runs published while not settled, with the JSON last
	// published for each. Only Run's goroutine touches it.
	watch map[string][]byte
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
	if opt.MaxClients <= 0 {
		opt.MaxClients = DefaultMaxClients
	}
	if opt.MaxClientsPerUser <= 0 {
		opt.MaxClientsPerUser = DefaultMaxPerUser
	}
	if opt.MaxWatched <= 0 {
		opt.MaxWatched = DefaultMaxWatched
	}
	return &Broker{runs: runs, hub: hub, zombies: zombies, opt: opt, nonce: newNonce(),
		clients: map[*client]struct{}{}, perUser: map[string]int{}, watch: map[string][]byte{}}
}

func newNonce() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// eventID is the SSE id of event n.
func (b *Broker) eventID(n uint64) string { return b.nonce + "-" + strconv.FormatUint(n, 10) }

// parseEventID reads a Last-Event-ID this broker issued. ok is false for an
// id from another process (a different nonce) or one that does not parse.
func (b *Broker) parseEventID(raw string) (n uint64, ok bool) {
	nonce, seq, found := strings.Cut(raw, "-")
	if !found || nonce != b.nonce {
		return 0, false
	}
	n, err := strconv.ParseUint(seq, 10, 64)
	return n, err == nil
}

// StartOnFirstSubscriber makes the first subscribe start Run(ctx) through
// spawn (the server's tracked goroutine group), so a server nobody streams
// from never runs the loop. Nothing starts once ctx is done.
func (b *Broker) StartOnFirstSubscriber(ctx context.Context, spawn func(func())) {
	b.mu.Lock()
	b.startCtx, b.spawn = ctx, spawn
	b.mu.Unlock()
}

func (b *Broker) ensureRunning() {
	b.mu.Lock()
	ctx, spawn := b.startCtx, b.spawn
	b.mu.Unlock()
	if spawn == nil || ctx.Err() != nil {
		return
	}
	b.startOnce.Do(func() { spawn(func() { b.Run(ctx) }) })
}

// Run drains the feed until ctx ends. The feed's events name a run; each
// named run is re-read once per FlushEvery and published with its progress
// chunks and workers.
//
// This is one goroutine on purpose: the per-tick work is one store read per
// run that changed since the last tick, and a run that is busy changes
// constantly, so the cost is bounded by the number of active runs (a handful),
// not by the number of progress events.
//
// The feed itself is lossy: the registry's EventHub drops an event for a
// subscriber whose 64-slot buffer is full. A dropped op.terminal would leave
// clients showing the run running forever, so every run published while not
// settled is also re-read each tick until a read finds it settled, and
// published again when it changed. That set is bounded by MaxWatched.
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
			b.recheckWatched()
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
	zs := b.zombieIDs()
	for id := range dirty {
		delete(dirty, id)
		b.refresh(id, zs, true)
	}
}

func (b *Broker) zombieIDs() map[string]bool {
	var zs map[string]bool
	if b.zombies != nil {
		for _, z := range b.zombies.Zombies() {
			if zs == nil {
				zs = map[string]bool{}
			}
			zs[z.OpID] = true
		}
	}
	return zs
}

// refresh reads run id and publishes it: always when an event named it
// (always), otherwise only when it differs from what was last published. It
// keeps the watch set: an unsettled run is watched, a settled or vanished one
// is not.
func (b *Broker) refresh(id string, zs map[string]bool, always bool) {
	rec, err := b.runs.GetRunV3(id)
	if err != nil {
		eventsLog.Warn("read run %s for event failed: %v", logger.SanitizeLogValue(id), err)
		return
	}
	if rec == nil {
		delete(b.watch, id)
		return
	}
	v := toView(*rec, zs)
	data, _ := json.Marshal(v)
	last, watched := b.watch[id]
	if always || !watched || string(last) != string(data) {
		b.publishScoped(v, scopeOf(rec))
	}
	if state.ClassifyV3(rec.State).Settled && !zs[id] {
		delete(b.watch, id)
		return
	}
	if watched || len(b.watch) < b.opt.MaxWatched {
		b.watch[id] = data
	}
}

// recheckWatched re-reads every watched run (see Run).
func (b *Broker) recheckWatched() {
	if len(b.watch) == 0 {
		return
	}
	zs := b.zombieIDs()
	ids := make([]string, 0, len(b.watch))
	for id := range b.watch {
		ids = append(ids, id)
	}
	for _, id := range ids {
		b.refresh(id, zs, false)
	}
}

// publish numbers v, stores it in the ring and offers it to every client
// without blocking.
func (b *Broker) publish(v RunView) { b.publishScoped(v, runScope{}) }

// runScope is the Repairs fixer a published run belongs to; the zero value is a
// run that is not a Repairs run.
type runScope struct {
	repairs bool
	defID   string
	fixerID string
}

// scopeOf is rec's runScope: a Repairs run carries its definition and fixer ("" when
// it states none, which no caller may read).
func scopeOf(rec *database.RunRecord) runScope {
	if !opaccess.IsRepairsOp(rec.DefID) {
		return runScope{}
	}
	return runScope{repairs: true, defID: rec.DefID, fixerID: opaccess.RunFixerID(rec)}
}

func (b *Broker) publishScoped(v RunView, sc runScope) {
	b.mu.Lock()
	b.seq++
	m := message{id: b.seq, view: v, repairs: sc.repairs, defID: sc.defID, fixerID: sc.fixerID}
	b.ring = append(b.ring, m)
	if len(b.ring) > b.opt.RingSize {
		b.ring = append(b.ring[:0], b.ring[len(b.ring)-b.opt.RingSize:]...)
	}
	for c := range b.clients {
		c.push(m)
	}
	b.mu.Unlock()
}

// streamBucket is the per-user cap's key. A signed-in user has two buckets
// of MaxClientsPerUser each: one for their browser session (any request not
// authenticated by an API key) and one shared by ALL of their API keys. So a
// script on a key cannot take the session's places, and minting more keys
// buys no more streams: one user holds at most 2 x MaxClientsPerUser. (A
// bucket per key would let a user multiply their allowance by minting keys,
// bounded only by MaxClients; a per-user total over per-key buckets would let
// keys fill the total and starve the session again.) It is "" when the
// request has no user: there is no cap then.
func streamBucket(c *gin.Context) string {
	u, ok := auth.UserFromContext(c.Request.Context())
	if !ok {
		return ""
	}
	if _, ok := middleware.CurrentAPIKey(c); ok {
		return apiKeyBucket(u.ID)
	}
	return sessionBucket(u.ID)
}

// sessionBucket and apiKeyBucket join the user id and the credential kind
// with a NUL, which a user id cannot contain.
func sessionBucket(userID string) string { return userID + "\x00session" }
func apiKeyBucket(userID string) string  { return userID + "\x00apikey" }

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

// resumeMode is what a new client asked for.
type resumeMode int

const (
	resumeNone   resumeMode = iota // no Last-Event-ID: live events only
	resumeAfter                    // replay the ring after lastID
	resumeResync                   // an id this broker cannot resume from
)

// subscribe registers a client. resumeAfter first queues the ring's events
// after lastID; when the ring no longer reaches back that far (or for
// resumeResync: an id of another process, or one that does not parse) the
// client is marked to resync instead. Past MaxClients it refuses, and past
// MaxClientsPerUser streams of the same bucket (a non-empty bucket, see
// streamBucket).
func (b *Broker) subscribe(bucket string, lastID uint64, mode resumeMode) (*client, func(), error) {
	c := newClient(b.opt.ClientBuffer)
	b.mu.Lock()
	if len(b.clients) >= b.opt.MaxClients {
		b.mu.Unlock()
		return nil, nil, ErrTooManyClients
	}
	if bucket != "" && b.perUser[bucket] >= b.opt.MaxClientsPerUser {
		b.mu.Unlock()
		return nil, nil, ErrTooManyUserClients
	}
	switch mode {
	case resumeResync:
		c.resync = true
	case resumeAfter:
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
	if bucket != "" {
		b.perUser[bucket]++
	}
	b.mu.Unlock()
	b.ensureRunning()
	var once sync.Once
	return c, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.clients, c)
			if bucket != "" {
				if b.perUser[bucket]--; b.perUser[bucket] <= 0 {
					delete(b.perUser, bucket)
				}
			}
			b.mu.Unlock()
		})
	}, nil
}

// Events implements GET /api/v3/ops/events: text/event-stream of
// `event: run` (a RunView, with progress.chunks and progress.workers), a
// `: heartbeat` comment every 15 s, and `event: resync` when events were
// missed (a client too slow for its buffer, a Last-Event-ID the ring no
// longer holds, or one from before a restart): reload /runs, then keep
// reading. Event ids are "<boot nonce>-<sequence>". The stream ends when the
// client leaves or the server starts shutting down (Deps.Closing). Past
// MaxClients open streams the answer is 503; past MaxClientsPerUser streams
// of the caller's bucket (streamBucket: the user's session, or all of the
// user's API keys) it is 429 with a
// Retry-After header. The loop ends when a write to the client fails.
func (h *Handler) Events(c *gin.Context) {
	b := h.d.Broker
	if b == nil {
		httputil.RespondWithServiceUnavailable(c, "operations event feed not initialized")
		return
	}
	var lastID uint64
	mode := resumeNone
	if raw := c.GetHeader("Last-Event-ID"); raw != "" {
		mode = resumeResync
		if n, ok := b.parseEventID(raw); ok {
			lastID, mode = n, resumeAfter
		}
	}
	cl, unsubscribe, err := b.subscribe(streamBucket(c), lastID, mode)
	if errors.Is(err, ErrTooManyUserClients) {
		c.Header("Retry-After", strconv.Itoa(streamRetryAfterSeconds))
		httputil.RespondWithError(c, http.StatusTooManyRequests, "too many open operations event streams for this user; close one and retry", "TOO_MANY_STREAMS")
		return
	}
	if err != nil {
		// ErrTooManyClients is the only other refusal: the server-wide cap,
		// which frees as any client's stream ends.
		c.Header("Retry-After", strconv.Itoa(streamRetryAfterSeconds))
		httputil.RespondWithServiceUnavailable(c, "too many open operations event streams; try again shortly")
		return
	}
	defer unsubscribe()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	if _, err := fmt.Fprint(c.Writer, ": heartbeat\n\n"); err != nil {
		return
	}
	c.Writer.Flush()

	hb := time.NewTicker(b.opt.Heartbeat)
	defer hb.Stop()
	done := c.Request.Context().Done()
	closing := h.d.Closing // nil blocks forever
	// A Repairs run the caller may not read never goes to this stream, whether it
	// is live or replayed from the ring. The verdict is per fixer, read once.
	reader := h.d.RepairsAccess.For(c)
	// A subscribe that queued a replay or a resync has something to send now.
	cl.signal()
	for {
		select {
		case <-done:
			return
		case <-closing:
			return
		case <-hb.C:
			if _, err := fmt.Fprint(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		case <-cl.wake:
			msgs, resync := cl.take()
			if resync {
				if _, err := fmt.Fprint(c.Writer, "event: resync\ndata: {\"reason\":\"events missed\"}\n\n"); err != nil {
					return
				}
			}
			for _, m := range msgs {
				if m.repairs && !reader.MayReadFixer(m.defID, m.fixerID) {
					continue
				}
				data, err := json.Marshal(m.view)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(c.Writer, "id: %s\nevent: run\ndata: %s\n\n", b.eventID(m.id), data); err != nil {
					return
				}
			}
			c.Writer.Flush()
		}
	}
}
