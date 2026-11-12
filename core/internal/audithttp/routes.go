// file: core/internal/audithttp/routes.go
// version: 1.0.3
// guid: b7ba0d64-88a0-4182-aa62-18009112fbcf
// last-edited: 2026-11-11

// Package audithttp is the HTTP surface of core's audit log (05-rbac §7.6,
// F4-05): GET /audit, a cursor-paged query, and GET /audit/export, the same
// records as an NDJSON stream. Both are registered on httpapi's core group
// (/api/core/v1) over audit.Reader, and both need core:audit.view.
//
// The audit log holds user names, client addresses and credential ids, so the
// routes refuse the auth-disabled principal with 401 authentication_required
// (05-authn §4.11) and show a caller who does not hold core:users.manage only
// the records whose action core classifies as safe for every audit viewer
// (audit.VisibilityOf); a credential or identity action, and an action core
// does not classify, are withheld. Exporting is itself audited: the route records
// core.audit.export synchronously before the first byte and streams nothing if
// that record cannot be written.
//
// The package is dark until F4-08 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package audithttp

import (
	"context"
	"encoding/json"
	"iter"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// permView is the scope both routes declare (05-rbac §7.6). It is
// AuthRequired, so the auth-disabled principal never holds it.
const permView rbac.Permission = "core:audit.view"

// permUsers is what lifts the withholding of authentication and user records.
const permUsers rbac.Permission = "core:users.manage"

// exportAction is the record an export writes about itself.
const exportAction = "core.audit.export"

// flushEvery is how many NDJSON lines are written between flushes.
const flushEvery = 100

// Recorder is the one method of audit.Logger the export uses: the synchronous
// write of its own record. audit.Logger satisfies it.
type Recorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// Deps is what Register needs. Reader serves both routes (nil makes each
// answer 503). Recorder writes the export's record (nil makes the export
// answer 503: an export that cannot be audited does not run). Log receives
// the causes of store failures (nil discards).
type Deps struct {
	Reader   audit.Reader
	Recorder Recorder
	Log      *slog.Logger
}

// api is what the handlers share. Its fields are fixed at Register.
type api struct {
	reader audit.Reader
	rec    Recorder
	log    *slog.Logger
	base   string // the group's base path, for the export record's route
	// scanCap bounds the records one GET /audit request reads for a viewer who
	// is not a user manager; 0 is defaultScanCap. Tests lower it.
	scanCap int
}

// defaultScanCap is how many records one GET /audit request reads, for a
// viewer who is not a user manager, before it answers with what it has and a
// cursor to go on from. The store filters by one action prefix only, so the
// withheld records of a log that is mostly identity traffic are read and
// dropped here; without a bound a single request could walk the whole log.
const defaultScanCap = 10_000

func (a *api) scanLimit() int {
	if a.scanCap > 0 {
		return a.scanCap
	}
	return defaultScanCap
}

// Register registers the audit routes on g, which must be the router's
// CoreGroup(). Both are reads, so neither carries Guards.Interactive; the
// export is RateExpensive (05-rbac §7.6).
func Register(g *httpapi.Group, deps Deps) {
	a := &api{reader: deps.Reader, rec: deps.Recorder, log: deps.Log, base: g.Base()}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	tags := []string{"audit"}
	perm := rbac.Need(permView)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/audit", Name: "listAudit", Perm: perm, Tags: tags,
		Summary: "Query the audit log, newest first",
		Doc:     "Filters: since and until (RFC 3339), actor (a user id), action (a prefix such as core.auth.), outcome (success, failure, denied, error) and target (module, module/kind or module/kind/id). The cursor continues a query with the same filters. A record whose action is about an identity or a credential (core.auth.*, core.user.*, core.apikey.*, core.session.*, core.invite.*, core.templogin.*, core.identity.*, core.bootstrap.*, core.permission.deny and the like), and a record whose action core does not classify (a module's own), is left out unless the caller holds core:users.manage, who also alone sees actor.credential_id and actor.client_addr. A caller without core:users.manage has withheld records read and dropped by the query, at most 10,000 per request: a page may then be short or empty with a next_cursor, and the client follows cursors until there is none. The auth-disabled principal is refused with 401.",
	}, a.list)
	httpapi.HandleRaw(g, httpapi.Route{
		Method: http.MethodGet, Path: "/audit/export", Name: "exportAudit", Perm: perm, Tags: tags, RateClass: httpapi.RateExpensive,
		Summary: "Export the audit log as NDJSON",
		Doc:     "The same filters as the query, without a cursor or limit; one JSON record per line, newest first, each as the query returns it. The export is recorded (core.audit.export) before the first line and does not start if that record cannot be written. A read that fails part-way ends the stream with the line {\"error\":\"export_incomplete\"}.",
	}, httpapi.RawDoc{Query: []string{"since", "until", "actor", "action", "outcome", "target"}}, http.HandlerFunc(a.export))
}

// ---- GET /audit -----------------------------------------------------------

func (a *api) list(ctx context.Context, in *httpapi.In[listReq]) (httpapi.CursorPage[AuditRecord], error) {
	var zero httpapi.CursorPage[AuditRecord]
	if err := a.guard(in.Principal); err != nil {
		return zero, err
	}
	r := in.Req
	q, err := parseQuery(r.Since, r.Until, r.Actor, r.Action, r.Outcome, r.Target)
	if err != nil {
		return zero, err
	}
	hash := q.hash()
	f := q.filter
	if r.Cursor != "" {
		pos, err := httpapi.DecodeCursor(r.Cursor, hash)
		if err != nil {
			return zero, err
		}
		if f.BeforeSeq, err = beforeSeq(pos); err != nil {
			return zero, err
		}
	}
	// A batch of one more than the page, so the record that starts the next
	// page is in hand when this one is full. The loop in records is correct for
	// any size; this one avoids reading a page twice.
	batch := min(max(r.Limit, 1)+1, audit.MaxQueryLimit)
	manager := isManager(in.Principal)
	// A manager's records are all kept, so what it reads is what it returns and
	// the page bounds the read. Anyone else's are filtered here, so the read
	// itself is bounded: see defaultScanCap.
	var budget *scanBudget
	if !manager {
		budget = &scanBudget{left: a.scanLimit()}
	}
	seq := func(yield func(httpapi.Positioned[AuditRecord], error) bool) {
		for rec, err := range a.scan(ctx, f, batch, visibleTo(in.Principal), budget) {
			if err != nil {
				yield(httpapi.Positioned[AuditRecord]{}, a.storeError(ctx, err))
				return
			}
			if !yield(httpapi.Positioned[AuditRecord]{Pos: cursorPos(rec.Seq), Item: wireFor(rec, manager)}, nil) {
				return
			}
		}
	}
	page, err := httpapi.PaginateCursor(ctx, r.CursorParams, seq, hash)
	if err != nil {
		return zero, err
	}
	// The read stopped at its bound with the page not full: the records beyond
	// the last one read are unread, not absent, so say where to go on from. The
	// page may then be short or empty with a cursor; a client follows cursors
	// until there is none.
	if page.NextCursor == "" && budget.stopped() {
		page.NextCursor = httpapi.EncodeCursor(cursorPos(budget.last-1), hash)
	}
	return page, nil
}

// ---- GET /audit/export ----------------------------------------------------

func (a *api) export(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	if err := a.guard(p); err != nil {
		httpapi.WriteError(w, r, err, a.log)
		return
	}
	q, err := queryOf(r.URL.Query())
	if err != nil {
		httpapi.WriteError(w, r, err, a.log)
		return
	}
	manager := isManager(p)
	next, stop := iter.Pull2(a.records(ctx, q.filter, audit.MaxQueryLimit, visibleTo(p)))
	defer stop()
	// The first record is read before anything is recorded or sent, so a store
	// that is down answers 503 and leaves no record of an export that never ran.
	first, ferr, more := next()
	if more && ferr != nil {
		httpapi.WriteError(w, r, a.storeError(ctx, ferr), a.log)
		return
	}
	// The export is audited before its first byte; a failed write stops it.
	if a.rec == nil {
		httpapi.WriteError(w, r, unavailable(), a.log)
		return
	}
	ev := audit.Event{
		Action: exportAction, Outcome: audit.Success, Permission: string(permView),
		Route: "GET " + a.base + "/audit/export", Details: q.details,
	}
	if bound, ok := audit.ActorFrom(ctx); ok {
		ev.Actor = bound
	} else {
		ev.Actor = actorOf(p)
	}
	if err := a.rec.Record(ctx, ev); err != nil {
		a.log.ErrorContext(ctx, "audithttp: recording the export failed; nothing was sent", slog.Any("error", err))
		httpapi.WriteError(w, r, unavailable(), a.log)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson")
	h.Set("Content-Disposition", `attachment; filename="audit-export.ndjson"`)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	rc := http.NewResponseController(w)
	n := 0
	for more {
		if ferr != nil {
			// A read that fails after the first line: the stream says so, as
			// data, so a consumer never mistakes it for the whole log.
			a.log.ErrorContext(ctx, "audithttp: the export read failed part-way", slog.Any("error", ferr))
			if err := enc.Encode(map[string]string{"error": "export_incomplete"}); err != nil {
				return
			}
			a.flushEnd(ctx, rc) // best effort: the stream ends here either way
			return
		}
		if err := enc.Encode(wireFor(first, manager)); err != nil {
			// The client went away; there is nobody to tell.
			return
		}
		if n++; n%flushEvery == 0 {
			if err := rc.Flush(); err != nil && !isNoFlush(err) {
				return
			}
		}
		first, ferr, more = next()
	}
	a.flushEnd(ctx, rc) // the end of the stream; a writer that cannot flush has sent it
}

// flushEnd flushes the last of the stream. A writer that cannot flush has
// sent it already; any other failure means the client went away, which is
// logged at debug and answered by nothing: the status line is out.
func (a *api) flushEnd(ctx context.Context, rc *http.ResponseController) {
	if err := rc.Flush(); err != nil && !isNoFlush(err) {
		a.log.DebugContext(ctx, "audithttp: the export's last flush failed", slog.Any("error", err))
	}
}

// queryOf reads the export's filters from the URL's query. Unknown parameters
// are ignored, as the typed route ignores them.
func queryOf(v url.Values) (query, error) {
	var since, until time.Time
	var fs []httpapi.FieldError
	for _, c := range []struct {
		name string
		dst  *time.Time
	}{{"since", &since}, {"until", &until}} {
		s := v.Get(c.name)
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			fs = append(fs, httpapi.Field("/query/"+c.name, "invalid_format", "must be an RFC 3339 time"))
			continue
		}
		*c.dst = t
	}
	if len(fs) > 0 {
		return query{}, httpapi.Invalid(fs...)
	}
	return parseQuery(since, until, v.Get("actor"), v.Get("action"), v.Get("outcome"), v.Get("target"))
}

// ---- reading and visibility -----------------------------------------------

// records is the records matching f that keep admits, newest first, read from
// the store in batches. The store's Limit counts the records its own filter
// matches, so a record keep withholds still uses up a place in a batch; the
// loop therefore asks again, from the last seq it saw, until a batch comes
// back short. It ends at the first error, after yielding it.
func (a *api) records(ctx context.Context, f audit.Filter, batch int, keep func(audit.Record) bool) iter.Seq2[audit.Record, error] {
	return a.scan(ctx, f, batch, keep, nil)
}

// scanBudget bounds how many records one request reads. A nil budget is
// unbounded. After the read, stopped says it ended on the bound, and last is
// the seq of the last record read: everything newer was read, everything older
// was not.
type scanBudget struct {
	left int
	last uint64
	hit  bool
}

// stopped reports whether the read ended on the bound with records left to
// read: a continuation cursor is owed, and there is something older than last.
func (b *scanBudget) stopped() bool { return b != nil && b.hit && b.last > 1 }

// scan is records with a budget; see records.
func (a *api) scan(ctx context.Context, f audit.Filter, batch int, keep func(audit.Record) bool, budget *scanBudget) iter.Seq2[audit.Record, error] {
	return func(yield func(audit.Record, error) bool) {
		for {
			if err := ctx.Err(); err != nil {
				yield(audit.Record{}, err)
				return
			}
			f.Limit = batch
			n, last := 0, uint64(0)
			for rec, err := range a.reader.Query(ctx, f) {
				if err != nil {
					yield(audit.Record{}, err)
					return
				}
				n++
				last = rec.Seq
				if keep(rec) && !yield(rec, nil) {
					return
				}
				if budget != nil {
					budget.last = rec.Seq
					if budget.left--; budget.left <= 0 {
						budget.hit = true
						return
					}
				}
			}
			// A short batch is the end. A last seq of 0 would make BeforeSeq 0,
			// which the store reads as "no bound": stop rather than start over.
			if n < batch || last == 0 {
				return
			}
			f.BeforeSeq = last
		}
	}
}

// isManager reports whether p holds core:users.manage: the viewer who sees
// every record whole.
func isManager(p *auth.Principal) bool {
	return p != nil && p.Perms != nil && p.Perms.Has(permUsers)
}

// visibleTo is the rule 05-rbac §7.6 sets, default-deny: a holder of
// core:users.manage sees every record; anyone else sees only a record whose
// action core's table classifies VisibleToAll. An action about an identity or a
// credential (core.auth.*, core.user.*, core.apikey.*, core.session.*, ...) is
// withheld, and so is one the table does not list: a module's own action, or a
// core action added without a classification. A withheld record is left out
// whole, never partly masked.
func visibleTo(p *auth.Principal) func(audit.Record) bool {
	manager := isManager(p)
	return func(r audit.Record) bool {
		return manager || audit.VisibilityOf(r.Action) == audit.VisibleToAll
	}
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
