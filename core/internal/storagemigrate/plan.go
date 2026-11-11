// file: core/internal/storagemigrate/plan.go
// version: 1.0.1
// guid: 6f314421-b57d-40b7-9a6c-89e8008985ef
// last-edited: 2026-11-10

package storagemigrate

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/falkcorp/mediacore/storage"
)

// samplesPerShard bounds the plan's key sample: at most this many keys per
// shard are kept while the source is read once, keys only.
const samplesPerShard = 64

// newPlan computes a migration's shard plan {n, boundaries} from its
// Parallelism and the key distribution (DATA-12): one keys-only pass over
// the source keeps an evenly spaced sample (every stride-th key, the stride
// doubling whenever the sample fills), and the boundaries are the sample's
// n-quantiles. The pass is deterministic, so two plans of one store agree;
// but only the first is ever used, because it is persisted before any shard
// runs and every resume reads it back.
func (r *Runner) newPlan(ctx context.Context, src storage.Reader, mg *migration) (planRecord, error) {
	n := max(r.opts.PlanShards(mg.m.Parallelism), 1)
	limit := samplesPerShard * n
	var sample [][]byte
	stride, seen := 1, 0
	for _, rg := range mg.source {
		for kv, err := range src.Scan(ctx, storage.ScanOptions{Start: rg.Start, End: rg.End, KeysOnly: true}) {
			if err != nil {
				return planRecord{}, err
			}
			if seen%stride == 0 {
				sample = append(sample, slices.Clone(kv.Key))
				if len(sample) >= 2*limit {
					// Keep every other key and double the stride: the sample
					// stays evenly spaced over everything read so far.
					half := sample[:0]
					for i := 0; i < len(sample); i += 2 {
						half = append(half, sample[i])
					}
					sample, stride = half, stride*2
				}
			}
			seen++
		}
	}
	var bounds [][]byte
	for j := 1; j < n && len(sample) > 0; j++ {
		b := sample[j*len(sample)/n]
		// Strictly increasing and never the first key, so no shard is a
		// duplicate and shard 0 is never empty by construction.
		if (len(bounds) == 0 && bytes.Equal(b, sample[0])) || (len(bounds) > 0 && bytes.Compare(b, bounds[len(bounds)-1]) <= 0) {
			continue
		}
		bounds = append(bounds, b)
	}
	return planRecord{Shards: len(bounds) + 1, Boundaries: bounds, Workers: n, At: r.opts.Clock().UTC(), Build: r.opts.Build}, nil
}

// shardState is one shard of a running migration: its range and, under mu,
// its committed cursor, the position its worker has stepped to and the keys
// the migration wrote into its unvisited part (RewritesInPlace).
//
// Shards are disjoint key ranges of the source, and each is run by exactly
// one worker of the tasks.ForEach pool, so no two workers ever step the same
// item (CLAUDE.md "partition the work into disjoint sets"). The mutex guards
// what another shard's worker may touch: a write into this shard's range
// reads cursor and pos (passed) and records itself in written. Only this
// shard's worker writes cursor and pos, under mu, so it reads them without.
type shardState struct {
	n      int
	lo, hi []byte // [lo, hi) relative to the source handle; nil lo = open below, nil hi = open above

	mu      sync.Mutex
	cursor  []byte              // the committed cursor (nil = nothing committed yet)
	pos     []byte              // the last key stepped (committed or not); nil = none
	written map[string]struct{} // keys written into the unvisited part, skipped when reached
}

// shards are the plan's shards with their committed cursors.
func (p planRecord) shards() []*shardState {
	out := make([]*shardState, p.Shards)
	for i := range out {
		s := &shardState{n: i, written: map[string]struct{}{}}
		if i > 0 {
			s.lo = p.Boundaries[i-1]
		}
		if i < len(p.Boundaries) {
			s.hi = p.Boundaries[i]
		}
		out[i] = s
	}
	return out
}

// shardOf is the index of the shard whose range holds key, when key lies in
// the source; -1 when it lies outside every source range (no shard visits
// it, so a write there is never revisited).
func shardOf(mg *migration, bounds [][]byte, key []byte) int {
	in := false
	for _, rg := range mg.source {
		if bytes.Compare(key, rg.Start) >= 0 && (rg.End == nil || bytes.Compare(key, rg.End) < 0) {
			in = true
			break
		}
	}
	if !in {
		return -1
	}
	// The first boundary above key closes key's shard.
	return sort.Search(len(bounds), func(i int) bool { return bytes.Compare(bounds[i], key) > 0 })
}

// passed reports whether the shard will never step key again, this run or
// after a crash: key is at or below its committed cursor, or, when own
// (the writer's own shard), at or below the item it is stepping, because a
// crash drops the writer's chunk and that write with it. Another shard's
// stepped but uncommitted part is not passed: the writer's chunk can commit
// while that shard's chunk is lost, and the resumed shard would then step
// the write. s.mu is held.
func (s *shardState) passed(key []byte, own bool) bool {
	if s.cursor != nil && bytes.Compare(key, s.cursor) <= 0 {
		return true
	}
	return own && s.pos != nil && bytes.Compare(key, s.pos) <= 0
}

// step marks key the shard's current item, unless the migration wrote it
// (RewritesInPlace): then it is skipped, and reported so.
func (s *shardState) step(key []byte) (skip bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, own := s.written[string(key)]; own {
		delete(s.written, string(key))
		skip = true
	}
	s.pos = key
	return skip
}

// commit records last, the key its chunk just committed, as the shard's
// cursor and drops the written keys at or below it: the worker never
// reaches them again.
func (s *shardState) commit(last []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor = last
	for k := range s.written {
		if bytes.Compare([]byte(k), last) <= 0 {
			delete(s.written, k)
		}
	}
}

// rewind sets the stepped position back to the committed cursor after an
// abandoned chunk.
func (s *shardState) rewind() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pos = s.cursor
}
