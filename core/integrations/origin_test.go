// file: core/integrations/origin_test.go
// version: 1.3.0
// guid: 394052ef-66a7-48bf-a8f2-6fb61af1e689
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The destination rules read the effective view: scheme, host and port from
// every layer (M1.3 Put, M1.4 step 2, SEC-15).

// clocktower is a hello.oracle instance at port, its host left to the
// operator overlay or the adapter default.
func clocktower(port int) integrations.InstanceConfig {
	return integrations.InstanceConfig{
		Ref: oracleRef, Adapter: oracle, Name: "Clock Tower", Enabled: true,
		Settings: map[string]any{"port": port},
	}
}

// withPort is cfg at port.
func withPort(cfg integrations.InstanceConfig, port int) integrations.InstanceConfig {
	cfg.Settings = maps.Clone(cfg.Settings)
	cfg.Settings["port"] = port
	return cfg
}

// sentWith counts the requests that carried credential, to any URL.
func sentWith(e *env, credential string) int {
	n := 0
	for _, s := range e.out.Sent() {
		if s.Credential == credential {
			n++
		}
	}
	return n
}

// rewriteRow rewrites ref's committed row through the native store, as a
// writer outside the Registry's Put can.
func rewriteRow(t *testing.T, e *env, ref integrations.Ref, edit func(*integrations.StoredInstance)) {
	t.Helper()
	ctx := context.Background()
	row, ok := e.stored(t, ref)
	if !ok {
		t.Fatalf("no row %s", ref)
	}
	edit(&row)
	b := e.ks.NewBatch()
	defer func() { _ = b.Close() }()
	if _, err := integrations.NativeInstanceStore(e.ks).Put(ctx, b, row); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
}

// TestPut_PortChangeBesideOperatorHostDropsSecrets (SEC-15): with the host
// set by the operator, a non-admin Put that changes only the port changes
// the destination: the stored secret is dropped and the write is audited as
// a destination change; a Test of that port sends no stored secret.
func TestPut_PortChangeBesideOperatorHostDropsSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, overlay("integrations.metadata.clocktower.host", "192.0.2.60"), nil)
	mustPut(t, admin(), r, clocktower(9000), cred("alfred-1"))
	if row, _ := e.stored(t, oracleRef); row.SecretsOrigin != "smtp://192.0.2.60:9000" || row.OperatorSecretOrigin["host"] != "smtp://192.0.2.60:9000" {
		t.Fatalf("origins recorded = %q / %v, want the effective destination", row.SecretsOrigin, row.OperatorSecretOrigin)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK || sentWith(e, "alfred-1") != 1 {
		t.Fatalf("Health = %+v, stored secret sent %d times", h, sentWith(e, "alfred-1"))
	}

	// A Test of the unchanged config is at the destination the secret was
	// written for, operator host included: it uses the stored secret.
	if res, err := r.Test(manager(), current(t, r, oracleRef), nil); err != nil || !res.OK || sentWith(e, "alfred-1") != 2 {
		t.Errorf("a Test at the stored destination = %+v, %v; stored secret sent %d times", res, err, sentWith(e, "alfred-1"))
	}
	if _, err := r.Test(manager(), withPort(current(t, r, oracleRef), 9443), nil); err != nil {
		t.Fatal(err)
	}
	if n := sentWith(e, "alfred-1"); n != 2 {
		t.Errorf("a Test at a new port sent the stored secret (%d sends)", n)
	}

	mustPut(t, manager(), r, withPort(current(t, r, oracleRef), 9443), nil)
	if a := lastAction(t, e); a != "core.integration.change_url secrets_dropped" {
		t.Errorf("audit = %q", a)
	}
	if v := e.secret(t, oracleRef, "password"); v != "" {
		t.Errorf("the stored secret survived the port change: %q", v)
	}
	if row, _ := e.stored(t, oracleRef); row.SecretsOrigin != "smtp://192.0.2.60:9443" || len(row.OperatorSecretOrigin) != 0 {
		t.Errorf("origins after a manager's port change = %q / %v", row.SecretsOrigin, row.OperatorSecretOrigin)
	}
	_, _ = r.Health(ctx, oracleRef)
	if n := sentWith(e, "alfred-1"); n != 2 {
		t.Errorf("the stored secret followed the port (%d sends)", n)
	}
}

// TestPut_PortChangeWithHostUnsetDropsSecrets: with no host anywhere (the
// adapter's default), the port alone is the destination; changing it drops
// the stored secret, and a Test of it sends none.
func TestPut_PortChangeWithHostUnsetDropsSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, manager(), r, clocktower(9000), cred("alfred-1"))
	if row, _ := e.stored(t, oracleRef); row.SecretsOrigin != "smtp://:9000" {
		t.Fatalf("SecretsOrigin = %q", row.SecretsOrigin)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK || sentWith(e, "alfred-1") != 1 {
		t.Fatalf("Health = %+v", h)
	}
	if _, err := r.Test(manager(), withPort(current(t, r, oracleRef), 9443), nil); err != nil {
		t.Fatal(err)
	}
	if n := sentWith(e, "alfred-1"); n != 1 {
		t.Errorf("a Test at a new port sent the stored secret (%d sends)", n)
	}
	mustPut(t, manager(), r, withPort(current(t, r, oracleRef), 9443), nil)
	if a := lastAction(t, e); a != "core.integration.change_url secrets_dropped" {
		t.Errorf("audit = %q", a)
	}
	if v := e.secret(t, oracleRef, "password"); v != "" {
		t.Errorf("the stored secret survived the port change: %q", v)
	}
	_, _ = r.Health(ctx, oracleRef)
	if n := sentWith(e, "alfred-1"); n != 1 {
		t.Errorf("the stored secret followed the port (%d sends)", n)
	}
}

// TestOperatorSecret_AdminPortBesideOperatorHostServed (R8-5): an admin's
// port beside the operator's host and password is recorded as the effective
// destination, so the password is served and Local trusts it; a Test of
// another port records nothing and sends nothing; a manager's port change
// withholds the password until an admin stores a port again.
func TestOperatorSecret_AdminPortBesideOperatorHostServed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, overlay(
		"integrations.metadata.clocktower.host", "192.0.2.60",
		"integrations.metadata.clocktower.password", "env-oracle-pass",
	), nil)
	in := clocktower(9000)
	in.Local = true
	mustPut(t, admin(), r, in, nil)
	if row, _ := e.stored(t, oracleRef); row.OperatorSecretOrigin["host"] != "smtp://192.0.2.60:9000" || row.LocalOrigin != "smtp://192.0.2.60:9000" {
		t.Fatalf("origins recorded = %v / %q", row.OperatorSecretOrigin, row.LocalOrigin)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK || sentWith(e, "env-oracle-pass") != 1 {
		t.Fatalf("Health beside the admin's port = %+v", h)
	}
	_, release := lease(t, r, oracleRef)
	release()
	if tr := lastSpec(t, e, oracleRef).Trust; !slices.Equal(tr.PrivateOrigins, []string{"smtp://192.0.2.60:9000"}) {
		t.Errorf("Local trust = %v", tr)
	}

	if _, err := r.Test(admin(), withPort(current(t, r, oracleRef), 9443), nil); err != nil {
		t.Fatal(err)
	}
	if n := sentWith(e, "env-oracle-pass"); n != 1 {
		t.Errorf("a Test at an unrecorded port sent the operator secret (%d sends)", n)
	}
	if row, _ := e.stored(t, oracleRef); row.OperatorSecretOrigin["host"] != "smtp://192.0.2.60:9000" {
		t.Errorf("a Test recorded provenance: %v", row.OperatorSecretOrigin)
	}

	moved := withPort(current(t, r, oracleRef), 9444)
	moved.Local = false
	mustPut(t, manager(), r, moved, nil)
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthDegraded || h.Message != integrations.ReasonOperatorSecretWithheld+": password" {
		t.Errorf("Health after a manager's port change = %+v", h)
	}
	if n := sentWith(e, "env-oracle-pass"); n != 1 {
		t.Errorf("the operator secret followed a manager's port (%d sends)", n)
	}

	again := withPort(current(t, r, oracleRef), 9445)
	again.Local = true
	mustPut(t, admin(), r, again, nil)
	if row, _ := e.stored(t, oracleRef); row.OperatorSecretOrigin["host"] != "smtp://192.0.2.60:9445" || row.LocalOrigin != "smtp://192.0.2.60:9445" {
		t.Errorf("origins after the admin's port = %v / %q", row.OperatorSecretOrigin, row.LocalOrigin)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK || sentWith(e, "env-oracle-pass") != 2 {
		t.Errorf("Health beside the admin's new port = %+v", h)
	}
}

// TestOperatorSecret_OperatorPortMoveWithheld (M1.4 step 2): with the
// operator setting only the port, an admin-stored host binds the operator's
// password to that host and port; when the operator moves the port the
// effective origin equals neither the operator's own (the default host at
// the new port) nor the recorded one, so the password is withheld.
func TestOperatorSecret_OperatorPortMoveWithheld(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, overlay(
		"integrations.metadata.clocktower.port", "9000",
		"integrations.metadata.clocktower.password", "env-oracle-pass",
	), nil)
	mustPut(t, admin(), r, integrations.InstanceConfig{
		Ref: oracleRef, Adapter: oracle, Enabled: true, Settings: map[string]any{"host": "192.0.2.60"},
	}, nil)
	if row, _ := e.stored(t, oracleRef); row.OperatorSecretOrigin["host"] != "smtp://192.0.2.60:9000" {
		t.Fatalf("OperatorSecretOrigin = %v", row.OperatorSecretOrigin)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK || sentWith(e, "env-oracle-pass") != 1 {
		t.Fatalf("Health = %+v", h)
	}
	if err := r.SetOverlay(ctx, overlay(
		"integrations.metadata.clocktower.port", "9443",
		"integrations.metadata.clocktower.password", "env-oracle-pass",
	)); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthDegraded {
		t.Errorf("Health after the operator moved the port = %+v", h)
	}
	if n := sentWith(e, "env-oracle-pass"); n != 1 {
		t.Errorf("the operator secret was sent to an unrecorded origin (%d sends)", n)
	}
}

// TestPut_KeepsAWithheldSecretWithItsOrigin (M1.3 Put, SEC-15): a Put that
// moves no destination and writes no secret keeps a stored secret whose
// SecretsOrigin names another destination, still bound to that origin: it
// is kept, never re-bound to the destination it was not written for, and
// stays withheld (reported as such) until a Put re-supplies it.
func TestPut_KeepsAWithheldSecretWithItsOrigin(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	mustPut(t, admin(), e.registry(t, nil, nil), signal("http://192.0.2.20/"), cred("alfred-1"))
	rewriteRow(t, e, signalRef, func(row *integrations.StoredInstance) { row.SecretsOrigin = "http://192.0.2.99:80" })
	r := e.registry(t, nil, nil)
	renamed := current(t, r, signalRef)
	renamed.Name = "Arkham watch"
	mustPut(t, manager(), r, renamed, nil)
	if a := lastAction(t, e); a != "core.integration.update" {
		t.Errorf("audit = %q", a)
	}
	if v := e.secret(t, signalRef, "password"); v != "alfred-1" {
		t.Errorf("the withheld secret was not kept: %q", v)
	}
	if row, _ := e.stored(t, signalRef); row.SecretsOrigin != "http://192.0.2.99:80" {
		t.Errorf("SecretsOrigin = %q, want the origin the secret was written for", row.SecretsOrigin)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthMisconfigured || h.Message != integrations.ReasonStoredSecretWithheld+": password" {
		t.Errorf("Health = %+v", h)
	}
	if n := sentWith(e, "alfred-1"); n != 0 {
		t.Errorf("a secret written for another destination was sent %d times", n)
	}

	mustPut(t, manager(), r, current(t, r, signalRef), cred("alfred-2"))
	if row, _ := e.stored(t, signalRef); row.SecretsOrigin != "http://192.0.2.20:80" {
		t.Errorf("SecretsOrigin after a re-supply = %q", row.SecretsOrigin)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK || !slices.Equal(e.credentialsSentTo("http://192.0.2.20/"), []string{"alfred-2"}) {
		t.Errorf("Health after a re-supply = %+v, sent %v", h, e.credentialsSentTo("http://192.0.2.20/"))
	}
}

// TestPut_SaveDuringOperatorURLKeepsStoredSecret (M1.3 Put, M1.4 step 2):
// while the operator overlay moves the URL, a stored secret is withheld but
// kept. A Save that moves no stored destination and writes no secret (a
// rename, or the echoed mask) keeps it and its SecretsOrigin, and removing
// the overlay serves it at its own destination again. A secret written
// during the overlay belongs to the overlay's destination instead.
func TestPut_SaveDuringOperatorURLKeepsStoredSecret(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	moved := overlay("integrations.notify.gcpd-roof.url", "http://192.0.2.99/")
	if err := r.SetOverlay(ctx, moved); err != nil {
		t.Fatal(err)
	}
	withheld := integrations.ReasonStoredSecretWithheld + ": password"
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthMisconfigured || h.Message != withheld {
		t.Errorf("Health beside the operator's URL = %+v", h)
	}

	renamed := current(t, r, signalRef)
	renamed.Name = "Arkham watch"
	mustPut(t, manager(), r, renamed, nil)
	mustPut(t, admin(), r, current(t, r, signalRef), map[string]secrets.Value{"password": secrets.NewValue("********")})
	if a := e.actions(t); !slices.Equal(a[len(a)-2:], []string{"core.integration.update", "core.integration.update"}) {
		t.Errorf("audit of the Saves = %v", a)
	}
	if v := e.secret(t, signalRef, "password"); v != "alfred-1" {
		t.Errorf("a Save beside the operator's URL deleted the stored secret: %q", v)
	}
	if row, _ := e.stored(t, signalRef); row.SecretsOrigin != "http://192.0.2.20:80" || row.Config.Settings["url"] != "http://192.0.2.20/" {
		t.Errorf("row after the Saves = %q / %v", row.SecretsOrigin, row.Config.Settings)
	}
	if res, err := r.Test(manager(), current(t, r, signalRef), nil); err != nil || res.OK || res.Class != integrations.ClassInvalid {
		t.Errorf("a Test beside the operator's URL = %+v, %v", res, err)
	}
	if n := sentWith(e, "alfred-1"); n != 0 {
		t.Errorf("the stored secret followed the operator's URL (%d sends)", n)
	}

	if err := r.SetOverlay(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK || !slices.Equal(e.credentialsSentTo("http://192.0.2.20/"), []string{"alfred-1"}) {
		t.Errorf("Health once the overlay is removed = %+v, sent %v", h, e.credentialsSentTo("http://192.0.2.20/"))
	}

	if err := r.SetOverlay(ctx, moved); err != nil {
		t.Fatal(err)
	}
	mustPut(t, manager(), r, current(t, r, signalRef), cred("alfred-2"))
	if row, _ := e.stored(t, signalRef); row.SecretsOrigin != "http://192.0.2.99:80" {
		t.Errorf("SecretsOrigin of a secret written beside the operator's URL = %q", row.SecretsOrigin)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK || !slices.Equal(e.credentialsSentTo("http://192.0.2.99/"), []string{"alfred-2"}) {
		t.Errorf("Health with the new secret = %+v", h)
	}
	if err := r.SetOverlay(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthMisconfigured || h.Message != withheld {
		t.Errorf("Health back at the stored URL = %+v", h)
	}
	if got := e.credentialsSentTo("http://192.0.2.20/"); !slices.Equal(got, []string{"alfred-1"}) {
		t.Errorf("credentials to the stored URL = %v", got)
	}
}

// TestRegistry_DrainingLeaseNeverReadsTheMovedSecret (M1.6, SEC-15): a
// lease held across a Put reads a rotated secret at the same destination,
// but never one written after the instance moved: the old instance still
// dials the old destination.
func TestRegistry_DrainingLeaseNeverReadsTheMovedSecret(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	held, release := lease(t, r, signalRef)
	defer release()

	mustPut(t, admin(), r, current(t, r, signalRef), cred("alfred-2"))
	if h := held.Call(ctx); h.Status != integrations.HealthOK {
		t.Errorf("the held instance after a rotation = %+v", h)
	}
	if got := e.credentialsSentTo("http://192.0.2.20/"); !slices.Equal(got, []string{"alfred-2"}) {
		t.Errorf("credentials after a rotation = %v", got)
	}

	mustPut(t, admin(), r, withURL(current(t, r, signalRef), "http://192.0.2.99/"), cred("alfred-3"))
	if _, err := held.Env.Secrets.Secret(ctx, "password"); err == nil || integrations.ClassOf(ctx, err) != integrations.ClassRefusedLocal {
		t.Errorf("the draining instance read the moved secret: %v", err)
	}
	_ = held.Call(ctx)
	if got := e.credentialsSentTo("http://192.0.2.20/"); !slices.Equal(got, []string{"alfred-2"}) {
		t.Errorf("credentials to the old destination after the move = %v", got)
	}
	if n := sentWith(e, "alfred-3"); n != 0 {
		t.Errorf("the moved secret was sent %d times", n)
	}
}

// envShadow answers every read of a password secret with an operator env
// value, as core secrets does for a name the operator also binds.
type envShadow struct{ integrations.SecretStore }

func (s envShadow) Get(ctx context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	if strings.HasSuffix(string(n), ".password") {
		return secrets.NewValue("env-shadow"), secrets.Meta{Name: n, Set: true, Source: secrets.SourceEnv}, nil
	}
	return s.SecretStore.Get(ctx, n)
}

// TestRegistry_EnvValueUnderStoredNameRefused (M1.4 step 2): the stored
// secret path serves only a stored copy; an operator value core secrets
// holds under the same name reaches an instance only through the overlay.
func TestRegistry_EnvValueUnderStoredNameRefused(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, func(o *integrations.Options) { o.Secrets = envShadow{o.Secrets} })
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	inst, release := lease(t, r, signalRef)
	defer release()
	if _, err := inst.Env.Secrets.Secret(ctx, "password"); !errors.Is(err, integrations.ErrOperatorSecretWithheld) {
		t.Errorf("an env value under the stored name: %v", err)
	}
	_ = inst.Call(ctx)
	if n := sentWith(e, "env-shadow"); n != 0 {
		t.Errorf("the env value was sent %d times", n)
	}
}

// TestRegistry_LeaseRacesDrainTimeout (M1.6): leases and probes taken
// while swaps retire their slots and a zero-length drain timeout closes them
// race nothing (run under -race), and a slot force-closed between its lease
// and its build is retried on its replacement, never reported as missing or
// misconfigured.
func TestRegistry_LeaseRacesDrainTimeout(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.Settings = func() integrations.Settings { return integrations.Settings{DrainTimeout: time.Nanosecond} }
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := r.SetOverlay(ctx, overlay("integrations.notify.gcpd-roof.channel", "robin-"+strconv.Itoa(i))); err != nil {
				t.Error(err)
				return
			}
		}
	})
	var lessees sync.WaitGroup
	for range 4 {
		lessees.Go(func() {
			for range 500 {
				inst, release, err := integrations.Lease[integrations.Instance](ctx, r, signalRef)
				if err != nil {
					t.Error(err)
					return
				}
				if f, ok := inst.(*integrationstest.FakeInstance); !ok || f.Config.Ref != signalRef {
					t.Errorf("a lease got %T", inst)
				}
				release()
			}
		})
	}
	lessees.Go(func() {
		for range 300 {
			h, err := r.Health(ctx, signalRef)
			if err != nil || h.Status == integrations.HealthMisconfigured {
				t.Errorf("Health while swapping = %+v, %v", h, err)
				return
			}
		}
	})
	lessees.Wait()
	close(stop)
	wg.Wait()
}

// TestRegistry_SetOverlayAfterClose: a closed Registry swaps nothing.
func TestRegistry_SetOverlayAfterClose(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.notify.gcpd-roof.channel", "batcave")); !errors.Is(err, integrations.ErrClosed) {
		t.Errorf("SetOverlay after Close: %v", err)
	}
}

// TestRegistry_DefaultDestinationSecretNotServedOnceSet: a secret stored
// with every destination at the adapter default is bound to "no
// destination", not to any: once a layer sets a host it is withheld, and a
// row that recorded no SecretsOrigin at all serves none.
func TestRegistry_DefaultDestinationSecretNotServedOnceSet(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, integrations.InstanceConfig{Ref: oracleRef, Adapter: oracle, Enabled: true, Settings: map[string]any{}}, cred("alfred-1"))
	if row, _ := e.stored(t, oracleRef); row.SecretsOrigin != "adapter-default" {
		t.Fatalf("SecretsOrigin of an instance at the adapter defaults = %q", row.SecretsOrigin)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK || sentWith(e, "alfred-1") != 1 {
		t.Fatalf("Health at the adapter defaults = %+v", h)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.metadata.clocktower.host", "192.0.2.60")); err != nil {
		t.Fatal(err)
	}
	inst, release := lease(t, r, oracleRef)
	if _, err := inst.Env.Secrets.Secret(ctx, "password"); err == nil {
		t.Error("a secret stored for the adapter defaults followed an operator host")
	}
	release()
	// The password is optional: the probe runs, and the instance reports the
	// withheld secret as itself.
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthDegraded || h.Message != integrations.ReasonStoredSecretWithheld+": password" {
		t.Errorf("Health with an optional stored secret withheld = %+v", h)
	}
	// A Test of the same config builds the instance the Put would leave:
	// the secret is kept and still withheld, so nothing authenticates.
	if res, err := r.Test(manager(), current(t, r, oracleRef), nil); err != nil || res.OK {
		t.Errorf("a Test beside the operator's host = %+v, %v", res, err)
	}
	if n := sentWith(e, "alfred-1"); n != 1 {
		t.Errorf("the stored secret was sent to the operator's host (%d sends)", n)
	}

	// A row that recorded no SecretsOrigin serves no stored secret, even
	// at the adapter defaults.
	e2 := newEnv(t)
	mustPut(t, admin(), e2.registry(t, nil, nil), integrations.InstanceConfig{Ref: oracleRef, Adapter: oracle, Enabled: true, Settings: map[string]any{}}, cred("alfred-1"))
	rewriteRow(t, e2, oracleRef, func(row *integrations.StoredInstance) { row.SecretsOrigin = "" })
	unrecorded, release := lease(t, e2.registry(t, nil, nil), oracleRef)
	defer release()
	if _, err := unrecorded.Env.Secrets.Secret(ctx, "password"); err == nil {
		t.Error("a row without a SecretsOrigin served its stored secret")
	}

	// The importer refuses a record that holds a secret but names no
	// SecretsOrigin.
	im := integrations.NativeImporter(e2.ks, integrations.ImportAudit(e2.rec))
	b := e2.ks.NewBatch()
	defer func() { _ = b.Close() }()
	rec := integrations.StoredInstance{Config: integrations.InstanceConfig{
		Ref: integrations.Ref{Kind: integrations.KindNotify, Instance: "arkham"}, Adapter: batsignal, Enabled: true,
		Settings: map[string]any{"password": secrets.RefTo("integrations.notify.arkham.password")},
	}}
	if err := im.ImportInstance(ctx, b, rec); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("an import with a secret and no SecretsOrigin: %v", err)
	}
	rec.SecretsOrigin = "adapter-default"
	if err := im.ImportInstance(ctx, b, rec); err != nil {
		t.Errorf("an import bound to the adapter defaults: %v", err)
	}
}

// parkingStore parks the next secret read (Get, or Meta when onMeta is set)
// until the test releases it.
type parkingStore struct {
	integrations.SecretStore
	onMeta bool
	mu     sync.Mutex
	park   chan struct{}
	parked chan struct{}
}

// arm parks the next Get: parked is closed once it parks, and release lets
// it read.
func (s *parkingStore) arm() (parked <-chan struct{}, release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.park, s.parked = make(chan struct{}), make(chan struct{})
	p := s.park
	return s.parked, func() { close(p) }
}

// wait parks the calling read when one is armed for it.
func (s *parkingStore) wait(meta bool) {
	s.mu.Lock()
	if s.park == nil || s.onMeta != meta {
		s.mu.Unlock()
		return
	}
	park, parked := s.park, s.parked
	s.park, s.parked = nil, nil
	s.mu.Unlock()
	close(parked)
	<-park
}

func (s *parkingStore) Get(ctx context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	s.wait(false)
	return s.SecretStore.Get(ctx, n)
}

func (s *parkingStore) Meta(ctx context.Context, n secrets.Name) (secrets.Meta, error) {
	s.wait(true)
	return s.SecretStore.Meta(ctx, n)
}

// TestRegistry_ParkedSecretReadNeverGetsTheMovedSecret (SEC-15, M1.6): a
// draining instance's stored-secret read and a Put that moves the instance
// and writes a new secret do not interleave: the Put cannot commit while the
// read is in flight, so the read returns the secret written for the old
// destination, and every read after the move is refused.
func TestRegistry_ParkedSecretReadNeverGetsTheMovedSecret(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	ps := &parkingStore{}
	r := e.registry(t, nil, func(o *integrations.Options) {
		ps.SecretStore = o.Secrets
		o.Secrets = ps
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	held, release := lease(t, r, signalRef)
	defer release()
	moved := withURL(current(t, r, signalRef), "http://192.0.2.99/")

	type read struct {
		v   string
		err error
	}
	parked, unpark := ps.arm()
	got := make(chan read, 1)
	go func() {
		v, err := held.Env.Secrets.Secret(ctx, "password")
		got <- read{v.Reveal(), err}
	}()
	<-parked
	putDone := make(chan error, 1)
	go func() {
		_, err := r.Put(admin(), moved, cred("alfred-2"))
		putDone <- err
	}()
	committedWhileParked := false
	var putErr error
	select {
	case putErr = <-putDone:
		committedWhileParked = true
	case <-time.After(300 * time.Millisecond):
	}
	unpark()
	res := <-got
	if !committedWhileParked {
		putErr = <-putDone
	}
	if putErr != nil {
		t.Fatalf("the move: %v", putErr)
	}
	if committedWhileParked {
		t.Error("a Put committed while a stored-secret read was in flight")
	}
	if res.v == "alfred-2" {
		t.Fatal("the parked read of the old instance returned the secret written for the new destination")
	}
	if res.err == nil && res.v != "alfred-1" {
		t.Errorf("the parked read = %q", res.v)
	}
	if _, err := held.Env.Secrets.Secret(ctx, "password"); err == nil || integrations.ClassOf(ctx, err) != integrations.ClassRefusedLocal {
		t.Errorf("a read by the old instance after the move: %v", err)
	}
}

// TestRegistry_LocalBoundToItsDestination (SEC-15, SEC2-7): Local trusts
// the effective destination an admin granted it for; once the operator
// moves the host, the new destination is not trusted through Local (the
// pair is not wholly operator-set: the port is stored).
func TestRegistry_LocalBoundToItsDestination(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, integrations.InstanceConfig{
		Ref: oracleRef, Adapter: oracle, Enabled: true, Local: true,
		Settings: map[string]any{"host": "192.0.2.60", "port": 9000},
	}, nil)
	_, release := lease(t, r, oracleRef)
	release()
	if tr := lastSpec(t, e, oracleRef).Trust; !slices.Equal(tr.PrivateOrigins, []string{"smtp://192.0.2.60:9000"}) {
		t.Fatalf("Local trust = %v", tr)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.metadata.clocktower.host", "192.0.2.61")); err != nil {
		t.Fatal(err)
	}
	_, release = lease(t, r, oracleRef)
	release()
	if tr := lastSpec(t, e, oracleRef).Trust; len(tr.PrivateOrigins) != 0 {
		t.Errorf("Local followed the operator's host: %v", tr)
	}
}

// TestOperatorSecret_AdminSaveDuringOperatorURLKeepsApproval (R8-5): an
// admin's Save while the operator supplies the whole URL neither approves
// the stored URL under the lock (the admin never saw it) nor forgets an
// approval an earlier admin Save recorded for it. Once the operator's URL is
// removed, the operator password is served at the URL an admin chose and
// withheld at one a manager chose.
func TestOperatorSecret_AdminSaveDuringOperatorURLKeepsApproval(t *testing.T) {
	ctx := context.Background()
	pass := overlay("integrations.notify.gcpd-roof.password", "env-signal-pass")
	movedPass := overlay(
		"integrations.notify.gcpd-roof.url", "http://192.0.2.99/",
		"integrations.notify.gcpd-roof.password", "env-signal-pass",
	)
	for _, tc := range []struct {
		name   string
		by     context.Context
		served bool
	}{
		{"an admin's URL", admin(), true},
		{"a manager's URL", manager(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			r := e.registry(t, pass, nil)
			mustPut(t, tc.by, r, signal("http://192.0.2.20/"), nil)
			if err := r.SetOverlay(ctx, movedPass); err != nil {
				t.Fatal(err)
			}
			if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK || !slices.Equal(e.credentialsSentTo("http://192.0.2.99/"), []string{"env-signal-pass"}) {
				t.Fatalf("Health at the operator's URL = %+v", h)
			}
			renamed := current(t, r, signalRef)
			renamed.Name = "Arkham watch"
			mustPut(t, admin(), r, renamed, nil)
			var want map[string]string
			if tc.served {
				want = map[string]string{"url": "http://192.0.2.20:80"}
			}
			if row, _ := e.stored(t, signalRef); !maps.Equal(row.OperatorSecretOrigin, want) {
				t.Errorf("OperatorSecretOrigin after the admin's Save = %v, want %v", row.OperatorSecretOrigin, want)
			}

			if err := r.SetOverlay(ctx, pass); err != nil {
				t.Fatal(err)
			}
			h, _ := r.Health(ctx, signalRef)
			sent := e.credentialsSentTo("http://192.0.2.20/")
			switch {
			case tc.served && (h.Status != integrations.HealthOK || !slices.Equal(sent, []string{"env-signal-pass"})):
				t.Errorf("Health back at the admin's URL = %+v, sent %v", h, sent)
			case !tc.served && (h.Status != integrations.HealthDegraded || h.Message != integrations.ReasonOperatorSecretWithheld+": password" || slices.Contains(sent, "env-signal-pass")):
				t.Errorf("Health back at the manager's URL = %+v, sent %v", h, sent)
			}
		})
	}
}

// TestOperatorSecret_AdminSaveDuringOperatorHostReplacesApproval pins a
// residual of R8-5's one origin per destination field: with the operator
// setting the host beside an admin's port, the destination is part the
// admin's, so an admin Save records the joined origin and replaces the
// approval of the admin's own host. Once the operator's host is removed the
// password is withheld at that host until an admin saves again.
func TestOperatorSecret_AdminSaveDuringOperatorHostReplacesApproval(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	pass := overlay("integrations.metadata.clocktower.password", "env-oracle-pass")
	r := e.registry(t, pass, nil)
	mustPut(t, admin(), r, integrations.InstanceConfig{
		Ref: oracleRef, Adapter: oracle, Enabled: true, Settings: map[string]any{"host": "192.0.2.60", "port": 9000},
	}, nil)
	if err := r.SetOverlay(ctx, overlay(
		"integrations.metadata.clocktower.host", "192.0.2.61",
		"integrations.metadata.clocktower.password", "env-oracle-pass",
	)); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthDegraded {
		t.Errorf("Health beside the operator's host before an admin Save = %+v", h)
	}
	mustPut(t, admin(), r, current(t, r, oracleRef), nil)
	if row, _ := e.stored(t, oracleRef); row.OperatorSecretOrigin["host"] != "smtp://192.0.2.61:9000" {
		t.Errorf("OperatorSecretOrigin = %v, want the joined origin", row.OperatorSecretOrigin)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK {
		t.Errorf("Health beside the operator's host after an admin Save = %+v", h)
	}
	if err := r.SetOverlay(ctx, pass); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthDegraded || h.Message != integrations.ReasonOperatorSecretWithheld+": password" {
		t.Errorf("Health back at the admin's host = %+v", h)
	}
	mustPut(t, admin(), r, current(t, r, oracleRef), nil)
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK {
		t.Errorf("Health after the admin saves again = %+v", h)
	}
	if n := sentWith(e, "env-oracle-pass"); n != 2 {
		t.Errorf("the operator secret was sent %d times, want 2", n)
	}
}

// TestRegistry_ForceClosedBeforeBuildLeaksNothing (M1.6): a probe that took
// its lease before a swap and reaches its build only after the drain timeout
// force-closed the swapped-out slot builds no instance there (one no closer
// would ever Close): every instance built is closed by Close.
func TestRegistry_ForceClosedBeforeBuildLeaksNothing(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	ps := &parkingStore{onMeta: true}
	r := e.registry(t, nil, func(o *integrations.Options) {
		ps.SecretStore = o.Secrets
		o.Secrets = ps
		o.Settings = func() integrations.Settings { return integrations.Settings{DrainTimeout: time.Nanosecond} }
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	parked, unpark := ps.arm()
	probed := make(chan error, 1)
	go func() {
		_, err := r.Health(ctx, signalRef)
		probed <- err
	}()
	<-parked // the probe holds its lease and has not built
	if err := r.SetOverlay(ctx, overlay("integrations.notify.gcpd-roof.channel", "robin")); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(ctx); err != nil { // waits for the swapped-out slot's closer
		t.Fatal(err)
	}
	unpark()
	if err := <-probed; !errors.Is(err, integrations.ErrClosed) {
		t.Errorf("the probe of the force-closed slot = %v, want ErrClosed", err)
	}
	for i, inst := range e.fake.Built() {
		if inst.Closed() == 0 {
			t.Errorf("instance %d (%s) was built and never closed", i, inst.Config.Name)
		}
	}
}
