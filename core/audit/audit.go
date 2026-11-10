// file: core/audit/audit.go
// version: 1.0.0
// guid: 868e4187-08f4-4ee4-8747-ba40f7c9e404
// last-edited: 2026-11-10

// Package audit is core's audit log (05-rbac §3.5, §7; G13): who did what to
// which entity, with what outcome. Synchronous records (Record, RecordIn)
// commit with the change they describe or fail it; high-volume events (Emit:
// denials, login failures) queue to one writer goroutine and may be dropped
// under load, counted. Reads go through Reader.Query.
//
// The package is dark until F4: nothing calls it, no route serves it and no
// task prunes it. New writes nothing.
//
// Layer L4 (03 R4): it imports entity, metrics and storage only, and holds no
// package-level mutable state (R5).
package audit

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
)

type Outcome string

const (
	Success Outcome = "success"
	Failure Outcome = "failure" // e.g. bad password
	Denied  Outcome = "denied"  // permission/guard refusal
	Error   Outcome = "error"   // the system failed
)

type Actor struct {
	UserID       string // "" for anonymous
	Username     string // snapshot at write time
	Method       string // auth.Method value
	CredentialID string // session public id / key id; never a secret
	ClientAddr   string // as resolved by httpapi (trusted-proxy aware)
	UserAgent    string // truncated to 256 bytes
}

// Event is what callers submit.
type Event struct {
	Action     string         // "<ns>.<noun>.<verb>", present tense (03 §3.6, COR2-19): "core.auth.login", "core.role.update", "books.user.merge"
	Outcome    Outcome
	Actor      Actor          // filled from ctx principal when zero
	Target     entity.Ref     // the one entity reference type (03 §3.3, COR-17)
	Permission string         // scope that was checked, for denials and approvals
	Route      string         // "POST /api/core/v1/roles/{id}" (pattern, not raw path)
	RequestID  string         // filled from ctx when empty
	TraceID    string
	Reason     string         // short machine reason: "scope_escalation", "bad_password"
	Details    map[string]any // redacted by key name + value scan, capped at 4 KiB JSON
}

// Record is what is stored and returned.
type Record struct {
	Seq       uint64    // unique, increasing in allocation order; may have gaps (§7.2)
	ID        string    // ULID
	Time      time.Time // UTC
	Event
	Coalesced uint32 // >1 when identical denials were folded (§7.3)
}

type Logger interface {
	// Record writes synchronously; use for state changes. Returns an error and
	// the caller must fail the operation (fail-closed).
	Record(ctx context.Context, e Event) error
	// RecordIn appends the record to b so it commits atomically with the change.
	RecordIn(ctx context.Context, b *storage.Batch, e Event) error // the caller's batch, never a copy (COR2-12)
	// Emit queues asynchronously (bounded); use for high-volume events (denials,
	// login failures). Never blocks; drops are counted.
	Emit(ctx context.Context, e Event)
}

type Filter struct {
	Since, Until time.Time
	ActorUserID  string
	ActionPrefix string // "core.auth."
	Outcome      Outcome
	Target       entity.Ref // zero fields match anything
	BeforeSeq    uint64     // cursor (§7.6)
	Limit        int
}

type Reader interface {
	Query(ctx context.Context, f Filter) iter.Seq2[Record, error]
}

// Actor plumbing: auth's middleware binds the actor once per request; Event.Actor
// left zero is filled from here.

// actorKey is the context key of WithActor's value.
type actorKey struct{}

// WithActor returns a context carrying a. A zero Actor in an Event is filled
// from the nearest enclosing WithActor.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor WithActor bound to ctx.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Defaults of Options (05-rbac §7.3, §10). A zero field of Options takes one.
const (
	DefaultAsyncBuffer     = 4096
	DefaultCoalesceWindow  = time.Minute
	DefaultDetailsMaxBytes = 4096
	DefaultBatchMax        = 256
	DefaultBatchWait       = 100 * time.Millisecond

	// DefaultQueryLimit is a Query's page size when Filter.Limit is zero;
	// MaxQueryLimit caps a larger one.
	DefaultQueryLimit = 100
	MaxQueryLimit     = 1000
)

// Options are the core.audit.* values New takes (05-rbac §10) and the two
// constants of §7.3. A zero field takes its default; a negative CoalesceWindow
// turns coalescing off.
type Options struct {
	AsyncBuffer     int           // core.audit.async_buffer, default 4096
	CoalesceWindow  time.Duration // core.audit.coalesce_window, default 1m
	DetailsMaxBytes int           // core.audit.details_max_bytes, default 4096
	BatchMax        int           // 256 (§7.3); not a config key
	BatchWait       time.Duration // 100ms (§7.3); not a config key
}

// withDefaults returns o with every zero field replaced by its default.
func (o Options) withDefaults() Options {
	if o.AsyncBuffer <= 0 {
		o.AsyncBuffer = DefaultAsyncBuffer
	}
	if o.CoalesceWindow == 0 {
		o.CoalesceWindow = DefaultCoalesceWindow
	}
	if o.DetailsMaxBytes <= 0 {
		o.DetailsMaxBytes = DefaultDetailsMaxBytes
	}
	if o.BatchMax <= 0 {
		o.BatchMax = DefaultBatchMax
	}
	if o.BatchWait <= 0 {
		o.BatchWait = DefaultBatchWait
	}
	return o
}

// Deps is what New needs.
type Deps struct {
	Keyspace storage.Keyspace  // c/audit/
	Seq      *storage.Sequence // allocation persists the high-water mark first (DATA-19)
	// Metrics is the core "audit" scope. It is nil when the store is built
	// before the metrics Provider exists (the kernel builds it before P4b,
	// the Provider is P5): Instrument registers the instruments later. The
	// store counts on its own atomics either way, so nothing recorded before
	// the instruments exist is lost.
	Metrics *metrics.Scope
	Log     *slog.Logger
	Now     func() time.Time // nil = time.Now
}

// Errors of the write path. A caller of Record or RecordIn fails its
// operation on any error (fail-closed); Emit counts and drops instead.
var (
	// ErrInvalidAction: Event.Action is not "<ns>.<noun>.<verb>" (03 §3.6).
	ErrInvalidAction = errors.New("audit: the action does not match <ns>.<noun>.<verb> ([a-z][a-z0-9_]* segments)")
	// ErrInvalidOutcome: Event.Outcome is none of the four Outcome values.
	ErrInvalidOutcome = errors.New("audit: the outcome is not success, failure, denied or error")
	// ErrNilBatch: RecordIn without a batch.
	ErrNilBatch = errors.New("audit: RecordIn needs the caller's batch")
	// ErrStarted and ErrStopped: Start's lifecycle (once, never again).
	ErrStarted = errors.New("audit: the async writer was already started")
	ErrStopped = errors.New("audit: the store is stopped")
)
