// file: core/tasks/erased.go
// version: 1.0.0
// guid: 58d07b6d-facf-4955-9b5a-66995374e863
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"encoding/json"
	"fmt"
)

// The type-erased runners of a definition (F2-03). Task, Batch and Fixer
// keep their typed closures here, converted once at construction to the same
// spec types instantiated over any (params, items, candidates) and
// json.RawMessage (results), so core/internal/executor runs every definition
// without knowing its type parameters. They are reached only through
// (*spec).ExecutorRunners, a method of the unexported Definition
// implementation: package tasks gains no exported identifier, and an app
// cannot name or call it except by a deliberate interface assertion, which
// it has no reason to make (seams.d/F2-03.md).

// runners is what a definition's constructor erased; exactly one of task,
// batch and fixer is set (none for a definition with constructor problems
// that left its body nil).
type runners struct {
	decode func(json.RawMessage) (any, error)
	task   *TaskSpec[any, json.RawMessage]
	batch  *BatchSpec[any, any, json.RawMessage]
	fixer  *FixerSpec[any, any]
}

// ExecutorRunners returns the definition's type-erased runners for
// core/internal/executor: decode turns stored params into the value the
// runners take (empty or null params decode to the zero P), and exactly one
// of task, batch and fixer is non-nil for a definition without constructor
// problems. It is a method of the unexported Definition implementation, so
// it is not part of tasks' exported API (seams.d/F2-03.md).
func (s *spec) ExecutorRunners() (decode func(json.RawMessage) (any, error),
	task *TaskSpec[any, json.RawMessage], batch *BatchSpec[any, any, json.RawMessage], fixer *FixerSpec[any, any]) {
	return s.run.decode, s.run.task, s.run.batch, s.run.fixer
}

// as converts an erased value back to T; a nil interface (an erased zero
// value of an interface type) becomes T's zero value instead of panicking.
func as[T any](v any) T {
	t, _ := v.(T)
	return t
}

// decoder decodes params into a P for kind.
func decoder[P any](kind Kind) func(json.RawMessage) (any, error) {
	return func(raw json.RawMessage) (any, error) {
		var p P
		if len(raw) == 0 || string(raw) == "null" {
			return p, nil
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("tasks: params of %s do not decode: %w", kind, err)
		}
		return p, nil
	}
}

// encodeResult is a run's result as stored (RunRecord.Result).
func encodeResult[R any](kind Kind, r R) (json.RawMessage, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("tasks: result of %s does not encode: %w", kind, err)
	}
	return b, nil
}

func eraseTask[P, R any](kind Kind, s TaskSpec[P, R]) runners {
	if s.Run == nil {
		return runners{decode: decoder[P](kind)}
	}
	run := s.Run
	return runners{decode: decoder[P](kind), task: &TaskSpec[any, json.RawMessage]{
		Run: func(rc *RC, p any) (json.RawMessage, error) {
			r, err := run(rc, as[P](p))
			if err != nil {
				return nil, err
			}
			return encodeResult(kind, r)
		},
	}}
}

func eraseBatch[P, T, R any](kind Kind, s BatchSpec[P, T, R]) runners {
	if s.Source == nil || s.Item == nil {
		return runners{decode: decoder[P](kind)}
	}
	src, item := s.Source, s.Item
	b := &BatchSpec[any, any, json.RawMessage]{
		Source: func(rc *RC, p any) (Source[any], error) {
			out, err := src(rc, as[P](p))
			if err != nil {
				return Source[any]{}, err
			}
			return eraseSource(out), nil
		},
		Item:        func(ic *ItemRC, p any, it any) error { return item(ic, as[P](p), as[T](it)) },
		Parallelism: s.Parallelism,
		ChunkSize:   s.ChunkSize,
		ItemTimeout: s.ItemTimeout,
		OnItemError: s.OnItemError,
	}
	if f := s.Finish; f != nil {
		b.Finish = func(rc *RC, p any, sum Summary) (json.RawMessage, error) {
			r, err := f(rc, as[P](p), sum)
			if err != nil {
				return nil, err
			}
			return encodeResult(kind, r)
		}
	}
	if f := s.PartitionBy; f != nil {
		b.PartitionBy = func(it any) string { return f(as[T](it)) }
	}
	if f := s.ExclusiveRefs; f != nil {
		b.ExclusiveRefs = func(it any) []Resource { return f(as[T](it)) }
	}
	if f := s.Label; f != nil {
		b.Label = func(it any) string { return f(as[T](it)) }
	}
	return runners{decode: decoder[P](kind), batch: b}
}

func eraseFixer[P, C any](kind Kind, s FixerSpec[P, C]) runners {
	if s.Candidates == nil || s.Load == nil || s.Evaluate == nil || s.Apply == nil {
		return runners{decode: decoder[P](kind)}
	}
	cands, load, evaluate, apply := s.Candidates, s.Load, s.Evaluate, s.Apply
	f := &FixerSpec[any, any]{
		Candidates: func(rc *RC, p any) (Source[any], error) {
			out, err := cands(rc, as[P](p))
			if err != nil {
				return Source[any]{}, err
			}
			return eraseSource(out), nil
		},
		Load: func(rc *RC, p any, rowID string) (any, bool, error) {
			c, ok, err := load(rc, as[P](p), rowID)
			return c, ok, err
		},
		Evaluate:    func(ic *ItemRC, p any, c any) (Row, error) { return evaluate(ic, as[P](p), as[C](c)) },
		Apply:       func(ic *ItemRC, w Writer, p any, row Row) error { return apply(ic, w, as[P](p), row) },
		Parallelism: s.Parallelism,
		PartitionBy: s.PartitionBy,
		AfterApply:  s.AfterApply,
		Guards:      append([]Guard(nil), s.Guards...),
	}
	return runners{decode: decoder[P](kind), fixer: f}
}

// eraseSource converts a typed source to the runner's. Items are copied
// once; Pages, Key and a DirtySet's loader are wrapped.
func eraseSource[T any](s Source[T]) Source[any] {
	out := Source[any]{Order: s.Order, Total: s.Total}
	if s.Items != nil {
		out.Items = make([]any, len(s.Items))
		for i, v := range s.Items {
			out.Items[i] = v
		}
	}
	if pages := s.Pages; pages != nil {
		out.Pages = func(ctx context.Context, after string, limit int) ([]any, string, error) {
			items, next, err := pages(ctx, after, limit)
			erased := make([]any, len(items))
			for i, v := range items {
				erased[i] = v
			}
			return erased, next, err
		}
	}
	if key := s.Key; key != nil {
		out.Key = func(v any) string { return key(as[T](v)) }
	}
	if d := s.dirty; d != nil {
		load := d.load
		out.dirty = &dirtySpec[any]{prefix: d.prefix, load: func(ctx context.Context, ids []string) ([]any, error) {
			items, err := load(ctx, ids)
			erased := make([]any, len(items))
			for i, v := range items {
				erased[i] = v
			}
			return erased, err
		}}
	}
	return out
}
