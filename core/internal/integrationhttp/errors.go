// file: core/internal/integrationhttp/errors.go
// version: 1.0.0
// guid: e3a9c1d4-52b7-4f06-8a1d-7c40b9e2f635
// last-edited: 2026-11-11

package integrationhttp

import (
	"errors"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
)

// mapError turns the Registry's sentinels into the statuses 05-metadata M1.5
// fixes (I4), reading the sentinel and never the message. Every Detail is
// fixed text, so no answer carries a value the Registry formatted: its 422
// text can quote a submitted URL (a credential pasted into a URL field would
// come back, and into any log that keeps response bodies), and its schema
// errors quote the offending value. config keeps no message a Validator
// writes for the same reason (05-config). The cause stays on Error.Err of the
// 403, 404, 409 and 503 answers, whose texts name only refs and fields, and
// httpapi never serializes it; a 422 keeps no cause at all, because Error()
// would carry the quoted value to anything that logs it.
//
// An error that is none of these is returned unchanged: httpapi answers it
// 500 and withholds its text.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, integrations.ErrForbidden):
		return &httpapi.Error{Status: http.StatusForbidden, Code: "permission_denied",
			Detail: "this needs core:integrations.manage, and setting Local needs core:admin in an interactive session", Err: err}
	case errors.Is(err, integrations.ErrNotConfigured):
		return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such integration instance", Err: err}
	case errors.Is(err, integrations.ErrFieldLocked):
		return &httpapi.Error{Status: http.StatusConflict, Code: "field_locked",
			Detail: "a field of this instance is set by an operator value and cannot be changed here", Err: err}
	case errors.Is(err, integrations.ErrVersionConflict):
		return &httpapi.Error{Status: http.StatusConflict, Code: "version_conflict",
			Detail: "the instance changed or already exists; read it again and retry", Err: err}
	case errors.Is(err, integrations.ErrDisabled):
		return &httpapi.Error{Status: http.StatusConflict, Code: "integrations_disabled",
			Detail: "outbound integrations are switched off (core.integrations.disabled)", Err: err}
	case errors.Is(err, integrations.ErrUnknownAdapter):
		return &httpapi.Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed",
			Fields: []httpapi.FieldError{httpapi.Field("/body/adapter", "unknown_adapter", "this binary has no such adapter")}}
	case errors.Is(err, integrations.ErrInvalid):
		code, msg := "invalid", "the instance is not valid for its adapter"
		if _, ok := errors.AsType[*config.FieldError](err); ok {
			code, msg = "settings_schema", "the settings do not match the adapter's settings schema"
		}
		return &httpapi.Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed",
			Fields: []httpapi.FieldError{httpapi.Field("/body", code, msg)}}
	case errors.Is(err, integrations.ErrUnavailable), errors.Is(err, integrations.ErrClosed):
		return &httpapi.Error{Status: http.StatusServiceUnavailable, Code: "unavailable",
			Detail: "the integration store or the secrets backend is unavailable; nothing was written", Err: err}
	}
	return err
}

// unauthenticated is the 401 a route answers when no principal reached it.
// httpapi never passes a nil principal to a non-Public route (stage 13 turns
// it away), so this is a backstop for a mounting mistake, not the check.
func unauthenticated() error {
	return &httpapi.Error{Status: http.StatusUnauthorized, Code: "unauthenticated", Detail: "authentication is required"}
}

// guard is every handler's first line. It is not a permission check: the
// route's Perm is the router's to enforce (05-http-api §4.2 stage 13), and
// the Registry re-checks core:integrations.manage on its own writes. It makes
// a handler fail closed when it was mounted without what it needs: no
// principal (401) or no Registry (503).
func (a *api) guard(p *auth.Principal) error {
	switch {
	case p == nil:
		return unauthenticated()
	case a.reg == nil:
		return httpapi.Unavailable("integrations are not available", 5*time.Second)
	}
	return nil
}
