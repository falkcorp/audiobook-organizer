// file: internal/server/handlers/system/config_handler_test.go
// version: 1.1.0
// guid: 9c2d7e41-0b58-4f36-8a1d-3e6f5a90c274
// last-edited: 2026-11-19

package system_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// stubFacade is a config.Facade that records what the handler asks of it and
// answers with what the test set. Only View, Update and Snapshot are reached
// by the settings routes; the rest panic so a stray call fails loudly.
type stubFacade struct {
	config.Facade // nil: any method the routes should not call panics

	mu       sync.Mutex
	view     config.View
	owner    string
	updates  []config.Update
	actors   []config.Actor
	viewHits int
	result   config.UpdateResult
	err      error
}

func (s *stubFacade) View() config.View {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewHits++
	return s.view
}

func (s *stubFacade) Snapshot() config.Config { return config.Config{OwnerEmail: s.owner} }

func (s *stubFacade) Update(_ context.Context, a config.Actor, u config.Update) (config.UpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, u)
	s.actors = append(s.actors, a)
	return s.result, s.err
}

// configHandler builds a handler whose Facade is the stub, with an optional
// request-shaping middleware (a signed-in user, an auth method).
func configHandler(t *testing.T, f *stubFacade, pre ...gin.HandlerFunc) func(method, body string) (int, map[string]any) {
	t.Helper()
	h, _ := newTestHandler(t)
	h.WithConfigFacade(func() config.Facade {
		if f == nil {
			return nil
		}
		return f
	})
	return func(method, body string) (int, map[string]any) {
		var raw []byte
		if body != "" {
			raw = []byte(body)
		}
		w := run(method, "/config", "/config", raw, func(r *gin.Engine) {
			for _, m := range pre {
				r.Use(m)
			}
			r.GET("/config", h.GetConfig)
			r.PUT("/config", h.UpdateConfig)
		})
		var out map[string]any
		if w.Body.Len() > 0 {
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		}
		return w.Code, out
	}
}

func data(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	d, ok := body["data"].(map[string]any)
	require.True(t, ok, "no data envelope in %v", body)
	return d
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// --- GET ---

// A secret is write-only: the response carries its metadata and never a
// value or a mask, whatever the Facade's view held.
func TestGetConfig_SecretIsMetadataOnly(t *testing.T) {
	const plaintext = "batarang-sentinel-7f3a"
	when := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	f := &stubFacade{view: config.View{Generation: 7, Keys: []config.KeyView{
		{Name: "openai_api_key", Class: config.KeyClassSecret,
			// A Facade that leaked would put the plaintext here; the handler
			// must not be the layer that repairs that, but it must not add one.
			Secret: &config.SecretMeta{Set: true, Source: "override", UpdatedAt: when, Fingerprint: "ab12cd34"}},
		{Name: "scan_on_startup", Class: config.KeyClassKey, Value: raw(`true`)},
	}}}
	code, body := configHandler(t, f)(http.MethodGet, "")
	require.Equal(t, http.StatusOK, code)
	d := data(t, body)
	assert.EqualValues(t, 7, d["generation"])
	keys := d["keys"].([]any)
	require.Len(t, keys, 2)

	secret := keys[0].(map[string]any)
	assert.Equal(t, "openai_api_key", secret["name"])
	assert.Equal(t, "s", secret["class"])
	assert.NotContains(t, secret, "value", "a secret has no value on the wire")
	meta := secret["secret"].(map[string]any)
	assert.Equal(t, true, meta["set"])
	assert.Equal(t, "override", meta["source"])
	assert.Equal(t, "ab12cd34", meta["fingerprint"])
	assert.Equal(t, when.Format(time.RFC3339), meta["updated_at"])

	enc, err := json.Marshal(body)
	require.NoError(t, err)
	assert.NotContains(t, string(enc), plaintext)
	assert.NotContains(t, string(enc), "****", "no mask of any shape")

	plain := keys[1].(map[string]any)
	assert.Equal(t, true, plain["value"])
	assert.NotContains(t, plain, "secret")
}

func TestGetConfig_ShowsLocksClassesAndRestartPending(t *testing.T) {
	f := &stubFacade{view: config.View{Keys: []config.KeyView{
		{Name: "log_level", Class: config.KeyClassKey, Value: raw(`"warn"`), LockedBy: "env ABO_CORE_LOG_LEVEL"},
		{Name: "database_path", Class: config.KeyClassEnv, Value: raw(`"/srv/cave.db"`)},
		{Name: "listen_port", Class: config.KeyClassKey, Value: raw(`8080`), RestartPending: true, Admin: true},
	}}}
	_, body := configHandler(t, f)(http.MethodGet, "")
	keys := data(t, body)["keys"].([]any)
	byName := map[string]map[string]any{}
	for _, k := range keys {
		m := k.(map[string]any)
		byName[m["name"].(string)] = m
	}
	assert.Equal(t, "env ABO_CORE_LOG_LEVEL", byName["log_level"]["locked_by"])
	assert.Equal(t, "o", byName["database_path"]["class"], "an env-only key is shown as class o")
	assert.Equal(t, true, byName["listen_port"]["restart_pending"])
	assert.Equal(t, true, byName["listen_port"]["admin"])
	assert.NotContains(t, byName["scan_on_startup"], "locked_by")
}

func TestGetConfig_KeysIsAnArrayAndCarriesNamingTokens(t *testing.T) {
	f := &stubFacade{view: config.View{Generation: 1}} // Keys nil
	_, body := configHandler(t, f)(http.MethodGet, "")
	d := data(t, body)
	keys, ok := d["keys"].([]any)
	require.True(t, ok, "keys must encode as [] not null, got %T", d["keys"])
	assert.Empty(t, keys)
	tokens, ok := d["naming_pattern_tokens"].([]any)
	require.True(t, ok)
	assert.NotEmpty(t, tokens)
}

func TestConfigRoutes_Answer503WithoutAFacade(t *testing.T) {
	call := configHandler(t, nil)
	code, _ := call(http.MethodGet, "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	code, _ = call(http.MethodPut, `{"set":{"scan_on_startup":true}}`)
	assert.Equal(t, http.StatusServiceUnavailable, code)
}

// --- PUT: request shape ---

func TestUpdateConfig_BodyIsOneUpdate(t *testing.T) {
	const plaintext = "batarang-sentinel-7f3a"
	f := &stubFacade{
		view: config.View{Generation: 9, Keys: []config.KeyView{{Name: "scan_on_startup", Class: config.KeyClassKey, Value: raw(`false`)}}},
		result: config.UpdateResult{Generation: 9, Applied: []string{"scan_on_startup", "openai_api_key", "smtp_password"}, RestartPending: []string{"listen_port"},
			Ops: map[string]string{"dedup_rescore_op_id": "op-rescore-1", "keys": "must-not-replace-the-view", "": "ignored"}},
	}
	code, body := configHandler(t, f)(http.MethodPut,
		`{"set":{"scan_on_startup":false,"openai_api_key":"`+plaintext+`","listen_port":8081},"unset":["smtp_password"]}`)
	require.Equal(t, http.StatusOK, code, "%v", body)

	require.Len(t, f.updates, 1, "all keys go to the Facade in one Update, never one call per key")
	u := f.updates[0]
	assert.Len(t, u.Set, 3)
	assert.JSONEq(t, `false`, string(u.Set["scan_on_startup"]))
	assert.JSONEq(t, `"`+plaintext+`"`, string(u.Set["openai_api_key"]), "a secret is sent as plaintext, unmodified")
	assert.JSONEq(t, `8081`, string(u.Set["listen_port"]))
	assert.Equal(t, []string{"smtp_password"}, u.Unset)

	d := data(t, body)
	assert.ElementsMatch(t, []any{"scan_on_startup", "openai_api_key", "smtp_password"}, d["applied"].([]any))
	assert.Equal(t, []any{"listen_port"}, d["restart_pending"])
	assert.EqualValues(t, 9, d["generation"])
	assert.Len(t, d["keys"].([]any), 1, "the response is the fresh View, and an op key cannot overwrite it")
	assert.Equal(t, "op-rescore-1", d["dedup_rescore_op_id"], "an operation a hook started is named in the response under its own key")
	enc, _ := json.Marshal(body)
	assert.NotContains(t, string(enc), plaintext, "the plaintext is never echoed")
}

func TestUpdateConfig_RejectsMalformedBodies(t *testing.T) {
	cases := map[string]string{
		"the old flat body":      `{"scan_on_startup":true}`,
		"empty object":           `{}`,
		"empty set and unset":    `{"set":{},"unset":[]}`,
		"set is not an object":   `{"set":["scan_on_startup"]}`,
		"empty set key":          `{"set":{"":1}}`,
		"blank set key":          `{"set":{"  ":1}}`,
		"empty unset key":        `{"unset":[""]}`,
		"key unset twice":        `{"unset":["smtp_password","smtp_password"]}`,
		"two json documents":     `{"set":{"a":1}} {"set":{"b":2}}`,
		"not json":               `set scan_on_startup`,
		"unknown top-level key ": `{"set":{"a":1},"force":true}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := &stubFacade{}
			code, resp := configHandler(t, f)(http.MethodPut, body)
			assert.Equal(t, http.StatusBadRequest, code)
			assert.NotEmpty(t, resp["error"])
			assert.Empty(t, f.updates, "a malformed body reaches the Facade as nothing")
		})
	}
}

// --- PUT: error mapping ---

func TestUpdateConfig_ErrorMapping(t *testing.T) {
	const put = `{"set":{"scan_on_startup":true}}`
	cases := []struct {
		name       string
		err        error
		status     int
		code       string
		wantFields map[string]any
	}{
		{"read-only env key", fmt.Errorf("%w", config.ErrKeyReadOnly), http.StatusBadRequest, "CONFIG_ERROR", nil},
		{"unknown key", fmt.Errorf("%w: openai_key", coreconfig.ErrUnknownKey), http.StatusBadRequest, "CONFIG_ERROR", nil},
		{"missing section permission", fmt.Errorf("%w", coreconfig.ErrForbidden), http.StatusForbidden, "FORBIDDEN", nil},
		{"operator lock", &coreconfig.LockedError{Path: "log_level", By: "env ABO_CORE_LOG_LEVEL"}, http.StatusConflict, "CONFIG_LOCKED",
			map[string]any{"locked_by": "env ABO_CORE_LOG_LEVEL"}},
		{"protected: interactive", &coreconfig.ProtectedError{Path: "database_path", Need: coreconfig.ProtectInteractive}, http.StatusForbidden, "FORBIDDEN",
			map[string]any{"refused_keys": []any{"database_path"}}},
		{"protected: admin", &coreconfig.ProtectedError{Path: "listen_port", Need: coreconfig.ProtectAdmin}, http.StatusForbidden, "FORBIDDEN",
			map[string]any{"refused_keys": []any{"listen_port"}}},
		{"protected: owner", &coreconfig.ProtectedError{Path: "owner_email", Need: coreconfig.ProtectOwner}, http.StatusForbidden, "FORBIDDEN",
			map[string]any{"refused_keys": []any{"owner_email"}}},
		{"anything else", errors.New("pebble: disk gremlins at /var/lib/cave"), http.StatusInternalServerError, "CONFIG_ERROR", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &stubFacade{err: tc.err, view: config.View{Keys: []config.KeyView{{Name: "must-not-be-read"}}}}
			code, body := configHandler(t, f)(http.MethodPut, put)
			assert.Equal(t, tc.status, code)
			assert.Equal(t, tc.code, body["code"])
			assert.EqualValues(t, tc.status, body["status"])
			assert.NotEmpty(t, body["error"])
			for k, v := range tc.wantFields {
				assert.Equal(t, v, body[k], k)
			}
			assert.Zero(t, f.viewHits, "a refused write does not render a view: nothing changed")
			assert.NotContains(t, body, "data")
		})
	}
}

func TestUpdateConfig_ProtectedMessages(t *testing.T) {
	put := func(need coreconfig.Protection) string {
		f := &stubFacade{err: &coreconfig.ProtectedError{Path: "database_path", Need: need}}
		_, body := configHandler(t, f, asUser("user-ace", []string{"editor"}, auth.MethodSession, ""))(http.MethodPut, `{"set":{"database_path":"/srv/cave.db"}}`)
		return body["error"].(string)
	}
	assert.Equal(t, auth.CredentialChangeRefusedMessage, put(coreconfig.ProtectInteractive),
		"the credential wording every credential refusal uses")
	assert.Contains(t, put(coreconfig.ProtectAdmin), "admin role")
	assert.Contains(t, put(coreconfig.ProtectOwner), "owner")
}

// The refusal says what the caller can act on. A caller that may not change
// credentials at all (an API key, an ABS token) gets the credential-change
// wording for any key that needs a session or the admin role, as the update
// service gave it; an interactive caller who merely lacks the admin role is
// told that.
func TestUpdateConfig_NonInteractiveCallerGetsTheCredentialWording(t *testing.T) {
	put := func(need coreconfig.Protection, pre gin.HandlerFunc) string {
		f := &stubFacade{err: &coreconfig.ProtectedError{Path: "root_dir", Need: need}}
		code, body := configHandler(t, f, pre)(http.MethodPut, `{"set":{"root_dir":"/srv/cave"}}`)
		require.Equal(t, http.StatusForbidden, code)
		return body["error"].(string)
	}
	key := asUser("user-alfred", []string{"admin"}, auth.MethodAPIKey, "")
	session := asUser("user-ace", []string{"editor"}, auth.MethodSession, "")
	assert.Equal(t, auth.CredentialChangeRefusedMessage, put(coreconfig.ProtectAdmin, key))
	assert.Equal(t, auth.CredentialChangeRefusedMessage, put(coreconfig.ProtectInteractive, key))
	assert.Contains(t, put(coreconfig.ProtectAdmin, session), "admin role")
	assert.NotEqual(t, auth.CredentialChangeRefusedMessage, put(coreconfig.ProtectAdmin, session))
}

func TestUpdateConfig_OperatorOnlyKeyPointsAtTheHost(t *testing.T) {
	f := &stubFacade{err: &coreconfig.ProtectedError{Path: "owner_email", Need: coreconfig.ProtectOperator}}
	code, body := configHandler(t, f, asUser("user-robin", []string{"admin"}, auth.MethodSession, ""))(http.MethodPut, `{"set":{"owner_email":"x@wayne.example"}}`)
	require.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body["error"], "only on the server")
	assert.Contains(t, body["error"], "owner_email")
}

// Every refused key is named, not the first of a join.
func TestUpdateConfig_ProtectedNamesEveryKey(t *testing.T) {
	err := errors.Join(
		&coreconfig.ProtectedError{Path: "root_dir", Need: coreconfig.ProtectAdmin},
		&coreconfig.ProtectedError{Path: "enable_auth", Need: coreconfig.ProtectAdmin},
	)
	f := &stubFacade{err: err}
	code, body := configHandler(t, f)(http.MethodPut, `{"set":{"root_dir":"/a","enable_auth":true}}`)
	require.Equal(t, http.StatusForbidden, code)
	assert.ElementsMatch(t, []any{"root_dir", "enable_auth"}, body["refused_keys"].([]any))
}

func TestUpdateConfig_OwnerRefusalSaysWhyTheProofFailed(t *testing.T) {
	f := &stubFacade{owner: "bruce.wayne@wayne.example", err: &coreconfig.ProtectedError{Path: "owner_email", Need: coreconfig.ProtectOwner}}
	_, body := configHandler(t, f)(http.MethodPut, `{"set":{"owner_email":"x@wayne.example"}}`)
	assert.Contains(t, body["error"], "Cloudflare Access", "the owner proof's own explanation reaches the page")
	assert.Contains(t, body["error"], "owner_email")
}

func TestUpdateConfig_InvalidNamesEveryField(t *testing.T) {
	err := fmt.Errorf("%w: %w", coreconfig.ErrInvalid, errors.Join(
		&coreconfig.FieldError{Path: "listen_port", Err: errors.New("must be 1-65535")},
		&coreconfig.FieldError{Path: "log_level", Err: errors.New(`"loud" is not one of debug, info, warn, error`)},
	))
	f := &stubFacade{err: err}
	code, body := configHandler(t, f)(http.MethodPut, `{"set":{"listen_port":0,"log_level":"loud"}}`)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "CONFIG_INVALID", body["code"])
	fields := body["fields"].([]any)
	require.Len(t, fields, 2, "errors.As would have found one; both are reported")
	got := map[string]string{}
	for _, e := range fields {
		m := e.(map[string]any)
		got[m["key"].(string)] = m["error"].(string)
	}
	assert.Equal(t, "must be 1-65535", got["listen_port"])
	assert.Contains(t, got["log_level"], "not one of")
	assert.Contains(t, body["error"], "nothing was saved")
}

func TestUpdateConfig_InvalidWithOneFieldNamesTheKey(t *testing.T) {
	err := fmt.Errorf("%w: %w", coreconfig.ErrInvalid, errors.Join(&coreconfig.FieldError{Path: "listen_port", Err: errors.New("too high")}))
	code, body := configHandler(t, &stubFacade{err: err})(http.MethodPut, `{"set":{"listen_port":99999}}`)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Contains(t, body["error"], "listen_port")
	assert.Len(t, body["fields"], 1)
}

func TestUpdateConfig_InvalidWithoutFieldErrorsIsReportedWhole(t *testing.T) {
	err := fmt.Errorf("%w: cross-field rule broke", coreconfig.ErrInvalid)
	code, body := configHandler(t, &stubFacade{err: err})(http.MethodPut, `{"set":{"a":1}}`)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	fields := body["fields"].([]any)
	require.Len(t, fields, 1)
	assert.Contains(t, fields[0].(map[string]any)["error"], "cross-field rule broke")
}

func TestUpdateConfig_InternalErrorDoesNotLeakItsText(t *testing.T) {
	f := &stubFacade{err: errors.New("pebble: disk gremlins at /var/lib/cave")}
	code, body := configHandler(t, f)(http.MethodPut, `{"set":{"a":1}}`)
	require.Equal(t, http.StatusInternalServerError, code)
	assert.NotContains(t, fmt.Sprint(body), "gremlins")
}

// The write committed and an after-commit hook failed: nothing is rolled back
// and the caller is told the change is in effect.
func TestUpdateConfig_PostCommitFailureIsSavedButReported(t *testing.T) {
	f := &stubFacade{result: config.UpdateResult{
		Applied:    []string{"dedup.signals"},
		Ops:        map[string]string{"dedup_rescore_op_id": "op-rescore-2"},
		PostCommit: &coreconfig.PostCommitError{Section: "modules.books.dedup", Err: errors.New("engine hand-off failed")},
	}}
	code, body := configHandler(t, f)(http.MethodPut, `{"set":{"dedup.signals":{}}}`)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Equal(t, true, body["saved"])
	assert.Equal(t, []any{"dedup.signals"}, body["applied"])
	assert.Equal(t, "op-rescore-2", body["dedup_rescore_op_id"], "an operation that did start is still named")
	assert.Contains(t, body["error"], "saved and is in effect")
}

// --- Actor ---

func asUser(id string, roles []string, method auth.Method, email string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := auth.WithMethod(c.Request.Context(), method)
		if email != "" {
			ctx = auth.WithAccessEmail(ctx, email)
		}
		c.Request = c.Request.WithContext(ctx)
		if id != "" {
			c.Set("auth_user", &database.User{ID: id, Roles: roles})
		}
		c.Next()
	}
}

func actorFor(t *testing.T, f *stubFacade, pre ...gin.HandlerFunc) config.Actor {
	t.Helper()
	code, body := configHandler(t, f, pre...)(http.MethodPut, `{"set":{"a":1}}`)
	require.Equal(t, http.StatusOK, code, "%v", body)
	require.Len(t, f.actors, 1)
	return f.actors[0]
}

func TestUpdateConfig_ActorFromTheRequest(t *testing.T) {
	t.Run("an interactive admin session", func(t *testing.T) {
		a := actorFor(t, &stubFacade{}, asUser("user-robin", []string{"admin"}, auth.MethodSession, ""))
		assert.Equal(t, "user-robin", a.Subject)
		assert.True(t, a.Interactive)
		assert.False(t, a.Owner)
		require.NotNil(t, a.Can)
		assert.True(t, a.Can("core:admin"))
	})
	t.Run("an API key may not change credentials", func(t *testing.T) {
		a := actorFor(t, &stubFacade{}, asUser("user-alfred", []string{"admin"}, auth.MethodAPIKey, ""))
		assert.Equal(t, "user-alfred", a.Subject)
		assert.False(t, a.Interactive)
		assert.False(t, a.Can("core:admin"), "an admin's API key without every scope does not act as admin")
	})
	t.Run("a delegated session may", func(t *testing.T) {
		a := actorFor(t, &stubFacade{}, asUser("user-ace", nil, auth.MethodSessionDelegated, ""))
		assert.True(t, a.Interactive)
	})
	t.Run("an editor is no admin", func(t *testing.T) {
		a := actorFor(t, &stubFacade{}, asUser("user-ace", []string{"editor"}, auth.MethodSession, ""))
		assert.False(t, a.Can("core:admin"))
	})
	t.Run("no principal at all holds nothing", func(t *testing.T) {
		a := actorFor(t, &stubFacade{})
		assert.Empty(t, a.Subject)
		assert.False(t, a.Interactive, "auth off: spec 1.3.7, HEAD's exemption ends at F3")
		assert.False(t, a.Owner)
		assert.False(t, a.Can("core:admin"))
		assert.False(t, a.Can("core:settings.manage"))
	})
}

func TestUpdateConfig_ActorOwnerProof(t *testing.T) {
	const owner = "bruce.wayne@wayne.example"
	t.Run("verified Cloudflare Access sign-in as the owner", func(t *testing.T) {
		a := actorFor(t, &stubFacade{owner: owner}, asUser("user-bruce", []string{"admin"}, auth.MethodCFAccess, owner))
		assert.True(t, a.Owner)
	})
	t.Run("someone else's Access sign-in", func(t *testing.T) {
		a := actorFor(t, &stubFacade{owner: owner}, asUser("user-selina", []string{"admin"}, auth.MethodCFAccess, "selina.kyle@wayne.example"))
		assert.False(t, a.Owner)
	})
	t.Run("the owner's password session is not the proof", func(t *testing.T) {
		a := actorFor(t, &stubFacade{owner: owner}, asUser("user-bruce", []string{"admin"}, auth.MethodSession, owner))
		assert.False(t, a.Owner)
	})
	t.Run("no owner configured proves nobody", func(t *testing.T) {
		a := actorFor(t, &stubFacade{}, asUser("user-bruce", []string{"admin"}, auth.MethodCFAccess, owner))
		assert.False(t, a.Owner)
	})
}

// A section permission is held when the principal holds an atom whose native
// scopes include it; with no lookup installed the handler holds none (fail
// closed).
func TestUpdateConfig_ActorCanThroughTheAtomLookup(t *testing.T) {
	atoms := func(perm string) []string {
		if perm == "core:settings.manage" {
			return []string{"settings.manage"}
		}
		return nil
	}
	asSettingsManager := func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), []auth.Permission{"settings.manage"}))
		c.Next()
	}
	canFor := func(withLookup bool, perm string, pre ...gin.HandlerFunc) bool {
		f := &stubFacade{}
		h, _ := newTestHandler(t)
		h.WithConfigFacade(func() config.Facade { return f })
		if withLookup {
			h.WithConfigAtoms(atoms)
		}
		w := run(http.MethodPut, "/config", "/config", []byte(`{"set":{"a":1}}`), func(r *gin.Engine) {
			for _, m := range pre {
				r.Use(m)
			}
			r.PUT("/config", h.UpdateConfig)
		})
		require.Equal(t, http.StatusOK, w.Code)
		return f.actors[0].Can(perm)
	}
	assert.True(t, canFor(true, "core:settings.manage", asSettingsManager))
	assert.False(t, canFor(true, "core:settings.manage"), "no atom held")
	assert.False(t, canFor(true, "core:tasks.manage", asSettingsManager), "an atom that does not carry the scope")
	assert.False(t, canFor(false, "core:settings.manage", asSettingsManager), "no lookup installed: fails closed")
}

// The Facade provider is read at request time, so one installed after the
// handlers were wired is the one used.
func TestConfigFacadeIsResolvedPerRequest(t *testing.T) {
	var cur config.Facade
	h, _ := newTestHandler(t)
	h.WithConfigFacade(func() config.Facade { return cur })
	get := func() int {
		return run(http.MethodGet, "/config", "/config", nil, func(r *gin.Engine) { r.GET("/config", h.GetConfig) }).Code
	}
	assert.Equal(t, http.StatusServiceUnavailable, get())
	cur = &stubFacade{}
	assert.Equal(t, http.StatusOK, get())
}

// Unset provider means config.Current: with the stub installed process-wide the
// handler finds it. config.Install is process-global, so no t.Parallel here.
func TestConfigFacadeDefaultsToCurrent(t *testing.T) {
	prev := config.Install(&stubFacade{view: config.View{Generation: 41}})
	t.Cleanup(func() { config.Install(prev) })
	h, _ := newTestHandler(t)
	w := run(http.MethodGet, "/config", "/config", nil, func(r *gin.Engine) { r.GET("/config", h.GetConfig) })
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.EqualValues(t, 41, data(t, body)["generation"])
}
