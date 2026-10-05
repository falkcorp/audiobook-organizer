// file: internal/authority/authoritybuild/apply.go
// version: 1.0.0
// guid: 5d1b8e27-9c4a-4f63-b0d2-7a6e3f18c945
// last-edited: 2026-10-04

package authoritybuild

import (
	"bytes"
	"context"
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

// KeyValue is one planned write.
type KeyValue struct {
	Key   string
	Value []byte
}

// PrefixCounts is what a plan does under one prefix.
type PrefixCounts struct {
	Write     int `json:"write"`
	Unchanged int `json:"unchanged"`
	Stale     int `json:"stale"`
}

// Plan is the set of writes and deletes that turns the stored rebuildable
// keyspace into a Result. A dry run computes it and stops.
type Plan struct {
	Writes []KeyValue
	// Stale are rebuildable keys no source produced any more. Never an
	// override key.
	Stale  []string
	Counts map[string]*PrefixCounts
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
	render      func(existing []byte) (value []byte, changed bool, err error)
}

// PlanApply compares a Result with the store. Reads run on opt.Workers
// goroutines (bounded; at least 1). Timestamps are not content: a row whose
// only difference is FirstSeen/LastSeen is unchanged, so a second apply of
// the same sources writes nothing.
func PlanApply(ctx context.Context, kv authority.Scanner, res *Result, now time.Time, opt Options) (*Plan, error) {
	targets := buildTargets(res, now)
	plan := &Plan{Counts: map[string]*PrefixCounts{}}
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
			val, changed, err := t.render(existing)
			if err != nil {
				return fmt.Errorf("render %s: %w", t.key, err)
			}
			defer opt.tick(&done, "plan", len(targets))
			mu.Lock()
			defer mu.Unlock()
			if changed {
				plan.Writes = append(plan.Writes, KeyValue{Key: t.key, Value: val})
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
				if !want[p.Key] {
					plan.Stale = append(plan.Stale, p.Key)
					plan.Counts[prefix].Stale++
				}
			}
			if next == "" {
				break
			}
			after = next
		}
	}
	return plan, nil
}

func buildTargets(res *Result, now time.Time) []target {
	out := make([]target, 0, len(res.Persons)+len(res.Publishers)+len(res.ASINs)+len(res.Ledger))
	for f, p := range res.Persons {
		out = append(out, target{key: authority.PersonPrefix + f, prefix: authority.PersonPrefix, render: func(existing []byte) ([]byte, bool, error) {
			return renderTimestamped(p, existing, now,
				func(v *authority.Person) (time.Time, time.Time) { return v.FirstSeen, v.LastSeen },
				func(v *authority.Person, first, last time.Time) { v.FirstSeen, v.LastSeen = first, last })
		}})
	}
	for f, p := range res.Publishers {
		out = append(out, target{key: authority.PublisherPrefix + f, prefix: authority.PublisherPrefix, render: func(existing []byte) ([]byte, bool, error) {
			return renderTimestamped(p, existing, now,
				func(v *authority.Publisher) (time.Time, time.Time) { return v.FirstSeen, v.LastSeen },
				func(v *authority.Publisher, first, last time.Time) { v.FirstSeen, v.LastSeen = first, last })
		}})
	}
	for a, ref := range res.ASINs {
		out = append(out, target{key: authority.PersonASINKey(a), prefix: authority.ASINPrefix, render: func(existing []byte) ([]byte, bool, error) {
			val, err := json.Marshal(ref)
			if err != nil {
				return nil, false, err
			}
			return val, !bytes.Equal(val, existing), nil
		}})
	}
	for k, digest := range res.Ledger {
		out = append(out, target{key: k, prefix: authority.SourcePrefix, render: func(existing []byte) ([]byte, bool, error) {
			val := []byte(digest)
			return val, !bytes.Equal(val, existing), nil
		}})
	}
	return out
}

// renderTimestamped renders v for a key holding existing. Content is
// compared with both timestamps zeroed; when it is unchanged nothing is
// written. A changed row keeps the stored FirstSeen and gets LastSeen = now.
// A stored value that does not decode is rewritten.
func renderTimestamped[T any](v *T, existing []byte, now time.Time,
	get func(*T) (time.Time, time.Time), set func(*T, time.Time, time.Time)) ([]byte, bool, error) {
	cur := *v
	set(&cur, time.Time{}, time.Time{})
	canon, err := json.Marshal(&cur)
	if err != nil {
		return nil, false, err
	}
	first := now
	if existing != nil {
		var old T
		if json.Unmarshal(existing, &old) == nil {
			oldFirst, _ := get(&old)
			set(&old, time.Time{}, time.Time{})
			if prev, err := json.Marshal(&old); err == nil && bytes.Equal(prev, canon) {
				return existing, false, nil
			}
			if !oldFirst.IsZero() {
				first = oldFirst
			}
		}
	}
	set(&cur, first, now)
	val, err := json.Marshal(&cur)
	return val, true, err
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
