// file: internal/searchcache/cache.go
// version: 2.1.0
// guid: bcadc16f-696c-468a-a3e4-afea4c81bc5c
// last-edited: 2026-09-25

package searchcache

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
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

// DefaultWait is how long a caller that accepts a pending answer waits for a
// new build before it is told the search is still running.
const DefaultWait = 20 * time.Second

// DefaultPatchLimit caps how many changed books one incremental patch will
// insert. Each insertion is a binary search whose probes may each cost a small
// query, so past this a rebuild is cheaper.
const DefaultPatchLimit = 64

// entryOverheadBytes approximates an entry's fixed cost (struct, list element,
// map slot).
const entryOverheadBytes = 128

// ErrNotCurrent is returned to a caller that did not accept stale results
// (LookupOptions.AllowStale false) when the cache cannot produce a list that
// reflects every change recorded before the lookup began. The caller must run
// the search itself; a rebuild has been started for later lookups.
var ErrNotCurrent = errors.New("search cache: no current result")

// ErrBusy is returned when a new build cannot be queued because the bounded
// build queue is full. The caller must run the search itself.
var ErrBusy = errors.New("search cache: build queue full")

// errAbandoned ends a queued build nobody was waiting for any more.
var errAbandoned = errors.New("search abandoned: no caller was waiting for it")

// Evaluator computes and maintains one cached result. The cache calls it
// detached from any request context, so it must not depend on a request being
// alive.
type Evaluator interface {
	// Build returns the full ranked list of matching book IDs. progress may be
	// called with the number of matches found so far. A Build that could not
	// see every candidate (a failed read, a truncated window) must return an
	// error: whatever it returns is cached as the complete answer.
	Build(ctx context.Context, progress func(matches int)) ([]string, error)
	// Match re-evaluates ids against the query and returns the ones that match
	// now, in result order. ok=false means this evaluator cannot patch (the
	// caller then rebuilds).
	Match(ctx context.Context, ids []string) (matching []string, ok bool, err error)
	// Less reports whether a ranks before b in the result order. Both are
	// matching IDs.
	Less(ctx context.Context, a, b string) (bool, error)
}

// OrderDrifter is implemented by an evaluator whose order for UNCHANGED books
// can move when OTHER books change: a relevance order scored against
// corpus-wide statistics (Bleve TF-IDF), where any write shifts every term's
// weight. A patch fixes membership exactly, but only the changed books are
// re-placed, so for such an evaluator the cache serves the patched list and
// also rebuilds it in the background, which restores the exact fresh order.
type OrderDrifter interface {
	OrderDriftsOnPatch() bool
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
	// MaxConcurrentBuilds bounds how many builds run at once (default
	// runtime.NumCPU()). Builds past it queue.
	MaxConcurrentBuilds int
	// MaxQueuedBuilds bounds running plus queued builds (default 4x
	// MaxConcurrentBuilds). A lookup that would start one more gets ErrBusy
	// and runs its search uncached, so user input can never fan out an
	// unbounded number of goroutines.
	MaxQueuedBuilds int
	// AbandonAfter: a queued build that reaches the front with no caller
	// blocked on it and no lookup or poll for this long is dropped instead
	// of run (default 1 minute; the web client polls every second).
	AbandonAfter time.Duration
}

// LookupOptions says what a caller can accept.
type LookupOptions struct {
	// Wait bounds how long the caller waits for a build or a patch when it
	// accepts a pending (AllowPending) or stale (AllowStale) answer. <= 0
	// means the configured default.
	Wait time.Duration
	// AllowPending lets a build that outlives Wait end the lookup with a
	// *PendingError naming a pollable search. Without it the caller waits
	// until the build ends or ctx does.
	AllowPending bool
	// AllowStale lets the lookup return a list that predates changes it could
	// not patch (Result.Stale), while a rebuild runs in the background.
	// Without it the result always reflects every change recorded before the
	// lookup began, or the lookup fails with ErrNotCurrent.
	AllowStale bool
}

// Result is one lookup's answer. IDs is shared with the cache: callers slice
// it and must never modify it.
type Result struct {
	IDs []string
	// Stale is true when IDs predate a change the cache could not patch in
	// place; a rebuild is running in the background. Only returned to callers
	// that set AllowStale.
	Stale bool
	// Hit is true when no build or patch ran for this lookup.
	Hit bool
	// Gen is the change generation IDs are current to: every change recorded
	// at or below it is reflected. A caller that caches something derived
	// from IDs must stamp it with Gen, not with a generation it read itself.
	Gen uint64
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
	Status       string `json:"status"` // queued | running | done | error
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
	Jobs     int
	Building int
}

type entry struct {
	key   string
	ids   []string
	gen   uint64
	bytes int64
	elem  *list.Element
}

type job struct {
	id      string
	key     string
	done    chan struct{}
	matches atomic.Int64
	// interest is the UnixNano of the last lookup that joined this job or
	// poll that asked about it; a queued build nobody asked about since it
	// was queued is abandoned before it starts.
	interest atomic.Int64
	started  atomic.Bool

	// Guarded by Cache.mu.
	gen      uint64 // generation read just before Build ran
	ids      []string
	err      error
	finished time.Time
	// waiters counts lookups blocked in await. The result is kept on the job
	// only while one of them still has to read it: the job table outlives the
	// build (for polling) and must not pin an ID list outside the cap.
	waiters int
}

// Cache is the shared search result cache. Safe for concurrent use.
type Cache struct {
	changes *ChangeLog
	cfg     Config
	sem     chan struct{}

	mu       sync.Mutex
	entries  map[string]*entry
	lru      *list.List // front = most recently used
	bytes    int64
	building map[string]*job // key -> in-flight build (queued or running)
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
	if cfg.MaxConcurrentBuilds <= 0 {
		cfg.MaxConcurrentBuilds = runtime.NumCPU()
	}
	if cfg.MaxQueuedBuilds <= 0 {
		cfg.MaxQueuedBuilds = 4 * cfg.MaxConcurrentBuilds
	}
	if cfg.AbandonAfter <= 0 {
		cfg.AbandonAfter = time.Minute
	}
	if cfg.MaxQueuedBuilds < cfg.MaxConcurrentBuilds {
		cfg.MaxQueuedBuilds = cfg.MaxConcurrentBuilds
	}
	return &Cache{
		changes:  changes,
		cfg:      cfg,
		sem:      make(chan struct{}, cfg.MaxConcurrentBuilds),
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
// it through ev as needed, within what opts accepts.
//
// A build runs detached from ctx: if ctx ends first, Lookup returns ctx.Err()
// and the build still completes and is cached.
func (c *Cache) Lookup(ctx context.Context, key string, ev Evaluator, opts LookupOptions) (Result, error) {
	if opts.Wait <= 0 {
		opts.Wait = c.cfg.Wait
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
			return Result{IDs: ids, Hit: true, Gen: egen}, nil
		}
		return c.refresh(ctx, key, ev, opts, gen)
	}
	c.misses.Add(1)
	j, err := c.startBuildLocked(key, ev)
	if err != nil {
		c.mu.Unlock()
		return Result{}, err
	}
	c.addWaiterLocked(j)
	c.mu.Unlock()
	res, err := c.await(ctx, j, opts)
	if err != nil {
		return res, err
	}
	if res.Gen >= gen {
		return res, nil
	}
	// A build this lookup joined started before a change the lookup must
	// reflect. Patch it forward, exactly like an older entry.
	return c.bringForward(ctx, key, ev, opts, res)
}

// refresh brings an entry built at an older generation up to date.
func (c *Cache) refresh(ctx context.Context, key string, ev Evaluator, opts LookupOptions, need uint64) (Result, error) {
	ch := c.patchSF.DoChan(key, func() (any, error) {
		// Detached: the patch is shared by every caller of this key and its
		// result is stored, so no single caller's ctx may cancel it.
		return c.patchEntry(key, ev), nil
	})
	var timeout <-chan time.Time
	if opts.AllowStale || opts.AllowPending {
		t := time.NewTimer(opts.Wait)
		defer t.Stop()
		timeout = t.C
	}
	var pr patchResult
	select {
	case r := <-ch:
		pr = r.Val.(patchResult)
	case <-timeout:
		// The patch outlived the wait. It keeps running and will be stored.
		c.mu.Lock()
		e := c.entries[key]
		c.mu.Unlock()
		if opts.AllowStale && e != nil {
			return Result{IDs: e.ids, Stale: true, Gen: e.gen}, nil
		}
		// A caller that accepts only a pending answer waits on the patch.
		select {
		case r := <-ch:
			pr = r.Val.(patchResult)
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	switch {
	case pr.evicted:
		// Evicted before the patch read it: a miss, under this caller's
		// options (not the cache default).
		c.misses.Add(1)
		c.mu.Lock()
		j, err := c.startBuildLocked(key, ev)
		if err != nil {
			c.mu.Unlock()
			return Result{}, err
		}
		c.addWaiterLocked(j)
		c.mu.Unlock()
		res, err := c.await(ctx, j, opts)
		if err != nil {
			return res, err
		}
		if res.Gen >= need {
			return res, nil
		}
		return c.bringForward(ctx, key, ev, opts, res)
	case pr.res.Gen >= need:
		return pr.res, nil
	case opts.AllowStale:
		pr.res.Stale = true
		return pr.res, nil
	default:
		return Result{}, ErrNotCurrent
	}
}

// bringForward patches res, a finished build's list current to res.Gen, to the
// current generation. Used when a lookup joined a build that started before a change the
// lookup must reflect.
func (c *Cache) bringForward(ctx context.Context, key string, ev Evaluator, opts LookupOptions, res Result) (Result, error) {
	changed, current, ok := c.changes.ChangedSince(res.Gen)
	if ok {
		if ids, patched := c.patch(context.WithoutCancel(ctx), res.IDs, changed, ev); patched {
			c.patches.Add(1)
			c.store(key, ids, current)
			c.afterPatch(key, ev)
			return Result{IDs: ids, Gen: current}, nil
		}
	}
	c.queueRebuild(key, ev)
	if opts.AllowStale {
		res.Stale = true
		return res, nil
	}
	return Result{}, ErrNotCurrent
}

type patchResult struct {
	res     Result
	evicted bool
}

// patchEntry patches key's entry to the current generation, or, when that is
// not possible, starts a background rebuild and reports the old list (whose
// Gen tells the caller it is not current).
func (c *Cache) patchEntry(key string, ev Evaluator) patchResult {
	c.mu.Lock()
	e := c.entries[key]
	c.mu.Unlock()
	if e == nil {
		return patchResult{evicted: true}
	}
	if e.gen >= c.changes.Generation() {
		return patchResult{res: Result{IDs: e.ids, Hit: true, Gen: e.gen}}
	}
	changed, current, ok := c.changes.ChangedSince(e.gen)
	if ok {
		if ids, patched := c.patch(context.Background(), e.ids, changed, ev); patched {
			c.patches.Add(1)
			c.store(key, ids, current)
			c.afterPatch(key, ev)
			return patchResult{res: Result{IDs: ids, Gen: current}}
		}
	}
	// Ring overflow, or a change this evaluator cannot patch: rebuild in the
	// background and report what we have.
	c.queueRebuild(key, ev)
	return patchResult{res: Result{IDs: e.ids, Gen: e.gen}}
}

// afterPatch schedules the drift-correcting rebuild for an OrderDrifter.
func (c *Cache) afterPatch(key string, ev Evaluator) {
	if d, ok := ev.(OrderDrifter); ok && d.OrderDriftsOnPatch() {
		c.queueRebuild(key, ev)
	}
}

// queueRebuild starts (or joins) a background rebuild of key that no caller
// waits on. The only failure is ErrBusy (the build queue is full): the entry
// then stays as it is, and the next lookup that finds it out of date asks
// again, so nothing is lost; it is logged so a saturated queue is visible.
func (c *Cache) queueRebuild(key string, ev Evaluator) {
	c.mu.Lock()
	_, err := c.startBuildLocked(key, ev)
	c.mu.Unlock()
	if err != nil {
		cacheLog.Warn("search cache: background rebuild not queued: %v", err)
	}
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
	// Only changed IDs may be inserted: an evaluator that answered with a book
	// it was not asked about would otherwise duplicate it in the list.
	kept := matching[:0:0]
	for _, m := range matching {
		if _, asked := drop[m]; asked {
			kept = append(kept, m)
		}
	}
	matching = kept
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

// startBuildLocked joins the in-flight build for key or queues one. c.mu held.
func (c *Cache) startBuildLocked(key string, ev Evaluator) (*job, error) {
	if j := c.building[key]; j != nil {
		j.interest.Store(time.Now().UnixNano())
		return j, nil
	}
	if len(c.building) >= c.cfg.MaxQueuedBuilds {
		return nil, ErrBusy
	}
	c.pruneJobsLocked()
	j := &job{id: newSearchID(), key: key, done: make(chan struct{})}
	j.interest.Store(time.Now().UnixNano())
	c.building[key] = j
	c.jobs[j.id] = j
	c.rebuilds.Add(1)
	go c.run(j, ev)
	return j, nil
}

func (c *Cache) addWaiterLocked(j *job) { j.waiters++ }

func (c *Cache) run(j *job, ev Evaluator) {
	c.sem <- struct{}{}
	defer func() { <-c.sem }()

	c.mu.Lock()
	idle := j.waiters == 0 && time.Since(time.Unix(0, j.interest.Load())) > c.cfg.AbandonAfter
	c.mu.Unlock()
	var (
		ids []string
		err error
		gen uint64
	)
	if idle {
		// Queued past AbandonAfter with nobody blocked on it and nobody polling:
		// whoever asked has gone, and the next lookup of the key queues a
		// fresh build.
		err = errAbandoned
	} else {
		j.started.Store(true)
		// The generation is read BEFORE the build starts, so any write that
		// lands while it runs leaves the entry older than the log and gets
		// patched on the next read.
		gen = c.changes.Generation()
		ids, err = func() (ids []string, err error) {
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
	}
	c.mu.Lock()
	j.gen, j.err, j.finished = gen, err, time.Now()
	if j.waiters > 0 {
		j.ids = ids
	}
	if c.building[j.key] == j {
		delete(c.building, j.key)
	}
	c.mu.Unlock()
	close(j.done)
}

// await waits for j under opts. The caller registered as a waiter.
func (c *Cache) await(ctx context.Context, j *job, opts LookupOptions) (Result, error) {
	defer func() {
		c.mu.Lock()
		j.waiters--
		if j.waiters == 0 && !j.finished.IsZero() {
			j.ids = nil
		}
		c.mu.Unlock()
	}()
	var timeout <-chan time.Time
	if opts.AllowPending {
		t := time.NewTimer(opts.Wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-j.done:
		c.mu.Lock()
		ids, gen, err := j.ids, j.gen, j.err
		c.mu.Unlock()
		if err != nil {
			return Result{}, err
		}
		return Result{IDs: ids, Gen: gen}, nil
	case <-timeout:
		return Result{}, &PendingError{SearchID: j.id, MatchesSoFar: int(j.matches.Load())}
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// entrySize is what an entry is charged against the cap: the backing array
// (cap, not len: 16 B per string header) plus each ID's allocation at Go's
// allocator size class, which is what a decoded ID actually occupies.
func entrySize(key string, ids []string) int64 {
	size := int64(entryOverheadBytes + len(key))
	size += int64(cap(ids)) * 16
	for _, id := range ids {
		size += allocSize(len(id))
	}
	return size
}

// allocSize rounds n up to the Go runtime's small-object size class (the
// classes up to 1 KiB); past that, to 8 bytes.
func allocSize(n int) int64 {
	classes := [...]int{8, 16, 24, 32, 48, 64, 80, 96, 112, 128, 144, 160, 176, 192, 208, 224, 240, 256, 288, 320, 352, 384, 416, 448, 480, 512, 576, 640, 704, 768, 896, 1024}
	if n <= 0 {
		return 0
	}
	for _, s := range classes {
		if n <= s {
			return int64(s)
		}
	}
	return int64((n + 7) &^ 7)
}

// store publishes ids for key at gen and evicts least-recently-used entries
// until the cache fits its cap. An older build never replaces a newer entry.
func (c *Cache) store(key string, ids []string, gen uint64) {
	size := entrySize(key, ids)
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
// still full, the oldest finished ones. Running jobs are never dropped; their
// number is bounded by MaxQueuedBuilds.
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

// Job reports a build by the search ID a *PendingError named. A poll counts
// as interest, so a queued build a client is polling is not abandoned.
func (c *Cache) Job(searchID string) (JobStatus, bool) {
	c.mu.Lock()
	c.pruneJobsLocked()
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
	j.interest.Store(time.Now().UnixNano())
	st := JobStatus{SearchID: searchID, Status: "running", MatchesSoFar: int(j.matches.Load())}
	if !j.started.Load() {
		st.Status = "queued"
	}
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
	n, b, nj, nb := len(c.entries), c.bytes, len(c.jobs), len(c.building)
	c.mu.Unlock()
	return Stats{
		Entries: n, Bytes: b,
		Hits: c.hits.Load(), Misses: c.misses.Load(), Patches: c.patches.Load(),
		Rebuilds: c.rebuilds.Load(), Evicted: c.evicted.Load(),
		Jobs: nj, Building: nb,
	}
}

// jobHoldsIDs reports whether the finished job for searchID still references
// a result list. Test hook for the job-memory contract.
func (c *Cache) jobHoldsIDs(searchID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.jobs[searchID]
	return j != nil && j.ids != nil
}

func newSearchID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
