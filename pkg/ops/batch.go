// file: pkg/ops/batch.go
// version: 1.0.0
// guid: d9e5b5a9-3c58-4d74-aac9-55d6b89b6410
// last-edited: 2026-10-13

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"time"
)

// BatchSpec is a source of items plus a per-item function. The runner owns
// the loop: concurrency, cancel points, progress, checkpoints, resume,
// partitioning, pause, stand-down renewal and per-item timeouts.
type BatchSpec[P any, T any, R any] struct {
	Common
	// Source produces the items; its Order decides whether a chunk-ledger
	// resume is sound.
	Source func(rc *RC, p P) (Source[T], error)
	// Item does the work for one item. It MUST be idempotent: a resume re-runs
	// every item of a chunk the ledger does not record as complete, including
	// the ones of that chunk that had already finished.
	Item func(rc *ItemRC, p P, item T) error
	// Finish runs once after every item settled, and also after a cancel with
	// rc.Canceled() true and the partial summary.
	Finish func(rc *RC, p P, s Summary) (R, error)
	// Concurrency defaults to CPU() -- NOT 1. Sequential is a choice with a
	// reason.
	Concurrency Concurrency
	// PartitionBy routes every item with the same key to the same worker, in
	// source order (partition-major chunks). Requires Order Snapshot (or a
	// DirtySet source).
	PartitionBy func(T) string
	// ChunkSize is the number of items leased to a worker at a time.
	// Default 256.
	ChunkSize int
	// ItemTimeout bounds one item; 0 = none. The clock starts after the pause
	// gate.
	ItemTimeout time.Duration
	// OnItemError: Continue (default; counted failed) or Stop.
	OnItemError ErrorPolicy
	// Label names an item for the "currently working on" line. It receives
	// the item only, no counters, so it cannot race (it runs inside workers).
	Label func(T) string
}

// DefaultChunkSize is BatchSpec.ChunkSize when it is zero.
const DefaultChunkSize = 256

// ErrorPolicy is what an item error does to the run.
type ErrorPolicy int

const (
	// Continue counts the item failed and goes on. The default.
	Continue ErrorPolicy = iota
	// Stop ends the run; the item's chunk stays incomplete.
	Stop
)

// Batch builds a Batch definition.
func Batch[P any, T any, R any](id string, s BatchSpec[P, T, R]) Definition {
	b := newSpec(id, KindBatch, s.Common)
	b.conc = s.Concurrency
	b.problems = append(b.problems, s.Concurrency.problems()...)
	if s.Source == nil {
		b.problem("Batch has no Source")
	}
	if s.Item == nil {
		b.problem("Batch has no Item")
	}
	if s.ChunkSize < 0 {
		b.problem("ChunkSize is negative")
	}
	b.batch = func(rc *RC, params json.RawMessage) (*BatchJob, error) {
		p, err := decodeParams[P](id, params)
		if err != nil {
			return nil, err
		}
		src, err := s.Source(rc, p)
		if err != nil {
			return nil, fmt.Errorf("%s: source: %w", id, err)
		}
		cfg := jobConfig[P, T]{
			id: id, p: p, src: src, run: s.Item, part: s.PartitionBy, label: s.Label,
			conc: s.Concurrency, chunk: s.ChunkSize, itemTimeout: s.ItemTimeout,
			stop: s.OnItemError == Stop, resume: s.Resume,
		}
		job, err := buildJob(cfg, params)
		if err != nil {
			return nil, err
		}
		job.Finish = func(rc *RC, sum Summary) (json.RawMessage, error) {
			if s.Finish == nil {
				return nil, nil
			}
			r, err := s.Finish(rc, p, sum)
			if err != nil {
				return nil, err
			}
			return json.Marshal(r)
		}
		return job, nil
	}
	return b
}

// Concurrency is a declared worker count with its reason.
type Concurrency struct {
	n      int
	reason string
	kind   concKind
	field  string
	max    int
}

type concKind int

const (
	concCPU concKind = iota // the zero value: an omitted Concurrency is CPU()
	concNetwork
	concSequential
	concParam
)

// CPU is runtime.NumCPU() workers: the CPU-bound default.
func CPU() Concurrency { return Concurrency{kind: concCPU} }

// Network is a fixed small pool for a rate-limited backend.
func Network(n int, why string) Concurrency {
	return Concurrency{kind: concNetwork, n: n, reason: why}
}

// Sequential is one worker. The reason is shown in the def list, and
// ValidateCatalog refuses an empty one.
func Sequential(why string) Concurrency {
	return Concurrency{kind: concSequential, n: 1, reason: why}
}

// FromParam reads the worker count from the run's params field, clamped to
// [1, max]; def is used when the field is absent.
func FromParam(field string, def, max int) Concurrency {
	return Concurrency{kind: concParam, field: field, n: def, max: max}
}

// IsSequential reports Sequential(...).
func (c Concurrency) IsSequential() bool { return c.kind == concSequential }

// Reason is the declared reason (Network, Sequential).
func (c Concurrency) Reason() string { return c.reason }

func (c Concurrency) problems() []string {
	switch c.kind {
	case concNetwork:
		if c.n < 1 {
			return []string{fmt.Sprintf("Network(%d): need at least one worker", c.n)}
		}
	case concParam:
		if c.field == "" || c.max < 1 {
			return []string{fmt.Sprintf("FromParam(%q, %d, %d): need a field and max >= 1", c.field, c.n, c.max)}
		}
	}
	return nil
}

// Workers resolves the worker count for one run (FromParam reads params).
// Always >= 1.
func (c Concurrency) Workers(params json.RawMessage) int {
	switch c.kind {
	case concNetwork, concSequential:
		return max(c.n, 1)
	case concParam:
		n := c.n
		var m map[string]json.RawMessage
		if json.Unmarshal(params, &m) == nil {
			var v int
			if raw, ok := m[c.field]; ok && json.Unmarshal(raw, &v) == nil {
				n = v
			}
		}
		return min(max(n, 1), max(c.max, 1))
	default:
		return max(runtime.NumCPU(), 1)
	}
}

// Source yields items. Order is a promise the runtime relies on for resume.
type Source[T any] struct {
	// Items is the full item list, or nil when Pages is used.
	Items []T
	// Pages streams items for sources too large to hold. A Snapshot source
	// that pages is drained once at start (the keys are frozen).
	Pages func(ctx context.Context, after string, limit int) (items []T, next string, err error)
	// Key returns an item's stable, unique id. Required: the snapshot, the
	// ledger and the journal are keyed by it.
	Key func(T) string
	// Order says whether a chunk-ledger resume is sound. Default Snapshot.
	Order SourceOrder
	// Total may be Unknown().
	Total Total

	dirty *dirtySpec[T]
}

// SourceOrder is what a source promises about where new items appear.
type SourceOrder int

const (
	// Snapshot freezes the item keys at start (opv3:snap:) and chunks that
	// frozen order. Always safe; required with PartitionBy.
	Snapshot SourceOrder = iota + 1
	// AppendOnly: new items only ever sort AFTER existing ones, in ascending
	// key order. The ledger records completed key ranges, not chunk numbers.
	AppendOnly
	// Unordered: items can appear below the cursor. Resume restarts from zero;
	// ResumeContinue is refused.
	Unordered
)

// String names the order.
func (o SourceOrder) String() string {
	switch o {
	case Snapshot:
		return "snapshot"
	case AppendOnly:
		return "append_only"
	case Unordered:
		return "unordered"
	default:
		return "unset"
	}
}

type dirtySpec[T any] struct {
	prefix string
	load   func(ctx context.Context, ids []string) ([]T, error)
}

// DirtySet is a Source over a durable per-subject dirty set (keys
// <prefix><subjectID>, whose value begins with an 8-byte big-endian mark
// epoch written by the marker, 02 PR 14). The runner drains it 256 keys at a
// time, one page per chunk, and after an item succeeds deletes its entry only
// if the epoch is unchanged, so a re-mark during the item is not lost. Order
// is Unordered (resume restarts from zero, which is cheap: processed entries
// are gone). load must return exactly one item per id, in the order given.
func DirtySet[T any](prefix string, load func(ctx context.Context, ids []string) ([]T, error)) Source[T] {
	return Source[T]{Order: Unordered, Total: Unknown(), dirty: &dirtySpec[T]{prefix: prefix, load: load}}
}

// BudgetRef names an existing provider rate limiter
// (internal/metadata/throttle_registry.go). It is a rate budget, not a
// calendar quota.
type BudgetRef struct{ Provider string }

// Budget names a provider's limiter.
func Budget(provider string) BudgetRef { return BudgetRef{Provider: provider} }

// Requirement is a standing per-subject prerequisite (v2 ReqOpCompleted /
// ReqFieldSet, same keyspace). The executor maps it 1:1.
type Requirement struct {
	kind    string
	defID   string
	field   string
	subject SubjectType
}

// Requirement kinds, spelled as the v2 registry spells them.
const (
	RequirementOpCompleted = "op_completed"
	RequirementFieldSet    = "field_set"
)

// AfterFor: do not run for a subject until defID has completed for it since
// it last changed.
func AfterFor(defID string, sub SubjectType) Requirement {
	return Requirement{kind: RequirementOpCompleted, defID: defID, subject: sub}
}

// FieldSet: the subject's field must be non-empty.
func FieldSet(field string) Requirement {
	return Requirement{kind: RequirementFieldSet, field: field}
}

// Kind is RequirementOpCompleted or RequirementFieldSet.
func (r Requirement) Kind() string { return r.kind }

// DefID is AfterFor's def.
func (r Requirement) DefID() string { return r.defID }

// Field is FieldSet's field.
func (r Requirement) Field() string { return r.field }

// Subject is AfterFor's subject type.
func (r Requirement) Subject() SubjectType { return r.subject }
