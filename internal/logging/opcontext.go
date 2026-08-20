// file: internal/logging/opcontext.go
// version: 1.0.1

package logging

import "context"

type contextKey string

const opContextKey contextKey = "opContext"

// OpContext holds structured metadata for an operation.
// It's passed through context.Context so all logs in the operation's
// call stack automatically get tagged with operation ID, type, and status.
type OpContext struct {
	ID       string              // Unique operation identifier (UUID or operation ID from DB)
	Type     string              // Operation type: metadata-fetch, dedup, organize, etc.
	Status   string              // pending, success, failed
	Entities map[string][]string // Entity refs: {"books": ["id1"], "genres": ["rock"], "playlists": ["main"]}
}

// WithOp returns a new context with the operation context attached.
func WithOp(ctx context.Context, op *OpContext) context.Context {
	return context.WithValue(ctx, opContextKey, op)
}

// OpFromContext extracts the operation context from ctx, or returns nil if not present.
func OpFromContext(ctx context.Context) *OpContext {
	op, ok := ctx.Value(opContextKey).(*OpContext)
	if !ok {
		return nil
	}
	return op
}

// AddEntity adds entity references to the operation context.
// entityType: "books", "genres", "playlists", etc.
// ids: list of entity identifiers to add
func (oc *OpContext) AddEntity(entityType string, ids ...string) {
	if oc == nil {
		return
	}
	if oc.Entities == nil {
		oc.Entities = make(map[string][]string)
	}
	oc.Entities[entityType] = append(oc.Entities[entityType], ids...)
}

// SetStatus updates the operation status and returns self for chaining.
func (oc *OpContext) SetStatus(status string) *OpContext {
	if oc != nil {
		oc.Status = status
	}
	return oc
}
