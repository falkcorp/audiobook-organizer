// file: core/integrations/metadata/cache.go
// version: 1.0.0
// guid: 72d542b0-8df4-46f9-8c64-b7e589f09c1b
// last-edited: 2026-11-11

package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Cache is a read-through answer cache stored in the CALLING MODULE's
// keyspace (the values are app types, so they are module data, R8).
//
// A row is the key (provider Ref, Version, QuestionKey) and a JSON entry. A
// provider's answer is written only when it IS an answer: an error is never
// written (a throttle or a 5xx must not look like "nothing found" for the
// next TTL), an empty answer or a ClassNotFound is written with the negative
// TTL, results with the positive TTL.
type Cache[Q any] struct {
	o   CacheOptions[Q]
	now func() time.Time

	mu      sync.Mutex
	flights map[string]*flight
	warned  map[string]struct{} // provider refs whose write failure was logged
}

// CacheOptions configures NewCache.
type CacheOptions[Q any] struct {
	// Keyspace is the module's own, e.g. m/books/pcache/. Required.
	Keyspace storage.Keyspace
	// QuestionKey normalises a question to its cache key; ok=false marks the
	// question uncacheable (it goes live, nothing is read or written).
	// Required.
	QuestionKey func(Q) (key string, ok bool)
	// Version invalidates every question when bumped (searchIdentityVersion's
	// analogue): rows of another Version are never read and Sweep removes them.
	Version string
	// TTL is the positive and negative lifetime per provider. Required. A zero
	// duration means that kind of answer is not cached.
	TTL func(provider integrations.Ref) TTL
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Log receives one warning per provider when a cache write fails (open: a
	// cache never blocks a live answer). nil discards.
	Log *slog.Logger
}

// TTL is one provider's cache lifetimes.
type TTL struct{ Positive, Negative time.Duration }

// NewCache validates opts and returns the Cache.
func NewCache[Q any](opts CacheOptions[Q]) (*Cache[Q], error) {
	switch {
	case opts.Keyspace.Owner() == "":
		return nil, errors.New("metadata: a Cache needs the module's Keyspace")
	case opts.QuestionKey == nil:
		return nil, errors.New("metadata: a Cache needs QuestionKey")
	case opts.TTL == nil:
		return nil, errors.New("metadata: a Cache needs TTL")
	}
	c := &Cache[Q]{o: opts, now: opts.Now, flights: map[string]*flight{}, warned: map[string]struct{}{}}
	if c.now == nil {
		c.now = time.Now
	}
	if c.o.Log == nil {
		c.o.Log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

type cacheMode uint8

const (
	cacheBypass   cacheMode = 1 << iota // skip the read, still write
	cacheReadOnly                       // read, never write
)

type cacheModeKey struct{}

func withCacheMode(ctx context.Context, m cacheMode) context.Context {
	prev, _ := ctx.Value(cacheModeKey{}).(cacheMode)
	return context.WithValue(ctx, cacheModeKey{}, prev|m)
}

// WithCacheBypass skips the cache read and still writes the live answer
// (a user's explicit refresh).
func WithCacheBypass(ctx context.Context) context.Context { return withCacheMode(ctx, cacheBypass) }

// WithCacheReadOnly reads the cache and never writes it (dry runs).
func WithCacheReadOnly(ctx context.Context) context.Context {
	return withCacheMode(ctx, cacheReadOnly)
}

func modeOf(ctx context.Context) cacheMode {
	m, _ := ctx.Value(cacheModeKey{}).(cacheMode)
	return m
}

// entry is the stored value of one answer.
type entry struct {
	Version string          `json:"ver"` // not "v": the codec envelope owns that name
	At      time.Time       `json:"at"`
	Expires time.Time       `json:"exp"`
	Empty   bool            `json:"empty,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (c *Cache[Q]) rowKey(provider integrations.Ref, key string) []byte {
	return keys.Join(keys.Str(provider.String()), keys.Str(c.o.Version), keys.Str(key))
}

// flight is one live call shared by the callers of one (provider, Version,
// key, write mode). Its fields are written before done is closed.
type flight struct {
	done chan struct{}
	// canceled: the leader's own context ended (or it panicked), so its
	// outcome says nothing about the question: followers call for themselves.
	canceled bool
	err      error
	at       time.Time
	result   any // the leader's []R, shared read-only with the followers
}

// Cached answers one provider question through the cache with single-flight
// per (provider, key): of N concurrent identical questions one goes live and
// the others share its answer. A leader whose own context ends does not
// cancel the followers: they call for themselves. Only err == nil answers are
// written; empty answers are written with the negative TTL.
//
// R is stored as JSON. The results a caller receives from a concurrent
// identical question are shared with it: treat them as read-only.
func Cached[Q, R any](ctx context.Context, c *Cache[Q], p Provider[Q, R], q Q) (rs []R, askedAt time.Time, err error) {
	rs, askedAt, _, err = cached(ctx, c, p, q)
	return rs, askedAt, err
}

// cached is Cached plus whether the answer came from the cache.
func cached[Q, R any](ctx context.Context, c *Cache[Q], p Provider[Q, R], q Q) (rs []R, askedAt time.Time, fromCache bool, err error) {
	if c == nil {
		rs, askedAt, err = live(ctx, time.Now, p, q)
		return rs, askedAt, false, err
	}
	key, ok := c.o.QuestionKey(q)
	if !ok {
		rs, askedAt, err = live(ctx, c.now, p, q)
		return rs, askedAt, false, err
	}
	ref := p.Ref()
	mode := modeOf(ctx)
	if mode&cacheBypass == 0 {
		if rs, at, hit := read[Q, R](ctx, c, ref, key); hit {
			return rs, at, true, nil
		}
	}
	fk := ref.String() + "\x00" + c.o.Version + "\x00" + key
	if mode&cacheReadOnly != 0 {
		fk += "\x00ro" // a read-only leader writes nothing, so it cannot serve a writing caller
	}
	for {
		c.mu.Lock()
		f, following := c.flights[fk]
		if !following {
			f = &flight{done: make(chan struct{}), canceled: true}
			c.flights[fk] = f
		}
		c.mu.Unlock()
		if !following {
			rs, askedAt, err = lead(ctx, c, f, fk, p, q, mode)
			return rs, askedAt, false, err
		}
		select {
		case <-f.done:
		case <-ctx.Done():
			return nil, time.Time{}, false, ctx.Err()
		}
		if f.canceled {
			continue // the leader gave up for its own reasons: ask again
		}
		if f.err != nil {
			return nil, time.Time{}, false, f.err
		}
		shared, _ := f.result.([]R)
		return slices.Clone(shared), f.at, false, nil
	}
}

// lead runs the live call of flight f, writes the answer and releases the
// followers.
func lead[Q, R any](ctx context.Context, c *Cache[Q], f *flight, fk string, p Provider[Q, R], q Q, mode cacheMode) (rs []R, at time.Time, err error) {
	defer func() {
		c.mu.Lock()
		delete(c.flights, fk)
		c.mu.Unlock()
		close(f.done)
	}()
	rs, at, err = live(ctx, c.now, p, q)
	f.err, f.at, f.result = err, at, rs
	f.canceled = err != nil && ctx.Err() != nil
	if err == nil && mode&cacheReadOnly == 0 {
		// An answer already obtained is stored even if the caller has just
		// gone away: the next asker gets it.
		write(context.WithoutCancel(ctx), c, p.Ref(), q, rs, at)
	}
	return rs, at, err
}

// live asks the provider. A ClassNotFound is an answer about the query (an
// empty one), never an error and never a failure of the provider (M3.5).
func live[Q, R any](ctx context.Context, now func() time.Time, p Provider[Q, R], q Q) ([]R, time.Time, error) {
	rs, err := p.Search(ctx, q)
	if err != nil {
		if ctx.Err() == nil && integrations.ClassOf(ctx, err) == integrations.ClassNotFound {
			return nil, now(), nil
		}
		return nil, time.Time{}, err
	}
	return rs, now(), nil
}

// read returns a fresh cached answer. Every failure is a miss: a cache never
// blocks a live answer (M3.5).
func read[Q, R any](ctx context.Context, c *Cache[Q], ref integrations.Ref, key string) (rs []R, at time.Time, hit bool) {
	raw, err := c.o.Keyspace.Get(ctx, c.rowKey(ref, key))
	if err != nil {
		return nil, time.Time{}, false
	}
	e, err := codec.JSON[entry](entryVersion).Decode(raw)
	if err != nil || e.Version != c.o.Version || !c.now().Before(e.Expires) {
		return nil, time.Time{}, false
	}
	if e.Empty {
		return nil, e.At, true
	}
	if err := json.Unmarshal(e.Data, &rs); err != nil || len(rs) == 0 {
		return nil, time.Time{}, false
	}
	return rs, e.At, true
}

// write stores one answer under its TTL. A failure is logged once per
// provider and otherwise ignored.
func write[Q, R any](ctx context.Context, c *Cache[Q], ref integrations.Ref, q Q, rs []R, at time.Time) {
	key, ok := c.o.QuestionKey(q)
	if !ok {
		return
	}
	ttl := c.o.TTL(ref)
	d, empty := ttl.Positive, len(rs) == 0
	if empty {
		d = ttl.Negative
	}
	if d <= 0 {
		return
	}
	e := entry{Version: c.o.Version, At: at, Expires: at.Add(d), Empty: empty}
	var err error
	if !empty {
		e.Data, err = json.Marshal(rs)
	}
	var raw []byte
	if err == nil {
		raw, err = codec.JSON[entry](entryVersion).Encode(e)
	}
	if err == nil {
		err = c.o.Keyspace.Set(ctx, c.rowKey(ref, key), raw)
	}
	if err != nil {
		c.warnOnce(ref, err)
	}
}

func (c *Cache[Q]) warnOnce(ref integrations.Ref, err error) {
	c.mu.Lock()
	_, seen := c.warned[ref.String()]
	c.warned[ref.String()] = struct{}{}
	c.mu.Unlock()
	if !seen {
		c.o.Log.Warn("metadata: a cache write failed; answers of this provider are not being cached", "provider", ref.String(), "error", err)
	}
}

// entryVersion is the codec version of entry.
const entryVersion = 1

// Invalidate drops provider's cached answer for key at the current Version.
func (c *Cache[Q]) Invalidate(ctx context.Context, provider integrations.Ref, key string) error {
	if err := c.o.Keyspace.Delete(ctx, c.rowKey(provider, key)); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("metadata: invalidate: %w", err)
	}
	return nil
}

// sweepBatch is how many rows one Sweep commit removes.
const sweepBatch = 500

// Sweep removes expired entries and entries of another Version, and returns
// how many. A row that is not an entry of this cache is left alone: the
// keyspace may still hold a module's legacy rows. A module task runs it (R7
// parallelism declared there: one scan, no per-row work worth fanning out).
func (c *Cache[Q]) Sweep(ctx context.Context) (removed int, err error) {
	now := c.now()
	var dead [][]byte
	for kv, serr := range c.o.Keyspace.Scan(ctx, storage.ScanOptions{}) {
		if serr != nil {
			return 0, fmt.Errorf("metadata: sweep scan: %w", serr)
		}
		e, derr := codec.JSON[entry](entryVersion).Decode(kv.Value)
		if derr != nil {
			continue
		}
		if e.Version != c.o.Version || !now.Before(e.Expires) {
			dead = append(dead, bytes.Clone(kv.Key))
		}
	}
	for chunk := range slices.Chunk(dead, sweepBatch) {
		b := c.o.Keyspace.NewBatch()
		for _, k := range chunk {
			if err := b.Delete(k); err != nil {
				_ = b.Close()
				return removed, fmt.Errorf("metadata: sweep: %w", err)
			}
		}
		err := b.Commit(ctx, storage.NoSync)
		_ = b.Close()
		if err != nil {
			return removed, fmt.Errorf("metadata: sweep: %w", err)
		}
		removed += len(chunk)
	}
	return removed, nil
}
