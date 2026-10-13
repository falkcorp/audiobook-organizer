// file: pkg/ops/runtime.go
// version: 1.0.0
// guid: 10c35003-dc39-4f70-8984-f571765b5a1f
// last-edited: 2026-10-13

package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// This file is the executor's view of a definition. Authors never use it:
// internal/operations/registry calls Compile and drives the result. The
// definitions are generic, the executor is not, so the item type T is erased
// here behind index-addressed ItemSets; no item crosses as an untyped value.

type (
	taskFunc  func(rc *RC, params json.RawMessage) (json.RawMessage, error)
	batchFunc func(rc *RC, params json.RawMessage) (*BatchJob, error)
)

// Compiled is a definition as the executor runs it, defaults resolved.
type Compiled struct {
	ID   string
	Kind Kind
	// Common has Exclusive, Priority and Timeout defaulted.
	Common Common
	// Problems are programmer errors found at construction; the executor
	// refuses to register a def that has any.
	Problems []string
	// Resume is the def-level policy: the kind default or Common.Resume.
	Resume ResumePolicy
	// Concurrency is a Batch's or Fixer's declared worker count.
	Concurrency Concurrency
	// RunTask runs a Task or a Pipeline; nil otherwise.
	RunTask func(rc *RC, params json.RawMessage) (json.RawMessage, error)
	// OpenBatch opens one run of a Batch or a Fixer; nil otherwise.
	OpenBatch func(rc *RC, params json.RawMessage) (*BatchJob, error)
	// Stages are a Pipeline's stages.
	Stages []CompiledStage
	// Origin is an adapted def's v2 definition.
	Origin Origin
}

// Compile resolves a definition for the executor.
func Compile(def Definition) Compiled {
	s := def.spec()
	c := Compiled{
		ID: s.id, Kind: s.kind, Common: s.common, Problems: append([]string(nil), s.problems...),
		Concurrency: s.conc, Stages: s.stages, Origin: s.origin,
		RunTask: s.task, OpenBatch: s.batch,
	}
	if c.Common.Exclusive == "" {
		c.Common.Exclusive = s.id
	}
	if c.Common.Priority == 0 {
		c.Common.Priority = PriorityNormal
	}
	if c.Common.Timeout == 0 {
		c.Common.Timeout = DefaultTimeout(s.kind)
	}
	c.Resume = defaultResume(s.kind)
	if s.common.Resume != nil {
		c.Resume = *s.common.Resume
	}
	return c
}

func defaultResume(k Kind) ResumePolicy {
	switch k {
	case KindBatch:
		return ResumeContinue
	case KindFixer:
		return ResumeAsk
	default:
		return ResumeDrop
	}
}

// BatchJob is one run of a Batch or Fixer, opened by Compiled.OpenBatch.
// Exactly one of Items, Pages and Dirty is set.
type BatchJob struct {
	Order       SourceOrder
	Total       Total
	Workers     int // >= 1
	ChunkSize   int // >= 1
	ItemTimeout time.Duration
	StopOnError bool
	// Partitioned: chunks must hold whole partitions (ItemSet.Partition).
	Partitioned bool
	// Resume is this run's policy: ResumeContinue reads the ledger,
	// ResumeFromZero ignores it. (Drop and Ask are decided by the executor
	// before a run is dispatched again; a run that is dispatched continues.)
	Resume ResumePolicy
	Items  ItemSet
	Pages  func(ctx context.Context, after string, limit int) (ItemSet, string, error)
	Dirty  *DirtySource
	Finish func(rc *RC, s Summary) (json.RawMessage, error)
}

// ItemSet is a page of items addressed by index.
type ItemSet interface {
	Len() int
	Key(i int) string
	// Partition is the PartitionBy key, "" when the def does not partition.
	Partition(i int) string
	// Label is Label(item), or the key when the def has no Label.
	Label(i int) string
	Run(rc *ItemRC, i int) error
}

// DirtySource is a DirtySet source: the runner pages Prefix itself and turns
// each page of subject ids into items with Load.
type DirtySource struct {
	Prefix string
	Load   func(ctx context.Context, ids []string) (ItemSet, error)
}

// CompiledStage is one Pipeline stage with its params hand-off erased to JSON.
type CompiledStage struct {
	Name   string
	Def    Definition
	After  []string
	Params func(params json.RawMessage, results Results) (json.RawMessage, error)
	OnFail StageFailure
}

// jobConfig is what buildJob needs from a Batch or a Fixer phase.
type jobConfig[P any, T any] struct {
	id          string
	p           P
	src         Source[T]
	run         func(*ItemRC, P, T) error
	part        func(T) string
	label       func(T) string
	conc        Concurrency
	chunk       int
	itemTimeout time.Duration
	stop        bool
	resume      *ResumePolicy
}

// buildJob turns a typed source into a BatchJob. It refuses, before any item
// runs, the combinations ValidateCatalog cannot see because a source's Order
// is only known once Source has run: PartitionBy on a non-Snapshot source
// (a DirtySet excepted) and an explicit ResumeContinue on an Unordered one.
func buildJob[P any, T any](cfg jobConfig[P, T], params json.RawMessage) (*BatchJob, error) {
	src := cfg.src
	order := src.Order
	if order == 0 {
		order = Snapshot
	}
	if src.dirty == nil && src.Key == nil {
		return nil, fmt.Errorf("%s: source has no Key", cfg.id)
	}
	if cfg.part != nil && order != Snapshot && src.dirty == nil {
		return nil, fmt.Errorf("%s: PartitionBy needs a Snapshot source, got %s", cfg.id, order)
	}
	resume := ResumeContinue
	if order == Unordered {
		resume = ResumeFromZero
	}
	if cfg.resume != nil {
		if *cfg.resume == ResumeContinue && order == Unordered {
			return nil, fmt.Errorf("%s: ResumeContinue is refused for an Unordered source (items can appear below the cursor)", cfg.id)
		}
		if *cfg.resume == ResumeFromZero {
			resume = ResumeFromZero
		}
	}
	chunk := cfg.chunk
	if chunk <= 0 {
		chunk = DefaultChunkSize
	}
	job := &BatchJob{
		Order: order, Total: src.Total, Workers: cfg.conc.Workers(params), ChunkSize: chunk,
		ItemTimeout: cfg.itemTimeout, StopOnError: cfg.stop, Partitioned: cfg.part != nil, Resume: resume,
	}
	mk := func(items []T, keys []string) ItemSet {
		return &sliceSet[P, T]{items: items, keys: keys, p: cfg.p, key: src.Key, part: cfg.part, label: cfg.label, run: cfg.run}
	}
	switch {
	case src.dirty != nil:
		d := src.dirty
		job.Dirty = &DirtySource{Prefix: d.prefix, Load: func(ctx context.Context, ids []string) (ItemSet, error) {
			items, err := d.load(ctx, ids)
			if err != nil {
				return nil, err
			}
			if len(items) != len(ids) {
				return nil, fmt.Errorf("%s: DirtySet load returned %d items for %d ids", cfg.id, len(items), len(ids))
			}
			// The subject id is the key, and with PartitionBy the partition:
			// unique per entry, so partition-major grouping is the identity.
			s := mk(items, ids).(*sliceSet[P, T])
			s.partIsKey = cfg.part != nil
			return s, nil
		}}
	case src.Pages != nil && src.Items == nil:
		pages := src.Pages
		job.Pages = func(ctx context.Context, after string, limit int) (ItemSet, string, error) {
			items, next, err := pages(ctx, after, limit)
			if err != nil {
				return nil, "", err
			}
			return mk(items, nil), next, nil
		}
	default:
		job.Items = mk(src.Items, nil)
		if !job.Total.known {
			job.Total = Known(int64(len(src.Items)))
		}
	}
	return job, nil
}

// sliceSet is an ItemSet over a typed slice.
type sliceSet[P any, T any] struct {
	items     []T
	keys      []string // DirtySet: the subject ids
	p         P
	key       func(T) string
	part      func(T) string
	partIsKey bool
	label     func(T) string
	run       func(*ItemRC, P, T) error
}

func (s *sliceSet[P, T]) Len() int { return len(s.items) }

func (s *sliceSet[P, T]) Key(i int) string {
	if s.keys != nil {
		return s.keys[i]
	}
	return s.key(s.items[i])
}

func (s *sliceSet[P, T]) Partition(i int) string {
	switch {
	case s.partIsKey:
		return s.Key(i)
	case s.part == nil:
		return ""
	default:
		return s.part(s.items[i])
	}
}

func (s *sliceSet[P, T]) Label(i int) string {
	if s.label == nil {
		return s.Key(i)
	}
	return s.label(s.items[i])
}

func (s *sliceSet[P, T]) Run(rc *ItemRC, i int) error { return s.run(rc, s.p, s.items[i]) }

// decodeParams decodes a run's params into P. Empty or null params are P's
// zero value; unknown fields (the executor's dry_run) are ignored.
func decodeParams[P any](id string, raw json.RawMessage) (P, error) {
	var p P
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return p, nil
	}
	if err := json.Unmarshal(trimmed, &p); err != nil {
		return p, fmt.Errorf("%s: params do not decode: %w", id, err)
	}
	return p, nil
}
