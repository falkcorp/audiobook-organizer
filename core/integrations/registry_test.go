// file: core/integrations/registry_test.go
// version: 1.0.0
// guid: 15dbbe11-ae4b-4f69-8aa6-03dc9b368cd6
// last-edited: 2026-11-11

package integrations_test

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"maps"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
)

// TestRegistry_RejectsForeignAdapterPrefix (M1.4 step 1, I7): an adapter ID
// carries its registering module's prefix; core.* comes from
// app.Options.Adapters only; a descriptor that is not a valid adapter fails
// registration.
func TestRegistry_RejectsForeignAdapterPrefix(t *testing.T) {
	e := newEnv(t)
	desc := func(id integrations.AdapterID) integrations.Descriptor {
		return e.fake.Descriptor(integrations.Meta{ID: id, Kind: integrations.KindNotify, Settings: schema[batsignalSettings](t)})
	}
	for _, c := range []struct {
		origin string
		d      integrations.Descriptor
	}{
		{"hello", desc("core.batsignal")},
		{"hello", desc("gotham.batsignal")},
		{"core", desc("hello.batsignal")},
		{"hello", desc("batsignal")},
		{"Hello", desc("Hello.batsignal")},
		{"hello", desc("hello.Bat-Signal")},
		{"hello", integrations.Descriptor{}},
		{"hello", e.fake.Descriptor(integrations.Meta{ID: "hello.belt", Kind: "gadgets", Settings: schema[batsignalSettings](t)})},
		{"hello", e.fake.Descriptor(integrations.Meta{ID: "hello.belt", Kind: integrations.KindNotify})},
		{"hello", e.fake.Descriptor(integrations.Meta{ID: "hello.belt", Kind: integrations.KindNotify, Settings: schema[struct {
			Token string `json:"token"`
		}](t)})},
		{"hello", integrations.NewDescriptor[integrations.Instance](integrations.Meta{ID: "hello.belt", Kind: integrations.KindNotify, Settings: schema[batsignalSettings](t)}, nil)},
	} {
		r, err := integrations.New(e.options())
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Register(c.origin, c.d); !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("Register(%q, %q): err = %v, want ErrInvalid", c.origin, c.d.Meta().ID, err)
		}
		if n := len(slices.Collect(r.Adapters(integrations.KindNotify))); n != 0 {
			t.Errorf("Register(%q, %q) registered %d adapters", c.origin, c.d.Meta().ID, n)
		}
	}

	r, err := integrations.New(e.options())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register("hello", desc("hello.batsignal")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("core", desc("core.batsignal")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("hello", desc("hello.batsignal")); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("a duplicate registered: err = %v", err)
	}
	var got []string
	for m := range r.Adapters(integrations.KindNotify) {
		got = append(got, string(m.ID)+"@"+m.Origin)
	}
	if want := []string{"core.batsignal@core", "hello.batsignal@hello"}; !slices.Equal(got, want) {
		t.Errorf("Adapters = %v, want %v", got, want)
	}
	if n := len(slices.Collect(r.Adapters(integrations.KindAI))); n != 0 {
		t.Errorf("Adapters(ai) = %d", n)
	}
	if err := r.Load(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("hello", desc("hello.late")); err == nil {
		t.Error("Register after Load succeeded")
	}
	if err := r.Load(context.Background(), nil); err == nil {
		t.Error("a second Load succeeded")
	}
	if _, err := integrations.New(integrations.Options{}); err == nil {
		t.Error("New without its stores succeeded")
	}
}

// TestRegistry_UnknownAdapterRowRetained (M1.4 step 2): a row whose adapter
// the binary does not register is kept, served as misconfigured "adapter not
// in this binary", and never rewritten.
func TestRegistry_UnknownAdapterRowRetained(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: "lazarus-pit"}
	b := e.ks.NewBatch()
	if _, err := integrations.NativeInstanceStore(e.ks).Put(ctx, b, integrations.StoredInstance{Config: integrations.InstanceConfig{
		Ref: ref, Adapter: "hello.lazarus", Enabled: true, Origin: integrations.OriginUI,
		Settings: map[string]any{"url": "http://192.0.2.40/"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	before := rawRows(t, e)

	r := e.registry(t, nil, nil)
	cfg, ok := r.Config(ref)
	if !ok || !cfg.Stored || cfg.Adapter != "hello.lazarus" || cfg.Version != 1 {
		t.Fatalf("Config = %+v, %v", cfg, ok)
	}
	h, err := r.Health(ctx, ref)
	if err != nil || h.Status != integrations.HealthMisconfigured || h.Message != integrations.ReasonAdapterNotInBinary {
		t.Errorf("Health = %+v, %v", h, err)
	}
	_, _, err = integrations.Lease[integrations.Instance](ctx, r, ref)
	if !errors.Is(err, integrations.ErrUnknownAdapter) || integrations.ClassOf(ctx, err) != integrations.ClassRefusedLocal {
		t.Errorf("Lease: err = %v (class %v), want ErrUnknownAdapter, refused_local", err, integrations.ClassOf(ctx, err))
	}
	if err := r.SetOverlay(ctx, overlay("integrations.notify.lazarus-pit.url", "http://192.0.2.41/")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Put(admin(), cfg, nil); !errors.Is(err, integrations.ErrUnknownAdapter) {
		t.Errorf("Put of the unknown adapter's instance: err = %v", err)
	}
	if after := rawRows(t, e); !maps.EqualFunc(before, after, bytes.Equal) {
		t.Error("the row of an unknown adapter was rewritten")
	}
	if err := r.Delete(admin(), ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := e.stored(t, ref); ok {
		t.Error("the row survived its Delete")
	}
	if cfg, ok := r.Config(ref); !ok || cfg.Stored {
		t.Errorf("after the Delete the operator value still names it: Config = %+v, %v", cfg, ok)
	}
}

// rawRows is every raw key/value of the integrations keyspace.
func rawRows(t testing.TB, e *env) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for kv, err := range e.ks.ScanPrefix(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out[string(kv.Key)] = bytes.Clone(kv.Value)
	}
	return out
}

// TestRegistry_OverlayNeverPersisted (M1.4 step 2, SIMP2-5): with operator
// values present, no row is written for an operator-only instance, no
// operator secret reaches core secrets, and a Put stores the stored layer,
// never the overlay.
func TestRegistry_OverlayNeverPersisted(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r0 := e.registry(t, nil, nil)
	mustPut(t, admin(), r0, signal("http://192.0.2.20/"), cred("alfred-1"))
	if err := r0.Close(ctx); err != nil {
		t.Fatal(err)
	}

	tower := integrations.Ref{Kind: integrations.KindNotify, Instance: "wayne-tower"}
	ov := overlay(
		"integrations.notify.gcpd-roof.channel", "batcave",
		"integrations.notify.gcpd-roof.password", "env-roof-pass",
		"integrations.notify.wayne-tower.url", "http://192.0.2.30/",
		"integrations.notify.wayne-tower.password", "env-tower-pass",
	)
	r := e.registry(t, ov, func(o *integrations.Options) {
		o.OperatorInstances = map[integrations.Ref]integrations.AdapterID{tower: batsignal}
	})
	cfg, _ := r.Config(signalRef)
	if cfg.Settings["channel"] != "batcave" || cfg.Locked["channel"] == "" || cfg.Locked["password"] == "" {
		t.Fatalf("effective view = %+v", cfg)
	}
	in := cfg
	in.Name = "GCPD roof, night shift"
	mustPut(t, admin(), r, in, nil)

	row, _ := e.stored(t, signalRef)
	if row.Config.Settings["channel"] != "gcpd" || row.Config.Name != "GCPD roof, night shift" {
		t.Errorf("the stored row took the overlay: %+v", row.Config)
	}
	if _, ok := e.stored(t, tower); ok {
		t.Error("an operator-only instance was persisted")
	}
	if got := e.secret(t, signalRef, "password"); got != "alfred-1" {
		t.Errorf("stored secret = %q, want the stored alfred-1", got)
	}
	if got := e.secret(t, tower, "password"); got != "" {
		t.Errorf("the operator secret reached core secrets: %q", got)
	}
	for k, v := range rawRows(t, e) {
		for _, leak := range []string{"batcave", "env-roof-pass", "env-tower-pass", "192.0.2.30"} {
			if bytes.Contains(v, []byte(leak)) {
				t.Errorf("row %q holds the operator value %q", k, leak)
			}
		}
	}
}

// TestRegistry_OverlayRemovedRevertsToStoredRow (M1.4 steps 2 and 4): when
// the operator value goes away the stored row is served again, and the
// instance is swapped.
func TestRegistry_OverlayRemovedRevertsToStoredRow(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if err := r.SetOverlay(ctx, overlay("integrations.notify.gcpd-roof.channel", "batcave")); err != nil {
		t.Fatal(err)
	}
	cfg, _ := r.Config(signalRef)
	if cfg.Settings["channel"] != "batcave" || cfg.Locked["channel"] != "env integrations.notify.gcpd-roof.channel" {
		t.Fatalf("overlaid view = %+v", cfg)
	}
	first, release := lease(t, r, signalRef)
	release()
	if first.Config.Settings["channel"] != "batcave" {
		t.Errorf("the built instance did not get the overlay: %v", first.Config.Settings)
	}
	if err := r.SetOverlay(ctx, nil); err != nil {
		t.Fatal(err)
	}
	cfg, _ = r.Config(signalRef)
	if cfg.Settings["channel"] != "gcpd" || len(cfg.Locked) != 0 {
		t.Errorf("after the overlay went away: %+v", cfg)
	}
	second, release := lease(t, r, signalRef)
	defer release()
	if second == first || second.Config.Settings["channel"] != "gcpd" {
		t.Errorf("the instance was not swapped back to the stored row")
	}
	if row, _ := e.stored(t, signalRef); row.Config.Version != 1 {
		t.Errorf("the overlay's coming and going rewrote the row: version %d", row.Config.Version)
	}
}

// TestRegistry_OperatorOverlayLocksAndTrusts (M1.4 step 2): an operator
// value locks its field and an operator-sourced destination is trusted for
// private networks; a stored destination is trusted only while Local holds.
func TestRegistry_OperatorOverlayLocksAndTrusts(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	_, release := lease(t, r, signalRef)
	release()
	if tr := lastSpec(t, e, signalRef).Trust; len(tr.PrivateOrigins) != 0 {
		t.Errorf("a stored destination without Local is trusted: %v", tr)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.notify.gcpd-roof.url", "http://192.0.2.50:9000/")); err != nil {
		t.Fatal(err)
	}
	cfg, _ := r.Config(signalRef)
	if cfg.Locked["url"] != "env integrations.notify.gcpd-roof.url" || cfg.Settings["url"] != "http://192.0.2.50:9000/" {
		t.Fatalf("overlaid view = %+v", cfg)
	}
	_, release = lease(t, r, signalRef)
	release()
	if tr := lastSpec(t, e, signalRef).Trust; !slices.Equal(tr.PrivateOrigins, []string{"http://192.0.2.50:9000"}) {
		t.Errorf("operator destination trust = %v", tr)
	}

	// Local, granted by an admin, trusts the stored origin it was granted for.
	o := integrations.InstanceConfig{
		Ref: oracleRef, Adapter: oracle, Enabled: true, Local: true,
		Settings: map[string]any{"host": "192.0.2.60", "port": 9000},
	}
	mustPut(t, admin(), r, o, nil)
	_, release = lease(t, r, oracleRef)
	release()
	if tr := lastSpec(t, e, oracleRef).Trust; !slices.Equal(tr.PrivateOrigins, []string{"tcp://192.0.2.60:9000"}) {
		t.Errorf("Local trust = %v", tr)
	}
}

// lastSpec is the last OutboundSpec built for ref.
func lastSpec(t testing.TB, e *env, ref integrations.Ref) integrations.OutboundSpec {
	t.Helper()
	specs := e.out.Specs()
	for i := len(specs) - 1; i >= 0; i-- {
		if specs[i].Ref == ref {
			return specs[i]
		}
	}
	t.Fatalf("no outbound spec for %s", ref)
	return integrations.OutboundSpec{}
}

// TestRegistry_LockedFieldWriteRefused (M1.3 Put): a Put that changes a
// locked field is refused whole; one that omits it or repeats its effective
// value stores the rest and keeps the field's stored value.
func TestRegistry_LockedFieldWriteRefused(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if err := r.SetOverlay(ctx, overlay(
		"integrations.notify.gcpd-roof.channel", "batcave",
		"integrations.notify.gcpd-roof.password", "env-pass",
	)); err != nil {
		t.Fatal(err)
	}
	cfg, _ := r.Config(signalRef)

	changed := cfg
	changed.Settings = maps.Clone(cfg.Settings)
	changed.Settings["channel"] = "arkham"
	changed.Name = "Arkham"
	if _, err := r.Put(admin(), changed, nil); !errors.Is(err, integrations.ErrFieldLocked) {
		t.Errorf("a locked field's change: err = %v, want ErrFieldLocked", err)
	}
	if _, err := r.Put(admin(), cfg, cred("alfred-2")); !errors.Is(err, integrations.ErrFieldLocked) {
		t.Errorf("a locked secret's re-supply: err = %v, want ErrFieldLocked", err)
	}
	cleared := cfg
	cleared.Settings = maps.Clone(cfg.Settings)
	cleared.Settings["password"] = nil
	if _, err := r.Put(admin(), cleared, nil); !errors.Is(err, integrations.ErrFieldLocked) {
		t.Errorf("a locked secret's clear: err = %v, want ErrFieldLocked", err)
	}
	if row, _ := e.stored(t, signalRef); row.Config.Version != 1 || row.Config.Name != "GCPD roof" {
		t.Errorf("a refused Put wrote: %+v", row.Config)
	}

	omitted := cfg
	omitted.Settings = maps.Clone(cfg.Settings)
	delete(omitted.Settings, "channel")
	omitted.Name = "GCPD roof, renamed"
	mustPut(t, admin(), r, omitted, cred("********"))
	row, _ := e.stored(t, signalRef)
	if row.Config.Settings["channel"] != "gcpd" || row.Config.Name != "GCPD roof, renamed" {
		t.Errorf("after a Put omitting the locked field: %+v", row.Config)
	}
	if got := e.secret(t, signalRef, "password"); got != "alfred-1" {
		t.Errorf("the stored secret under the lock = %q", got)
	}
}

// TestRegistry_EnvOnlyInstanceHasStableID (M1.4 step 2, M1.10): an
// operator-only instance is served under the ID its operator paths name,
// unchanged across reloads, and is not deletable.
func TestRegistry_EnvOnlyInstanceHasStableID(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	tower := integrations.Ref{Kind: integrations.KindNotify, Instance: "wayne-tower"}
	ov := overlay(
		"integrations.notify.wayne-tower.url", "http://192.0.2.30/",
		"integrations.notify.wayne-tower.password", "env-tower-pass",
		"integrations.notify.joker-van.url", "http://192.0.2.31/",
		"integrations.notify.Bad_ID.url", "http://192.0.2.32/",
		"integrations.notify", "http://192.0.2.33/",
		"integrations.gadgets.belt.url", "http://192.0.2.34/",
	)
	r := e.registry(t, ov, func(o *integrations.Options) {
		o.OperatorInstances = map[integrations.Ref]integrations.AdapterID{tower: batsignal}
	})
	var ids []integrations.InstanceID
	for c := range r.Instances(integrations.KindNotify) {
		ids = append(ids, c.Ref.Instance)
		if c.Stored {
			t.Errorf("%s is reported stored", c.Ref)
		}
	}
	if want := []integrations.InstanceID{"joker-van", "wayne-tower"}; !slices.Equal(ids, want) {
		t.Fatalf("Instances = %v, want %v", ids, want)
	}
	first, release := lease(t, r, tower)
	release()
	if err := r.SetOverlay(ctx, ov); err != nil {
		t.Fatal(err)
	}
	second, release := lease(t, r, tower)
	release()
	if second != first {
		t.Error("an unchanged overlay swapped the operator-only instance")
	}
	h, _ := r.Health(ctx, tower)
	if h.Status != integrations.HealthOK {
		t.Errorf("Health = %+v", h)
	}
	if got := e.credentialsSentTo("http://192.0.2.30/"); !slices.Equal(got, []string{"env-tower-pass"}) {
		t.Errorf("credentials sent to the operator URL = %v", got)
	}
	if h, _ := r.Health(ctx, integrations.Ref{Kind: integrations.KindNotify, Instance: "joker-van"}); h.Message != integrations.ReasonAdapterNotInBinary {
		t.Errorf("an unmapped operator instance's health = %+v", h)
	}
	if err := r.Delete(admin(), tower); !errors.Is(err, integrations.ErrFieldLocked) {
		t.Errorf("Delete of an operator-only instance: err = %v, want ErrFieldLocked", err)
	}
	created := mustPut(t, admin(), r, integrations.InstanceConfig{
		Ref: tower, Enabled: true, Settings: map[string]any{"channel": "wayne"},
	}, nil)
	if created.Ref != tower || !created.Stored || created.Adapter != batsignal || created.Settings["url"] != "http://192.0.2.30/" {
		t.Errorf("a row for the operator instance = %+v", created)
	}
}

// TestRegistry_PutWritesRowAndSecretsAtomically (M1.4, M1.5): the row, the
// secrets and the audit records commit in one batch or not at all.
func TestRegistry_PutWritesRowAndSecretsAtomically(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	got := mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if got.Version != 1 || got.Origin != integrations.OriginUI || !got.Stored {
		t.Errorf("Put returned %+v", got)
	}
	if e.secret(t, signalRef, "password") != "alfred-1" {
		t.Error("the secret was not written")
	}
	if want := []string{"core.integration.create", "core.integration.write_secret"}; !slices.Equal(e.actions(t), want) {
		t.Errorf("audit = %v, want %v", e.actions(t), want)
	}

	e.rec.failWith(errors.New("the audit store is out of space"))
	next := got
	next.Settings = maps.Clone(got.Settings)
	next.Settings["channel"] = "arkham"
	if _, err := r.Put(admin(), next, cred("alfred-2")); !errors.Is(err, integrations.ErrUnavailable) {
		t.Fatalf("a Put whose audit fails: err = %v, want ErrUnavailable", err)
	}
	other := signal("http://192.0.2.21/")
	other.Ref.Instance = "wayne-manor"
	if _, err := r.Put(admin(), other, cred("manor-1")); !errors.Is(err, integrations.ErrUnavailable) {
		t.Fatalf("a create whose audit fails: err = %v", err)
	}
	e.rec.failWith(nil)
	row, _ := e.stored(t, signalRef)
	if row.Config.Version != 1 || row.Config.Settings["channel"] != "gcpd" || e.secret(t, signalRef, "password") != "alfred-1" {
		t.Errorf("a failed Put left a partial write: row %+v, secret %q", row.Config, e.secret(t, signalRef, "password"))
	}
	if _, ok := e.stored(t, other.Ref); ok || e.secret(t, other.Ref, "password") != "" {
		t.Error("a failed create left a row or a secret")
	}
	if cfg, _ := r.Config(signalRef); cfg.Version != 1 {
		t.Errorf("a failed Put was served: version %d", cfg.Version)
	}
	if _, err := r.Put(admin(), signal("http://192.0.2.20/"), nil); !errors.Is(err, integrations.ErrVersionConflict) {
		t.Errorf("a stale create: err = %v, want ErrVersionConflict", err)
	}
}

// TestRegistry_StoreLoadFailureServesNone (M1.5): a store that cannot be read
// serves no stored instance and refuses every write; operator-only
// instances are served.
func TestRegistry_StoreLoadFailureServesNone(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r0 := e.registry(t, nil, nil)
	mustPut(t, admin(), r0, signal("http://192.0.2.20/"), cred("alfred-1"))

	tower := integrations.Ref{Kind: integrations.KindNotify, Instance: "wayne-tower"}
	o := e.options()
	o.Store = brokenStore{InstanceStore: o.Store}
	o.OperatorInstances = map[integrations.Ref]integrations.AdapterID{tower: batsignal}
	r, err := integrations.New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(ctx) }()
	if err := r.Register("hello", e.descriptors(t)...); err != nil {
		t.Fatal(err)
	}
	err = r.Load(ctx, overlay("integrations.notify.wayne-tower.password", "env-tower-pass"))
	if !errors.Is(err, integrations.ErrUnavailable) {
		t.Fatalf("Load over a broken store: err = %v, want ErrUnavailable", err)
	}
	var ids []integrations.InstanceID
	for c := range r.Instances(integrations.KindNotify) {
		ids = append(ids, c.Ref.Instance)
	}
	if !slices.Equal(ids, []integrations.InstanceID{"wayne-tower"}) {
		t.Errorf("Instances = %v, want the operator-only instance alone", ids)
	}
	if _, ok := r.Config(signalRef); ok {
		t.Error("a stored instance is served from an unreadable store")
	}
	if h, err := r.Health(ctx, signalRef); err != nil || h.Status != integrations.HealthMisconfigured || h.Message != integrations.ReasonStoreUnavailable {
		t.Errorf("Health = %+v, %v", h, err)
	}
	if _, _, err := integrations.Lease[integrations.Instance](ctx, r, signalRef); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Errorf("Lease of a stored instance: err = %v", err)
	}
	in := signal("http://192.0.2.20/")
	in.Version = 1
	if _, err := r.Put(admin(), in, nil); !errors.Is(err, integrations.ErrUnavailable) {
		t.Errorf("Put over an unreadable store: err = %v, want ErrUnavailable", err)
	}
	if err := r.Delete(admin(), signalRef); !errors.Is(err, integrations.ErrUnavailable) {
		t.Errorf("Delete over an unreadable store: err = %v, want ErrUnavailable", err)
	}
	if row, _ := e.stored(t, signalRef); row.Config.Version != 1 {
		t.Errorf("the unreadable store was written: %+v", row.Config)
	}
	if _, release := lease(t, r, tower); release == nil {
		t.Error("the operator-only instance is not served")
	} else {
		release()
	}
}

// brokenStore is a store whose Load fails.
type brokenStore struct{ integrations.InstanceStore }

func (brokenStore) Load(context.Context) ([]integrations.StoredInstance, error) {
	return nil, errors.New("pebble: the Batcave flooded")
}

// TestRegistry_BuildDoesNoIO (I5): New, Register, Load, SetOverlay and Put
// build nothing and send nothing; the first lease builds and still sends
// nothing.
func TestRegistry_BuildDoesNoIO(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	for _, ov := range []iter.Seq2[config.Path, config.OperatorValue]{overlay("integrations.notify.gcpd-roof.channel", "batcave"), nil} {
		if err := r.SetOverlay(ctx, ov); err != nil {
			t.Fatal(err)
		}
	}
	if n, s, sent := len(e.fake.Built()), len(e.out.Specs()), len(e.out.Sent()); n+s+sent != 0 {
		t.Fatalf("before any lease: %d builds, %d outbound specs, %d requests", n, s, sent)
	}
	_, release := lease(t, r, signalRef)
	defer release()
	if n, sent := len(e.fake.Built()), len(e.out.Sent()); n != 1 || sent != 0 {
		t.Errorf("the first lease: %d builds, %d requests (want 1, 0)", n, sent)
	}
	_, again := lease(t, r, signalRef)
	again()
	if n := len(e.fake.Built()); n != 1 {
		t.Errorf("a second lease rebuilt: %d builds", n)
	}
}

// TestRegistry_LifecycleEdges covers the reads and the lease refusals the
// named cases do not: unknown refs, a disabled instance, a type mismatch, a
// build failure, the health callback and a closed Registry.
func TestRegistry_LifecycleEdges(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var changes []integrations.HealthStatus
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.HealthChanged = func(_ integrations.Ref, h integrations.Health) { changes = append(changes, h.Status) }
	})
	nowhere := integrations.Ref{Kind: integrations.KindNotify, Instance: "nowhere"}
	if _, ok := r.Config(nowhere); ok {
		t.Error("Config of an unknown ref")
	}
	if _, err := r.Health(ctx, nowhere); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Errorf("Health of an unknown ref: %v", err)
	}
	if _, _, err := integrations.Lease[integrations.Instance](ctx, r, nowhere); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Errorf("Lease of an unknown ref: %v", err)
	}
	off := signal("http://192.0.2.20/")
	off.Enabled = false
	mustPut(t, admin(), r, off, cred("alfred-1"))
	if _, _, err := integrations.Lease[integrations.Instance](ctx, r, signalRef); !errors.Is(err, integrations.ErrDisabled) {
		t.Errorf("Lease of a disabled instance: %v", err)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthDisabled || h.Message != integrations.ReasonInstanceDisabled {
		t.Errorf("Health of a disabled instance = %+v", h)
	}
	cfg, _ := r.Config(signalRef)
	cfg.Enabled = true
	mustPut(t, admin(), r, cfg, nil)
	if _, _, err := integrations.Lease[*integrationsTypeMismatch](ctx, r, signalRef); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Errorf("Lease as the wrong type: %v", err)
	}
	if err := r.ProbeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(changes, []integrations.HealthStatus{integrations.HealthDisabled, integrations.HealthOK}) {
		t.Errorf("health changes = %v", changes)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := integrations.Lease[integrations.Instance](canceled, r, signalRef); !errors.Is(err, context.Canceled) {
		t.Errorf("Lease on a canceled context: %v", err)
	}
	if err := r.ProbeAll(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("ProbeAll on a canceled context: %v", err)
	}
	if err := r.SetOverlay(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("SetOverlay on a canceled context: %v", err)
	}

	broken := newEnv(t)
	broken.fake.BuildErr = errors.New("the Batmobile will not start")
	rb := broken.registry(t, nil, nil)
	mustPut(t, admin(), rb, signal("http://192.0.2.20/"), cred("alfred-1"))
	if _, _, err := integrations.Lease[integrations.Instance](ctx, rb, signalRef); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Errorf("Lease of an instance whose build fails: %v", err)
	}
	if h, _ := rb.Health(ctx, signalRef); h.Status != integrations.HealthMisconfigured || h.Message != integrations.ReasonBuildFailed {
		t.Errorf("Health of an instance whose build fails = %+v", h)
	}

	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := integrations.Lease[integrations.Instance](ctx, r, signalRef); !errors.Is(err, integrations.ErrClosed) {
		t.Errorf("Lease after Close: %v", err)
	}
	if _, err := r.Put(admin(), cfg, nil); !errors.Is(err, integrations.ErrClosed) {
		t.Errorf("Put after Close: %v", err)
	}
	if err := r.Delete(admin(), signalRef); !errors.Is(err, integrations.ErrClosed) {
		t.Errorf("Delete after Close: %v", err)
	}
	if _, err := r.Test(admin(), cfg, nil); !errors.Is(err, integrations.ErrClosed) {
		t.Errorf("Test after Close: %v", err)
	}
	if _, err := r.Health(ctx, signalRef); !errors.Is(err, integrations.ErrClosed) {
		t.Errorf("Health after Close: %v", err)
	}
}

// integrationsTypeMismatch is an Instance type no adapter builds.
type integrationsTypeMismatch struct{ integrations.Instance }
