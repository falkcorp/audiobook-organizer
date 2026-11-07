// file: internal/server/opcontext.go
// version: 1.0.0
// guid: 8352260a-f5f8-4a9a-8cf1-7ce09ab5b072
// last-edited: 2026-11-07

package server

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// opContext is the books-side stand-in for core log's deleted OpContext
// (05-logging §11 item 2): the operation ID, type, status and the entities an
// operation touched, logged on every record of the operation. Until 05-tasks
// sets run attributes, an op handler carries it in the context with
//
//	ctx = logging.WithAttrs(ctx, slog.Any("", op))
//
// and logs through logger.InfoCtx and friends. It is a slog.LogValuer, so its
// value is read when each record is logged: a SetStatus or AddEntity after
// WithAttrs shows on later records. The empty key makes the handlers inline
// its group, so the keys stay flat (opID, opType, opStatus, entities), as
// HEAD's helpers wrote them.
//
// The mutex makes LogValue safe against SetStatus and AddEntity from another
// goroutine of the same operation (a worker logging while the handler updates
// the status).
type opContext struct {
	mu       sync.Mutex
	ID       string              // the operation's run ID
	Type     string              // the operation type, e.g. "dedup.book-scan"
	Status   string              // pending, success, failed, retry_later
	Entities map[string][]string // entity refs, e.g. {"books": ["id1"]}
}

// AddEntity appends ids under entityType ("books", "series", ...). Nil-safe.
func (oc *opContext) AddEntity(entityType string, ids ...string) {
	if oc == nil {
		return
	}
	oc.mu.Lock()
	defer oc.mu.Unlock()
	if oc.Entities == nil {
		oc.Entities = make(map[string][]string)
	}
	oc.Entities[entityType] = append(oc.Entities[entityType], ids...)
}

// SetStatus sets the status and returns oc for chaining. Nil-safe.
func (oc *opContext) SetStatus(status string) *opContext {
	if oc != nil {
		oc.mu.Lock()
		oc.Status = status
		oc.mu.Unlock()
	}
	return oc
}

// LogValue is the group opID, opType, opStatus and, when any were added,
// entities (JSON, keys sorted), read at log time.
func (oc *opContext) LogValue() slog.Value {
	if oc == nil {
		return slog.GroupValue()
	}
	oc.mu.Lock()
	defer oc.mu.Unlock()
	attrs := []slog.Attr{
		slog.String("opID", oc.ID),
		slog.String("opType", oc.Type),
		slog.String("opStatus", oc.Status),
	}
	if len(oc.Entities) > 0 {
		// A map[string][]string always marshals; the error is unreachable.
		b, _ := json.Marshal(oc.Entities)
		attrs = append(attrs, slog.String("entities", string(b)))
	}
	return slog.GroupValue(attrs...)
}
