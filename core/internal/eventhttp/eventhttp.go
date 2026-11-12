// file: core/internal/eventhttp/eventhttp.go
// version: 1.0.0
// guid: d6d9880c-108c-4fb8-9936-39c18c441761
// last-edited: 2026-11-11

// Package eventhttp registers GET /api/core/v1/events, the one realtime
// stream (05-realtime §3, §4.1; 05-http-api §5 item 4), on core's group. The
// handler is the realtime hub's own: it parses the query, expands the topic
// patterns over what the caller may see now and refuses with a problem+json
// status before the first stream byte (401, 403, 400, 429, 503 with
// Retry-After). This package only mounts it, with the route's contract:
// Perm rbac.AnyAuthenticated, HandleRaw (the handler writes its own response,
// so the router neither binds nor compresses it), the default rate class and
// no request body.
//
// Layer L6 (03 R4, internal/<capability>http), imported only by kernel, with
// no package-level mutable state (R5). It is dark until F6-03 mounts it.
package eventhttp

import (
	"errors"
	"net/http"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// Deps are what Register needs.
type Deps struct {
	// Hub is the App's one hub (R5); required.
	Hub *realtime.Hub
}

// ErrNoHub is Register's refusal of a nil hub.
var ErrNoHub = errors.New("eventhttp: Deps.Hub is required")

// Register registers the stream route on g, which must be the router's
// CoreGroup(). It registers nothing and returns ErrNoHub for a nil hub.
func Register(g *httpapi.Group, d Deps) error {
	if d.Hub == nil {
		return ErrNoHub
	}
	httpapi.HandleRaw(g, route(), httpapi.RawDoc{Query: []string{"topic", "exclude", "key", "last_event_id"}}, d.Hub.Handler())
	return nil
}

// route is GET /events. It is AnyAuthenticated at the router: a principal
// with no compiled checker, and a Confined key (a worker or facade key,
// 05-rbac §4.10), never opens a stream. MaxBody 0 and a GET carry no body.
func route() httpapi.Route {
	return httpapi.Route{
		Method: http.MethodGet, Path: "/events", Name: "streamEvents", Perm: rbac.AnyAuthenticated,
		Tags:    []string{"events"},
		Summary: "Stream events (server-sent events)",
		Doc: "The one realtime stream. topic is repeatable and is an exact topic name, a module wildcard (module.*) or * (the default); exclude removes topics with the same grammar; key narrows to one entity; last_event_id (or the Last-Event-ID header, which wins) resumes after an event id inside the server's bounded window. " +
			"An exact topic you may not see is 403 topic_not_permitted and an unknown one is 400 unknown_topic; a wildcard silently leaves out topics you may not see. Over the stream limits the answer is 429 (per user and credential class) or 503, with Retry-After. Every refusal is a problem+json status before the stream starts. " +
			"The stream carries unnamed frames: id: <bootNonce>-<seq> and data: {topic, ts, key, data}, a comment line per heartbeat, and the id-less control frames core.stream.opened.v1, core.stream.reset.v1 (restart, gap or overflow: refetch over REST) and core.stream.closed.v1 (shutdown, credential_revoked or permission_revoked: reconnect). Each event is filtered for you at send time, replays included.",
	}
}
