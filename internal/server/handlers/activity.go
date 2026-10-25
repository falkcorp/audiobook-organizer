// file: internal/server/handlers/activity.go
// version: 1.16.0
// guid: d4e5f6a7-b8c9-0123-def0-234567890123
// last-edited: 2026-10-25

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

// statusClientClosedRequest is nginx's 499: the client went away before the
// response was written. Not in net/http, so it is spelled out here.
const statusClientClosedRequest = 499

// activityLog routes this file's own log lines through internal/logger, which
// strips control characters from values; the slog guard forbids new direct calls.
var activityLog = logger.New("activity")

// abortIfClientGone reports whether err is a cancelled/expired request context
// and, when it is, ends the request with 499 and no body.
//
// Two things this deliberately avoids. It does NOT fall through to
// httputil.InternalError: an abandoned request is not a server fault, and
// routing every one of them through the 500 path would turn this fix into a
// 5xx alert storm. It also does NOT use a bare c.Abort(), because Gin's default
// status is 200 and a cancelled scan must never be reported as a successful
// empty result — the caller would cache or render it as real data.
func abortIfClientGone(c *gin.Context, err error, op string) bool {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	slog.Debug("[activity] request abandoned; scan cancelled",
		"op", op, "path", logger.SanitizeLogValue(c.Request.URL.Path), "error", err)
	c.AbortWithStatus(statusClientClosedRequest)
	return true
}

// ActivityService is the narrow interface ActivityHandler requires from the
// activity log service.
//
// Query and GetDistinctSources take a context and every handler below passes
// c.Request.Context(). Gin cancels that context when the client disconnects,
// which is what lets an abandoned request stop scanning the activity log
// instead of running to completion against a socket nobody is reading.
type ActivityService interface {
	Query(ctx context.Context, filter database.ActivityFilter) ([]database.ActivityEntry, int, error)
	GetDistinctSources(ctx context.Context, filter database.ActivityFilter) ([]database.SourceCount, error)
}

// activityPartialQuerier is the optional read ListActivity prefers: Query plus
// the partial flag. *activity.Service implements it (asserted below); a test
// double that only implements ActivityService gets Partial=false.
type activityPartialQuerier interface {
	QueryWithPartial(ctx context.Context, filter database.ActivityFilter) (database.ActivityQueryResult, error)
}

var _ activityPartialQuerier = (*activity.Service)(nil)

// ActivityOpsStore is the narrow interface for op-log fallback in
// ListOperationActivity. It may be nil when the backing store does not
// implement OpsV2.
type ActivityOpsStore interface {
	GetOpLogsV2(opID string, limit int) ([]database.OpLogV2Row, error)
}

// OperationActivityEntry is the unified response shape for a single
// chronological event inside an operation transcript.
// Exported so tests outside the handlers package can unmarshal the response.
type OperationActivityEntry struct {
	Timestamp     time.Time `json:"timestamp"`
	Level         string    `json:"level"`
	OperationID   string    `json:"operation_id"`
	OperationType string    `json:"operation_type"`
	Message       string    `json:"message"`
	Details       string    `json:"details,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
}

// operationActivityEntry is an alias for the exported type, used internally.
type operationActivityEntry = OperationActivityEntry

// ActivityHandler handles activity-log HTTP endpoints.
type ActivityHandler struct {
	svc      ActivityService
	opsStore ActivityOpsStore    // may be nil
	opDefs   ActivityOpDefLookup // may be nil: ?type= is then matched literally
	// othersUserData reports whether the caller may read other users' own
	// data (users.manage). nil: no caller may (activity.WithoutOthersUserData
	// applies to everyone). See WithActivityOthersUserData.
	othersUserData func(*gin.Context) bool

	// repairsAccess and opRows decide, per operation, whether the caller may read
	// the activity of a Repairs operation (see WithActivityRepairsAccess). opRows
	// nil means no operation can be resolved, so nothing is hidden; repairsAccess
	// nil with opRows set refuses every Repairs operation. opRuns is the v3 run
	// lookup for an operation whose v2 row is missing.
	repairsAccess *opaccess.Repairs
	opRows        ActivityOperationReader
	opRuns        ActivityRunReader // may be nil
}

// ActivityOperationReader is what the activity handler reads of an operation to
// tell whether it is a Repairs one: its operations_v2 row, whose definition and
// params say which fixer it belongs to. database.Store implements it. It is
// separate from ActivityOpsStore, which is mocked and read only for log
// fallbacks.
type ActivityOperationReader interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
}

// ActivityRunReader is the same read through the operation's v3 run record, for
// an operation whose v2 row is missing. The PebbleStore implements it (it is not
// part of database.Store); nil means no v3 lookup.
type ActivityRunReader interface {
	GetRunV3(id string) (*database.RunRecord, error)
}

// ActivityOpDefLookup resolves an operation def ID -- canonical or a former ID
// of a renamed op -- to its def. *opsregistry.Registry implements it.
type ActivityOpDefLookup interface {
	Def(id string) (opsregistry.OperationDef, bool)
}

var _ ActivityOpDefLookup = (*opsregistry.Registry)(nil)

// ActivityHandlerOption configures an ActivityHandler.
type ActivityHandlerOption func(*ActivityHandler)

// WithActivityOpDefs makes the ?type= filter alias-aware: a type that names an
// operation def matches the def's canonical ID and every former ID. Pass nil
// (or omit the option) to match ?type= literally.
func WithActivityOpDefs(defs ActivityOpDefLookup) ActivityHandlerOption {
	return func(h *ActivityHandler) { h.opDefs = defs }
}

// WithActivityOthersUserData installs the check that keeps the Details of an
// entry that carry other users' listening state (activity.WithoutOthersUserData:
// the trash purge's per-user progress summary) to callers who may manage
// users. Omitting it fails closed: those Details are withheld from everyone.
func WithActivityOthersUserData(f func(*gin.Context) bool) ActivityHandlerOption {
	return func(h *ActivityHandler) { h.othersUserData = f }
}

// mayReadOthersUserData is othersUserData, false when unset.
func (h *ActivityHandler) mayReadOthersUserData(c *gin.Context) bool {
	return h.othersUserData != nil && h.othersUserData(c)
}

// withoutOthersUserData is entries with activity.WithoutOthersUserData applied
// to each, in place, unless seeAll.
func withoutOthersUserData(entries []database.ActivityEntry, seeAll bool) {
	if seeAll {
		return
	}
	for i := range entries {
		entries[i] = activity.WithoutOthersUserData(entries[i])
	}
}

// WithActivityRepairsAccess keeps the activity of a Repairs operation
// (repairs.plan, repairs.apply) to callers who hold the permissions of the
// operation's fixer, as every other read of those operations does (opaccess).
// The engine's log lines for an owner apply carry the owner's user id, and an
// activity row is tied to its operation by id only (the slog "system" rows carry
// no def tag), so each read resolves the operation from its operations_v2 row
// and, when that is gone, its v3 run record (runs may be nil: no v3 lookup):
//
//   - GET /operations/:id/activity answers 403 for an operation the caller may
//     not read, before querying anything (the op-log fallback included);
//   - POST /operations/activity/merged leaves such members out of the read and of
//     operation_ids, and answers 403 when every member is one;
//   - GET /activity leaves their rows (and the Summarize rows naming them) out
//     inside the store's scan, so its page, offset and total are counted
//     without them. A daily digest (CompactByDay folds a day's rows into one
//     row whose Details.items copy each folded row's type, operation id,
//     summary and tags) is not hidden: it names no operation itself, and
//     hiding it would hide the day's other activity. Its items are redacted on
//     the returned page instead, by the same verdicts, which leaves the page,
//     offset and total as the store counted them. Its counts, original_count
//     and tag_counts are aggregates over every row of the day and are not
//     redacted: they say how many rows of a type were folded, nothing more.
//
// An operation with neither record is not assumed harmless: an entry of it whose
// type or def: tag names repairs.plan or repairs.apply is hidden (an entry with
// no operation id at all too), so a pruned record fails closed; its plain log
// lines carry nothing to tell them apart and are shown. So is an operation whose
// record cannot be read (a corrupt row): it falls back to the same type and tag
// test, and the failure is logged once per operation per request, rather than
// hiding every row of an unrelated operation from every caller.
//
// Without the option (not wired) no operation is resolved and nothing is hidden;
// TestWireHandlers_ActivityRepairsAccessIsInstalled pins the server's wiring.
func WithActivityRepairsAccess(a *opaccess.Repairs, rows ActivityOperationReader, runs ActivityRunReader) ActivityHandlerOption {
	return func(h *ActivityHandler) { h.repairsAccess, h.opRows, h.opRuns = a, rows, runs }
}

// opVerdict is what one request learned about an operation id: known says a
// record (v2 row or v3 run) was found, and ok whether the caller may read it.
type opVerdict struct{ known, ok bool }

// opVisibility is one request's judgement of which activity entries the caller
// may read, remembering what it learned per operation id. The store calls it
// from its scan and the merged read from a worker pool, so it is locked.
type opVisibility struct {
	h      *ActivityHandler
	reader *opaccess.Reader
	mu     sync.Mutex
	seen   map[string]opVerdict
	// failed holds the operation ids whose lookup errored this request: they are
	// not asked again, and each is logged once.
	failed map[string]struct{}
	// resolve says operations can be looked up at all (the handler was given a
	// reader); without it no operation is judged, and only user data is.
	resolve bool
	// seeAll is mayReadOthersUserData: without it an entry that names a user, or
	// carries one user's own data, is hidden (activity.NamesOthersUserData).
	seeAll bool
}

// visibility binds the check to c's request. It is never nil: without an
// operation reader no operation is resolved (no Repairs run is hidden), but
// other users' data still is.
func (h *ActivityHandler) visibility(c *gin.Context) *opVisibility {
	return &opVisibility{
		h: h, reader: h.repairsAccess.For(c), seen: map[string]opVerdict{}, failed: map[string]struct{}{},
		resolve: h.opRows != nil, seeAll: h.mayReadOthersUserData(c),
	}
}

// verdict looks an operation up: its operations_v2 row first, then, when that is
// gone, its v3 run record (the two are mirrors, and a pruned or never-mirrored
// half must not decide for the other). known is false only when neither exists.
// A lookup that fails is an error and is asked again next time.
func (v *opVisibility) verdict(id string) (opVerdict, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if r, ok := v.seen[id]; ok {
		return r, nil
	}
	var r opVerdict
	row, err := v.h.opRows.GetOperationV2(id)
	if err != nil {
		return opVerdict{}, err
	}
	if row != nil {
		r = opVerdict{known: true, ok: v.reader.MayRead(row)}
	} else {
		if v.h.opRuns != nil {
			rec, err := v.h.opRuns.GetRunV3(id)
			if err != nil {
				return opVerdict{}, err
			}
			if rec != nil {
				r = opVerdict{known: true, ok: v.reader.MayReadRun(rec)}
			}
		}
	}
	v.seen[id] = r
	return r, nil
}

// looksRepairs reports whether an entry says it belongs to the Repairs lane: its
// type or its def: tag names repairs.plan or repairs.apply. It is what is left to
// go on for an operation no record exists for.
func looksRepairs(e database.ActivityEntry) bool {
	if opaccess.IsRepairsOp(e.Type) {
		return true
	}
	for _, t := range e.Tags {
		if def, ok := strings.CutPrefix(t, activityDefTagPrefix); ok && opaccess.IsRepairsOp(def) {
			return true
		}
	}
	return false
}

// excludes is the store's ExcludeEntry predicate: true hides the entry. An entry
// is hidden when it belongs to an operation whose record says the caller may not
// read it; when no record exists for an operation (pruned, never written), or it
// cannot be read, the entry's own type and tags decide, and an entry that looks
// like Repairs is hidden (fail closed). So is a Repairs-looking entry that names
// no operation at all.
//
// It reads only the fields of e a filter reads (never Details; see
// ActivityFilter.ExcludeEntry), because the store's filter-index path does not
// hand it Details.
func (v *opVisibility) excludes(e database.ActivityEntry) bool {
	if !v.seeAll && activity.NamesOthersUserData(e) {
		return true
	}
	if !v.resolve {
		return false
	}
	named := false
	hide := database.EntryOperationIDs(e, func(id string) bool {
		named = true
		r, err := v.lookup(id)
		if err != nil {
			return looksRepairs(e)
		}
		if r.known {
			return !r.ok
		}
		return looksRepairs(e)
	})
	return hide || (!named && looksRepairs(e))
}

// lookup is verdict for the activity listing, where a failed read must not decide
// the whole operation: the first failure for an id is logged, remembered for the
// request, and every later ask returns it without reading the store again. The
// caller falls back to the entry's own type and tags.
func (v *opVisibility) lookup(id string) (opVerdict, error) {
	v.mu.Lock()
	_, bad := v.failed[id]
	v.mu.Unlock()
	if bad {
		return opVerdict{}, errOpLookupFailed
	}
	r, err := v.verdict(id)
	if err == nil {
		return r, nil
	}
	v.mu.Lock()
	_, again := v.failed[id]
	v.failed[id] = struct{}{}
	v.mu.Unlock()
	if !again {
		activityLog.Warn("operation lookup failed; judging its rows by type and tags: operation_id=%s: %v",
			logger.SanitizeLogValue(id), err)
	}
	return opVerdict{}, errOpLookupFailed
}

// errOpLookupFailed marks an operation whose record could not be read earlier in
// this request; the underlying error was logged where it happened.
var errOpLookupFailed = errors.New("operation lookup failed")

// redactDigest removes from a daily digest's Details.items every item the caller
// may not read, and returns e otherwise untouched. CompactByDay copies each row it
// folds into an item (type, tier, operation id, summary, tags, and for a warn or
// error row its error details), so the log line of a hidden Repairs run -- the
// owner-apply line naming the owner's user id, a failed run's error naming its
// target -- survives there after the row itself is deleted. The digest row names
// no operation itself, so the store's predicate cannot see it (and on the
// filter-index path has no Details to look at); the page is redacted instead.
// Only items go: the row, its position and the count of rows stay, so the page,
// offset and total the store computed remain right.
//
// An item is judged the way a row is (excludes), from its type, operation id and
// tags. One that is not a JSON object cannot be judged and is dropped. The
// returned entry has its own Details map and items slice; the one passed in
// is never written to, because a store may hand out a map it keeps.
func (v *opVisibility) redactDigest(e database.ActivityEntry) database.ActivityEntry {
	if v == nil || (e.Tier != database.DigestTier && e.Type != database.DailyDigestType) {
		return e
	}
	raw, present := e.Details["items"]
	if !present || raw == nil {
		return e
	}
	items, ok := raw.([]any)
	if !ok {
		// A typed slice (an in-process store keeping DigestItem values): take
		// its JSON form, which is what a client is sent anyway.
		b, err := json.Marshal(raw)
		if err == nil {
			err = json.Unmarshal(b, &items)
		}
		if err != nil {
			items = nil // unreadable: nothing in it can be shown
		}
	}
	kept := make([]any, 0, len(items))
	for _, it := range items {
		m, isObj := it.(map[string]any)
		if !isObj || v.excludes(digestItemEntry(m)) {
			continue
		}
		kept = append(kept, it)
	}
	details := maps.Clone(e.Details)
	details["items"] = kept
	e.Details = details
	return e
}

// digestItemEntry is the part of a DigestItem that excludes reads, as the entry
// the item was folded from. Source is left empty on purpose: the Summarize
// row's op: tags are not a digest item's business, only its own operation_id.
func digestItemEntry(m map[string]any) database.ActivityEntry {
	e := database.ActivityEntry{}
	e.Type, _ = m["type"].(string)
	e.Tier, _ = m["tier"].(string)
	e.OperationID, _ = m["operation_id"].(string)
	// The summary and the error details of the folded row decide whether it names
	// a user (activity.NamesOthersUserData); the Source is gone, but the item's
	// source: tag says it.
	e.Summary, _ = m["summary"].(string)
	if d, _ := m["details"].(string); d != "" {
		e.Summary += " " + d
	}
	switch tags := m["tags"].(type) {
	case []any:
		for _, t := range tags {
			if s, ok := t.(string); ok {
				e.Tags = append(e.Tags, s)
			}
		}
	case []string:
		e.Tags = tags
	}
	return e
}

// keep returns the entries the caller may read, and how many it dropped.
func (v *opVisibility) keep(entries []database.ActivityEntry) ([]database.ActivityEntry, int) {
	if v == nil {
		return entries, 0
	}
	out := entries[:0:0]
	for _, e := range entries {
		if v.excludes(e) {
			continue
		}
		out = append(out, e)
	}
	return out, len(entries) - len(out)
}

// denied reports whether operation id has a record the caller may not read (an
// error is a refusal too, with err set). An operation with no record is not
// denied here: its entries are judged one by one (excludes).
func (v *opVisibility) denied(id string) (bool, error) {
	if v == nil || !v.resolve || id == "" {
		return false, nil
	}
	r, err := v.verdict(id)
	if err != nil {
		return true, err
	}
	return r.known && !r.ok, nil
}

// require answers for the caller: 403 for an operation whose record they may not
// read, 500 when it cannot be looked up. false means the response is written.
func (v *opVisibility) require(c *gin.Context, id string) bool {
	denied, err := v.denied(id)
	if err != nil {
		httputil.InternalError(c, "failed to fetch operation", err)
		return false
	}
	if denied {
		httputil.RespondWithForbidden(c, "permission denied: this operation belongs to a Repairs fixer you may not use")
		return false
	}
	return true
}

// NewActivityHandler constructs an ActivityHandler.
// opsStore may be nil when the backing store does not implement OpsV2.
func NewActivityHandler(svc ActivityService, opsStore ActivityOpsStore, opts ...ActivityHandlerOption) *ActivityHandler {
	h := &ActivityHandler{svc: svc, opsStore: opsStore}
	for _, o := range opts {
		o(h)
	}
	return h
}

// activityTypeSpellings returns the type filter for a ?type= value. An op's
// activity entries carry the def ID they were recorded under
// (reporter_db.go writes Type: defID), and rows are never rewritten, so a
// renamed op's history is split: rows from before the rename carry a former
// ID, rows after it the canonical one. When asked names a registered def --
// by either spelling -- the filter is the canonical ID plus every former ID, so
// both spellings return the whole history, as GET /operations/timeline?def_id=
// already does. Anything else (a non-op type, or no registry) is matched
// literally.
func (h *ActivityHandler) activityTypeSpellings(asked string) (typ string, aliases []string) {
	if asked == "" || h.opDefs == nil {
		return asked, nil
	}
	def, ok := h.opDefs.Def(asked)
	if !ok {
		return asked, nil
	}
	if asked != def.ID {
		if nr, ok := h.opDefs.(deprecatedDefIDNoter); ok {
			nr.NoteDeprecatedDefIDUse(asked, opsregistry.AliasEntryActivityFilter)
		}
	}
	return def.ID, append([]string(nil), def.FormerIDs...)
}

// activityDefTagPrefix is the tag reporter_db.go puts on every op activity row:
// "def:" + the def ID the run was recorded under.
const activityDefTagPrefix = "def:"

// activityTagSpellings is activityTypeSpellings for one ?tags= value. A
// def:<id> tag names the op ID a row ran under, which is never rewritten, so a
// renamed op's rows carry def:<former ID> before the rename and def:<canonical
// ID> after it. When asked is def:<id> and id names a registered def -- by
// either spelling -- the tag becomes def:<canonical> with every def:<former>
// as an alias, so the one AND term matches both halves of the history.
// Anything else (a non-def tag, an unknown def, or no registry) is literal.
func (h *ActivityHandler) activityTagSpellings(asked string) (tag string, aliases []string) {
	id, isDef := strings.CutPrefix(asked, activityDefTagPrefix)
	if !isDef || id == "" || h.opDefs == nil {
		return asked, nil
	}
	def, ok := h.opDefs.Def(id)
	if !ok {
		return asked, nil
	}
	if id != def.ID {
		if nr, ok := h.opDefs.(deprecatedDefIDNoter); ok {
			nr.NoteDeprecatedDefIDUse(id, opsregistry.AliasEntryActivityFilter)
		}
	}
	for _, former := range def.FormerIDs {
		aliases = append(aliases, activityDefTagPrefix+former)
	}
	return activityDefTagPrefix + def.ID, aliases
}

// ListActivity handles GET /api/v1/activity.
//
// Supported query parameters:
//
//	limit            – max entries to return (default 50)
//	offset           – pagination offset
//	type             – filter by entry type; an operation def ID also matches
//	                   the op's former (pre-rename) IDs, in either direction
//	tier             – filter by tier (realtime|background|debug|audit)
//	level            – filter by level (info|warn|error|debug)
//	operation_id     – filter by operation ID
//	book_id          – filter by book ID
//	since            – RFC3339 lower-bound timestamp (inclusive)
//	until            – RFC3339 upper-bound timestamp (inclusive)
//	tags             – comma-separated list of required tags (AND semantics);
//	                   a def:<op id> tag also matches the op's former IDs
//	search           – substring match on summary
//	source           – show only entries from this source
//	exclude_sources  – comma-separated list of sources to hide
//	exclude_tiers    – comma-separated list of tiers to hide
//	exclude_tags     – comma-separated list of tags to hide (ANY semantics);
//	                   a def:<op id> tag also hides the op's former IDs
func (h *ActivityHandler) ListActivity(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}

	filter := database.ActivityFilter{}

	params := httputil.ParsePaginationParams(c)
	filter.Limit = params.Limit
	filter.Offset = params.Offset

	filter.Type, filter.TypeAliases = h.activityTypeSpellings(c.Query("type"))
	filter.Tier = c.Query("tier")
	filter.Level = c.Query("level")
	filter.OperationID = c.Query("operation_id")
	filter.BookID = c.Query("book_id")

	if v := c.Query("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid since: must be RFC3339")
			return
		}
		filter.Since = &t
	}

	if v := c.Query("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid until: must be RFC3339")
			return
		}
		filter.Until = &t
	}

	if v := c.Query("tags"); v != "" {
		for tag := range strings.SplitSeq(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			tag, aliases := h.activityTagSpellings(tag)
			if slices.Contains(filter.Tags, tag) {
				continue // both spellings of one op asked for: one term
			}
			filter.Tags = append(filter.Tags, tag)
			if len(aliases) > 0 {
				if filter.TagAliases == nil {
					filter.TagAliases = make(map[string][]string)
				}
				filter.TagAliases[tag] = aliases
			}
		}
	}

	filter.Search = c.Query("search")
	filter.Source = c.Query("source")
	if v := c.Query("exclude_sources"); v != "" {
		for src := range strings.SplitSeq(v, ",") {
			src = strings.TrimSpace(src)
			if src != "" {
				filter.ExcludeSources = append(filter.ExcludeSources, src)
			}
		}
	}
	if v := c.Query("exclude_tiers"); v != "" {
		for tier := range strings.SplitSeq(v, ",") {
			tier = strings.TrimSpace(tier)
			if tier != "" {
				filter.ExcludeTiers = append(filter.ExcludeTiers, tier)
			}
		}
	}
	if v := c.Query("exclude_tags"); v != "" {
		for tag := range strings.SplitSeq(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			// Same resolution as ?tags=: hiding def:<id> hides the op's rows
			// under every spelling it ran under.
			tag, aliases := h.activityTagSpellings(tag)
			if slices.Contains(filter.ExcludeTags, tag) {
				continue // both spellings of one op asked for: one entry
			}
			filter.ExcludeTags = append(filter.ExcludeTags, tag)
			if len(aliases) > 0 {
				if filter.ExcludeTagAliases == nil {
					filter.ExcludeTagAliases = make(map[string][]string)
				}
				filter.ExcludeTagAliases[tag] = aliases
			}
		}
	}

	// Repairs operations the caller may not read are hidden inside the store's
	// scan, so the page and total are counted without them.
	vis := h.visibility(c)
	filter.ExcludeEntry = vis.excludes

	var (
		entries []database.ActivityEntry
		total   int
		partial bool
		err     error
	)
	if pq, ok := h.svc.(activityPartialQuerier); ok {
		var res database.ActivityQueryResult
		res, err = pq.QueryWithPartial(c.Request.Context(), filter)
		entries, total, partial = res.Entries, res.Total, res.Partial
	} else {
		entries, total, err = h.svc.Query(c.Request.Context(), filter)
	}
	if err != nil {
		if abortIfClientGone(c, err, "ListActivity") {
			return
		}
		if errors.Is(err, database.ErrActivityOffsetTooDeep) {
			httputil.RespondWithBadRequest(c, err.Error())
			return
		}
		httputil.InternalError(c, "failed to query activity log", err)
		return
	}

	// Ensure entries is always a JSON array, never null.
	if entries == nil {
		entries = []database.ActivityEntry{}
	}
	withoutOthersUserData(entries, h.mayReadOthersUserData(c))

	// A daily digest carries the rows it folded; take out those the caller may
	// not read. Same count of rows, so total and paging are untouched.
	for i := range entries {
		entries[i] = vis.redactDigest(entries[i])
	}

	// partial: the store stopped at its scan budget before filling the page or
	// exhausting the matches, so older matches were NOT examined and total is
	// only a lower bound. The UI shows that instead of "no more results".
	httputil.RespondWithOK(c, struct {
		Entries []database.ActivityEntry `json:"entries"`
		Total   int                      `json:"total"`
		Partial bool                     `json:"partial"`
	}{Entries: entries, Total: total, Partial: partial})
}

// ListActivitySources handles GET /api/v1/activity/sources.
//
// Returns distinct sources with their entry counts, filtered by the same
// tier/level/since/until parameters as ListActivity.
func (h *ActivityHandler) ListActivitySources(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}
	filter := database.ActivityFilter{
		Tier:  c.Query("tier"),
		Level: c.Query("level"),
	}
	if v := c.Query("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Since = &t
		}
	}
	if v := c.Query("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Until = &t
		}
	}
	sources, err := h.svc.GetDistinctSources(c.Request.Context(), filter)
	if err != nil {
		if abortIfClientGone(c, err, "ListActivitySources") {
			return
		}
		httputil.InternalError(c, "failed to get sources", err)
		return
	}
	if sources == nil {
		sources = []database.SourceCount{}
	}
	httputil.RespondWithOK(c, struct {
		Sources []database.SourceCount `json:"sources"`
	}{Sources: sources})
}

// operationActivityLimitDefault and operationActivityLimitMax bound the
// per-request entry cap shared by the single-op and merged transcript reads.
const (
	operationActivityLimitDefault = 1000
	operationActivityLimitMax     = 10000
)

// mergedOperationIDsMax caps how many members one merged read may name. The
// bell's groups are bounded by a 24-hour window of one operation kind, so this
// is a sanity ceiling against a runaway client, not a tuning knob.
const mergedOperationIDsMax = 1000

// parseOperationActivityLimit reads a positive integer limit, clamped to
// operationActivityLimitMax; anything absent or unparseable is the default.
func parseOperationActivityLimit(raw string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
		return min(n, operationActivityLimitMax)
	}
	return operationActivityLimitDefault
}

// operationActivity is the one transcript read: the newest `limit` activity
// log entries for opID in chronological order, or the op-log v2 rows when the
// activity log holds nothing for it. Both HTTP readers go through here so the
// fallback rule cannot drift between a single op and a group of them.
//
// The returned slice is never nil. total is the activity log's own count for
// the op (which can exceed len(entries) once limit bites) or, on the fallback
// path, the number of op-log rows returned.
//
// seeAll is mayReadOthersUserData: without it an entry's Details that carry
// other users' own data are withheld (activity.WithoutOthersUserData).
func (h *ActivityHandler) operationActivity(ctx context.Context, vis *opVisibility, opID string, limit int, seeAll bool) ([]operationActivityEntry, int, error) {
	entries, total, err := h.svc.Query(ctx, database.ActivityFilter{OperationID: opID, Limit: limit})
	if err != nil {
		return nil, 0, err
	}
	withoutOthersUserData(entries, seeAll)
	// The op is readable (the caller checked), but a digest row for it can also
	// name another operation the caller may not read.
	entries, dropped := vis.keep(entries)
	total -= dropped
	if len(entries) == 0 {
		opLogEntries, opLogErr := h.operationActivityFromOpLogs(vis, opID, limit)
		if opLogErr != nil {
			return nil, 0, opLogErr
		}
		if opLogEntries != nil {
			return opLogEntries, len(opLogEntries), nil
		}
		return []operationActivityEntry{}, total, nil
	}
	// Query returns entries newest-first; reverse for ASC chronological order.
	out := make([]operationActivityEntry, 0, len(entries))
	for _, entry := range slices.Backward(entries) {
		out = append(out, activityEntryToOperationEntry(entry))
	}
	return out, total, nil
}

// ListOperationActivity handles GET /api/v1/operations/:id/activity.
//
// Returns all activity log entries for the given operation ID, ordered by
// timestamp ASC (oldest first). Falls back to op-log v2 rows when the
// activity log has no entries for that operation.
func (h *ActivityHandler) ListOperationActivity(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}
	opID := strings.TrimSpace(c.Param("id"))
	if opID == "" {
		httputil.RespondWithBadRequest(c, "operation id required")
		return
	}
	limit := parseOperationActivityLimit(c.Query("limit"))

	vis := h.visibility(c)
	if !vis.require(c, opID) {
		return
	}
	entries, total, err := h.operationActivity(c.Request.Context(), vis, opID, limit, h.mayReadOthersUserData(c))
	if err != nil {
		if abortIfClientGone(c, err, "ListOperationActivity") {
			return
		}
		httputil.InternalError(c, "failed to query operation activity", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		OperationID string                   `json:"operation_id"`
		Entries     []operationActivityEntry `json:"entries"`
		Total       int                      `json:"total"`
	}{OperationID: opID, Entries: entries, Total: total})
}

// mergedOperationActivityRequest is the body of POST /operations/activity/merged.
type mergedOperationActivityRequest struct {
	IDs   []string `json:"ids"`
	Limit int      `json:"limit"`
}

// ListMergedOperationActivity handles POST /api/v1/operations/activity/merged.
//
// It is the transcript of a GROUP: the bell and the Activity page fold runs
// of same-kind operations into one synthetic row (web/src/stores/
// operationGrouping.ts), and that row's id names no record, so opening it
// needs a read that takes the members' ids instead. The body is
// {"ids": [...], "limit": N}. POST rather than GET only because a group can
// hold several hundred ULIDs, which does not fit a query string; it writes
// nothing.
//
// Each member is read exactly the way the single-op endpoint reads it —
// activity log first, op-log v2 fallback per member — and the results are
// merged into one chronological timeline. `limit` caps the MERGED timeline and
// drops the oldest entries: a reader opening a group wants to see how it
// ended. `total` is the sum of the members' own totals, so it says how much
// exists rather than how much came back; `truncated` says whether the cap bit.
func (h *ActivityHandler) ListMergedOperationActivity(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}
	var req mergedOperationActivityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body: expected {\"ids\": [...], \"limit\": n}")
		return
	}
	ids := dedupeOperationIDs(req.IDs)
	if len(ids) == 0 {
		httputil.RespondWithBadRequest(c, "at least one operation id required")
		return
	}
	if len(ids) > mergedOperationIDsMax {
		httputil.RespondWithBadRequest(c, "too many operation ids: at most "+strconv.Itoa(mergedOperationIDsMax))
		return
	}
	limit := operationActivityLimitDefault
	if req.Limit > 0 {
		limit = min(req.Limit, operationActivityLimitMax)
	}

	// Members the caller may not read (Repairs operations of a fixer they may not
	// use) are left out of the read and out of operation_ids; a request that names
	// only such members is a 403, as for the single read.
	vis := h.visibility(c)
	if vis != nil {
		readable := ids[:0:0]
		for _, id := range ids {
			denied, err := vis.denied(id)
			if err != nil {
				httputil.InternalError(c, "failed to fetch operation", err)
				return
			}
			if !denied {
				readable = append(readable, id)
			}
		}
		if len(readable) == 0 {
			httputil.RespondWithForbidden(c, "permission denied: these operations belong to a Repairs fixer you may not use")
			return
		}
		ids = readable
	}

	// One indexed read per member, fanned out over a bounded pool: a group can
	// name hundreds of members, and each read is a store round-trip. Results
	// land in their own slot so the merge below is deterministic regardless of
	// which read finished first.
	// Asked once, here: a gin.Context is not for the pool's goroutines.
	seeAll := h.mayReadOthersUserData(c)
	perOp := make([][]operationActivityEntry, len(ids))
	totals := make([]int, len(ids))
	g, ctx := errgroup.WithContext(c.Request.Context())
	g.SetLimit(runtime.NumCPU())
	for i, id := range ids {
		g.Go(func() error {
			entries, total, err := h.operationActivity(ctx, vis, id, limit, seeAll)
			if err != nil {
				return err
			}
			perOp[i], totals[i] = entries, total
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		if abortIfClientGone(c, err, "ListMergedOperationActivity") {
			return
		}
		httputil.InternalError(c, "failed to query operation activity", err)
		return
	}

	total, n := 0, 0
	for i := range ids {
		total += totals[i]
		n += len(perOp[i])
	}
	merged := make([]operationActivityEntry, 0, n)
	for _, entries := range perOp {
		merged = append(merged, entries...)
	}
	// Stable, so two members' entries with the same timestamp keep the caller's
	// member order (the group lists its members oldest first).
	sort.SliceStable(merged, func(a, b int) bool { return merged[a].Timestamp.Before(merged[b].Timestamp) })
	truncated := false
	if len(merged) > limit {
		merged = merged[len(merged)-limit:]
		truncated = true
	}

	httputil.RespondWithOK(c, struct {
		OperationIDs []string                 `json:"operation_ids"`
		Entries      []operationActivityEntry `json:"entries"`
		Total        int                      `json:"total"`
		Truncated    bool                     `json:"truncated"`
	}{OperationIDs: ids, Entries: merged, Total: total, Truncated: truncated})
}

// dedupeOperationIDs trims, drops blanks, and keeps the first occurrence of
// each id in the caller's order.
func dedupeOperationIDs(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// operationActivityFromOpLogs fetches op-log v2 rows as a fallback when the
// activity log has no entries for an operation. Returns nil (not an error)
// when opsStore is nil or returns no rows.
func (h *ActivityHandler) operationActivityFromOpLogs(vis *opVisibility, opID string, limit int) ([]operationActivityEntry, error) {
	if h.opsStore == nil {
		return nil, nil
	}
	logs, err := h.opsStore.GetOpLogsV2(opID, limit)
	if err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		return []operationActivityEntry{}, nil
	}
	out := make([]operationActivityEntry, 0, len(logs))
	for _, l := range logs {
		// The op-log rows are the source of the activity rows mirrored from them,
		// so a line that names a user is withheld here as it is there.
		if !vis.seeAll && activity.NamesUserText(l.Message) {
			continue
		}
		attrs := opLogAttrs(l.Attrs)
		opType, _ := attrs["def_id"].(string)
		out = append(out, operationActivityEntry{
			Timestamp:     l.CreatedAt,
			Level:         l.Level,
			OperationID:   l.OperationID,
			OperationType: opType,
			Message:       l.Message,
			Details:       l.Attrs,
			Tags:          operationLogTags(l.OperationID, l.Level, opType, attrs),
		})
	}
	return out, nil
}

// POST /api/v1/activity/compact and POST /api/v1/admin/recompact-digests moved
// to ActivityCompactHandler (activity_compact.go) on 2026-09-10: each enqueues
// its maintenance op instead of doing the work inside the request.

// activityEntryToOperationEntry converts an ActivityEntry to the operation
// transcript response shape.
func activityEntryToOperationEntry(e database.ActivityEntry) operationActivityEntry {
	details := ""
	if len(e.Details) > 0 {
		if b, err := json.Marshal(e.Details); err == nil {
			details = string(b)
		}
	}
	return operationActivityEntry{
		Timestamp:     e.Timestamp,
		Level:         e.Level,
		OperationID:   e.OperationID,
		OperationType: e.Type,
		Message:       e.Summary,
		Details:       details,
		Tags:          e.Tags,
	}
}

func opLogAttrs(raw string) map[string]any {
	if raw == "" || raw == "{}" {
		return map[string]any{}
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(raw), &attrs); err != nil {
		return map[string]any{}
	}
	return attrs
}

func operationLogTags(opID, level, opType string, attrs map[string]any) []string {
	entry := database.ActivityEntry{
		Tier:        "info",
		Type:        opType,
		Level:       level,
		OperationID: opID,
		Details:     attrs,
		Tags:        []string{"operation"},
	}
	if plugin, ok := attrs["plugin"].(string); ok {
		entry.Source = plugin
		entry.Tags = append(entry.Tags, "plugin:"+plugin)
	}
	if opType != "" {
		entry.Tags = append(entry.Tags, "def:"+opType)
	}
	activity.EnrichTags(&entry)
	return entry.Tags
}
