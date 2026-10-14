// file: internal/operations/registry/chunk_ledger.go
// version: 1.3.0
// guid: 1d4e18b1-a85c-41de-bd02-4a39b90f180a
// last-edited: 2026-10-13

package registry

import (
	"maps"
	"slices"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The chunk ledger (owner decision D28a, sdk-api.md §5) replaces the
// contiguous-prefix watermark of run_items.go's completionTracker. A resume
// leases every chunk the ledger does not record as complete, from its first
// item, and never leases a recorded one again, so seven workers that ran far
// ahead of an eighth do not redo their work after a deploy.
//
// Only the batch runner's ledger goroutine touches a chunkLedger; it has no
// lock because nothing else can reach it.

// snapshotPageSize is how many frozen keys one opv3:snap: page holds.
const snapshotPageSize = 10_000

// chunkTable is a Snapshot run's frozen chunk boundaries: chunk c is
// keys[starts[c]:starts[c+1]] (the last chunk ends at len(keys)).
type chunkTable struct {
	keys   []string
	starts []int
}

func (t chunkTable) count() int { return len(t.starts) }

// bounds returns chunk c's [lo, hi) range in keys.
func (t chunkTable) bounds(c int) (int, int) {
	hi := len(t.keys)
	if c+1 < len(t.starts) {
		hi = t.starts[c+1]
	}
	return t.starts[c], hi
}

// cutChunks freezes a Snapshot run's chunk table.
//
// Without partitions it is arithmetic: every chunk holds size items, the last
// one the remainder.
//
// With partitions it is partition-major (sdk-api.md §5.2): order is the
// source order stable-sorted by partition key, so a partition's items stay in
// source order. Whole partitions are added to the current chunk until it holds
// at least size items, so a boundary is always pushed forward to the next
// partition edge and a partition never straddles two chunks; a partition
// larger than size closes the current chunk first and becomes one oversized
// chunk of its own. The chunk table depends only on the snapshot, never on
// the worker count, so a resume on a different machine leases the same
// chunks.
//
// It returns the frozen order (indices into keys) and the chunk start offsets
// into that order.
func cutChunks(keys, parts []string, size int) (order, starts []int) {
	n := len(keys)
	order = make([]int, n)
	for i := range order {
		order[i] = i
	}
	if n == 0 {
		return order, nil
	}
	if parts == nil {
		for s := 0; s < n; s += size {
			starts = append(starts, s)
		}
		return order, starts
	}
	sort.SliceStable(order, func(a, b int) bool { return parts[order[a]] < parts[order[b]] })
	cnt := 0
	for i := 0; i < n; {
		j := i + 1
		for j < n && parts[order[j]] == parts[order[i]] {
			j++
		}
		run := j - i
		if run > size && cnt > 0 {
			cnt = 0 // close the open chunk: an oversized partition stands alone
		}
		if cnt == 0 {
			starts = append(starts, i)
		}
		cnt += run
		if cnt >= size {
			cnt = 0
		}
		i = j
	}
	return order, starts
}

// chunkLedger is the folded state of a run: which chunks are complete (a
// bitmap for Snapshot, merged key ranges for AppendOnly) and the counters of
// the completed chunks. A crash loses only what is not in a completed chunk.
type chunkLedger struct {
	l database.Ledger
	// after holds, for each range this attempt completed, the key of the
	// stream item just before its first item, so ranges completed out of
	// order still merge. Merging only shrinks the ledger; a range left
	// unmerged is still skipped correctly on resume.
	after map[string]string // range First -> preceding stream key
	// chunksDone counts completed chunks across every attempt; persisted as
	// Ledger.ChunksDone because merged AppendOnly ranges cannot be counted
	// back into chunks.
	chunksDone int
	// marks is every key items recorded with rc.Mark in completed chunks,
	// across every attempt (Summary.Marks). pending is the part of it folded
	// since the last successful commit: each mark is written once, as its
	// own opv3:mark: key in the commit that records its chunk, never
	// rewritten, so there is no cap to drop past.
	marks, pending map[string][]string
	// fresh: this ledger was started, not loaded from a stored one. Its
	// first successful commit sets LedgerBatch.ResetMarks, so marks an
	// abandoned earlier ledger of the run stored are dropped.
	fresh bool
}

func newChunkLedger(size, count int) *chunkLedger {
	return &chunkLedger{
		l: database.Ledger{SchemaVersion: 1, ChunkSize: size, ChunkCount: count,
			Done: make([]byte, (count+7)/8), Counters: map[string]int64{}},
		after: map[string]string{},
		marks: map[string][]string{}, pending: map[string][]string{},
		fresh: true,
	}
}

// loadChunkLedger resumes from a stored ledger and the marks stored with it.
func loadChunkLedger(stored *database.Ledger, marks map[string][]string) *chunkLedger {
	l := *stored
	l.Done = slices.Clone(stored.Done)
	l.Ranges = slices.Clone(stored.Ranges)
	l.Counters = maps.Clone(stored.Counters)
	if l.Counters == nil {
		l.Counters = map[string]int64{}
	}
	cl := &chunkLedger{l: l, after: map[string]string{}, marks: map[string][]string{}, pending: map[string][]string{}}
	for k, v := range marks {
		cl.marks[k] = slices.Clone(v)
	}
	if want := (l.ChunkCount + 7) / 8; len(cl.l.Done) < want {
		cl.l.Done = append(cl.l.Done, make([]byte, want-len(cl.l.Done))...)
	}
	cl.chunksDone = stored.ChunksDone
	if cl.chunksDone == 0 {
		// A ledger written before ChunksDone was persisted: count the bitmap.
		for c := range l.ChunkCount {
			if cl.isDone(c) {
				cl.chunksDone++
			}
		}
	}
	return cl
}

func (c *chunkLedger) isDone(chunk int) bool {
	b := chunk / 8
	return b < len(c.l.Done) && c.l.Done[b]&(1<<(chunk%8)) != 0
}

// markDone sets a Snapshot chunk's bit. A chunk already set is not counted
// twice (it cannot be leased twice; this only guards the fold).
func (c *chunkLedger) markDone(chunk int) bool {
	if c.isDone(chunk) {
		return false
	}
	c.l.Done[chunk/8] |= 1 << (chunk % 8)
	c.chunksDone++
	return true
}

// addRange records a completed AppendOnly chunk {first..last} whose first
// item followed the stream key after, merging it with a completed neighbor.
func (c *chunkLedger) addRange(first, last, after string) {
	c.chunksDone++
	r := database.KeyRange{First: first, Last: last}
	c.after[first] = after
	for {
		merged := false
		for i, o := range c.l.Ranges {
			switch {
			case after != "" && o.Last == after, o.First <= r.Last && r.First <= o.Last:
				// o ends right before r, or they overlap.
				r.First, r.Last = min(o.First, r.First), max(o.Last, r.Last)
			case c.after[o.First] == r.Last && r.Last != "":
				// o starts right after r.
				r.Last = max(o.Last, r.Last)
			default:
				continue
			}
			after = c.after[r.First]
			c.l.Ranges = slices.Delete(c.l.Ranges, i, i+1)
			merged = true
			break
		}
		if !merged {
			break
		}
	}
	c.after[r.First] = after
	c.l.Ranges = append(c.l.Ranges, r)
	sort.Slice(c.l.Ranges, func(i, j int) bool { return c.l.Ranges[i].First < c.l.Ranges[j].First })
}

// inRanges reports whether key falls inside one of the sorted, disjoint
// completed AppendOnly ranges rs.
func inRanges(rs []database.KeyRange, key string) bool {
	i := sort.Search(len(rs), func(i int) bool { return rs[i].Last >= key })
	return i < len(rs) && rs[i].First <= key
}

// fold adds a completed chunk's buffered counts.
func (c *chunkLedger) fold(b *chunkTally) {
	c.l.Items.Done += b.ok + b.failed + b.skipped
	c.l.Items.Failed += b.failed
	c.l.Items.Skipped += b.skipped
	for k, v := range b.counters {
		c.l.Counters[k] += v
	}
	for name, keys := range b.marks {
		c.marks[name] = append(c.marks[name], keys...)
		c.pending[name] = append(c.pending[name], keys...)
	}
}

// pendingMarks is the marks folded since the last successful commit; the
// commit that stores them calls clearPending.
func (c *chunkLedger) pendingMarks() map[string][]string {
	if len(c.pending) == 0 {
		return nil
	}
	return c.pending
}

func (c *chunkLedger) clearPending() { c.pending = map[string][]string{} }

// allMarks is a copy of every mark, for the Summary.
func (c *chunkLedger) allMarks() map[string][]string {
	out := make(map[string][]string, len(c.marks))
	for k, v := range c.marks {
		out[k] = slices.Clone(v)
	}
	return out
}

// snapshot returns a copy safe to hand to the store.
func (c *chunkLedger) snapshot() database.Ledger {
	l := c.l
	l.Done = slices.Clone(c.l.Done)
	l.Ranges = slices.Clone(c.l.Ranges)
	l.Counters = maps.Clone(c.l.Counters)
	l.ChunksDone = c.chunksDone
	return l
}

// chunkTally is one chunk's buffered counts, owned by the worker running the
// chunk until it hands the whole tally to the ledger goroutine.
type chunkTally struct {
	ok, failed, skipped int64
	counters            map[string]int64
	marks               map[string][]string
	rows                []database.ItemOutcome
}

func (t *chunkTally) add(name string, d int64) {
	if t.counters == nil {
		t.counters = map[string]int64{}
	}
	t.counters[name] += d
}
