// file: core/realtime/stream.go
// version: 1.0.0
// guid: 296e9d57-e9d7-4a07-895f-152672e4463d
// last-edited: 2026-11-11

package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/sse"
	"github.com/falkcorp/mediacore/rbac"
)

// strikes is how many consecutive store failures a stream tolerates before it
// ends: a short blip must not drop every stream, and a persistent failure
// must not keep a revoked credential streaming (05-realtime §6).
const strikes = 3

// stream is one subscriber's policy: its principal, the topics it may see now
// and the verdict on each event. The broker's stream loop calls Deliver and
// Check on the stream's own goroutine only, so none of its fields is locked;
// the one exception is user and cred, which are written before Subscribe and
// only read afterwards (End's matcher).
type stream struct {
	h        *Hub
	p        *auth.Principal
	user     auth.UserID // "" for the auth-disabled principal
	cred     string
	explicit map[string]struct{}         // topics the caller named exactly: losing one ends the stream
	allowed  map[string]events.TopicSpec // topics it may see now; only ever shrinks

	seenGen      uint64 // the rbac generation allowed was computed at
	revalFails   int    // consecutive Revalidate store failures
	refreshFails int    // consecutive Refresh store failures
}

var _ sse.Judge = (*stream)(nil)

// holds is the in-handler form of a permission check: a principal with no
// compiled checker holds nothing, and a Confined key never satisfies
// AnyAuthenticated (05-rbac §4.10). A bare "a principal exists" is not enough.
func holds(p *auth.Principal, need rbac.Requirement) bool {
	return p != nil && p.Perms != nil && p.Perms.Satisfies(need)
}

// Deliver is the one send-time filter, shared by live entries and the ring's
// replay (05-realtime §4.4): the topic must still be in the stream's set, its
// catalog Permission must be satisfied by the principal's checker as it stands
// now, then Visible, then Redact. A panic anywhere withholds the event from
// this subscriber and is counted; nothing a buggy owner filter does reaches
// the hub or another subscriber. Visible and Redact are given their own copy
// of the payload, because the ring's bytes are shared by every subscriber.
func (st *stream) Deliver(ctx context.Context, e *sse.Entry) (data []byte, ok bool) {
	it, isItem := e.Item.(*item)
	spec, allowed := st.allowed[e.Topic]
	if !isItem || !allowed {
		st.h.m.filtered(e.Topic, "perm")
		return nil, false
	}
	verdict := ""
	func() {
		defer func() {
			if recover() != nil {
				verdict = "panic"
			}
		}()
		if !holds(st.p, spec.Permission) {
			verdict = "perm"
			return
		}
		raw := it.raw
		if spec.Visible != nil {
			raw.Payload = bytes.Clone(it.raw.Payload)
			if !spec.Visible(ctx, st.p.Perms, raw) {
				verdict = "visible"
				return
			}
		}
		body := it.raw.Payload
		if spec.Redact != nil {
			body = spec.Redact(st.p.Perms, bytes.Clone(it.raw.Payload))
			if !json.Valid(body) {
				verdict = "redact"
				return
			}
		}
		data = make([]byte, 0, len(it.head)+len(body)+1)
		data = append(append(append(data, it.head...), body...), '}')
	}()
	switch verdict {
	case "":
		st.h.m.delivered(e.Topic)
		return data, true
	case "panic":
		st.h.m.dropped("filter_panic")
		st.h.warnOnce(e.Topic, "realtime: a topic's Visible or Redact panicked; the event is withheld from that subscriber (counted; logged once per topic per minute)")
	case "redact":
		st.h.m.dropped("redact")
		st.h.warnOnce(e.Topic, "realtime: a topic's Redact returned something that is not JSON; the event is withheld (counted; logged once per topic per minute)")
	default:
		st.h.m.filtered(e.Topic, verdict)
	}
	return nil, false
}

// transient reports a failure that says nothing about the credential: the
// store is down, or the stream's own context ended.
func transient(err error) bool {
	return errors.Is(err, auth.ErrStoreUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Check runs on every wake and every heartbeat (05-realtime §4.6). A changed
// rbac generation re-resolves the principal on the next wake; a heartbeat
// asks auth whether the credential still stands, which also covers the
// session simply expiring.
func (st *stream) Check(ctx context.Context, beat bool) string {
	if ctx.Err() != nil {
		return ""
	}
	if g := st.h.gen.Load(); g != st.seenGen && (st.refreshFails == 0 || beat) {
		if r := st.rightsChanged(ctx, g); r != "" {
			return r
		}
	}
	if beat {
		return st.revalidate(ctx)
	}
	return ""
}

// revalidate is auth.Service.Revalidate on a heartbeat: a definite refusal
// ends the stream at once, a store failure only after strikes in a row.
func (st *stream) revalidate(ctx context.Context) string {
	err := st.h.d.Auth.Revalidate(ctx, st.p)
	switch {
	case err == nil:
		st.revalFails = 0
		return ""
	case ctx.Err() != nil:
		return ""
	case !transient(err):
		return sse.CloseCredentialRevoked
	}
	st.revalFails++
	st.h.log.Warn("realtime: credential revalidation unavailable", "strikes", st.revalFails)
	if st.revalFails >= strikes {
		return sse.CloseCredentialRevoked
	}
	return ""
}

// rightsChanged reacts to a new rbac generation g. Without a Refresh hook the
// stream cannot know what it may still see, so it ends and the client
// reconnects. With one, the principal is rebuilt: topics it may no longer see
// are dropped from the set, and losing one it named exactly ends the stream
// with permission_revoked (05-realtime §4.6). Topics it has gained are not
// added: the set is fixed at subscribe, so a widening never exposes more
// than the request asked for.
func (st *stream) rightsChanged(ctx context.Context, g uint64) string {
	refresh := st.h.d.Refresh
	if refresh == nil {
		return sse.ClosePermissionRevoked
	}
	np, err := refresh(ctx, st.p)
	switch {
	case err != nil && ctx.Err() != nil:
		return ""
	case err != nil && transient(err):
		st.refreshFails++
		if st.refreshFails >= strikes {
			return sse.ClosePermissionRevoked
		}
		return ""
	case err != nil:
		return sse.CloseCredentialRevoked
	case !holds(np, rbac.AnyAuthenticated), userOf(np) != st.user:
		return sse.ClosePermissionRevoked
	}
	st.refreshFails = 0
	st.p, st.seenGen = np, g
	lost := false
	for _, name := range slices.Sorted(maps.Keys(st.allowed)) {
		if holds(np, st.allowed[name].Permission) {
			continue
		}
		delete(st.allowed, name)
		if _, named := st.explicit[name]; named {
			lost = true
		}
	}
	if lost {
		return sse.ClosePermissionRevoked
	}
	return ""
}

// userOf is p's user ID, "" for the auth-disabled principal.
func userOf(p *auth.Principal) auth.UserID {
	if p == nil || p.User == nil {
		return ""
	}
	return p.User.ID
}
