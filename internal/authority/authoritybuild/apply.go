// file: internal/authority/authoritybuild/apply.go
// version: 1.1.0
// guid: 5d1b8e27-9c4a-4f63-b0d2-7a6e3f18c945
// last-edited: 2026-10-05

package authoritybuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
)

// Writer is the surface an apply needs. It is a subset of
// database.RawKVStore: no new store method, so the production decorator
// forwards it with no capability assertion.
type Writer interface {
	authority.Scanner
	SetRaw(key string, value []byte) error
	DeleteRawBatch(keys []string) error
}

const (
	scanPageSize    = 1000
	deleteBatchSize = 1000
)

// KeyValue is one planned write. Canon is the value with its timestamps
// zeroed: what the plan digest covers, so the digest of the same plan
// computed twice (a dry run, then the apply that re-derives it) matches.
type KeyValue struct {
	Key   string
	Value []byte
	Canon []byte
	// before is the hash of the value stored when the plan was made (zero
	// for a new key), so the digest also covers the store's prior state.
	before [32]byte
}

// PrefixCounts is what a plan does under one prefix.
type PrefixCounts struct {
	Write     int `json:"write"`
	Unchanged int `json:"unchanged"`
	Stale     int `json:"stale"`
	// Held: stale rows kept because a source they came from did not run in
	// this build (or the row does not say where it came from).
	Held int `json:"held"`
}

// Plan is the set of writes and deletes that turns the stored rebuildable
// keyspace into a Result. A dry run computes it and stops.
type Plan struct {
	Writes []KeyValue
	// Stale are the rebuildable keys this plan prunes: rows no source
	// produced this run AND whose every recorded source ran in this build.
	// Never an override key.
	Stale []string
	// Held are rows no source produced this run but that are kept, because
	// at least one source they came from did not run (or the row records no
	// sources). A partial build never prunes what it did not look at.
	Held   []string
	Counts map[string]*PrefixCounts
	// Digest identifies the plan: every write's key, timestamp-free content
	// and the hash of the value it replaces, and every pruned and held key
	// with the hash of its stored value. Any change to a row the plan
	// touches, or to its sources, changes it. An apply re-derives the
	// plan and refuses unless its digest equals the reviewed dry run's.
	Digest string

	// staleSums hashes the stored value of every Stale and Held key.
	staleSums map[string][32]byte
}

// Options tune a plan or apply.
type Options struct {
	// Workers bounds the goroutines doing store reads (plan) or writes
	// (apply); at least 1.
	Workers int
	// Progress, when set, is told how far a phase got: every
	// progressEvery items and once at the end. It is called from worker
	// goroutines and must be safe for concurrent use. An op wires it to its
	// reporter so the watchdog sees a long plan or apply moving.
	Progress func(phase string, done, total int)
}

// progressEvery throttles Options.Progress.
const progressEvery = 1000

func (o Options) workers() int { return max(1, o.Workers) }

// tick counts one item and reports every progressEvery and at the end.
func (o Options) tick(n *atomic.Int64, phase string, total int) {
	d := int(n.Add(1))
	if o.Progress != nil && (d%progressEvery == 0 || d == total) {
		o.Progress(phase, d, total)
	}
}

// target is one key the Result wants, with how to render its value against
// what is stored.
type target struct {
	key, prefix string
	render      func(existing []byte) (value, canon []byte, changed bool, err error)
}

// PlanApply compares a Result with the store. ran is the set of sources
// that ran in this build; a stored row is pruned only when every source it
// records is in ran. Reads run on opt.Workers
// goroutines (bounded; at least 1). Timestamps are not content: a row whose
// only difference is FirstSeen/LastSeen is unchanged, so a second apply of
// the same sources writes nothing.
func PlanApply(ctx context.Context, kv authority.Scanner, res *Result, ran map[string]bool, now time.Time, opt Options) (*Plan, error) {
	targets := buildTargets(res, now)
	plan := &Plan{Counts: map[string]*PrefixCounts{}, staleSums: map[string][32]byte{}}
	for _, p := range authority.RebuildablePrefixes() {
		plan.Counts[p] = &PrefixCounts{}
	}

	var mu sync.Mutex
	var done atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opt.workers())
	for _, t := range targets {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			existing, err := kv.GetRaw(t.key)
			if err != nil {
				return fmt.Errorf("read %s: %w", t.key, err)
			}
			val, canon, changed, err := t.render(existing)
			if err != nil {
				return fmt.Errorf("render %s: %w", t.key, err)
			}
			defer opt.tick(&done, "plan", len(targets))
			mu.Lock()
			defer mu.Unlock()
			if changed {
				kv := KeyValue{Key: t.key, Value: val, Canon: canon}
				if existing != nil {
					kv.before = sha256.Sum256(existing)
				}
				plan.Writes = append(plan.Writes, kv)
				plan.Counts[t.prefix].Write++
			} else {
				plan.Counts[t.prefix].Unchanged++
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	slices.SortFunc(plan.Writes, func(a, b KeyValue) int { return strings.Compare(a.Key, b.Key) })

	want := make(map[string]bool, len(targets))
	for _, t := range targets {
		want[t.key] = true
	}
	for _, prefix := range authority.RebuildablePrefixes() {
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			pairs, next, err := kv.ScanPrefixPage(prefix, after, scanPageSize)
			if err != nil {
				return nil, fmt.Errorf("scan %s: %w", prefix, err)
			}
			for _, p := range pairs {
				if want[p.Key] {
					continue
				}
				plan.staleSums[p.Key] = sha256.Sum256(p.Value)
				if allSourcesRan(p.Key, p.Value, ran) {
					plan.Stale = append(plan.Stale, p.Key)
					plan.Counts[prefix].Stale++
				} else {
					plan.Held = append(plan.Held, p.Key)
					plan.Counts[prefix].Held++
				}
			}
			if next == "" {
				break
			}
			after = next
		}
	}
	plan.Digest = planDigest(plan)
	return plan, nil
}

// allSourcesRan reports whether every source a stored row records ran. A
// ref_src: key names its source; person, publisher and ASIN rows carry a
// Sources list. A row that does not decode, or records no sources, is never
// pruned.
func allSourcesRan(key string, value []byte, ran map[string]bool) bool {
	var sources []string
	if rest, ok := strings.CutPrefix(key, authority.SourcePrefix); ok {
		src, _, found := strings.Cut(rest, ":")
		if !found {
			return false
		}
		sources = []string{src}
	} else {
		var row struct {
			Sources []string `json:"sources"`
		}
		if json.Unmarshal(value, &row) != nil {
			return false
		}
		sources = row.Sources
	}
	if len(sources) == 0 {
		return false
	}
	for _, s := range sources {
		if !ran[s] {
			return false
		}
	}
	return true
}

// planDigest hashes the plan's writes (key, prior-value hash and
// timestamp-free content), its pruned keys and its held keys (each with its
// stored value's hash), all in key order.
func planDigest(p *Plan) string {
	h := sha256.New()
	for _, w := range p.Writes {
		h.Write([]byte(fmt.Sprintf("w\x00%s\x00%x\x00%d\x00", w.Key, w.before, len(w.Canon))))
		h.Write(w.Canon)
	}
	stale := slices.Clone(p.Stale)
	slices.Sort(stale)
	for _, k := range stale {
		h.Write([]byte(fmt.Sprintf("d\x00%s\x00%x\x00", k, p.staleSums[k])))
	}
	held := slices.Clone(p.Held)
	slices.Sort(held)
	for _, k := range held {
		h.Write([]byte(fmt.Sprintf("h\x00%s\x00%x\x00", k, p.staleSums[k])))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildTargets(res *Result, now time.Time) []target {
	out := make([]target, 0, len(res.Persons)+len(res.Publishers)+len(res.ASINs)+len(res.Ledger))
	for f, p := range res.Persons {
		out = append(out, target{key: authority.PersonPrefix + f, prefix: authority.PersonPrefix, render: func(existing []byte) ([]byte, []byte, bool, error) {
			return renderTimestamped(p, existing, now,
				func(v *authority.Person) (time.Time, time.Time) { return v.FirstSeen, v.LastSeen },
				func(v *authority.Person, first, last time.Time) { v.FirstSeen, v.LastSeen = first, last })
		}})
	}
	for f, p := range res.Publishers {
		out = append(out, target{key: authority.PublisherPrefix + f, prefix: authority.PublisherPrefix, render: func(existing []byte) ([]byte, []byte, bool, error) {
			return renderTimestamped(p, existing, now,
				func(v *authority.Publisher) (time.Time, time.Time) { return v.FirstSeen, v.LastSeen },
				func(v *authority.Publisher, first, last time.Time) { v.FirstSeen, v.LastSeen = first, last })
		}})
	}
	for a, ref := range res.ASINs {
		out = append(out, target{key: authority.PersonASINKey(a), prefix: authority.ASINPrefix, render: func(existing []byte) ([]byte, []byte, bool, error) {
			val, err := json.Marshal(ref)
			if err != nil {
				return nil, nil, false, err
			}
			return val, val, !bytes.Equal(val, existing), nil
		}})
	}
	for k, digest := range res.Ledger {
		out = append(out, target{key: k, prefix: authority.SourcePrefix, render: func(existing []byte) ([]byte, []byte, bool, error) {
			val := []byte(digest)
			return val, val, !bytes.Equal(val, existing), nil
		}})
	}
	return out
}

// renderTimestamped renders v for a key holding existing. Content is
// compared with both timestamps zeroed; when it is unchanged nothing is
// written. A changed row keeps the stored FirstSeen and gets LastSeen = now.
// A stored value that does not decode is rewritten.
func renderTimestamped[T any](v *T, existing []byte, now time.Time,
	get func(*T) (time.Time, time.Time), set func(*T, time.Time, time.Time)) ([]byte, []byte, bool, error) {
	cur := *v
	set(&cur, time.Time{}, time.Time{})
	canon, err := json.Marshal(&cur)
	if err != nil {
		return nil, nil, false, err
	}
	first := now
	if existing != nil {
		var old T
		if json.Unmarshal(existing, &old) == nil {
			oldFirst, _ := get(&old)
			set(&old, time.Time{}, time.Time{})
			if prev, err := json.Marshal(&old); err == nil && bytes.Equal(prev, canon) {
				return existing, canon, false, nil
			}
			if !oldFirst.IsZero() {
				first = oldFirst
			}
		}
	}
	set(&cur, first, now)
	val, err := json.Marshal(&cur)
	return val, canon, true, err
}

// ApplyResult counts what an apply did.
type ApplyResult struct {
	Written int `json:"written"`
	Deleted int `json:"deleted"`
}

// Apply writes a plan: every write first (on opt.Workers goroutines, so
// Pebble can group the synced commits), then the stale deletes, so a crash
// part-way leaves a superset of the new index, never a hole. Only
// rebuildable keys are ever deleted; an override key in Stale is refused.
func Apply(ctx context.Context, kv Writer, plan *Plan, opt Options) (ApplyResult, error) {
	var res ApplyResult
	for _, k := range plan.Stale {
		if !authority.IsRebuildableKey(k) {
			return res, fmt.Errorf("authority apply: refusing to delete non-rebuildable key %q", k)
		}
	}
	for _, w := range plan.Writes {
		if !authority.IsRebuildableKey(w.Key) {
			return res, fmt.Errorf("authority apply: refusing to write non-rebuildable key %q", w.Key)
		}
	}
	var mu sync.Mutex
	var done atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opt.workers())
	for _, w := range plan.Writes {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			if err := kv.SetRaw(w.Key, w.Value); err != nil {
				return fmt.Errorf("write %s: %w", w.Key, err)
			}
			mu.Lock()
			res.Written++
			mu.Unlock()
			opt.tick(&done, "apply", len(plan.Writes))
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return res, err
	}
	for i := 0; i < len(plan.Stale); i += deleteBatchSize {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		chunk := plan.Stale[i:min(i+deleteBatchSize, len(plan.Stale))]
		if err := kv.DeleteRawBatch(chunk); err != nil {
			return res, fmt.Errorf("delete stale rows: %w", err)
		}
		res.Deleted += len(chunk)
		if opt.Progress != nil {
			opt.Progress("prune", res.Deleted, len(plan.Stale))
		}
	}
	return res, nil
}
