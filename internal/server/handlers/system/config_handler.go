// file: internal/server/handlers/system/config_handler.go
// version: 1.3.0
// guid: 5b8e1c63-2f47-4d90-a6c1-7e3d09b4f825
// last-edited: 2026-11-19

package system

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/gin-gonic/gin"
)

// This file is GET and PUT /api/v1/config over the config Facade (F3-07 e3;
// 05-config section 1.12.1 "Between the config flip and the identity flip").
//
// The handler holds no config rules. Which keys exist, who may write them,
// what an operator layer locks and what a value must look like are the
// Facade's, and so core's: the handler hands the whole request to one
// Facade.Update, so a write commits entirely or not at all, and turns the
// error that comes back into a status and a body. A secret is write-only: the
// View carries its metadata, never a value or a mask, and a write carries the
// new plaintext (set) or names the key (unset). There is no echo detection,
// because there is nothing to echo.

// configHandlerLog carries this file's diagnostics through the log-injection
// barrier: a refused key's name comes from the request.
var configHandlerLog = logger.New("config")

// The "code" of the standard envelope for each refusal: CONFIG_ERROR is a key
// or value the request got wrong, FORBIDDEN a missing permission or
// credential, CONFIG_LOCKED a key an operator layer sets, CONFIG_INVALID a
// value that failed validation.
const (
	configCodeError     = "CONFIG_ERROR"
	configCodeForbidden = "FORBIDDEN"
	configCodeLocked    = "CONFIG_LOCKED"
	configCodeInvalid   = "CONFIG_INVALID"
)

// maxConfigBodyBytes bounds a PUT /config body. A settings write is a few
// keys; the largest legitimate value is a list of path aliases or AI
// endpoints, nowhere near this.
const maxConfigBodyBytes = 1 << 20

// ConfigPermissionAtoms names the HEAD permission atoms that carry a native
// scope: the atoms of books' atom table (05-rbac section 6.1) whose Scopes
// include perm. The table is books' code and books imports this package, so
// the handler cannot read it; books installs this lookup (WithConfigAtoms).
type ConfigPermissionAtoms func(perm string) []string

// WithConfigFacade installs the provider of the Facade the settings routes
// read and write. Lazy, so a Facade installed after the handlers are wired
// (books installs it from Register) is the one a request uses; unset, the
// provider is config.Current. A provider yielding nil answers 503.
func (h *Handler) WithConfigFacade(f func() config.Facade) *Handler {
	h.configFacade = f
	return h
}

// WithConfigAtoms installs the lookup Actor.Can answers a section permission
// with. Unset, Can holds no section permission, so every write that needs one
// is refused: the lookup fails closed.
func (h *Handler) WithConfigAtoms(f ConfigPermissionAtoms) *Handler {
	h.configAtoms = f
	return h
}

// WithConfigFirstRun installs the report of the first-run window: no user exists
// yet and setup is not complete. Inside it the Actor holds config.FirstRunPerm,
// which lets the setup wizard's keys (and only those) be written before the first
// account exists, as HEAD let a fresh install's wizard set the library path.
// Unset, the window is never open.
func (h *Handler) WithConfigFirstRun(f func() bool) *Handler {
	h.configFirstRun = f
	return h
}

// facade resolves the Facade for one request, nil when none is installed.
func (h *Handler) facade() config.Facade {
	if h.configFacade != nil {
		return h.configFacade()
	}
	return config.Current()
}

// configResponse is the body GET answers with, which a PUT repeats after it
// commits (beside what it did): the Facade's View (generation and every key) plus the placeholders a
// folder or file naming pattern may use, straight from the resolver so the
// Settings help text cannot advertise one BuildPath rejects.
type configResponse struct {
	config.View
	NamingPatternTokens []string `json:"naming_pattern_tokens"`
}

// configUpdateBody is PUT /config's request: the keys to set, each to its new
// JSON value (a secret's value is its plaintext), and the keys to clear. At
// a body that names neither is an empty patch, which changes nothing.
type configUpdateBody struct {
	Set   map[string]json.RawMessage `json:"set"`
	Unset []string                   `json:"unset"`
}

// GetConfig implements GET /config.
func (h *Handler) GetConfig(c *gin.Context) {
	f := h.facade()
	if f == nil {
		respondConfigUnavailable(c)
		return
	}
	httputil.RespondWithOK(c, newConfigResponse(f.View()))
}

func newConfigResponse(v config.View) configResponse {
	if v.Keys == nil {
		v.Keys = []config.KeyView{}
	}
	return configResponse{View: v, NamingPatternTokens: organizer.NamingPatternTokens()}
}

func respondConfigUnavailable(c *gin.Context) {
	httputil.RespondWithServiceUnavailable(c, "configuration is not available yet")
}

// UpdateConfig implements PUT /config: the body becomes one Facade.Update.
func (h *Handler) UpdateConfig(c *gin.Context) {
	f := h.facade()
	if f == nil {
		respondConfigUnavailable(c)
		return
	}
	upd, ok := decodeConfigUpdate(c)
	if !ok {
		return
	}

	if len(upd.Set) == 0 && len(upd.Unset) == 0 {
		// An empty patch names no key, so there is nothing to authorize or
		// write: answer with the current View, as HEAD's update did for a body
		// that changed nothing.
		respondConfigApplied(c, f.View(), config.UpdateResult{})
		return
	}

	actor := h.configActor(c, f)
	res, err := f.Update(c.Request.Context(), actor, upd)
	if err != nil {
		h.respondConfigUpdateError(c, err, actor)
		return
	}
	if res.PostCommit != nil {
		// The write committed and an after-commit hook failed: the change is
		// stored and in effect, so nothing is rolled back and the response
		// says so, as the update service's "saved" 500 did.
		configHandlerLog.Error("config update committed but an after-commit hook failed: %s", logger.SanitizeLogErr(res.PostCommit))
		fields := map[string]any{"saved": true, "applied": nonNil(res.Applied), "restart_pending": nonNil(res.RestartPending)}
		addOps(fields, res.Ops)
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"the configuration was saved and is in effect, but a follow-up step failed: "+res.PostCommit.Error(),
			configCodeError, fields)
		return
	}
	respondConfigApplied(c, f.View(), res)
}

// respondConfigApplied answers a PUT that committed (or was empty): the fresh
// View, what the write did, and the operations the write's hooks started under
// the response keys the Facade names them by ("dedup_rescore_op_id" when a dedup
// weight change queued a re-band).
func respondConfigApplied(c *gin.Context, v config.View, res config.UpdateResult) {
	view := newConfigResponse(v)
	body := map[string]any{
		"generation":            view.Generation,
		"keys":                  view.Keys,
		"naming_pattern_tokens": view.NamingPatternTokens,
		"applied":               nonNil(res.Applied),
		"restart_pending":       nonNil(res.RestartPending),
	}
	addOps(body, res.Ops)
	httputil.RespondWithOK(c, body)
}

// addOps copies each operation id into body under its response key. A key the
// response already uses is skipped, so a hook cannot overwrite the View.
func addOps(body map[string]any, ops map[string]string) {
	for k, id := range ops {
		if _, taken := body[k]; !taken && id != "" {
			body[k] = id
		}
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// decodeConfigUpdate reads and checks the body. A body that names no key, an
// unknown top-level field (the old flat shape would land here), a key twice,
// or an empty key is a 400; it writes the response itself. A body that names no
// key at all is valid: an empty patch, answered with the current View.
func decodeConfigUpdate(c *gin.Context) (config.Update, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxConfigBodyBytes))
	if err != nil {
		httputil.RespondWithBadRequest(c, "could not read the request body: "+err.Error())
		return config.Update{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body configUpdateBody
	if err := dec.Decode(&body); err != nil {
		httputil.RespondWithBadRequest(c, `the body must be {"set": {<key>: <value>}, "unset": [<key>]}: `+err.Error())
		return config.Update{}, false
	}
	if dec.More() {
		httputil.RespondWithBadRequest(c, "the body must be one JSON object")
		return config.Update{}, false
	}
	for name := range body.Set {
		if strings.TrimSpace(name) == "" {
			httputil.RespondWithBadRequest(c, `"set" has an empty key name`)
			return config.Update{}, false
		}
	}
	seen := make(map[string]struct{}, len(body.Unset))
	for _, name := range body.Unset {
		if strings.TrimSpace(name) == "" {
			httputil.RespondWithBadRequest(c, `"unset" has an empty key name`)
			return config.Update{}, false
		}
		if _, dup := seen[name]; dup {
			httputil.RespondWithBadRequest(c, fmt.Sprintf(`"unset" names %q twice`, name))
			return config.Update{}, false
		}
		seen[name] = struct{}{}
	}
	return config.Update{Set: body.Set, Unset: body.Unset}, true
}

// configActor fills the Actor from the request: who is writing (Subject is
// HEAD's user ID), whether the session may change credentials, whether the
// request proves the owner, and what the principal may do.
func (h *Handler) configActor(c *gin.Context, f config.Facade) config.Actor {
	a := config.Actor{
		Interactive: servermiddleware.CurrentAuthMethod(c).MayChangeCredentials(),
		Can:         h.configCan(c),
	}
	if u, ok := servermiddleware.CurrentUser(c); ok {
		a.Subject = u.ID
	}
	// The owner proof is the one every owner action uses: a verified
	// Cloudflare Access sign-in as the configured owner. No owner set means
	// no proof (OwnerProofWhyNot fails closed on an empty address).
	a.Owner = auth.OwnerProofWhyNot(c.Request.Context(), f.Snapshot().OwnerEmail, "") == ""
	return a
}

// configCan is Actor.Can for this request. core:admin is HEAD's admin role,
// held by the credential too (RequireAdmin's check: an admin's API key whose
// scopes leave out an atom does not act as admin). Any other permission is
// held when the principal holds an atom whose native scopes include it.
func (h *Handler) configCan(c *gin.Context) func(string) bool {
	return func(perm string) bool {
		// Under core identity (F4-08 B7) every answer is the principal stage 12
		// bound: core:admin is the permission itself (key scoping applied),
		// any other is held when the principal holds an atom whose native
		// scopes include it, and there is no first-run grant: it read books'
		// own user count, which is not the account record once core owns
		// identity.
		if id, bridged := legacyauth.IdentityFrom(c.Request.Context()); bridged {
			return h.configCanBridged(c, id.Bridge, perm)
		}
		if perm == config.FirstRunPerm {
			return h.configFirstRun != nil && h.configFirstRun()
		}
		if perm == "core:admin" {
			u, ok := servermiddleware.CurrentUser(c)
			return ok && slices.Contains(u.Roles, "admin") && servermiddleware.APIKeyMayActAsAdmin(c)
		}
		if h.configAtoms == nil {
			return false
		}
		ctx := c.Request.Context()
		for _, atom := range h.configAtoms(perm) {
			if auth.Can(ctx, atom) {
				return true
			}
		}
		return false
	}
}

// configCanBridged is configCan under core identity. core:admin keeps the
// legacy branch's whole test, HEAD's admin-role test as every other admin
// site writes it (handlers/user.go inviteMarkerMayBind): the active caller
// holds the admin role by name and its credential may act as admin
// (APIKeyMayActAsAdmin, through the bridge), and under core identity the
// principal must also hold core:admin (AdminHeld). Holding core:admin alone,
// as through a custom role granted it after the cutover, is not HEAD's admin
// (F4-08 B10y, item 5).
func (h *Handler) configCanBridged(c *gin.Context, b *legacyauth.Bridge, perm string) bool {
	ctx := c.Request.Context()
	switch {
	case perm == config.FirstRunPerm:
		return false
	case perm == "core:admin":
		u, ok := servermiddleware.CurrentUser(c)
		return ok && u != nil && u.Status == "active" && slices.Contains(u.Roles, "admin") &&
			servermiddleware.APIKeyMayActAsAdmin(c) && b.AdminHeld(ctx)
	case h.configAtoms == nil:
		return false
	}
	for _, atom := range h.configAtoms(perm) {
		if b.Can(ctx, string(atom)) {
			return true
		}
	}
	return false
}

// respondConfigUpdateError maps the Facade's error to a status and the JSON
// envelope. The statuses are the ones the update service answered with for the
// same refusal: 400 for a key or value the request got wrong or may not name,
// 403 for a missing permission or credential, 500 for a write that failed. The
// two cases the update service never had are a key an operator layer sets
// (409, the request is fine but the key's state refuses it) and a value that
// failed validation (422, with each field named).
func (h *Handler) respondConfigUpdateError(c *gin.Context, err error, actor config.Actor) {
	var (
		locked    *coreconfig.LockedError
		protected *coreconfig.ProtectedError
	)
	switch {
	case errors.Is(err, config.ErrKeyReadOnly):
		// An env-only key (class o): same 400 the update service gave an
		// environment-only setting.
		httputil.RespondWithError(c, http.StatusBadRequest, err.Error(), configCodeError)
	case errors.Is(err, coreconfig.ErrUnknownKey):
		// The refused names are in the error's text, which the Facade writes
		// with HEAD's names; the handler does not parse prose back into a list.
		httputil.RespondWithError(c, http.StatusBadRequest, err.Error(), configCodeError)
	case errors.As(err, &protected):
		h.respondProtected(c, err, protected, actor)
	case errors.Is(err, coreconfig.ErrForbidden):
		httputil.RespondWithError(c, http.StatusForbidden, err.Error(), configCodeForbidden)
	case errors.As(err, &locked):
		httputil.RespondWithErrorFields(c, http.StatusConflict, err.Error(), configCodeLocked,
			map[string]any{"locked_keys": []string{string(locked.Path)}, "locked_by": locked.By})
	case errors.Is(err, coreconfig.ErrInvalid):
		fields := configFieldErrors(err)
		httputil.RespondWithErrorFields(c, http.StatusUnprocessableEntity, invalidMessage(fields), configCodeInvalid,
			map[string]any{"fields": fields})
	default:
		configHandlerLog.Error("config update failed: %s", logger.SanitizeLogErr(err))
		httputil.RespondWithError(c, http.StatusInternalServerError, "failed to save configuration", configCodeError)
	}
}

// respondProtected answers a key whose protection the caller does not meet,
// naming every refused key (core joins one error per key; first is the one
// whose protection words the message). A caller that may not change
// credentials at all (an API key) is told so, in the wording and with the
// audit line every credential refusal uses, whether the key needed a session
// or the admin role. An interactive caller short of the admin role is told
// that; an owner refusal says why the proof failed; an operator-only key points
// at the host.
func (h *Handler) respondProtected(c *gin.Context, err error, first *coreconfig.ProtectedError, actor config.Actor) {
	msg := first.Error()
	switch {
	case first.Need == coreconfig.ProtectOwner:
		msg = "the owner's sign-in is required to change " + string(first.Path)
		if f := h.facade(); f != nil {
			if why := auth.OwnerProofWhyNot(c.Request.Context(), f.Snapshot().OwnerEmail, ""); why != "" {
				msg = why + " (" + string(first.Path) + ")"
			}
		}
	case first.Need == coreconfig.ProtectOperator:
		msg = string(first.Path) + " can be set only on the server: in the host environment or config file, then restart"
	case !actor.Interactive:
		servermiddleware.LogCredentialChangeRefusal(c)
		msg = auth.CredentialChangeRefusedMessage
	case first.Need == coreconfig.ProtectInteractive:
		servermiddleware.LogCredentialChangeRefusal(c)
		msg = auth.CredentialChangeRefusedMessage
	case first.Need == coreconfig.ProtectAdmin:
		msg = "the admin role is required to change " + string(first.Path)
	}
	var keys []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if pe, ok := e.(*coreconfig.ProtectedError); ok {
			keys = append(keys, string(pe.Path))
			return
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, w := range u.Unwrap() {
				walk(w)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	if len(keys) == 0 {
		keys = []string{string(first.Path)}
	}
	httputil.RespondWithErrorFields(c, http.StatusForbidden, msg, configCodeForbidden,
		map[string]any{"refused_keys": keys})
}

// configFieldError is one entry of a 422's "fields": the key, in HEAD's name,
// and why its value was refused.
type configFieldError struct {
	Key   string `json:"key"`
	Error string `json:"error"`
}

// configFieldErrors lists every *FieldError an ErrInvalid joins. errors.As
// finds only the first, so the join is walked: a PUT of two bad values answers
// with both. An invalid error that carries none is reported once, whole.
func configFieldErrors(err error) []configFieldError {
	var out []configFieldError
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		var fe *coreconfig.FieldError
		if errors.As(e, &fe) && e == error(fe) {
			msg := "invalid value"
			if fe.Err != nil {
				msg = fe.Err.Error()
			}
			out = append(out, configFieldError{Key: string(fe.Path), Error: msg})
			return
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	if len(out) == 0 {
		out = append(out, configFieldError{Error: err.Error()})
	}
	return out
}

func invalidMessage(fields []configFieldError) string {
	if len(fields) == 1 {
		if fields[0].Key == "" {
			return fields[0].Error + " — nothing was saved"
		}
		return fields[0].Key + ": " + fields[0].Error + " — nothing was saved"
	}
	keys := make([]string, 0, len(fields))
	for _, f := range fields {
		keys = append(keys, f.Key)
	}
	return fmt.Sprintf("%d settings are invalid (%s) — nothing was saved", len(fields), strings.Join(keys, ", "))
}
