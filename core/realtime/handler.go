// file: core/realtime/handler.go
// version: 1.0.2
// guid: 92294d19-9b17-417a-ac8b-ebeb60b136f0
// last-edited: 2026-11-12

package realtime

import (
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/sse"
	"github.com/falkcorp/mediacore/rbac"
)

// Query bounds. A request over any of them is a 400 before the stream, so a
// caller cannot make the hub expand an unbounded pattern list or hold an
// unbounded key.
const (
	maxQueryBytes  = 8 << 10
	maxPatterns    = 32  // topic and exclude each
	maxPatternLen  = 128 // one topic or exclude pattern
	maxKeyLen      = 256
	maxLastEventID = 128 // a longer id is a 400; sse treats anything past a real id's length as malformed
)

// query is a parsed subscription request (05-realtime §4.1).
type query struct {
	topics, exclude []string
	key             string
	lastID          string
}

// Handler serves GET /api/core/v1/events. internal/eventhttp registers it on
// CoreGroup with Perm rbac.AnyAuthenticated and HandleRaw. Every refusal is a
// problem+json status written before the first byte of the stream, and the
// handler applies AnyAuthenticated itself (stage 13 answers first once it
// exists, F4-08): no principal is 401, and a principal with no compiled
// checker or a Confined one is 403.
func (h *Hub) Handler() http.Handler { return http.HandlerFunc(h.serve) }

func (h *Hub) serve(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	switch {
	case p == nil:
		httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "sign in to open an event stream"}, h.log)
		return
	case !holds(p, rbac.AnyAuthenticated):
		httpapi.WriteError(w, r, forbidden(), h.log)
		return
	}
	q, err := parseQuery(r)
	if err != nil {
		httpapi.WriteError(w, r, err, h.log)
		return
	}
	sub, err := h.open(p, q)
	if err != nil {
		httpapi.WriteError(w, r, h.refusal(err), h.log)
		return
	}
	sub.Serve(r.Context(), w)
}

func forbidden() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusForbidden, Code: "permission_denied", Detail: "this route needs a signed-in caller"}
}

// refusal is the problem a pre-stream error is answered with (05-realtime §6).
func (h *Hub) refusal(err error) error {
	switch {
	case errors.Is(err, ErrUnknownTopic):
		return &httpapi.Error{Status: http.StatusBadRequest, Code: "unknown_topic", Detail: "a topic or exclude pattern matches no realtime topic"}
	case errors.Is(err, ErrTopicDenied):
		return &httpapi.Error{Status: http.StatusForbidden, Code: "topic_not_permitted", Detail: "you may not stream a topic you named"}
	case errors.Is(err, ErrNotPermitted):
		return forbidden()
	case errors.Is(err, ErrTooManyForUser):
		return httpapi.Throttled(h.opt.RetryAfter)
	case errors.Is(err, ErrTooManyClients), errors.Is(err, ErrNotReady), errors.Is(err, ErrClosed):
		return httpapi.Unavailable("event streams are unavailable; retry shortly", h.opt.RetryAfter)
	}
	return err
}

// parseQuery reads topic, exclude, key and last_event_id, and the
// Last-Event-ID header, which wins over the query form (a reconnecting
// EventSource sets only the header).
func parseQuery(r *http.Request) (query, error) {
	bad := func(detail string) error {
		return &httpapi.Error{Status: http.StatusBadRequest, Code: "invalid_query", Detail: detail}
	}
	if len(r.URL.RawQuery) > maxQueryBytes {
		return query{}, bad("the query string is too long")
	}
	vals, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return query{}, bad("the query string is malformed")
	}
	var q query
	for _, f := range []struct {
		name string
		dst  *[]string
	}{{"topic", &q.topics}, {"exclude", &q.exclude}} {
		ps := vals[f.name]
		if len(ps) > maxPatterns {
			return query{}, bad("too many " + f.name + " patterns")
		}
		for _, p := range ps {
			if p == "" || len(p) > maxPatternLen {
				return query{}, bad("a " + f.name + " pattern is empty or too long")
			}
		}
		*f.dst = ps
	}
	for _, f := range []struct {
		name string
		max  int
		dst  *string
	}{{"key", maxKeyLen, &q.key}, {"last_event_id", maxLastEventID, &q.lastID}} {
		vs := vals[f.name]
		if len(vs) > 1 || (len(vs) == 1 && len(vs[0]) > f.max) {
			return query{}, bad(f.name + " is repeated or too long")
		}
		if len(vs) == 1 {
			*f.dst = vs[0]
		}
	}
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		if len(h) > maxLastEventID {
			return query{}, bad("Last-Event-ID is too long")
		}
		q.lastID = h
	}
	return q, nil
}

// open expands the request against the catalog for p, registers the
// subscriber and returns it. Nothing has been written to any response, so a
// returned error is still a plain status.
func (h *Hub) open(p *auth.Principal, q query) (*sse.Subscriber, error) {
	tab := h.table.Load()
	switch {
	case tab == nil:
		return nil, ErrNotReady
	case h.closed.Load():
		return nil, ErrClosed
	case !holds(p, rbac.AnyAuthenticated): // serve already refuses this; open repeats it so it is safe on its own
		return nil, ErrNotPermitted
	}
	bucket, ok := bucketOf(p)
	if !ok {
		return nil, ErrNotPermitted
	}
	// The generation is read before rights are computed, so a change that
	// lands during the expansion is seen by the first Check.
	st := &stream{h: h, req: p, p: p, user: userOf(p), cred: p.CredentialID, seenGen: h.gen.Load(), explicit: map[string]struct{}{}}
	var err error
	if st.allowed, err = h.expand(tab, p, q, st.explicit); err != nil {
		if errors.Is(err, ErrTopicDenied) {
			h.m.refused("denied")
		}
		return nil, err
	}
	sub, err := h.br.Subscribe(sse.Subscription{
		Bucket: bucket, Topics: slices.Sorted(maps.Keys(st.allowed)), Key: q.key, LastEventID: q.lastID,
		Judge: st, Meta: st,
	})
	switch {
	case errors.Is(err, sse.ErrTooManyClients):
		h.m.refused("global_cap")
		return nil, ErrTooManyClients
	case errors.Is(err, sse.ErrTooManyBucket):
		h.m.refused("bucket_cap")
		return nil, ErrTooManyForUser
	case errors.Is(err, sse.ErrClosed):
		return nil, ErrClosed
	case err != nil:
		return nil, err
	}
	st.sub = sub // before Serve starts: the judge is its only other user
	return sub, nil
}

// bucketOf is the per-user cap's key: a user's interactive sessions share one
// bucket, all of that user's API keys another, and any other method (an
// extension, a module's compat surface) its own, so minting keys or signing in
// elsewhere never buys more streams or takes another class's. Auth-disabled
// principals have no user: they take no bucket and only MaxClients bounds
// them. ok is false for a principal that is neither (no user and not
// auth-disabled), which may open nothing.
func bucketOf(p *auth.Principal) (bucket string, ok bool) {
	if p.User == nil {
		return "", p.Method == auth.MethodAuthDisabled
	}
	class := "method:" + string(p.Method)
	switch {
	case p.Method == auth.MethodAPIKey:
		class = "apikey"
	case p.Method.Interactive():
		class = "session"
	}
	// A NUL joins them: a user ID cannot contain one.
	return string(p.User.ID) + "\x00" + class, true
}

// expand resolves the request's topic and exclude patterns to the realtime
// topics the principal may see NOW (05-realtime §4.1). A pattern is exact, a
// module wildcard "<module>.*", or "*" (the default). An exact topic that is
// not a realtime topic, or a wildcard matching none, is ErrUnknownTopic; an
// exact topic the principal may not see is ErrTopicDenied (fail closed and
// visible), while a wildcard silently leaves such topics out. explicit
// receives the exact topics that survive exclude.
func (h *Hub) expand(tab *topicTable, p *auth.Principal, q query, explicit map[string]struct{}) (map[string]events.TopicSpec, error) {
	include := q.topics
	if len(include) == 0 {
		include = []string{"*"}
	}
	excluded := map[string]struct{}{}
	for _, pat := range q.exclude {
		names, _, err := match(tab, pat)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			excluded[n] = struct{}{}
		}
	}
	out := map[string]events.TopicSpec{}
	for _, pat := range include {
		names, exact, err := match(tab, pat)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if _, skip := excluded[n]; skip {
				continue
			}
			spec := tab.specs[n]
			switch {
			case holds(p, spec.Permission):
				out[n] = spec
				if exact {
					explicit[n] = struct{}{}
				}
			case exact:
				return nil, ErrTopicDenied
			}
		}
	}
	return out, nil
}

// match is the realtime topics pattern names, and whether it named one topic
// exactly.
func match(tab *topicTable, pat string) (names []string, exact bool, err error) {
	switch {
	case pat == "*":
		return tab.names, false, nil // an app with no realtime topic streams nothing, not an error
	case strings.HasSuffix(pat, ".*"):
		module := strings.TrimSuffix(pat, ".*")
		for _, n := range tab.names {
			if m, _, _ := strings.Cut(n, "."); m == module {
				names = append(names, n)
			}
		}
	default:
		if _, ok := tab.specs[pat]; ok {
			return []string{pat}, true, nil
		}
	}
	if len(names) == 0 {
		return nil, false, ErrUnknownTopic
	}
	return names, false, nil
}
