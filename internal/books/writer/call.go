// file: internal/books/writer/call.go
// version: 0.1.0
// guid: 0c6a4e81-5f23-4d97-b8a0-3e71d9c2f564
// last-edited: 2026-11-22

package writer

import (
	"context"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// Err is Gate.Do for a store call that returns only an error: the call is one
// gated write stamped with method's whole row, and a refusal leaves call
// uncalled.
func (g *Gate) Err(ctx context.Context, method string, subject entity.Ref, call func() error) error {
	return g.Do(ctx, method, subject, func(context.Context) ([]tasks.Change, error) { return nil, call() })
}

// Value is Gate.Do for a store call that returns a value and an error. When the
// gate refuses, the zero T and the refusal come back and call is not run.
func Value[T any](ctx context.Context, g *Gate, method string, subject entity.Ref, call func() (T, error)) (T, error) {
	var out T
	err := g.Do(ctx, method, subject, func(context.Context) ([]tasks.Change, error) {
		var e error
		out, e = call()
		return nil, e
	})
	// A call that ran and failed keeps what it returned (the store's partial
	// result); a refusal never ran call, so out is already the zero value.
	return out, err
}

// Ref is the subject of a call on one entity of a books resource, or on the
// resource as a whole when id is empty.
func Ref(resource, id string) entity.Ref {
	if id == "" {
		return tasks.Res(Module, resource)
	}
	return tasks.ResID(Module, resource, id)
}
