// file: core/integrations/secret_write_test.go
// version: 0.1.0
// guid: c8128d08-6fa5-4421-8cb0-5de241b45cf5
// last-edited: 2026-11-28

package integrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/secrets"
)

// The secret-write rule (F7-09 W-2): a Put that stores or clears a secret
// field needs the ProtectAdmin check, as HEAD's config write does for every
// class-s key (appconfig/write.go; TestAPIKeyIsRefusedWhereASessionIsNot).
// core:integrations.manage alone, an API key, and the auth-disabled
// principal do not pass it. A write that touches no secret is not held to
// it (SEC2-7), and Test sends body secrets without storing them.

// noRowNoSecret fails t when a refused write left a row, a secret or an audit
// record behind.
func noRowNoSecret(t *testing.T, e *env) {
	t.Helper()
	if _, ok := e.stored(t, signalRef); ok {
		t.Error("the refused Put wrote a row")
	}
	if got := e.secret(t, signalRef, "password"); got != "" {
		t.Errorf("the refused Put stored a secret (%d bytes)", len(got))
	}
	if a := e.actions(t); len(a) != 0 {
		t.Errorf("the refused Put left audit records: %v", a)
	}
}

// Cell 1: an API key is refused a secret write even when it holds
// core:admin, and so is the clearing of a stored secret.
func TestPut_SecretWriteRefusedForAdminAPIKey(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	_, err := r.Put(apiKey(), signal("http://192.0.2.20/"), cred("alfred-1"))
	if !errors.Is(err, integrations.ErrForbidden) {
		t.Fatalf("an admin API key storing a secret: err = %v, want ErrForbidden", err)
	}
	noRowNoSecret(t, e)

	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	clear := current(t, r, signalRef)
	clear.Settings = map[string]any{"channel": "gcpd", "url": "http://192.0.2.20/", "password": nil}
	if _, err := r.Put(apiKey(), clear, nil); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("an admin API key clearing a secret: err = %v, want ErrForbidden", err)
	}
	if e.secret(t, signalRef, "password") != "alfred-1" {
		t.Error("the refused clear dropped the stored secret")
	}
}

// Cell 2: an interactive admin session stores a secret. A delegated admin
// session does too, because core's ProtectAdmin counts it (the owner's
// 2026-11-06 amendment) and HEAD's Actor.Interactive does the same.
func TestPut_SecretWriteAcceptedForInteractiveAdmin(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if got := e.secret(t, signalRef, "password"); got != "alfred-1" {
		t.Fatalf("the admin's secret was not stored (%d bytes)", len(got))
	}
	delegated := integrationstest.As(context.Background(), auth.MethodSessionDelegated, integrationstest.Manage, integrationstest.Admin)
	mustPut(t, delegated, r, current(t, r, signalRef), cred("alfred-2"))
	if got := e.secret(t, signalRef, "password"); got != "alfred-2" {
		t.Errorf("a delegated admin session's secret was not stored (%d bytes)", len(got))
	}
}

// Cell 3: the same admin API key still writes a non-secret field, and
// keeps a stored secret across the write (a ref or an empty value is no
// write).
func TestPut_NonSecretWriteAcceptedForAdminAPIKey(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	got := mustPut(t, apiKey(), r, signal("http://192.0.2.20/"), nil)
	if got.Origin != integrations.OriginAPI {
		t.Errorf("the key's write = %+v", got)
	}
	mustPut(t, admin(), r, current(t, r, signalRef), cred("alfred-1"))

	renamed := current(t, r, signalRef)
	renamed.Name = "renamed by a key"
	renamed.Settings = map[string]any{"channel": "arkham", "url": "http://192.0.2.20/", "password": secrets.RefTo("integrations.notify.gcpd-roof.password")}
	got = mustPut(t, apiKey(), r, renamed, nil)
	if got.Name != "renamed by a key" || got.Settings["channel"] != "arkham" {
		t.Errorf("the key's non-secret write = %+v", got)
	}
	if e.secret(t, signalRef, "password") != "alfred-1" {
		t.Error("a non-secret write by a key lost the stored secret")
	}
	empty := current(t, r, signalRef)
	empty.Settings = map[string]any{"channel": "gcpd", "url": "http://192.0.2.20/", "password": ""}
	mustPut(t, apiKey(), r, empty, map[string]secrets.Value{"password": secrets.NewValue("")})
	if e.secret(t, signalRef, "password") != "alfred-1" {
		t.Error("an empty secret value by a key was taken for a write")
	}
}

// Cell 4: a non-admin holder of core:integrations.manage, in an interactive
// session (a custom role), is refused a secret write; a Test with the same
// body secret is still allowed, since it stores nothing.
func TestPut_SecretWriteRefusedForManagerWithoutAdmin(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	if _, err := r.Put(manager(), signal("http://192.0.2.20/"), cred("alfred-1")); !errors.Is(err, integrations.ErrForbidden) {
		t.Fatalf("a manager storing a secret: err = %v, want ErrForbidden", err)
	}
	noRowNoSecret(t, e)
	if res, err := r.Test(manager(), signal("http://192.0.2.20/"), cred("alfred-1")); err != nil || !res.OK {
		t.Errorf("a manager's Test with a body secret = %+v, %v", res, err)
	}
	mustPut(t, manager(), r, signal("http://192.0.2.20/"), nil)
	if _, err := r.Put(manager(), current(t, r, signalRef), cred("alfred-2")); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("a manager replacing a secret: err = %v, want ErrForbidden", err)
	}
}

// The auth-disabled principal never passes ProtectAdmin, so it cannot store a
// secret either. HEAD refuses the same write: with auth off no user holds the
// admin role (config_handler.go configCan reads core:admin from the signed-in
// user's roles), and there is no first-run grant under core identity.
func TestPut_SecretWriteRefusedForAuthOff(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	if _, err := r.Put(authOff(), signal("http://192.0.2.20/"), cred("alfred-1")); !errors.Is(err, integrations.ErrForbidden) {
		t.Fatalf("auth off storing a secret: err = %v, want ErrForbidden", err)
	}
	noRowNoSecret(t, e)
}
