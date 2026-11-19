// file: core/internal/loghttp/routes.go
// version: 1.0.0
// guid: 2b8e41d6-7c05-4f93-a1e8-5d3f09c6b7a4
// last-edited: 2026-11-18

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
// blank on every record it is served.
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
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
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
		Doc: "Filters: level (debug, info, warn or error: that level and above; default debug), module, since and until (RFC 3339; since is inclusive, until exclusive), q (a case-insensitive substring of the message). The cursor continues a query with the same filters. limit defaults to 50 and is clamped to 1 to 1000. " +
			"A caller without core:users.view is not served a record a module flags as naming another user's data, and sees user_id blank on the records it is served. " +
			"With authentication disabled the route answers 401.",
	}, a.list)
	return nil
}

type queryRequest struct { // query parameters, snake_case (05-http-api §3.4)
	Level  string `query:"level"`  // "", debug, info, warn, error: JournalQuery.MinLevel ("" = debug)
	Module string `query:"module"` // JournalQuery.Module ("" = all)
	Since  string `query:"since"`  // RFC 3339; JournalQuery.Since (absent = zero)
	Until  string `query:"until"`  // RFC 3339; JournalQuery.Until (absent = zero)
	Q      string `query:"q"`      // JournalQuery.Text
	Cursor string `query:"cursor"` // JournalQuery.Cursor, from a previous page's next
	Limit  int    `query:"limit"`  // JournalQuery.Limit: default 50, clamped to [1, 1000] (05-logging §5)
}

type recordView struct { // one journal record on the wire
	Time      time.Time         `json:"time"`
	Level     string            `json:"level"` // slog level name, lower case
	Module    string            `json:"module"`
	Subsystem string            `json:"subsystem"`
	Message   string            `json:"message"`
	Attrs     map[string]string `json:"attrs"` // dotted keys, as the native store flattens them
	TraceID   string            `json:"trace_id,omitempty"`
	SpanID    string            `json:"span_id,omitempty"`
}

type pageView struct {
	Records []recordView `json:"records"`
	Next    string       `json:"next"` // "" when exhausted
}

// badParam is the 400 for a malformed query parameter.
func badParam(name, why string) error {
	return &httpapi.Error{
		Status: http.StatusBadRequest, Code: "invalid_query_parameter", Detail: name + " " + why,
		Fields: []httpapi.FieldError{httpapi.Field("/query/"+name, "invalid_value", why)},
	}
}

// parse maps the request onto a JournalQuery, or reports the first malformed
// parameter. Nothing is queried unless every parameter is well formed.
func (r queryRequest) parse() (log.JournalQuery, error) {
	q := log.JournalQuery{Module: r.Module, Text: r.Q, Cursor: r.Cursor, MinLevel: log.LevelDebug}
	if r.Level != "" {
		lv, err := log.ParseLevel(r.Level)
		if err != nil || lv < log.LevelDebug { // trace is not a journal level on this route
			return q, badParam("level", "must be debug, info, warn or error")
		}
		q.MinLevel = lv
	}
	var err error
	if q.Since, err = parseTime("since", r.Since); err != nil {
		return q, err
	}
	if q.Until, err = parseTime("until", r.Until); err != nil {
		return q, err
	}
	q.Limit = min(r.Limit, maxLimit)
	if q.Limit <= 0 {
		q.Limit = defaultLimit
	}
	return q, nil
}

func parseTime(name, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, badParam(name, "must be an RFC 3339 time")
	}
	return t, nil
}

func (a *api) list(ctx context.Context, in *httpapi.In[queryRequest]) (pageView, error) {
	q, err := in.Req.parse()
	if err != nil {
		return pageView{}, err
	}
	page, err := a.journal.Query(ctx, q)
	if err != nil {
		// A plain error: httpapi logs it and answers 500 without echoing it.
		return pageView{}, fmt.Errorf("loghttp: querying the journal: %w", err)
	}
	// The other-user rule: the viewer is the request principal; the predicate
	// runs on every record of the page (at most maxLimit), imported and new alike.
	viewer := ""
	if p, ok := auth.PrincipalFrom(ctx); ok && p.User != nil {
		viewer = string(p.User.ID)
	}
	mayRead := rbac.Can(ctx, permUsers)
	out := pageView{Records: make([]recordView, 0, len(page.Records)), Next: page.Next}
	for _, rec := range page.Records {
		if !mayRead && a.otherUser != nil && a.otherUser(rec, viewer) {
			continue // not counted anywhere on the wire
		}
		out.Records = append(out.Records, viewOf(rec, !mayRead))
	}
	return out, nil
}

// viewOf is rec on the wire. Attributes are flat (dotted keys) and rendered as
// text; a repeated key keeps its last value. With blank, every user_id
// attribute, whether top-level or inside a group (".user_id"), is present and
// empty.
func viewOf(rec log.Record, blank bool) recordView {
	v := recordView{
		Time: rec.Time, Level: strings.ToLower(rec.Level.String()), Module: rec.Module, Subsystem: rec.Subsystem,
		Message: rec.Message, Attrs: make(map[string]string, len(rec.Attrs)), TraceID: rec.TraceID, SpanID: rec.SpanID,
	}
	for _, a := range rec.Attrs {
		val := a.Value.Resolve().String()
		if blank && (a.Key == log.KeyUserID || strings.HasSuffix(a.Key, "."+log.KeyUserID)) {
			val = ""
		}
		v.Attrs[a.Key] = val
	}
	return v
}
