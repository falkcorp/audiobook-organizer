// file: internal/searchcache/cache.go
// version: 1.0.0
// guid: bcadc16f-696c-468a-a3e4-afea4c81bc5c
// last-edited: 2026-09-25

package searchcache

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var cacheLog = logger.New("searchcache")

// DefaultMaxBytes is the cache's memory cap when none is configured (owner
// decision 2026-09-25: 128 MiB).
const DefaultMaxBytes int64 = 128 << 20

// DefaultWait is how long a caller waits for a new build before it is told the
// search is still running.
const DefaultWait = 20 * time.Second

// DefaultPatchLimit caps how many changed books one incremental patch will
// insert. Each insertion is a binary search whose probes may each cost a small
// query, so past this a rebuild is cheaper.
const DefaultPatchLimit = 64

// idOverheadBytes approximates the per-ID cost beyond its bytes: the string
// header in the []string backing array.
const idOverheadBytes = 16

// entryOverheadBytes approximates an entry's fixed cost (struct, list element,
// map slot).
const entryOverheadBytes = 128

// Evaluator computes and maintains one cached result. The cache calls it
// detached from any request context, so it must not depend on a request being
// alive.
type Evaluator interface {
	// Build returns the full ranked list of matching book IDs. progress may be
	// called with the number of matches found so far.
	Build(ctx context.Context, progress func(matches int)) ([]string, error)
	// Match re-evaluates ids against the query and returns the ones that match
	// now, in result order. ok=false means this evaluator cannot patch (the
	// caller then rebuilds).
	Match(ctx context.Context, ids []string) (matching []string, ok bool, err error)
	// Less reports whether a ranks before b in the result order. Both are
	// matching IDs.
	Less(ctx context.Context, a, b string) (bool, error)
}

// Config sizes a Cache. Zero fields take the defaults.
type Config struct {
	MaxBytes   int64
	Wait       time.Duration
	PatchLimit int
	// JobTTL is how long a finished build stays pollable by its search ID.
	JobTTL time.Duration
	// MaxJobs bounds the pollable job table; the key is user input.
	MaxJobs int
}

// Result is one lookup's answer. IDs is shared with the cache: callers slice
// it and must never modify it.
type Result struct {
	IDs []string
	// Stale is true when IDs predate a change the cache could not patch in
	// place; a rebuild is running in the background.
	Stale bool
	// Hit is true when no build ran for this lookup.
	Hit bool
}

// PendingError is returned when a build outlives the caller's wait. The build
// keeps running and its result is cached; SearchID can be polled with Job.
type PendingError struct {
	SearchID     string
	MatchesSoFar int
}

func (e *PendingError) Error() string {
	return fmt.Sprintf("search %s still running (%d matches so far)", e.SearchID, e.MatchesSoFar)
}

// JobStatus is what a poll of a search ID reports.
type JobStatus struct {
	SearchID     string `json:"search_id"`
	Status       string `json:"status"` // running | done | error
	MatchesSoFar int    `json:"matches_so_far"`
	Error        string `json:"error,omitempty"`
}

// Stats is a point-in-time counter snapshot.
type Stats struct {
	Entries  int
	Bytes    int64
	Hits     int64
	Misses   int64
	Patches  int64
	Rebuilds int64
	Evicted  int64
}

type entry struct {
	key   string
	ids   []string
	gen   uint64
	bytes int64
	elem  *list.Element
}

type job struct {
	id       string
	key      string
	done     chan struct{}
	ids      []string
	err      error
	matches  atomic.Int64
	finished time.Time
}

// Cache is the shared search result cache. Safe for concurrent use.
type Cache struct {
	changes *ChangeLog
	cfg     Config

	mu       sync.Mutex
	entries  map[string]*entry
	lru      *list.List // front = most recently used
	bytes    int64
	building map[string]*job // key -> in-flight build
	jobs     map[string]*job // search ID -> build (running or recently finished)

	patchSF singleflight.Group

	hits, misses, patches, rebuilds, evicted atomic.Int64
}

// New returns a cache that invalidates against changes.
func New(changes *ChangeLog, cfg Config) *Cache {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.Wait <= 0 {
		cfg.Wait = DefaultWait
	}
	if cfg.PatchLimit <= 0 {
		cfg.PatchLimit = DefaultPatchLimit
	}
	if cfg.JobTTL <= 0 {
		cfg.JobTTL = 10 * time.Minute
	}
	if cfg.MaxJobs <= 0 {
		cfg.MaxJobs = 1024
	}
	return &Cache{
		changes:  changes,
		cfg:      cfg,
		entries:  map[string]*entry{},
		lru:      list.New(),
		building: map[string]*job{},
		jobs:     map[string]*job{},
	}
}

// Changes is the change log this cache invalidates against.
func (c *Cache) Changes() *ChangeLog { return c.changes }

// DefaultWait is the configured wait for a new build.
func (c *Cache) DefaultWait() time.Duration { return c.cfg.Wait }

// Lookup returns the ranked ID list for key, building, patching or rebuilding
// it through ev as needed. wait <= 0 means the configured default.
//
// A build runs detached from ctx: if ctx ends first, Lookup returns ctx.Err()
// and the build still completes and is cached. If the build outlives wait,
// Lookup returns a *PendingError naming a pollable search ID.
func (c *Cache) Lookup(ctx context.Context, key string, ev Evaluator, wait time.Duration) (Result, error) {
	if wait <= 0 {
		wait = c.cfg.Wait
	}
	gen := c.changes.Generation()
	c.mu.Lock()
	e := c.entries[key]
	if e != nil {
		c.lru.MoveToFront(e.elem)
		ids, egen := e.ids, e.gen
		c.mu.Unlock()
		if egen >= gen {
			c.hits.Add(1)
			return Result{IDs: ids, Hit: true}, nil
		}
		return c.refresh(ctx, key, ev)
	}
	c.misses.Add(1)
	j := c.startBuildLocked(key, ev)
	c.mu.Unlock()
	return c.await(ctx, j, wait)
}

// refresh brings an entry built at an older generation up to date: patched in
// place when the change ring still covers it, otherwise served stale while a
// background rebuild runs. Concurrent refreshes of one key share one patch.
func (c *Cache) refresh(ctx context.Context, key string, ev Evaluator) (Result, error) {
	v, _, _ := c.patchSF.Do(key, func() (any, error) {
		c.mu.Lock()
		e := c.entries[key]
		c.mu.Unlock()
		if e == nil {
			// Evicted between the read and here: treat as a stale miss.
			return Result{}, nil
		}
		if e.gen >= c.changes.Generation() {
			return Result{IDs: e.ids, Hit: true}, nil
		}
		changed, current, ok := c.changes.ChangedSince(e.gen)
		if ok {
			ids, patched := c.patch(context.WithoutCancel(ctx), e.ids, changed, ev)
			if patched {
				c.patches.Add(1)
				c.store(key, ids, current)
				return Result{IDs: ids}, nil
			}
		}
		// Ring overflow, or a change this evaluator cannot patch: serve what
		// we have, flagged, and rebuild in the background.
		c.mu.Lock()
		c.startBuildLocked(key, ev)
		c.mu.Unlock()
		return Result{IDs: e.ids, Stale: true}, nil
	})
	res := v.(Result)
	if res.IDs == nil && !res.Hit && !res.Stale {
		// The entry vanished (evicted); fall back to a normal lookup.
		c.mu.Lock()
		j := c.startBuildLocked(key, ev)
		c.mu.Unlock()
		return c.await(ctx, j, c.cfg.Wait)
	}
	return res, nil
}

// patch removes every changed ID from ids and binary-inserts the ones that
// still match. false means the patch was not possible (the caller rebuilds).
func (c *Cache) patch(ctx context.Context, ids, changed []string, ev Evaluator) ([]string, bool) {
	matching, ok, err := ev.Match(ctx, changed)
	if err != nil {
		cacheLog.Warn("search cache: patch re-evaluation failed, rebuilding instead: %v", err)
		return nil, false
	}
	if !ok || len(matching) > c.cfg.PatchLimit {
		return nil, false
	}
	drop := make(map[string]struct{}, len(changed))
	for _, id := range changed {
		drop[id] = struct{}{}
	}
	out := make([]string, 0, len(ids)+len(matching))
	for _, id := range ids {
		if _, gone := drop[id]; !gone {
			out = append(out, id)
		}
	}
	for _, m := range matching {
		var lessErr error
		pos := sort.Search(len(out), func(i int) bool {
			if lessErr != nil {
				return true
			}
			less, err := ev.Less(ctx, m, out[i])
			if err != nil {
				lessErr = err
				return true
			}
			return less
		})
		if lessErr != nil {
			cacheLog.Warn("search cache: patch ordering failed, rebuilding instead: %v", lessErr)
			return nil, false
		}
		out = append(out, "")
		copy(out[pos+1:], out[pos:])
		out[pos] = m
	}
	return out, true
}

// startBuildLocked joins the in-flight build for key or starts one. c.mu held.
func (c *Cache) startBuildLocked(key string, ev Evaluator) *job {
	if j := c.building[key]; j != nil {
		return j
	}
	c.pruneJobsLocked()
	j := &job{id: newSearchID(), key: key, done: make(chan struct{})}
	c.building[key] = j
	c.jobs[j.id] = j
	c.rebuilds.Add(1)
	go c.run(j, ev)
	return j
}

func (c *Cache) run(j *job, ev Evaluator) {
	// The generation is read BEFORE the build starts, so any write that lands
	// while it runs leaves the entry older than the log and gets patched on
	// the next read.
	gen := c.changes.Generation()
	ids, err := func() (ids []string, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("search build panicked: %v", r)
			}
		}()
		return ev.Build(context.Background(), func(n int) { j.matches.Store(int64(n)) })
	}()
	if err == nil {
		if ids == nil {
			ids = []string{}
		}
		j.matches.Store(int64(len(ids)))
		c.store(j.key, ids, gen)
	} else {
		cacheLog.Warn("search cache: build failed: %v", err)
	}
	c.mu.Lock()
	j.ids, j.err, j.finished = ids, err, time.Now()
	if c.building[j.key] == j {
		delete(c.building, j.key)
	}
	c.mu.Unlock()
	close(j.done)
}

func (c *Cache) await(ctx context.Context, j *job, wait time.Duration) (Result, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-j.done:
		if j.err != nil {
			return Result{}, j.err
		}
		return Result{IDs: j.ids}, nil
	case <-timer.C:
		return Result{}, &PendingError{SearchID: j.id, MatchesSoFar: int(j.matches.Load())}
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// store publishes ids for key at gen and evicts least-recently-used entries
// until the cache fits its cap. An older build never replaces a newer entry.
func (c *Cache) store(key string, ids []string, gen uint64) {
	size := int64(entryOverheadBytes + len(key))
	for _, id := range ids {
		size += int64(idOverheadBytes + len(id))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.entries[key]; old != nil {
		if old.gen > gen {
			return
		}
		c.removeLocked(old)
	}
	if size > c.cfg.MaxBytes {
		// Larger than the whole cache: serve it to the waiters, keep nothing.
		return
	}
	e := &entry{key: key, ids: ids, gen: gen, bytes: size}
	e.elem = c.lru.PushFront(e)
	c.entries[key] = e
	c.bytes += size
	for c.bytes > c.cfg.MaxBytes {
		back := c.lru.Back()
		if back == nil {
			break
		}
		c.removeLocked(back.Value.(*entry))
		c.evicted.Add(1)
	}
}

func (c *Cache) removeLocked(e *entry) {
	c.lru.Remove(e.elem)
	delete(c.entries, e.key)
	c.bytes -= e.bytes
}

// pruneJobsLocked drops finished jobs past their TTL and, if the table is
// still full, the oldest finished ones. Running jobs are never dropped.
func (c *Cache) pruneJobsLocked() {
	now := time.Now()
	for id, j := range c.jobs {
		if !j.finished.IsZero() && now.Sub(j.finished) > c.cfg.JobTTL {
			delete(c.jobs, id)
		}
	}
	for len(c.jobs) >= c.cfg.MaxJobs {
		var oldestID string
		var oldest time.Time
		for id, j := range c.jobs {
			if j.finished.IsZero() {
				continue
			}
			if oldestID == "" || j.finished.Before(oldest) {
				oldestID, oldest = id, j.finished
			}
		}
		if oldestID == "" {
			return
		}
		delete(c.jobs, oldestID)
	}
}

// Job reports a build by the search ID a *PendingError named.
func (c *Cache) Job(searchID string) (JobStatus, bool) {
	c.mu.Lock()
	j := c.jobs[searchID]
	finished := j != nil && !j.finished.IsZero()
	var err error
	if j != nil {
		err = j.err
	}
	c.mu.Unlock()
	if j == nil {
		return JobStatus{}, false
	}
	st := JobStatus{SearchID: searchID, Status: "running", MatchesSoFar: int(j.matches.Load())}
	if finished {
		st.Status = "done"
		if err != nil {
			st.Status = "error"
			st.Error = err.Error()
		}
	}
	return st, true
}

// Stats returns the cache's counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	n, b := len(c.entries), c.bytes
	c.mu.Unlock()
	return Stats{
		Entries: n, Bytes: b,
		Hits: c.hits.Load(), Misses: c.misses.Load(), Patches: c.patches.Load(),
		Rebuilds: c.rebuilds.Load(), Evicted: c.evicted.Load(),
	}
}

func newSearchID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
