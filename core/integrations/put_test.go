// file: core/integrations/put_test.go
// version: 1.0.1
// guid: 9ee5562e-9650-4bd0-912e-7df382b78912
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/secrets"
)

// withURL is cfg with its url setting replaced (deleted for "").
func withURL(cfg integrations.InstanceConfig, url string) integrations.InstanceConfig {
	cfg.Settings = maps.Clone(cfg.Settings)
	if url == "" {
		delete(cfg.Settings, "url")
	} else {
		cfg.Settings["url"] = url
	}
	return cfg
}

// current is ref's effective view, failing t when there is none.
func current(t testing.TB, r *integrations.Registry, ref integrations.Ref) integrations.InstanceConfig {
	t.Helper()
	cfg, ok := r.Config(ref)
	if !ok {
		t.Fatalf("no instance %s", ref)
	}
	return cfg
}

// lastAction is the newest committed audit record.
func lastAction(t testing.TB, e *env) string {
	t.Helper()
	a := e.actions(t)
	if len(a) == 0 {
		return ""
	}
	return a[len(a)-1]
}

// TestRegistry_TestDoesNotPersist (M1.3 Test): a test of an unsaved config
// reaches the remote through the same client and persists nothing: no row,
// no secret, no audit record, no served instance.
func TestRegistry_TestDoesNotPersist(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	res, err := r.Test(admin(), signal("http://192.0.2.20/"), cred("alfred-1"))
	if err != nil || !res.OK {
		t.Fatalf("Test = %+v, %v", res, err)
	}
	if got := e.credentialsSentTo("http://192.0.2.20/"); !slices.Equal(got, []string{"alfred-1"}) {
		t.Errorf("the test sent %v", got)
	}
	if _, ok := e.stored(t, signalRef); ok || e.secret(t, signalRef, "password") != "" || len(e.actions(t)) != 0 {
		t.Error("Test persisted something")
	}
	if n := len(slices.Collect(r.Instances(integrations.KindNotify))); n != 0 {
		t.Errorf("Test left %d served instances", n)
	}
	if built := e.fake.Built(); len(built) != 1 || built[0].Closed() != 1 {
		t.Errorf("the tested instance was not discarded: %d built", len(built))
	}
	if _, err := r.Test(admin(), integrations.InstanceConfig{Ref: signalRef, Adapter: "hello.lazarus"}, nil); !errors.Is(err, integrations.ErrUnknownAdapter) {
		t.Errorf("Test of an unknown adapter: %v", err)
	}
	if _, err := r.Test(admin(), integrations.InstanceConfig{Ref: integrations.Ref{Kind: "gadgets"}, Adapter: batsignal}, nil); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("Test of an unknown kind: %v", err)
	}
	if _, err := r.Test(context.Background(), signal("http://192.0.2.20/"), nil); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("Test without a principal: %v", err)
	}
}

// TestRegistry_URLChangeDropsSecrets (M1.3 Put, SEC-15): a change of a URL
// field's origin clears every secret not re-supplied and is audited as
// change_url with secrets_dropped; a path change is not a destination change.
func TestRegistry_URLChangeDropsSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))

	mustPut(t, admin(), r, withURL(current(t, r, signalRef), "http://192.0.2.20:80/beacon"), nil)
	if e.secret(t, signalRef, "password") != "alfred-1" || lastAction(t, e) != "core.integration.update" {
		t.Errorf("a path change dropped the secret or was not an update: %v", e.actions(t))
	}

	got := mustPut(t, admin(), r, withURL(current(t, r, signalRef), "http://192.0.2.21/"), nil)
	if e.secret(t, signalRef, "password") != "" {
		t.Error("a host change kept the secret")
	}
	if _, ok := got.Settings["password"]; ok {
		t.Errorf("the secret field survived the host change: %v", got.Settings)
	}
	if a := lastAction(t, e); a != "core.integration.change_url secrets_dropped" {
		t.Errorf("audit = %q", a)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthMisconfigured || h.Message != integrations.ReasonSecretMissing {
		t.Errorf("Health after the drop = %+v", h)
	}

	mustPut(t, admin(), r, withURL(current(t, r, signalRef), "https://192.0.2.21/"), cred("alfred-2"))
	if e.secret(t, signalRef, "password") != "alfred-2" || lastAction(t, e) != "core.integration.write_secret" {
		t.Errorf("a scheme change with a re-supplied secret: %q, %v", e.secret(t, signalRef, "password"), e.actions(t))
	}
	mustPut(t, admin(), r, withURL(current(t, r, signalRef), "https://192.0.2.21:8443/"), nil)
	if e.secret(t, signalRef, "password") != "" || lastAction(t, e) != "core.integration.change_url secrets_dropped" {
		t.Errorf("a port change kept the secret: %v", e.actions(t))
	}
	mustPut(t, admin(), r, withURL(current(t, r, signalRef), ""), cred("alfred-3"))
	mustPut(t, admin(), r, withURL(current(t, r, signalRef), "http://192.0.2.22/"), nil)
	if e.secret(t, signalRef, "password") != "" {
		t.Error("moving off the adapter default kept the secret")
	}
}

// TestRegistry_DestinationChangeDropsSecrets (lead decision, round 7): for an
// adapter that dials a host and port, a change of either clears the secrets.
func TestRegistry_DestinationChangeDropsSecrets(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	in := integrations.InstanceConfig{Ref: oracleRef, Adapter: oracle, Enabled: true, Settings: map[string]any{"host": "192.0.2.60", "port": 9000}}
	mustPut(t, admin(), r, in, cred("barbara-1"))
	set := func(k string, v any) integrations.InstanceConfig {
		c := current(t, r, oracleRef)
		c.Settings = maps.Clone(c.Settings)
		c.Settings[k] = v
		return c
	}
	mustPut(t, admin(), r, set("port", 9000), nil)
	if e.secret(t, oracleRef, "password") != "barbara-1" {
		t.Fatal("an unchanged destination dropped the secret")
	}
	mustPut(t, admin(), r, set("port", 9001), nil)
	if e.secret(t, oracleRef, "password") != "" || lastAction(t, e) != "core.integration.change_url secrets_dropped" {
		t.Errorf("a port change kept the secret: %v", e.actions(t))
	}
	mustPut(t, admin(), r, set("port", 9001), cred("barbara-2"))
	mustPut(t, admin(), r, set("host", "192.0.2.61"), nil)
	if e.secret(t, oracleRef, "password") != "" {
		t.Error("a host change kept the secret")
	}
}

// TestRegistry_APIKeyCannotSetLocal (SEC2-7): an API key with core:admin
// still fails the ProtectAdmin check, so it cannot set Local or keep it
// across a destination change; its writes are Origin "api".
func TestRegistry_APIKeyCannotSetLocal(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	local := signal("http://192.0.2.20/")
	local.Local = true
	if _, err := r.Put(apiKey(), local, nil); !errors.Is(err, integrations.ErrForbidden) {
		t.Fatalf("an API key setting Local: err = %v, want ErrForbidden", err)
	}
	if _, ok := e.stored(t, signalRef); ok {
		t.Fatal("the refused Put wrote a row")
	}
	got := mustPut(t, apiKey(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if got.Origin != integrations.OriginAPI || got.Local {
		t.Errorf("an API key's write = %+v", got)
	}
	cfg := current(t, r, signalRef)
	cfg.Local = true
	mustPut(t, admin(), r, cfg, nil)
	keep := current(t, r, signalRef)
	keep.Name = "kept by the key"
	if got := mustPut(t, apiKey(), r, keep, nil); !got.Local {
		t.Error("an API key could not keep Local over an unchanged destination")
	}
	if _, err := r.Put(apiKey(), withURL(current(t, r, signalRef), "http://192.0.2.70/"), nil); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("an API key keeping Local across a URL change: err = %v", err)
	}
}

// TestRegistry_LocalNeedsAdmin (M1.3 Put and Test): Local needs core:admin
// and an interactive principal; Local is bound to the origin it was granted
// for.
func TestRegistry_LocalNeedsAdmin(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	local := signal("http://192.0.2.20/")
	local.Local = true
	if _, err := r.Put(manager(), local, cred("alfred-1")); !errors.Is(err, integrations.ErrForbidden) {
		t.Fatalf("a manager setting Local: err = %v", err)
	}
	if _, err := r.Test(manager(), local, cred("alfred-1")); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("a manager testing a Local config: err = %v", err)
	}
	if res, err := r.Test(admin(), local, cred("alfred-1")); err != nil || !res.OK {
		t.Errorf("an admin testing a Local config: %+v, %v", res, err)
	}
	mustPut(t, admin(), r, local, cred("alfred-1"))
	if row, _ := e.stored(t, signalRef); !row.Config.Local || row.LocalOrigin != "http://192.0.2.20:80" {
		t.Errorf("the admin's Local = %v bound to %q", row.Config.Local, row.LocalOrigin)
	}
	keep := current(t, r, signalRef)
	keep.Name = "renamed by a manager"
	mustPut(t, manager(), r, keep, nil)
	if row, _ := e.stored(t, signalRef); !row.Config.Local || row.LocalOrigin != "http://192.0.2.20:80" {
		t.Errorf("a manager's rename lost Local: %+v %q", row.Config.Local, row.LocalOrigin)
	}
	moved := withURL(current(t, r, signalRef), "http://192.0.2.70/")
	if _, err := r.Put(manager(), moved, nil); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("a manager keeping Local across a URL change: err = %v", err)
	}
	if _, err := r.Test(manager(), moved, nil); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("a manager testing Local across a URL change: err = %v", err)
	}
	moved.Local = false
	mustPut(t, manager(), r, moved, nil)
	if row, _ := e.stored(t, signalRef); row.Config.Local || row.LocalOrigin != "" {
		t.Errorf("Local survived the manager's move: %+v %q", row.Config.Local, row.LocalOrigin)
	}
	delegated := integrationstest.As(context.Background(), auth.MethodSessionDelegated, integrationstest.Manage, integrationstest.Admin)
	again := current(t, r, signalRef)
	again.Local = true
	if got := mustPut(t, delegated, r, again, nil); !got.Local {
		t.Error("a delegated admin session could not set Local")
	}
}

// TestAuthOff_LocalNotSettable (05-rbac §4.11): the auth-disabled principal
// does not pass the ProtectAdmin check whatever it holds; its writes are
// Origin "ui". A write needs a principal holding core:integrations.manage.
func TestAuthOff_LocalNotSettable(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	local := signal("http://192.0.2.20/")
	local.Local = true
	if _, err := r.Put(authOff(), local, nil); !errors.Is(err, integrations.ErrForbidden) {
		t.Fatalf("auth off setting Local: err = %v", err)
	}
	got := mustPut(t, authOff(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if got.Origin != integrations.OriginUI || got.Local {
		t.Errorf("an auth-off write = %+v", got)
	}
	if _, err := r.Put(context.Background(), current(t, r, signalRef), nil); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("a write without a principal: err = %v", err)
	}
	viewer := integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Admin)
	if err := r.Delete(viewer, signalRef); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("a delete without core:integrations.manage: err = %v", err)
	}
	if err := r.Delete(authOff(), signalRef); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if e.secret(t, signalRef, "password") != "" || lastAction(t, e) != "core.integration.delete" {
		t.Errorf("Delete left the secret or was not audited: %v", e.actions(t))
	}
	if err := r.Delete(authOff(), signalRef); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Errorf("a second Delete: err = %v", err)
	}
	if err := r.Delete(authOff(), integrations.Ref{Kind: integrations.KindNotify, Instance: "Gotham"}); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("a Delete of an invalid ref: err = %v", err)
	}
}

// TestRegistry_TestReturnsNoBody (SEC-15): a test result carries OK, the
// Class and the status only, never the remote's body or error text.
func TestRegistry_TestReturnsNoBody(t *testing.T) {
	e := newEnv(t)
	e.fake.TestFn = func(_ context.Context, i *integrationstest.FakeInstance) error {
		return &integrations.Error{
			Ref: i.Config.Ref, Op: "caps", Class: integrations.ClassAuth, Status: http.StatusUnauthorized,
			Detail: "<html>The Joker's plan: gas the parade</html>", Err: errors.New("remote said: the Joker's plan"),
		}
	}
	r := e.registry(t, nil, nil)
	res, err := r.Test(admin(), signal("http://192.0.2.20/"), cred("alfred-1"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (integrations.TestResult{OK: false, Class: integrations.ClassAuth, Status: http.StatusUnauthorized}); res != want {
		t.Errorf("Test = %+v, want %+v", res, want)
	}
	if s := fmt.Sprintf("%v %+v %#v", res, res, res); strings.Contains(s, "Joker") {
		t.Errorf("the result carries the remote's text: %s", s)
	}
	e.out.Status = http.StatusServiceUnavailable
	if res, _ := r.Test(admin(), signal("http://192.0.2.20/"), cred("alfred-1")); res != (integrations.TestResult{Class: integrations.ClassTransient}) {
		t.Errorf("a failed probe = %+v", res)
	}
	e.out.Status = 0
	e.fake.BuildErr = errors.New("the Batmobile will not start")
	if res, _ := r.Test(admin(), signal("http://192.0.2.20/"), cred("alfred-1")); res != (integrations.TestResult{Class: integrations.ClassInvalid}) {
		t.Errorf("a failed build = %+v", res)
	}
}

// TestPut_EchoedMaskIsNotResupply (SEC3-4): an empty, echoed or mask-shaped
// secretsIn value keeps the stored secret and writes nothing.
func TestPut_EchoedMaskIsNotResupply(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	for _, echo := range []string{
		"", "   ", "********", "al********", "••••••••", "[REDACTED]", "secrets.Value([REDACTED])",
		"integrations.notify.gcpd-roof.password", `{"secret":"integrations.notify.gcpd-roof.password"}`,
	} {
		mustPut(t, admin(), r, current(t, r, signalRef), cred(echo))
		if got := e.secret(t, signalRef, "password"); got != "alfred-1" {
			t.Fatalf("the echo %q replaced the secret with %q", echo, got)
		}
		if lastAction(t, e) != "core.integration.update" {
			t.Fatalf("the echo %q was audited as %q", echo, lastAction(t, e))
		}
	}
	mustPut(t, admin(), r, current(t, r, signalRef), cred("alfred-2"))
	if e.secret(t, signalRef, "password") != "alfred-2" {
		t.Error("a real value was not stored")
	}
	cleared := current(t, r, signalRef)
	cleared.Settings = maps.Clone(cleared.Settings)
	cleared.Settings["password"] = nil
	mustPut(t, admin(), r, cleared, nil)
	if e.secret(t, signalRef, "password") != "" {
		t.Error("an explicit null did not clear the secret")
	}
}

// TestPut_SecretChangeReleasesAuthHold (M2.4): a Put that writes a new
// secret value releases the instance's auth hold; one that writes none, or
// only drops a secret, does not.
func TestPut_SecretChangeReleasesAuthHold(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var released []integrations.Ref
	fail := atomic.Bool{}
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.ReleaseAuthHold = func(_ context.Context, ref integrations.Ref) error {
			mu.Lock()
			defer mu.Unlock()
			released = append(released, ref)
			if fail.Load() {
				return errors.New("the hold store is down")
			}
			return nil
		}
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(released)
	}
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if count() != 1 || released[0] != signalRef {
		t.Fatalf("a create with a secret released %v", released)
	}
	renamed := current(t, r, signalRef)
	renamed.Name = "renamed"
	mustPut(t, admin(), r, renamed, cred("********"))
	if count() != 1 {
		t.Errorf("a Put without a new secret released the hold")
	}
	fail.Store(true)
	mustPut(t, admin(), r, current(t, r, signalRef), cred("alfred-2"))
	if count() != 2 {
		t.Errorf("a new secret did not release the hold")
	}
	mustPut(t, admin(), r, withURL(current(t, r, signalRef), "http://192.0.2.21/"), nil)
	if count() != 2 {
		t.Errorf("dropping a secret released the hold")
	}
}

// TestRegistry_DisabledMakesNoOutboundCall (I5, R6-7): with
// core.integrations.disabled set no lease is issued, no probe or test runs,
// and an instance leased before the switch refuses before sending; reads and
// Put stay open.
func TestRegistry_DisabledMakesNoOutboundCall(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var disabled atomic.Bool
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.Settings = func() integrations.Settings { return integrations.Settings{Disabled: disabled.Load()} }
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	held, release := lease(t, r, signalRef)
	defer release()

	disabled.Store(true)
	_, _, err := integrations.Lease[integrations.Instance](ctx, r, signalRef)
	if !errors.Is(err, integrations.ErrDisabled) || integrations.ClassOf(ctx, err) != integrations.ClassRefusedLocal {
		t.Errorf("Lease while disabled: %v", err)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthDisabled || h.Message != integrations.ReasonIntegrationsDisabled {
		t.Errorf("Health while disabled = %+v", h)
	}
	if err := r.ProbeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Test(admin(), signal("http://192.0.2.20/"), cred("alfred-1")); !errors.Is(err, integrations.ErrDisabled) {
		t.Errorf("Test while disabled: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.20/", nil)
	if _, err := held.Env.HTTP.Do(req); !errors.Is(err, integrations.ErrDisabled) {
		t.Errorf("Do on a held instance while disabled: %v", err)
	}
	if resp, err := held.Env.HTTP.Client().Do(req); err == nil {
		_ = resp.Body.Close()
		t.Error("the SDK client of a held instance sent while disabled")
	}
	if _, err := held.Env.Gate.Acquire(ctx, "requests"); !errors.Is(err, integrations.ErrDisabled) {
		t.Errorf("Acquire while disabled: %v", err)
	}
	if h := held.Call(ctx); h.Status != integrations.HealthDown {
		t.Errorf("a held instance's call while disabled = %+v", h)
	}
	if sent := e.out.Sent(); len(sent) != 0 {
		t.Fatalf("requests sent while disabled: %v", sent)
	}
	mustPut(t, admin(), r, withURL(current(t, r, signalRef), "http://192.0.2.20/beacon"), nil)
	if n := len(slices.Collect(r.Instances(integrations.KindNotify))); n != 1 {
		t.Errorf("reads while disabled: %d instances", n)
	}

	disabled.Store(false)
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK {
		t.Errorf("Health after the switch = %+v", h)
	}
	if n := len(e.out.Sent()); n != 1 {
		t.Errorf("after the switch %d requests, want 1", n)
	}
	gate := held.Env.HTTP.Gate()
	if _, err := gate.Acquire(ctx, "requests"); err != nil {
		t.Errorf("Acquire after the switch: %v", err)
	}
}

// TestPut_NonAdminURLDoesNotCarryEnvSecret (SEC7-3, R8-5): an operator
// secret follows a URL an admin stored, never one a non-admin stored.
func TestPut_NonAdminURLDoesNotCarryEnvSecret(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), nil)
	if row, _ := e.stored(t, signalRef); row.OperatorSecretOrigin["url"] != "http://192.0.2.20:80" {
		t.Fatalf("the admin's URL was not recorded: %v", row.OperatorSecretOrigin)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.notify.gcpd-roof.password", "env-pass")); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK {
		t.Fatalf("Health at the admin's URL = %+v", h)
	}
	if got := e.credentialsSentTo("http://192.0.2.20/"); !slices.Equal(got, []string{"env-pass"}) {
		t.Fatalf("credentials to the admin's URL = %v", got)
	}

	mustPut(t, manager(), r, withURL(current(t, r, signalRef), "http://192.0.2.66/"), nil)
	if row, _ := e.stored(t, signalRef); len(row.OperatorSecretOrigin) != 0 {
		t.Errorf("the manager's URL change kept the provenance: %v", row.OperatorSecretOrigin)
	}
	h, _ := r.Health(ctx, signalRef)
	if h.Status != integrations.HealthDegraded || h.Message != integrations.ReasonOperatorSecretWithheld+": password" {
		t.Errorf("Health at the manager's URL = %+v", h)
	}
	for _, s := range e.out.Sent() {
		if s.URL == "http://192.0.2.66/" && s.Credential != "" {
			t.Errorf("the operator secret was sent to the manager's URL: %+v", s)
		}
	}
	held, release := lease(t, r, signalRef)
	_, err := held.Env.Secrets.Secret(ctx, "password")
	release()
	if !errors.Is(err, integrations.ErrOperatorSecretWithheld) || integrations.ClassOf(ctx, err) != integrations.ClassRefusedLocal {
		t.Errorf("the withheld secret's read: %v", err)
	}

	mustPut(t, admin(), r, current(t, r, signalRef), nil)
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK {
		t.Errorf("Health once an admin stored the URL = %+v", h)
	}
	if got := e.credentialsSentTo("http://192.0.2.66/"); !slices.Equal(got, []string{"env-pass"}) {
		t.Errorf("credentials to the admin-approved URL = %v", got)
	}
}

// TestOperatorSecret_AdapterDefaultURLServed (M1.4 step 2): with no URL set
// anywhere the adapter dials its default, and the operator secret is
// served to it, for an operator-only instance and for a stored one.
func TestOperatorSecret_AdapterDefaultURLServed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	tower := integrations.Ref{Kind: integrations.KindNotify, Instance: "wayne-tower"}
	r := e.registry(t, overlay(
		"integrations.notify.wayne-tower.password", "env-tower-pass",
		"integrations.notify.gcpd-roof.password", "env-roof-pass",
	), func(o *integrations.Options) {
		o.OperatorInstances = map[integrations.Ref]integrations.AdapterID{tower: batsignal}
	})
	if h, _ := r.Health(ctx, tower); h.Status != integrations.HealthOK {
		t.Fatalf("Health = %+v", h)
	}
	mustPut(t, manager(), r, signal(""), nil)
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK {
		t.Fatalf("Health of the stored instance = %+v", h)
	}
	if got := e.credentialsSentTo("http://192.0.2.10/"); !slices.Equal(got, []string{"env-tower-pass", "env-roof-pass"}) {
		t.Errorf("credentials to the adapter default = %v", got)
	}
}

// TestOperatorSecret_AdminStoredHostServed (R8-5, SEC8-2): for a
// host-and-port adapter an operator secret follows the host and port an
// admin stored, and is withheld once anyone else changes either.
func TestOperatorSecret_AdminStoredHostServed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	in := integrations.InstanceConfig{Ref: oracleRef, Adapter: oracle, Enabled: true, Settings: map[string]any{"host": "192.0.2.60", "port": 9000}}
	mustPut(t, admin(), r, in, nil)
	if row, _ := e.stored(t, oracleRef); row.OperatorSecretOrigin["host"] != "smtp://192.0.2.60:9000" {
		t.Fatalf("the admin's host was not recorded: %v", row.OperatorSecretOrigin)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.metadata.clocktower.password", "env-oracle-pass")); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK {
		t.Fatalf("Health at the admin's host = %+v", h)
	}
	if got := e.credentialsSentTo("http://192.0.2.60/"); !slices.Equal(got, []string{"env-oracle-pass"}) {
		t.Fatalf("credentials to the admin's host = %v", got)
	}
	moved := current(t, r, oracleRef)
	moved.Settings = maps.Clone(moved.Settings)
	moved.Settings["port"] = 9001
	mustPut(t, manager(), r, moved, nil)
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthDegraded {
		t.Errorf("Health after a manager's port change = %+v", h)
	}
	if got := e.credentialsSentTo("http://192.0.2.60/"); len(got) != 1 {
		t.Errorf("the operator secret followed the manager's port: %v", got)
	}
}

// TestRegistryTest_ChangedURLUsesNoStoredSecret (SEC8-10): a test aimed at a
// destination no Put stored uses no stored secret.
func TestRegistryTest_ChangedURLUsesNoStoredSecret(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	cfg := current(t, r, signalRef)

	res, err := r.Test(admin(), withURL(cfg, "http://192.0.2.99/"), nil)
	if err != nil || res.OK {
		t.Errorf("a test at a new URL without a secret = %+v, %v", res, err)
	}
	if res, _ := r.Test(admin(), withURL(cfg, "http://192.0.2.99/"), cred("********")); res.OK {
		t.Errorf("a mask counted as a secret: %+v", res)
	}
	for _, s := range e.out.Sent() {
		if s.Credential == "alfred-1" {
			t.Fatalf("the stored secret was sent by a test: %+v", s)
		}
	}
	if res, _ := r.Test(admin(), cfg, nil); !res.OK {
		t.Errorf("a test at the stored URL = %+v", res)
	}
	if got := e.credentialsSentTo("http://192.0.2.20/"); !slices.Equal(got, []string{"alfred-1"}) {
		t.Errorf("credentials to the stored URL = %v", got)
	}
	if res, _ := r.Test(admin(), withURL(cfg, "http://192.0.2.99/"), cred("alfred-2")); !res.OK {
		t.Errorf("a test at a new URL with a re-supplied secret = %+v", res)
	}
	if got := e.credentialsSentTo("http://192.0.2.99/"); !slices.Equal(got, []string{"alfred-2"}) {
		t.Errorf("credentials to the new URL = %v", got)
	}
	unscoped := signal("http://192.0.2.98/")
	unscoped.Ref.Instance = ""
	if res, err := r.Test(admin(), unscoped, cred("alfred-3")); err != nil || !res.OK {
		t.Errorf("an unscoped test = %+v, %v", res, err)
	}
	if row, _ := e.stored(t, signalRef); row.Config.Version != 1 || e.secret(t, signalRef, "password") != "alfred-1" {
		t.Error("a test changed the stored instance")
	}
}

// TestPut_Refusals covers the validation a Put applies before staging.
func TestPut_Refusals(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.Settings = func() integrations.Settings { return integrations.Settings{MaxInstancesPerKind: 2} }
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	with := func(edit func(*integrations.InstanceConfig)) integrations.InstanceConfig {
		c := current(t, r, signalRef)
		c.Settings = maps.Clone(c.Settings)
		edit(&c)
		return c
	}
	for name, c := range map[string]struct {
		inst integrations.InstanceConfig
		in   map[string]secrets.Value
		want error
	}{
		"unknown field":           {with(func(c *integrations.InstanceConfig) { c.Settings["utility_belt"] = true }), nil, integrations.ErrInvalid},
		"secret in a plain field": {with(func(c *integrations.InstanceConfig) { c.Settings["channel"] = secrets.NewValue("x") }), nil, integrations.ErrInvalid},
		"value in a secret field": {with(func(c *integrations.InstanceConfig) { c.Settings["password"] = "alfred-2" }), nil, integrations.ErrInvalid},
		"foreign secret ref": {with(func(c *integrations.InstanceConfig) {
			c.Settings["password"] = secrets.RefTo("integrations.notify.arkham.password")
		}), nil, integrations.ErrInvalid},
		"secretsIn plain field": {with(func(*integrations.InstanceConfig) {}), map[string]secrets.Value{"channel": secrets.NewValue("x")}, integrations.ErrInvalid},
		"userinfo URL":          {with(func(c *integrations.InstanceConfig) { c.Settings["url"] = "http://bruce:alfred@192.0.2.20/" }), nil, integrations.ErrInvalid},
		"relative URL":          {with(func(c *integrations.InstanceConfig) { c.Settings["url"] = "/beacon" }), nil, integrations.ErrInvalid},
		"schema mismatch":       {with(func(c *integrations.InstanceConfig) { c.Settings["channel"] = 7 }), nil, integrations.ErrInvalid},
		"adapter change":        {with(func(c *integrations.InstanceConfig) { c.Adapter = oracle }), nil, integrations.ErrInvalid},
		"stale version":         {with(func(c *integrations.InstanceConfig) { c.Version = 7 }), nil, integrations.ErrVersionConflict},
		"invalid ref":           {with(func(c *integrations.InstanceConfig) { c.Ref.Instance = "GCPD" }), nil, integrations.ErrInvalid},
		"unknown adapter":       {integrations.InstanceConfig{Ref: integrations.Ref{Kind: integrations.KindNotify, Instance: "arkham"}, Adapter: "hello.lazarus"}, nil, integrations.ErrUnknownAdapter},
		"kind mismatch":         {integrations.InstanceConfig{Ref: integrations.Ref{Kind: integrations.KindAI, Instance: "arkham"}, Adapter: batsignal}, nil, integrations.ErrInvalid},
		"invalid adapter ID":    {integrations.InstanceConfig{Ref: integrations.Ref{Kind: integrations.KindAI, Instance: "arkham"}, Adapter: "Lazarus"}, nil, integrations.ErrInvalid},
	} {
		if _, err := r.Put(admin(), c.inst, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if row, _ := e.stored(t, signalRef); row.Config.Version != 1 {
		t.Errorf("a refused Put wrote: version %d", row.Config.Version)
	}
	second := signal("http://192.0.2.21/")
	second.Ref.Instance = "wayne-manor"
	mustPut(t, admin(), r, second, cred("manor-1"))
	third := signal("http://192.0.2.22/")
	third.Ref.Instance = "ace-chemicals"
	if _, err := r.Put(admin(), third, cred("ace-1")); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("a create past max_instances_per_kind: err = %v", err)
	}
}

// TestPut_CreateDeletesStaleSecret (F7-01, #4577): a create never revives a
// secret record a deleted instance of the same ID left behind.
func TestPut_CreateDeletesStaleSecret(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	if _, err := e.secrets.Put(ctx, "integrations.notify.gcpd-roof.password", secrets.NewValue("joker-left-this"), "joker"); err != nil {
		t.Fatal(err)
	}
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), nil)
	if got := e.secret(t, signalRef, "password"); got != "" {
		t.Errorf("a create revived the stale secret %q", got)
	}
}
