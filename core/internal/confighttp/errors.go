// file: core/internal/confighttp/errors.go
// version: 1.0.0
// guid: 2bf2666b-a918-45d3-936d-ca9c4b6827a9
// last-edited: 2026-11-10

package confighttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
)

// problemTypeBase is the stable URI prefix of every problem type, the same
// httpapi writes.
const problemTypeBase = "https://mediacore.dev/problems/"

// problemBody is httpapi.Problem plus the two extension members 05-config
// §1.3.4 names for PATCH: locked_by on a 409 and need on a 403. httpapi.Error
// carries neither, so PATCH writes its failures itself (httpapi.HandleRaw)
// and keeps every other member exactly as httpapi writes it.
//
// LockedBy maps each locked key to the operator layer that supplies it
// ("env ABO_CORE_LOG_LEVEL", "flag core.storage.path", "file <name>": the
// text config's LockedError carries, never a value). Need maps each refused
// key to what it needs: the protection level for a protected key, the
// section's write permission for a key the caller may not write.
type problemBody struct {
	httpapi.Problem
	LockedBy map[string]string `json:"locked_by,omitempty"`
	Need     map[string]string `json:"need,omitempty"`
}

// writeProblem completes pb as httpapi's writeProblem does (type, title,
// instance, request ID) and writes it as application/problem+json.
func writeProblem(w http.ResponseWriter, r *http.Request, pb problemBody, log *slog.Logger) {
	if pb.Code == "" {
		pb.Code = "error"
	}
	pb.Type = problemTypeBase + pb.Code
	pb.Title = http.StatusText(pb.Status)
	pb.Instance = r.URL.Path
	pb.RequestID = httpapi.RequestID(r.Context())
	writeJSON(r.Context(), w, pb.Status, "application/problem+json", pb, log)
}

// writeJSON encodes v before writing any header, so an encode failure is
// still a clean 500.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, contentType string, v any, log *slog.Logger) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		if log != nil {
			log.ErrorContext(ctx, "confighttp: encode response", "error", err)
		}
		http.Error(w, `{"code":"internal","status":500}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil && log != nil {
		log.DebugContext(ctx, "confighttp: write response", "error", err)
	}
}

// fail writes err: an *httpapi.Error or a *problemBody as itself, anything
// else as httpapi's 500 with the cause withheld from the body.
func fail(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	if pb, ok := errors.AsType[*problemBody](err); ok {
		writeProblem(w, r, *pb, log)
		return
	}
	httpapi.WriteError(w, r, err, log)
}

// Error makes a *problemBody an error, so the handlers return one value type.
func (pb *problemBody) Error() string { return "confighttp: " + pb.Code }

// problem builds a *problemBody.
func problem(status int, code, detail string) *problemBody {
	return &problemBody{Problem: httpapi.Problem{Status: status, Code: code, Detail: detail}}
}

// unauthenticated is the 401 of a request that carries no identity this
// route can name an actor from. httpapi never passes a nil principal to a
// non-Public route, so for a nil principal this is a backstop for a mounting
// mistake; for a principal with neither a user nor the auth-off method it is
// the refusal the route owes (a commit with no actor is not audited, so the
// route refuses before Apply rather than rely on config to).
func unauthenticated() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"}
}

// ---- request decoding -----------------------------------------------------

// maxBody bounds a PATCH body: a settings patch is a handful of keys, though
// one value can be a list or a map.
const maxBody = 1 << 20

// decodeRequest reads r's body as a SettingsRequest as httpapi's strict
// decoding would: application/json only, a bounded body, no unknown member,
// no trailing data. No answer echoes the body or encoding/json's text, which
// can quote a submitted value (a secret key's value is a member of "set").
func decodeRequest(w http.ResponseWriter, r *http.Request) (config.Patch, *httpapi.Error) {
	var zero config.Patch
	if !isJSON(r.Header.Get("Content-Type")) {
		return zero, &httpapi.Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type",
			Detail: "Content-Type must be application/json"}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if _, big := errors.AsType[*http.MaxBytesError](err); big {
			return zero, &httpapi.Error{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large", Detail: "the body is too large"}
		}
		return zero, malformed("the body could not be read")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	var members map[string]json.RawMessage
	if err := dec.Decode(&members); err != nil {
		return zero, malformed("the body is not a JSON object")
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return zero, malformed("trailing data after the JSON value")
	}
	for name := range members {
		if name != "set" && name != "unset" {
			return zero, &httpapi.Error{Status: http.StatusBadRequest, Code: "unknown_field",
				Detail: "the body has a member this route does not accept", Fields: []httpapi.FieldError{httpapi.Field("/body", "unknown_field", "only set and unset are accepted")}}
		}
	}
	var p config.Patch
	if raw, ok := members["set"]; ok {
		if err := json.Unmarshal(raw, &p.Set); err != nil {
			return zero, httpapi.Invalid(httpapi.Field("/body/set", "invalid_type", "must be an object of key to value"))
		}
	}
	if raw, ok := members["unset"]; ok {
		if err := json.Unmarshal(raw, &p.Unset); err != nil {
			return zero, httpapi.Invalid(httpapi.Field("/body/unset", "invalid_type", "must be an array of keys"))
		}
	}
	return p, nil
}

// malformed is a 400 malformed_json with fixed text.
func malformed(detail string) *httpapi.Error {
	return &httpapi.Error{Status: http.StatusBadRequest, Code: "malformed_json", Detail: detail}
}

// isJSON reports whether a Content-Type header names application/json.
func isJSON(h string) bool {
	mt, _, _ := strings.Cut(h, ";")
	return strings.EqualFold(strings.TrimSpace(mt), "application/json")
}

// ---- Apply's refusals -----------------------------------------------------

// mapApplyError turns Store.Apply's refusals into the statuses 05-config
// §1.3.4 fixes, reading the sentinel and the typed errors and never a
// message that can quote a value:
//
//	ErrUnknownKey  400 unknown_key
//	ErrForbidden   403 permission_denied, need: key -> the section's write permission
//	ErrProtected   403 protection_required, need: key -> the protection level
//	ErrLocked      409 locked, locked_by: key -> the operator layer
//	ErrInvalid     422 validation_failed, one field error per key
//
// Anything else (a storage failure, a closed store) is returned unchanged:
// httpapi answers it 500 and withholds the cause. The second result is false
// then.
//
// Apply's refusals are homogeneous (one kind per call, each naming every key
// it refuses), so the sentinel order below cannot misreport a mixed error.
func mapApplyError(err error, st Store, p config.Patch) (*problemBody, bool) {
	switch {
	case errors.Is(err, config.ErrUnknownKey):
		// The keys are not listed: Apply's text names them as the caller typed
		// them, and an unknown key is the caller's own input echoed back for
		// no use (the key either exists or the caller mistyped it).
		return problem(http.StatusBadRequest, "unknown_key", "the patch names a key this application does not have"), true
	case errors.Is(err, config.ErrForbidden):
		pb := problem(http.StatusForbidden, "permission_denied", "the caller lacks the permission of a key's section")
		pb.Need = forbiddenNeeds(err)
		// One permission missing for every key is the route's required_permission
		// too, as httpapi's own 403 carries it.
		if perm, same := onlyValue(pb.Need); same {
			pb.Required = perm
		}
		return pb, true
	case errors.Is(err, config.ErrProtected):
		pb := problem(http.StatusForbidden, "protection_required", "a key needs a stronger credential than the caller's")
		pb.Need = map[string]string{}
		for _, pe := range collect[*config.ProtectedError](err) {
			pb.Need[string(pe.Path)] = pe.Need.String()
		}
		return pb, true
	case errors.Is(err, config.ErrLocked):
		pb := problem(http.StatusConflict, "locked", "a key is set by the operator and cannot be changed here")
		pb.LockedBy = map[string]string{}
		for _, le := range collect[*config.LockedError](err) {
			pb.LockedBy[string(le.Path)] = le.By
		}
		return pb, true
	case errors.Is(err, config.ErrInvalid):
		return invalidProblem(err, st, p), true
	}
	return nil, false
}

// invalidProblem is the 422 of ErrInvalid: one field error per *FieldError,
// at the key's place in the body. A secret key's error carries a fixed
// message whatever config wrote (config already redacts a sensitive key's
// errors; this is the second lock, on the one class whose value must never
// leave). Any part of the error that is not a *FieldError is replaced by one
// fixed message on the body.
func invalidProblem(err error, st Store, p config.Patch) *problemBody {
	pb := problem(http.StatusUnprocessableEntity, "validation_failed", "")
	fes := collect[*config.FieldError](err)
	for _, fe := range fes {
		msg := "the value is not valid for this key"
		if fe.Err != nil {
			msg = fe.Err.Error()
		}
		if ks, ok := st.Lookup(fe.Path); ok && ks.Secret != nil {
			msg = "the value is not valid for this secret key"
		}
		pb.Errors = append(pb.Errors, httpapi.Field(pointerFor(fe.Path, p), "invalid", msg))
	}
	if len(pb.Errors) == 0 {
		pb.Errors = []httpapi.FieldError{httpapi.Field("/body", "invalid", "the patch is not valid")}
	}
	return pb
}

// pointerFor is the JSON pointer of key in the request: its member of set
// when it was set, else the unset list, else the body.
func pointerFor(key config.Path, p config.Patch) string {
	if _, ok := p.Set[key]; ok {
		return "/body/set/" + escapePointer(string(key))
	}
	for i, u := range p.Unset {
		if u == key {
			return "/body/unset/" + strconv.Itoa(i)
		}
	}
	return "/body"
}

// escapePointer escapes one JSON pointer segment (RFC 6901).
func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// collect is every error of type E in err's tree, in order, through joined
// errors and multiple %w.
func collect[E error](err error) []E {
	var out []E
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if t, ok := e.(E); ok {
			out = append(out, t)
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				walk(c)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return out
}

// forbiddenNeeds reads the keys and the write permissions out of Apply's
// ErrForbidden text, which config formats as "<sentinel>: <key> needs
// <permission>; <key> needs <permission>". config has no typed error for it
// (a *ForbiddenError, as it has *LockedError and *ProtectedError, would make
// this a type switch); until it has one, a text that does not parse yields no
// need at all: the 403 stands, only its hint is lost. Keys are registered
// paths (an unknown key is refused first) and permissions are catalog
// strings, so neither carries a caller-supplied value.
func forbiddenNeeds(err error) map[string]string {
	text := err.Error()
	_, rest, ok := strings.Cut(text, config.ErrForbidden.Error()+": ")
	if !ok {
		return nil
	}
	need := map[string]string{}
	for part := range strings.SplitSeq(rest, "; ") {
		i := strings.LastIndex(part, " needs ")
		if i <= 0 || strings.ContainsAny(part[i+len(" needs "):], " \t\n") {
			return nil
		}
		need[part[:i]] = part[i+len(" needs "):]
	}
	return need
}

// onlyValue is the one value of m when every entry holds the same, else false.
func onlyValue(m map[string]string) (string, bool) {
	var v string
	for _, x := range m {
		if v != "" && x != v {
			return "", false
		}
		v = x
	}
	return v, v != ""
}

// ---- identity -------------------------------------------------------------

// authDisabledSubject is the Subject of the auth-off principal, the name
// integrations' Registry gives its writes (it has no user to name).
const authDisabledSubject = "auth_disabled"

// subjectOf names p for config.Actor.Subject (the audit actor and every row's
// By): the user's ID, or authDisabledSubject for the auth-off principal. A
// principal that is neither names nobody, and the route refuses it.
func subjectOf(p *auth.Principal) (string, bool) {
	switch {
	case p.User != nil && p.User.ID != "":
		return string(p.User.ID), true
	case p.Method == auth.MethodAuthDisabled:
		return authDisabledSubject, true
	}
	return "", false
}
