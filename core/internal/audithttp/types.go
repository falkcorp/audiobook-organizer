// file: core/internal/audithttp/types.go
// version: 1.0.2
// guid: b9e9bb7e-8645-47b1-a871-1a1305a27406
// last-edited: 2026-11-11

package audithttp

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/httpapi"
)

// The wire types. audit.Record carries no JSON tags, so the routes answer
// with these: snake_case names (05-http-api §3.4) and every field of the
// record, so an export loses nothing the store holds. Their names carry the
// "Audit" prefix because an OpenAPI component is named <module>.<TypeName>
// and every core capability shares the "core" owner.

// AuditRecord is one audit record (05-rbac §7). Details is what the store
// kept: it was redacted by key name and by value when it was written, so
// nothing here re-masks it and nothing here can restore what the store
// withheld.
type AuditRecord struct {
	Seq        uint64         `json:"seq"`
	ID         string         `json:"id"`
	Time       time.Time      `json:"time"`
	Action     string         `json:"action"`
	Outcome    string         `json:"outcome"`
	Actor      AuditActor     `json:"actor"`
	Target     AuditTarget    `json:"target"`
	Permission string         `json:"permission"`
	Route      string         `json:"route"`
	RequestID  string         `json:"request_id"`
	TraceID    string         `json:"trace_id"`
	Reason     string         `json:"reason"`
	Details    map[string]any `json:"details,omitempty"`
	Coalesced  uint32         `json:"coalesced"`
}

// AuditActor is who did it; an anonymous actor has an empty user_id.
type AuditActor struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	Method       string `json:"method"`
	CredentialID string `json:"credential_id"`
	ClientAddr   string `json:"client_addr"`
	UserAgent    string `json:"user_agent"`
}

// AuditTarget is what it was done to.
type AuditTarget struct {
	Module string `json:"module"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}

// wireFor renders rec for a viewer. Anyone but a user manager gets the row
// without the credential ID and the client address: which key or session did
// it, and from where, is for the people who manage users; a viewer needs what
// was done and by whom.
func wireFor(rec audit.Record, manager bool) AuditRecord {
	w := wire(rec)
	if !manager {
		w.Actor.CredentialID, w.Actor.ClientAddr = "", ""
	}
	return w
}

// wire renders rec whole.
func wire(rec audit.Record) AuditRecord {
	return AuditRecord{
		Seq: rec.Seq, ID: rec.ID, Time: rec.Time, Action: rec.Action, Outcome: string(rec.Outcome),
		Actor: AuditActor{
			UserID: rec.Actor.UserID, Username: rec.Actor.Username, Method: rec.Actor.Method,
			CredentialID: rec.Actor.CredentialID, ClientAddr: rec.Actor.ClientAddr, UserAgent: rec.Actor.UserAgent,
		},
		Target:     AuditTarget{Module: rec.Target.Module, Kind: rec.Target.Kind, ID: rec.Target.ID},
		Permission: rec.Permission, Route: rec.Route, RequestID: rec.RequestID, TraceID: rec.TraceID,
		Reason: rec.Reason, Details: rec.Details, Coalesced: rec.Coalesced,
	}
}

// ---- the query ------------------------------------------------------------

// Bounds on the filters. 05-rbac §7.6 names the parameters and no bounds; an
// actor id, an action prefix and a target part each fit a stored field, so a
// longer value matches nothing and is refused instead.
const (
	maxActorLen  = 128
	maxActionLen = 64
	maxRefLen    = 128
)

// actionPrefixOK reports whether s fits audit.Filter's grammar for
// ActionPrefix: one or more lowercase letters, digits, underscores or dots.
func actionPrefixOK(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// listReq is GET /audit's query: the filters of §7.6 and the cursor.
type listReq struct {
	httpapi.CursorParams
	Since   time.Time `query:"since"`
	Until   time.Time `query:"until"`
	Actor   string    `query:"actor"`
	Action  string    `query:"action"`
	Outcome string    `query:"outcome"`
	Target  string    `query:"target"`
}

// Validate refuses a filter the store would never match. The handler parses
// again, so a caller that skips Validate gets the same answer.
func (r *listReq) Validate() error {
	_, err := parseQuery(r.Since, r.Until, r.Actor, r.Action, r.Outcome, r.Target)
	return err
}

// query is a validated set of filters.
type query struct {
	filter audit.Filter
	// canon is the filters as one string: the cursor's filter hash and the
	// export record's Details are both made from it.
	canon   string
	details map[string]any
}

// parseQuery validates the filters and builds the store's Filter. Every
// problem is reported, each at its own query parameter.
func parseQuery(since, until time.Time, actor, action, outcome, target string) (query, error) {
	var fs []httpapi.FieldError
	bad := func(name, code, msg string) { fs = append(fs, httpapi.Field("/query/"+name, code, msg)) }

	f := audit.Filter{Since: since, Until: until}
	details := map[string]any{}
	if !since.IsZero() {
		details["since"] = since.UTC().Format(time.RFC3339Nano)
	}
	if !until.IsZero() {
		details["until"] = until.UTC().Format(time.RFC3339Nano)
	}
	if !since.IsZero() && !until.IsZero() && since.After(until) {
		bad("since", "invalid_range", "since must not be after until")
	}
	if actor != "" {
		if len(actor) > maxActorLen {
			bad("actor", "too_long", "must be at most "+strconv.Itoa(maxActorLen)+" bytes")
		}
		f.ActorUserID = actor
		details["actor"] = actor
	}
	if action != "" {
		if len(action) > maxActionLen || !actionPrefixOK(action) {
			bad("action", "invalid_format", "an action prefix is up to "+strconv.Itoa(maxActionLen)+" of a-z, 0-9, _ and .")
		}
		f.ActionPrefix = action
		details["action"] = action
	}
	if outcome != "" {
		switch o := audit.Outcome(outcome); o {
		case audit.Success, audit.Failure, audit.Denied, audit.Error:
			f.Outcome = o
		default:
			bad("outcome", "invalid_enum", "must be one of success, failure, denied, error")
		}
		details["outcome"] = outcome
	}
	if target != "" {
		ref, ok := parseTarget(target)
		if !ok {
			bad("target", "invalid_format", "a target is module, module/kind or module/kind/id")
		}
		f.Target = ref
		details["target"] = target
	}
	if len(fs) > 0 {
		return query{}, httpapi.Invalid(fs...)
	}
	canon := fmt.Sprintf("%d|%d|%s|%s|%s|%s", unixNano(since), unixNano(until), actor, action, outcome, target)
	return query{filter: f, canon: canon, details: details}, nil
}

func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// parseTarget reads module, module/kind or module/kind/id. Each part present
// is non-empty and short, and a part is present only if the one before it is:
// the store narrows by module, then kind, then id.
func parseTarget(s string) (entity.Ref, bool) {
	parts := strings.SplitN(s, "/", 3)
	for _, p := range parts {
		if p == "" || len(p) > maxRefLen {
			return entity.Ref{}, false
		}
	}
	ref := entity.Ref{Module: parts[0]}
	if len(parts) > 1 {
		ref.Kind = parts[1]
	}
	if len(parts) > 2 {
		ref.ID = parts[2]
	}
	return ref, true
}

// hash is the cursor's filter hash.
func (q query) hash() uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(q.canon)) // hash.Hash.Write never fails
	return h.Sum64()
}

// ---- the cursor -----------------------------------------------------------

// A cursor's position is the 8-byte big-endian seq of the first record the
// page did not return. The next page starts there inclusively, and the store's
// BeforeSeq is exclusive, so it is that seq plus one.

func cursorPos(seq uint64) []byte { return binary.BigEndian.AppendUint64(nil, seq) }

// beforeSeq is the Filter.BeforeSeq a position stands for. A position that is
// not 8 bytes, or is the largest seq (one more would wrap to "no bound"), is
// forged: a 400 like any malformed cursor.
func beforeSeq(pos []byte) (uint64, error) {
	if len(pos) != 8 {
		return 0, invalidCursor()
	}
	seq := binary.BigEndian.Uint64(pos)
	if seq == math.MaxUint64 {
		return 0, invalidCursor()
	}
	return seq + 1, nil
}

func invalidCursor() error {
	return &httpapi.Error{Status: 400, Code: "invalid_cursor", Detail: "malformed cursor",
		Fields: []httpapi.FieldError{httpapi.Field("/query/cursor", "invalid_cursor", "malformed cursor")}}
}
