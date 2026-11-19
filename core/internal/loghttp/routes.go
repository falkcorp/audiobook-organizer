// file: core/internal/loghttp/routes.go
// version: 1.3.0
// guid: 2b8e41d6-7c05-4f93-a1e8-5d3f09c6b7a4
// last-edited: 2026-11-19

// Package loghttp serves the system-log journal's query route (05-logging §5):
// GET /api/core/v1/system/logs over log.JournalStore.Query, registered on
// httpapi's core group and needing core:system.manage.
//
// core:system.manage is AuthRequired (05-rbac §4.11), so with authentication
// off the router's chain answers 401 before the handler runs; the handler adds
// no auth-off branch and checks no permission of its own (the route's Perm is
// the router's to enforce, at stage 13).
//
// The other-user rule (05-logging §5, "User data in system log lines") is the
// handler's: a caller who does not also hold core:users.view is not served a
// record the module-registered log.OtherUserPredicate flags, and sees user_id
// blank on every record it is served. The predicate is handed to the store as
// JournalQuery.Hide, so a hidden record is skipped inside the scan: pages stay
// full and the cursor never names a record the caller may not see. Filtering
// after the query would leave both as a count and a time oracle on the
// hidden lines.
//
// The response is httpapi.CursorPage (05-http-api §4.4: {items, next_cursor}),
// not the brief's {records, next}: the spec names the system journal as a
// cursor-paged feed.
//
// The package is dark until F5-18 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package loghttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
)

const (
	// permManage is the scope the route declares.
	permManage rbac.Permission = "core:system.manage"
	// permUsers lifts the other-user rule: it is the read side of user data
	// (05-rbac §6.1, the successor of HEAD's users.manage gate).
	permUsers rbac.Permission = "core:users.view"

	// defaultLimit and maxLimit are HEAD's ParsePaginationParams default and
	// cap, which 05-logging §5 keeps. A limit of zero or less is the default.
	defaultLimit = 50
	maxLimit     = 1000
)

// ErrNoJournal is Register's refusal of Deps without a journal.
var ErrNoJournal = errors.New("loghttp: Deps.Journal is nil")

// Deps is what the kernel hands the package at mount time (F5-18).
type Deps struct {
	Journal   log.JournalStore       // core's native store (core/internal/logjournal), never nil
	OtherUser log.OtherUserPredicate // the module-registered predicate (Registrar.LogOtherUserData); nil = no module registered one
}

// api is what the handler shares. Its fields are fixed at Register.
type api struct {
	journal   log.JournalStore
	otherUser log.OtherUserPredicate
}

// Register adds the one route to g, which must be the router's CoreGroup(). It
// returns an error for Deps it cannot serve; a problem with the route itself
// (a bad binding, a duplicate) surfaces from Router.Build, as for every route.
func Register(g *httpapi.Group, d Deps) error {
	if d.Journal == nil {
		return ErrNoJournal
	}
	a := &api{journal: d.Journal, otherUser: d.OtherUser}
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/system/logs", Name: "listSystemLogs",
		Perm: rbac.Need(permManage), RateClass: httpapi.RateDefault, Tags: []string{"system"},
		Summary: "Query the system log journal, newest first",
		Doc: "Filters: level (debug, info, warn or error: that level and above; default debug), module, since and until (RFC 3339; since is inclusive, until exclusive; since after until is 422), q (a case-insensitive substring of the message). cursor is the next_cursor of a previous page; a cursor the journal did not issue is 400 invalid_cursor. limit defaults to 50 and is clamped to 1 to 1000. " +
			"A caller without core:users.view is not served a record a module flags as naming another user's data, and sees user_id blank on the records it is served. " +
			"With authentication disabled the route answers 401.",
	}, a.list)
	return nil
}

// queryRequest is the query string, snake_case (05-http-api §3.4). The embedded
// CursorParams binds limit (default 50, clamped to 1 to 1000, HEAD's
// ParsePaginationParams default and cap) and cursor (opaque, a previous page's
// next_cursor). A value that does not parse is httpapi's 422.
type queryRequest struct {
	httpapi.CursorParams
	Level  string    `query:"level"`  // "", debug, info, warn, error: JournalQuery.MinLevel ("" = debug)
	Module string    `query:"module"` // JournalQuery.Module ("" = all)
	Since  time.Time `query:"since"`  // RFC 3339; JournalQuery.Since (absent = zero)
	Until  time.Time `query:"until"`  // RFC 3339; JournalQuery.Until (absent = zero)
	Q      string    `query:"q"`      // JournalQuery.Text
}

// pageView is the response: records newest first (logjournal.RecordView, the
// live tail's payload type too), and next_cursor when more remain.
type pageView = httpapi.CursorPage[logjournal.RecordView]

// parse maps the request onto a JournalQuery, or answers 422 for every
// parameter that is malformed (the same status httpapi's binder gives an
// unparseable value). Nothing is queried unless every parameter is well formed.
func (r queryRequest) parse() (log.JournalQuery, error) {
	q := log.JournalQuery{Module: r.Module, Text: r.Q, Cursor: r.Cursor, MinLevel: log.LevelDebug, Since: r.Since, Until: r.Until}
	var bad []httpapi.FieldError
	if r.Level != "" {
		lv, err := log.ParseLevel(r.Level)
		if err != nil || lv < log.LevelDebug { // trace is not a journal level on this route
			bad = append(bad, httpapi.Field("/query/level", "invalid_enum", "must be debug, info, warn or error"))
		} else {
			q.MinLevel = lv
		}
	}
	if !r.Since.IsZero() && !r.Until.IsZero() && r.Since.After(r.Until) {
		bad = append(bad, httpapi.Field("/query/since", "invalid_range", "since must not be after until"))
	}
	if len(bad) > 0 {
		return q, httpapi.Invalid(bad...)
	}
	q.Limit = min(r.Limit, maxLimit)
	if q.Limit <= 0 {
		q.Limit = defaultLimit
	}
	return q, nil
}

// badCursor is the 400 for a cursor the journal did not issue (05-http-api §6).
func badCursor() error {
	return &httpapi.Error{
		Status: http.StatusBadRequest, Code: "invalid_cursor", Detail: "not a cursor this journal issued",
		Fields: []httpapi.FieldError{httpapi.Field("/query/cursor", "invalid_cursor", "not a cursor this journal issued")},
	}
}

func (a *api) list(ctx context.Context, in *httpapi.In[queryRequest]) (pageView, error) {
	q, err := in.Req.parse()
	if err != nil {
		return pageView{}, err
	}
	// The other-user rule: the viewer is the request principal. A caller without
	// core:users.view has the module predicate applied inside the store's scan
	// (JournalQuery.Hide), to every record the filters keep, imported and new
	// alike, so a hidden record is neither served nor counted nor named by the
	// cursor.
	viewer := ""
	if p, ok := auth.PrincipalFrom(ctx); ok && p.User != nil {
		viewer = string(p.User.ID)
	}
	mayRead := rbac.Can(ctx, permUsers)
	if !mayRead && a.otherUser != nil {
		// The predicate is shown log.PredicateInput(rec), as the live tail shows it.
		q.Hide = func(rec log.Record) bool { return a.otherUser(log.PredicateInput(rec), viewer) }
	}
	page, err := a.journal.Query(ctx, q)
	if err != nil {
		if errors.Is(err, logjournal.ErrBadCursor) {
			return pageView{}, badCursor()
		}
		// A plain error: httpapi logs it and answers 500 without echoing it.
		return pageView{}, fmt.Errorf("loghttp: querying the journal: %w", err)
	}
	out := pageView{Items: make([]logjournal.RecordView, 0, len(page.Records)), NextCursor: page.Next}
	for _, rec := range page.Records {
		out.Items = append(out.Items, viewOf(rec, !mayRead))
	}
	return out, nil
}

// viewOf is rec on the wire (logjournal.ViewOf: attributes flat, as text, a
// repeated key keeping its last value). With blank, every user_id attribute,
// top level or inside a group, is present and empty (logjournal.BlankUserIDs),
// as on the live tail.
func viewOf(rec log.Record, blank bool) logjournal.RecordView {
	v := logjournal.ViewOf(rec)
	if blank {
		logjournal.BlankUserIDs(v.Attrs)
	}
	return v
}
