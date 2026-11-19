// file: internal/books/writer/writertest/writertest.go
// version: 1.0.0
// guid: 2f8a6c40-9e13-4b75-8d02-c6a1f37e5b98
// last-edited: 2026-11-19

// Package writertest is the Effects-enforcing tasks.Writer the native kinds'
// tests run over: the executor's write fence without the executor.
//
// The executor's writer lives in core/internal/executor and cannot be imported
// from this module, and the kit's FakeRun hands a kind no writer at all. Every
// test host of a native kind used to bring its own writer, none of which
// checked the kind's Effects, so a kind that wrote a resource it never declared
// passed. Writer here applies the same predicate the executor applies
// (tasks.CheckWrite, one function, not a copy) before it records or performs
// the write, in Preview as in Live.
package writertest

import (
	"context"
	"fmt"
	"sync"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// Writer is a tasks.Writer that refuses a write outside the declared effects.
type Writer struct {
	kind     tasks.Kind
	declared []entity.Ref
	mode     tasks.Mode

	mu      sync.Mutex
	allowed []tasks.Write
	refused []Refusal
}

// Refusal is one write the fence refused, with its error.
type Refusal struct {
	Write tasks.Write
	Err   error
}

var _ tasks.Writer = (*Writer)(nil)

// New is a Writer for def's declared write set, in mode.
func New(def tasks.Definition, mode tasks.Mode) *Writer {
	return &Writer{kind: def.Kind(), declared: tasks.CommonOf(def).Effects.WriteSet(), mode: mode}
}

// ForSet is a Writer for an explicit declared set (a test that varies the
// declaration without building a definition).
func ForSet(kind tasks.Kind, declared []entity.Ref, mode tasks.Mode) *Writer {
	return &Writer{kind: kind, declared: declared, mode: mode}
}

// Do fences the write (tasks.CheckWrite), records it, and in Live calls fn. In
// Preview it records the intent, does not call fn and returns tasks.ErrPreview.
func (w *Writer) Do(ctx context.Context, wr tasks.Write, fn func(context.Context) ([]tasks.Change, error)) error {
	if err := tasks.CheckWrite(w.kind, w.declared, wr); err != nil {
		w.mu.Lock()
		w.refused = append(w.refused, Refusal{wr, err})
		w.mu.Unlock()
		return err
	}
	w.mu.Lock()
	w.allowed = append(w.allowed, wr)
	w.mu.Unlock()
	if w.mode == tasks.ModePreview {
		return tasks.ErrPreview
	}
	_, err := fn(ctx)
	return err
}

// MarkRowApplied is not modelled: no kind under this writer is a Fixer.
func (w *Writer) MarkRowApplied(context.Context, string) {}

// RowApplied reports false: no row is ever marked.
func (w *Writer) RowApplied(context.Context, string) (bool, error) { return false, nil }

// Writes are the writes the fence admitted, in order.
func (w *Writer) Writes() []tasks.Write {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]tasks.Write(nil), w.allowed...)
}

// Refusals are the writes the fence refused, in order.
func (w *Writer) Refusals() []Refusal {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Refusal(nil), w.refused...)
}

// Stamped is every distinct resource the admitted writes were stamped with,
// as "module/kind" strings.
func (w *Writer) Stamped() map[string]bool {
	out := map[string]bool{}
	for _, wr := range w.Writes() {
		for _, s := range wr.Stamps() {
			out[tasks.FormatRef(s)] = true
		}
	}
	return out
}

// RequireClean returns an error naming every refusal, or nil.
func (w *Writer) RequireClean() error {
	if r := w.Refusals(); len(r) > 0 {
		return fmt.Errorf("writertest: %d write(s) refused, first: %w", len(r), r[0].Err)
	}
	return nil
}
