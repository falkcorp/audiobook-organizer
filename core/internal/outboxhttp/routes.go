// file: core/internal/outboxhttp/routes.go
// version: 1.1.0
// guid: 53094039-febd-4731-bc69-141f4e2fb456
// last-edited: 2026-11-19

// Package outboxhttp is the operator surface of core's outbox (05-events
// §5E.11, F6-04): the dead letters (list, retry one, discard one), a
// consumer's backlog purge, the consumers' status and the topic catalog. All
// six routes are registered on httpapi's core group (/api/core/v1) over a
// narrow Operator (the methods of *outbox.Outbox the routes call), the
// events.Catalog and an audit.Logger.
//
// Every state change is audited with core:system.manage, synchronously and
// before the operation (05-rbac §3.5 and §8: a sync audit write that fails fails
// the operation, closed): the outbox operations commit in a batch of their own,
// so the record is a separate write that has to precede them. A record that
// cannot be written stops the request with the operation not taken (503, counted
// by the audit store in mediacore_audit_write_errors_total{path="sync"}). When
// the operation then fails, or turns out to have acted on another letter than
// the one the record named, a second record says so (outcome failure for a
// refusal, error for anything else; a purge that failed after some partitions
// committed says how many messages it skipped), and a purge that succeeds adds a
// record with the count it skipped, which is not known before it ran. A crash
// between the record and the operation leaves a record of an operation that
// never ran: over-recording is the direction the spec chose. The operation runs
// on a context that outlives the client's hang-up (bounded by opTimeout), so a
// disconnect cannot split a purge between its partitions, and the records after
// it are written the same way (bounded by auditTimeout). A route mounted without
// its audit logger refuses before anything happens. The purge is the one route
// that also needs an interactive sign-in (httpapi.Guards.Interactive), as it is
// the only way a disabled module's retained messages are discarded.
//
// Nothing here hides a row from a caller who may reach the route: the dead
// letter list, the consumer status and the catalog are the same for every holder
// of the scope, and a response never carries a message payload or its key.
// Permission and guard enforcement is the router's (stage 13, F4-08); the
// handlers add none.
//
// The package is dark until F6-03 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package outboxhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
)

// permView and permManage are the scopes of the routes (05-events §5E.11).
const (
	permView   rbac.Permission = "core:system.view"
	permManage rbac.Permission = "core:system.manage"
)

// The audit actions of the three audited routes (05-rbac §7.1, `core.outbox.*`).
const (
	actionRetry   = "core.outbox.dead_retry"
	actionDiscard = "core.outbox.dead_discard"
	actionPurge   = "core.outbox.backlog_purge"
)

// opTimeout bounds an operation and auditTimeout each audit write. Both run on
// a context detached from the request's cancellation (an operation must not be
// split by a hang-up, and a record must outlive one), which drops its deadline
// too, so each carries its own.
const (
	opTimeout    = 2 * time.Minute
	auditTimeout = 10 * time.Second
)

// The reasons of the records after the first, and of a purge's first and last.
const (
	reasonRequested   = "requested"     // a purge's first record: the count is not known yet
	reasonCompleted   = "completed"     // a purge's record of what it skipped
	reasonPartial     = "partial"       // a purge failed after some partitions committed
	reasonRefused     = "refused"       // the operation was refused (404, 409)
	reasonFailed      = "failed"        // the operation failed
	reasonTopicDiffer = "topic_differs" // the letter taken was on another topic than the first record named
)

// maxDeadPage is the most dead letters one request returns: outbox.ListDead's
// own bound, below httpapi's limit of 1000.
const maxDeadPage = 500

// Operator is what the routes call of the outbox, so the kernel hands this
// package *outbox.Outbox and nothing wider (03 §3.3).
type Operator interface {
	ListDead(ctx context.Context, after string, limit int) (letters []outbox.DeadLetter, next string, err error)
	DeadTopic(ctx context.Context, consumer string, seq uint64) (topic string, err error)
	RetryDead(ctx context.Context, consumer string, seq uint64) (topic string, err error)
	DiscardDead(ctx context.Context, consumer string, seq uint64) (topic string, err error)
	PurgeBacklog(ctx context.Context, consumer string) (skipped int, err error)
	Status(ctx context.Context) (outbox.Status, error)
}

var _ Operator = (*outbox.Outbox)(nil)

// Option configures Register.
type Option func(*api)

// WithLogger sets the logger that receives the causes of failures the response
// withholds (nil discards, the default).
func WithLogger(l *slog.Logger) Option { return func(a *api) { a.log = l } }

// api is what the handlers share. Its fields are fixed at Register.
type api struct {
	op   Operator
	cat  events.Catalog
	rec  audit.Logger
	log  *slog.Logger
	base string // the group's base path, for the audit records' routes
}

// Register registers the six operator routes on g, which must be the router's
// CoreGroup(). A nil op makes every route that needs it answer 503, a nil cat
// the topics route, and a nil rec every state change: nothing is applied that
// cannot be audited.
func Register(g *httpapi.Group, op Operator, cat events.Catalog, rec audit.Logger, opts ...Option) {
	a := newAPI(g.Base(), op, cat, rec, opts...)
	tags := []string{"outbox"}
	manage, view := rbac.Need(permManage), rbac.Need(permView)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/outbox/dead", Name: "listDeadLetters", Perm: manage, Tags: tags,
		Summary: "List the outbox's dead letters",
		Doc:     "Dead letters in (consumer, topic, seq) order, at most 500 per page; follow next_cursor until there is none (a page that ends exactly at the last letter still carries one, and the next page is empty). A letter shows its consumer, topic, seq, partition, attempts, the token that says why it died, and its idempotency key; never its payload or its key. A dead letter stays until an operator retries or discards it.",
	}, a.listDead)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodPost, Path: "/outbox/dead/{consumer}/{seq}/retry", Name: "retryDeadLetter", Perm: manage, Tags: tags,
		Summary: "Retry one dead letter",
		Doc:     "Hands the message back to its consumer alone, at the end of the stream under a new seq, and deletes the dead letter. Audited as core.outbox.dead_retry before the retry runs: when the record cannot be written the answer is 503 and nothing was retried; a retry that then fails is recorded as the failure it was. 404 when the consumer is unknown or has no dead letter at seq; 409 when the letter changed under the operation (list it again).",
	}, a.retry)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodDelete, Path: "/outbox/dead/{consumer}/{seq}", Name: "discardDeadLetter", Perm: manage, Tags: tags,
		Summary: "Discard one dead letter",
		Doc:     "Deletes the dead letter for good. Audited as core.outbox.dead_discard before the delete runs: when the record cannot be written the answer is 503 and nothing was discarded; a discard that then fails is recorded as the failure it was. 404 and 409 as for a retry.",
	}, a.discard)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodDelete, Path: "/outbox/consumers/{consumer}/backlog", Name: "purgeConsumerBacklog", Perm: manage, Tags: tags,
		Guards:  httpapi.Guards{Interactive: true},
		Summary: "Purge a consumer's backlog",
		Doc:     "Advances every cursor of the consumer to the head of its topic and clears its retries, discarding the messages it has not taken over; answers how many it skipped. Dead letters stay, and messages sequenced after the purge are delivered normally. It is the only way a disabled module's retained messages are discarded. Needs an interactive sign-in (not an API key). Audited as core.outbox.backlog_purge before the purge runs: when the record cannot be written the answer is 503 and nothing was purged; the purge then records how many messages it skipped, or the failure it ended in. The purge runs to its end even if the client hangs up. 404 when the consumer is unknown or has no backlog (it never ran); 409 when the topic is not available to this build or its partition count changed.",
	}, a.purge)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/outbox/consumers", Name: "listOutboxConsumers", Perm: view, Tags: tags,
		Summary: "List the outbox's consumers",
		Doc:     "Every declared consumer, enabled or not, with its lag, pending, retrying and dead counts and its topic's intake backlog. A disabled module's consumer is listed with the backlog the store keeps for it (retained_backlog). A count marked capped is a floor: the store counts at most 1000 per partition. What the relay will not work on until an operator acts (a topic, a pair, a family of stored keys) is listed under stopped, beside the page and whole on every page, each with a closed cause token.",
	}, a.consumers)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/events/topics", Name: "listEventTopics", Perm: view, Tags: []string{"events"},
		Summary: "List the topic catalog",
		Doc:     "Every declared topic, by name: the permission that observes it, its payload JSON Schema, whether it is durable (an outbox topic, with its partitions) and its notification and realtime metadata. The catalog is the same for every caller of the route.",
	}, a.topics)
}

// newAPI is the handlers' shared state; base is the group's base path.
func newAPI(base string, op Operator, cat events.Catalog, rec audit.Logger, opts ...Option) *api {
	a := &api{op: op, cat: cat, rec: rec, base: base}
	for _, o := range opts {
		o(a)
	}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	return a
}

// ---- wire types ------------------------------------------------------------

// The wire types carry the "Outbox" or "Event" prefix because an OpenAPI
// component is named <module>.<TypeName> and every core capability shares the
// "core" owner.

// OutboxDeadLetter is one dead-lettered message. Error is the token that says
// why it died (permanent, schema, mode_unset, panic, timeout, error), never an
// error text. The message's payload and key are not part of it.
type OutboxDeadLetter struct {
	Consumer       string    `json:"consumer"`
	Topic          string    `json:"topic"`
	Seq            uint64    `json:"seq"`
	Partition      int       `json:"partition"`
	Attempts       int       `json:"attempts"`
	Error          string    `json:"error"`
	IdempotencyKey string    `json:"idempotency_key"`
	Producer       string    `json:"producer,omitempty"`
	StagedAt       time.Time `json:"staged_at"`
	DeadAt         time.Time `json:"dead_at"`
}

// OutboxPurgeResult is a purge's answer: the messages its consumer advanced past.
type OutboxPurgeResult struct {
	Skipped int `json:"skipped"`
}

// OutboxConsumer is one consumer's status. Lag is the age of the oldest message
// it has not taken over; a count that is a floor says so in its capped flag.
type OutboxConsumer struct {
	Name            string `json:"name"`
	Module          string `json:"module"`
	Topic           string `json:"topic"`
	Enabled         bool   `json:"enabled"`
	Declared        bool   `json:"declared"`
	Pending         int    `json:"pending"`
	PendingCapped   bool   `json:"pending_capped"`
	Retrying        int    `json:"retrying"`
	LagMS           int64  `json:"lag_ms"`
	Dead            int    `json:"dead"`
	IntakeBacklog   int    `json:"intake_backlog"`
	IntakeCapped    bool   `json:"intake_capped"`
	RetainedBacklog int    `json:"retained_backlog"`
}

// OutboxConsumerPage is the offset page of consumers (httpapi.Page's fields) with
// the outbox's stopped list beside it. Stopped is the whole list on every page:
// what stopped is not always a consumer's (a topic, a counter family), so it is
// not split across the consumers it might concern and none of it is left out.
type OutboxConsumerPage struct {
	Items   []OutboxConsumer `json:"items"`
	Count   int              `json:"count"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
	Total   int64            `json:"total"`
	Stopped []OutboxStopped  `json:"stopped"`
}

// OutboxStopped is something the relay will not work on until an operator acts.
// Cause is a closed token: corrupt_row, unknown_version, partitions_changed or
// counters_unbuilt. Topic is the topic it concerns, or, for a family of stored
// keys, that family's name (dead/, deadn/, a counter subject). Consumer and
// Partition are set when it is one consumer's pair on one partition.
type OutboxStopped struct {
	Cause     string `json:"cause"`
	Topic     string `json:"topic,omitempty"`
	Consumer  string `json:"consumer,omitempty"`
	Partition *int   `json:"partition,omitempty"`
}

// EventTopic is one entry of the topic catalog.
type EventTopic struct {
	Name       string              `json:"name"`
	Permission string              `json:"permission"`
	Schema     json.RawMessage     `json:"schema,omitempty"`
	Durable    *EventTopicDurable  `json:"durable,omitempty"`
	Notify     *EventTopicNotify   `json:"notify,omitempty"`
	Realtime   *EventTopicRealtime `json:"realtime,omitempty"`
}

// EventTopicDurable marks an outbox topic.
type EventTopicDurable struct {
	Partitions int `json:"partitions"`
}

// EventTopicNotify is a topic's notification metadata.
type EventTopicNotify struct {
	Title           string `json:"title"`
	DefaultSeverity string `json:"default_severity"`
}

// EventTopicRealtime is a topic's realtime stream metadata.
type EventTopicRealtime struct {
	CoalesceMS int64 `json:"coalesce_ms"`
}

// ---- GET /outbox/dead -------------------------------------------------------

type listDeadReq struct {
	httpapi.CursorParams
}

// deadCursorHash is the filter hash of the dead letter cursor. The list has no
// filters, so it only tells this route's cursors from another route's.
func deadCursorHash() uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("core.outbox.dead"))
	return h.Sum64()
}

// listDead pages the dead letters. The store pages in key order and its cursor
// is exclusive (the key to go on after), so the route builds its CursorPage
// from the store's page directly: httpapi.PaginateCursor's next position is
// inclusive. Nothing is filtered per caller, so no cursor can be moved by a row
// the caller may not see.
func (a *api) listDead(ctx context.Context, in *httpapi.In[listDeadReq]) (httpapi.CursorPage[OutboxDeadLetter], error) {
	var zero httpapi.CursorPage[OutboxDeadLetter]
	if a.op == nil {
		return zero, unavailable()
	}
	after, err := decodeDeadCursor(in.Req.Cursor)
	if err != nil {
		return zero, err
	}
	letters, next, err := a.op.ListDead(ctx, after, min(in.Req.Limit, maxDeadPage))
	if err != nil {
		return zero, err
	}
	page := httpapi.CursorPage[OutboxDeadLetter]{Items: make([]OutboxDeadLetter, 0, len(letters))}
	for _, d := range letters {
		page.Items = append(page.Items, OutboxDeadLetter{
			Consumer: d.Consumer, Topic: d.Topic, Seq: d.Seq, Partition: d.Partition, Attempts: d.Attempts,
			Error: d.Token, IdempotencyKey: d.IdempotencyKey, Producer: d.Producer, StagedAt: d.StagedAt, DeadAt: d.DeadAt,
		})
	}
	if next != "" {
		key, err := base64.RawURLEncoding.DecodeString(next)
		if err != nil {
			return zero, fmt.Errorf("outboxhttp: the store's next cursor is not base64url: %w", err)
		}
		page.NextCursor = httpapi.EncodeCursor(key, deadCursorHash())
	}
	return page, nil
}

// decodeDeadCursor is the store's `after` of a request's cursor ("" starts at
// the first letter). The cursor holds a dead-letter storage key, and the store
// scans from the key it is given, so a position that is not a well-formed dead
// letter key (a forged one, below the dead letters, would start the scan in
// another key family) is a 400 like any malformed cursor.
func decodeDeadCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	pos, err := httpapi.DecodeCursor(cursor, deadCursorHash())
	if err != nil {
		return "", err
	}
	if _, _, _, err := outboxstore.ParseDeadKey(pos); err != nil {
		return "", invalidCursor()
	}
	return base64.RawURLEncoding.EncodeToString(pos), nil
}

func invalidCursor() error {
	return &httpapi.Error{Status: http.StatusBadRequest, Code: "invalid_cursor", Detail: "malformed cursor",
		Fields: []httpapi.FieldError{httpapi.Field("/query/cursor", "invalid_cursor", "malformed cursor")}}
}

// ---- the audited routes -----------------------------------------------------

type deadReq struct {
	Consumer string `path:"consumer"`
	Seq      uint64 `path:"seq"`
}

type purgeReq struct {
	Consumer string `path:"consumer"`
}

// retry and discard are one flow over two outbox operations. The operation is
// a closure over a.op, not a method value: a nil operator must reach begin's 503,
// and evaluating a method value of a nil interface panics.
func (a *api) retry(ctx context.Context, in *httpapi.In[deadReq]) (httpapi.NoContent, error) {
	return a.dead(ctx, in, actionRetry, "POST "+a.base+"/outbox/dead/{consumer}/{seq}/retry",
		func(ctx context.Context, consumer string, seq uint64) (string, error) {
			return a.op.RetryDead(ctx, consumer, seq)
		})
}

func (a *api) discard(ctx context.Context, in *httpapi.In[deadReq]) (httpapi.NoContent, error) {
	return a.dead(ctx, in, actionDiscard, "DELETE "+a.base+"/outbox/dead/{consumer}/{seq}",
		func(ctx context.Context, consumer string, seq uint64) (string, error) {
			return a.op.DiscardDead(ctx, consumer, seq)
		})
}

// dead runs one dead-letter operation and audits it: availability and the actor
// first (nothing is applied that cannot be audited or attributed), the letter's
// topic next (a read of the consumer's keys that changes nothing, so a letter
// that is not there is a 404 with nothing to record), then the record, and only
// then the operation. The operation answers the topic of the letter it took; it
// differs from the one the record named only when the set of letters changed in
// between, and then a second record names the letter actually taken.
func (a *api) dead(ctx context.Context, in *httpapi.In[deadReq], action, route string,
	do func(ctx context.Context, consumer string, seq uint64) (string, error)) (httpapi.NoContent, error) {
	actor, err := a.begin(ctx, in.Principal)
	if err != nil {
		return httpapi.NoContent{}, err
	}
	consumer, seq := in.Req.Consumer, in.Req.Seq
	gone := deadLetterGone(consumer, seq)
	topic, err := a.op.DeadTopic(ctx, consumer, seq)
	if err != nil {
		return httpapi.NoContent{}, mapError(err, gone)
	}
	ev := a.event(in.RequestID, action, route, actor, consumer)
	ev.Details = map[string]any{"seq": seq, "topic": topic}
	if err := a.recordFirst(ctx, ev); err != nil {
		return httpapi.NoContent{}, err
	}
	opCtx, cancel := detached(ctx, opTimeout)
	defer cancel()
	taken, err := do(opCtx, consumer, seq)
	if err != nil {
		mapped := mapError(err, gone)
		a.recordFailure(ctx, ev, mapped, err)
		return httpapi.NoContent{}, mapped
	}
	if taken != topic {
		a.recordFollowUp(ctx, ev, reasonTopicDiffer, map[string]any{"topic": taken})
	}
	return httpapi.NoContent{}, nil
}

// purge records the request first (its count is not known yet), purges on a
// context a hang-up cannot cancel, and then records what it skipped, or the
// failure it ended in with what it had skipped by then (the count of committed
// partitions only: the outbox reports nothing for a partition that did not commit).
func (a *api) purge(ctx context.Context, in *httpapi.In[purgeReq]) (OutboxPurgeResult, error) {
	var zero OutboxPurgeResult
	actor, err := a.begin(ctx, in.Principal)
	if err != nil {
		return zero, err
	}
	consumer := in.Req.Consumer
	topic, known, err := a.consumerTopic(ctx, consumer)
	if err != nil {
		return zero, err
	}
	route := "DELETE " + a.base + "/outbox/consumers/{consumer}/backlog"
	ev := a.event(in.RequestID, actionPurge, route, actor, consumer)
	ev.Reason = reasonRequested
	ev.Details = map[string]any{}
	if topic != "" {
		ev.Details["topic"] = topic
	}
	if err := a.recordFirst(ctx, ev); err != nil {
		return zero, err
	}
	opCtx, cancel := detached(ctx, opTimeout)
	defer cancel()
	skipped, err := a.op.PurgeBacklog(opCtx, consumer)
	if err != nil {
		mapped := mapError(err, noBacklog(consumer, known))
		failure := ev
		failure.Details = cloneWith(ev.Details, map[string]any{"skipped": skipped})
		a.recordFailure(ctx, failure, mapped, err)
		return zero, mapped
	}
	a.recordFollowUp(ctx, ev, reasonCompleted, map[string]any{"skipped": skipped})
	return OutboxPurgeResult{Skipped: skipped}, nil
}

// begin is the first thing a state change does: it refuses when the route has
// no operator or no audit logger (503), and when nothing names the caller (401,
// a mounting mistake: stage 12 always binds a principal). It returns the audit
// actor, the one the router bound to the context or else the principal's.
func (a *api) begin(ctx context.Context, p *auth.Principal) (audit.Actor, error) {
	if a.op == nil || a.rec == nil {
		return audit.Actor{}, unavailable()
	}
	if bound, ok := audit.ActorFrom(ctx); ok {
		return bound, nil
	}
	if p == nil {
		return audit.Actor{}, &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"}
	}
	return actorOf(p), nil
}

// consumerTopic is the topic a consumer's backlog is on, and whether the status
// read knows the consumer at all: a purge's 404 says whether the consumer has no
// backlog (it never ran) or does not exist, and its audit record names the
// topic. A consumer the read lists with no topic has no registration and no
// cursor, so there is nothing to purge.
func (a *api) consumerTopic(ctx context.Context, consumer string) (topic string, known bool, err error) {
	st, err := a.op.Status(ctx)
	if err != nil {
		return "", false, err
	}
	for _, c := range st.Consumers {
		if c.Name == consumer {
			return c.Topic, true, nil
		}
	}
	return "", false, nil
}

// event is the audit record of a state change, as requested (outcome success).
func (a *api) event(requestID, action, route string, actor audit.Actor, consumer string) audit.Event {
	return audit.Event{
		Action: action, Outcome: audit.Success, Actor: actor, Permission: string(permManage), Route: route,
		Target:    entity.Ref{Module: "core", Kind: "outbox_consumer", ID: consumer},
		RequestID: requestID,
	}
}

// recordFirst writes the record of an operation before it runs. A write that
// fails stops the request with the operation not taken (05-rbac §3.5, §8). The
// write is bounded, and keeps the request's own cancellation: a client that has
// hung up before it has nothing to be recorded for.
func (a *api) recordFirst(ctx context.Context, ev audit.Event) error {
	wctx, cancel := context.WithTimeout(ctx, auditTimeout)
	defer cancel()
	if err := a.rec.Record(wctx, ev); err != nil {
		a.log.ErrorContext(ctx, "outboxhttp: the audit record could not be written, so the operation was not run",
			a.attribution(ev, "error", err)...)
		return (&httpapi.Error{Status: http.StatusServiceUnavailable, Code: "unavailable",
			Detail: "the audit log is not available; nothing was changed", Err: err}).WithRetryAfter(5 * time.Second)
	}
	return nil
}

// recordFailure writes the second record of an operation recorded as requested
// that was then refused (outcome failure) or failed (outcome error); a purge
// that failed after skipping messages is the error "partial". The request
// answers the operation's error either way, so a write that fails here is only
// logged, with everything that says who did what: the first record stands, and
// this log line is then all that tells the operation's end.
func (a *api) recordFailure(ctx context.Context, ev audit.Event, answered, cause error) {
	ev.Outcome, ev.Reason = audit.Error, reasonFailed
	if isRefusal(answered) {
		ev.Outcome, ev.Reason = audit.Failure, reasonRefused
	}
	if n, _ := ev.Details["skipped"].(int); n > 0 {
		ev.Outcome, ev.Reason = audit.Error, reasonPartial
	}
	a.writeDetached(ctx, ev, "outboxhttp: the outcome of a recorded operation could not be audited", cause)
}

// recordFollowUp writes a record after an operation that ran: a purge's count,
// or the topic of the letter an operation took when it was not the one the first
// record named. The operation is applied and recorded by then, so a failed write
// is only logged.
func (a *api) recordFollowUp(ctx context.Context, first audit.Event, reason string, extra map[string]any) {
	ev := first
	ev.Outcome, ev.Reason = audit.Success, reason
	ev.Details = cloneWith(first.Details, extra)
	a.writeDetached(ctx, ev, "outboxhttp: a follow-up audit record could not be written", nil)
}

// writeDetached writes ev on a context that outlives the request's cancellation
// (a hang-up is the likeliest reason an operation failed) and is bounded.
func (a *api) writeDetached(ctx context.Context, ev audit.Event, msg string, cause error) {
	wctx, cancel := detached(ctx, auditTimeout)
	defer cancel()
	if err := a.rec.Record(wctx, ev); err != nil {
		attrs := a.attribution(ev, "error", err)
		if cause != nil {
			attrs = append(attrs, slog.Any("operation_error", cause))
		}
		a.log.ErrorContext(ctx, msg, attrs...)
	}
}

// attribution is the log attributes that say who did what to what: the actor,
// the target, the numbers the record carries and the request. Never a message.
func (a *api) attribution(ev audit.Event, errKey string, err error) []any {
	attrs := []any{
		slog.String("action", ev.Action), slog.String("outcome", string(ev.Outcome)), slog.String("reason", ev.Reason),
		slog.String("actor_user_id", ev.Actor.UserID), slog.String("actor_username", ev.Actor.Username),
		slog.String("actor_method", ev.Actor.Method), slog.String("actor_credential_id", ev.Actor.CredentialID),
		slog.String("client_addr", ev.Actor.ClientAddr), slog.String("consumer", ev.Target.ID),
		slog.String("request_id", ev.RequestID),
	}
	for _, k := range []string{"seq", "topic", "skipped"} {
		if v, ok := ev.Details[k]; ok {
			attrs = append(attrs, slog.Any(k, v))
		}
	}
	return append(attrs, slog.Any(errKey, err))
}

// detached is ctx without its cancellation and deadline, bounded by d.
func detached(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}

// cloneWith is a copy of m with the entries of extra added: an event's details
// are shared by every copy of the event.
func cloneWith(m, extra map[string]any) map[string]any {
	out := make(map[string]any, len(m)+len(extra))
	maps.Copy(out, m)
	maps.Copy(out, extra)
	return out
}

// isRefusal reports whether err is one of the answers a client acts on (a 4xx
// problem) rather than a failure of the system.
func isRefusal(err error) bool {
	he, ok := errors.AsType[*httpapi.Error](err)
	return ok && he.Status >= 400 && he.Status < 500
}

// actorOf is the audit actor of p, for a context no middleware bound one to.
func actorOf(p *auth.Principal) audit.Actor {
	a := audit.Actor{Method: string(p.Method), CredentialID: p.CredentialID}
	if p.User != nil {
		a.UserID, a.Username = string(p.User.ID), p.User.Username
	}
	if p.ClientAddr.IsValid() {
		a.ClientAddr = p.ClientAddr.String()
	}
	return a
}

// ---- errors ----------------------------------------------------------------

func unavailable() error {
	return httpapi.Unavailable("the outbox is not available", 5*time.Second)
}

func deadLetterGone(consumer string, seq uint64) *httpapi.Error {
	return httpapi.NotFound("dead letter", fmt.Sprintf("%s/%d", consumer, seq))
}

func noBacklog(consumer string, known bool) *httpapi.Error {
	if known {
		return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: fmt.Sprintf("consumer %q has no backlog (it never ran)", consumer)}
	}
	return httpapi.NotFound("consumer", consumer)
}

// mapError turns the outbox's refusals into the statuses of an operator route,
// by sentinel and never by text. gone is the 404 of a missing dead letter; an
// unknown consumer is the same 404 for the operation that has no letter to name.
// What is not a refusal (a store failure, a canceled context) is returned
// unchanged: httpapi answers it 500 and withholds the cause.
func mapError(err error, gone *httpapi.Error) error {
	switch {
	case errors.Is(err, outbox.ErrDeadNotFound), errors.Is(err, outbox.ErrUnknownConsumer):
		return gone
	case errors.Is(err, outbox.ErrDeadConflict):
		return httpapi.Conflict("conflict", "the dead letter changed under the operation; list it again")
	case errors.Is(err, outbox.ErrUnknownTopic), errors.Is(err, outbox.ErrNotDurable), errors.Is(err, outbox.ErrBadPartitions),
		errors.Is(err, outboxstore.ErrPartitionsChanged):
		return httpapi.Conflict("conflict", "the consumer's topic is not available to this build or its partition count changed")
	}
	return err
}

// ---- GET /outbox/consumers and GET /events/topics ---------------------------

type pageReq struct {
	httpapi.PageParams
}

func (a *api) consumers(ctx context.Context, in *httpapi.In[pageReq]) (OutboxConsumerPage, error) {
	var zero OutboxConsumerPage
	if a.op == nil {
		return zero, unavailable()
	}
	st, err := a.op.Status(ctx)
	if err != nil {
		return zero, err
	}
	all := make([]OutboxConsumer, 0, len(st.Consumers))
	for _, c := range st.Consumers {
		all = append(all, OutboxConsumer{
			Name: c.Name, Module: c.Module, Topic: c.Topic, Enabled: c.Enabled, Declared: c.Declared,
			Pending: c.Pending, PendingCapped: c.PendingCapped, Retrying: c.Retrying, LagMS: c.Lag.Milliseconds(),
			Dead: c.Dead, IntakeBacklog: c.IntakeBacklog, IntakeCapped: c.IntakeCapped, RetainedBacklog: c.RetainedBacklog,
		})
	}
	p := pageOf(all, in.Req.PageParams)
	out := OutboxConsumerPage{Items: p.Items, Count: p.Count, Limit: p.Limit, Offset: p.Offset, Total: p.Total,
		Stopped: make([]OutboxStopped, 0, len(st.Stopped))}
	for _, s := range st.Stopped {
		v := OutboxStopped{Cause: s.Cause, Topic: s.Topic, Consumer: s.Consumer}
		if s.Consumer != "" && s.Partition >= 0 { // -1 is the counter of letters that do not decode: no partition
			pp := s.Partition
			v.Partition = &pp
		}
		out.Stopped = append(out.Stopped, v)
	}
	return out, nil
}

func (a *api) topics(_ context.Context, in *httpapi.In[pageReq]) (httpapi.Page[EventTopic], error) {
	var zero httpapi.Page[EventTopic]
	if a.cat == nil {
		return zero, unavailable()
	}
	var all []EventTopic
	for spec := range a.cat.All() {
		all = append(all, topicView(spec))
	}
	slices.SortFunc(all, func(x, y EventTopic) int {
		switch {
		case x.Name < y.Name:
			return -1
		case x.Name > y.Name:
			return 1
		}
		return 0
	})
	return pageOf(all, in.Req.PageParams), nil
}

func topicView(s events.TopicSpec) EventTopic {
	v := EventTopic{Name: s.Name, Permission: s.Permission.String(), Schema: s.Schema}
	if s.Durable != nil {
		v.Durable = &EventTopicDurable{Partitions: s.Durable.Partitions}
	}
	if s.Notify != nil {
		v.Notify = &EventTopicNotify{Title: s.Notify.Title, DefaultSeverity: s.Notify.DefaultSeverity}
	}
	if s.Realtime != nil {
		v.Realtime = &EventTopicRealtime{CoalesceMS: s.Realtime.Coalesce.Milliseconds()}
	}
	return v
}

// pageOf is the offset page of all, the default list shape (05-http-api §4.4):
// both lists are bounded by what the build declares, so the whole set is in hand.
func pageOf[T any](all []T, p httpapi.PageParams) httpapi.Page[T] {
	start := min(p.Offset, len(all))
	end := min(start+p.Limit, len(all))
	items := make([]T, 0, end-start)
	items = append(items, all[start:end]...)
	return httpapi.Page[T]{Items: items, Count: len(items), Limit: p.Limit, Offset: p.Offset, Total: int64(len(all))}
}
