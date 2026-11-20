// file: core/integrations/validate_test.go
// version: 1.0.1
// guid: 7c1e5a92-3b48-4d06-a9f7-5e20c84b1d63
// last-edited: 2026-11-19

package integrations_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// errNoWildcards is what the test validator refuses a "*" channel with. It is
// deliberately not an ErrInvalid: the Registry makes every refusal one.
var errNoWildcards = errors.New("a channel is never a wildcard")

// seen is one call of a recording validator.
type seen struct {
	adapter  integrations.AdapterID
	settings map[string]any
	stored   map[string]any
}

// recorder is a validator that refuses a wildcard channel and records its
// calls.
type validatorLog struct {
	mu    sync.Mutex
	calls []seen
}

func (v *validatorLog) validate(a integrations.AdapterID, settings, stored map[string]any) error {
	v.mu.Lock()
	v.calls = append(v.calls, seen{a, settings, stored})
	v.mu.Unlock()
	if c, _ := settings["channel"].(string); strings.Contains(c, "*") {
		return errNoWildcards
	}
	return nil
}

func (v *validatorLog) last(t testing.TB) seen {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.calls) == 0 {
		t.Fatal("the validator was never called")
	}
	return v.calls[len(v.calls)-1]
}

func (v *validatorLog) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.calls)
}

func withChannel(inst integrations.InstanceConfig, c string) integrations.InstanceConfig {
	s := map[string]any{}
	maps.Copy(s, inst.Settings)
	s["channel"] = c
	inst.Settings = s
	return inst
}

// TestKindValidator_PutRefusesAndStoresNothing: a kind's validator runs on
// every Put of that kind; its refusal is an ErrInvalid (the 422) whatever it
// returned, and nothing is stored.
func TestKindValidator_PutRefusesAndStoresNothing(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	v := &validatorLog{}
	if err := r.SetValidator(integrations.KindNotify, v.validate); err != nil {
		t.Fatalf("SetValidator: %v", err)
	}

	_, err := r.Put(admin(), withChannel(signal("http://192.0.2.20/"), "*"), cred("alfred-1"))
	if !errors.Is(err, integrations.ErrInvalid) {
		t.Fatalf("Put of a wildcard channel: err = %v, want ErrInvalid", err)
	}
	if !errors.Is(err, errNoWildcards) {
		t.Errorf("the validator's own error is lost: %v", err)
	}
	if _, ok := e.stored(t, signalRef); ok {
		t.Error("a refused Put stored a row")
	}
	if e.secret(t, signalRef, "password") != "" {
		t.Error("a refused Put stored a secret")
	}
	if got := v.last(t); got.adapter != batsignal || got.stored != nil {
		t.Errorf("a create reached the validator as %+v, want adapter %s and no stored row", got, batsignal)
	}

	// A good create passes; an update hands the validator the stored layer.
	created := mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	update := withChannel(created, "arkham")
	if _, err := r.Put(admin(), update, nil); err != nil {
		t.Fatalf("a valid update: %v", err)
	}
	if got := v.last(t); got.stored["channel"] != "gcpd" || got.settings["channel"] != "arkham" {
		t.Errorf("an update reached the validator as settings %v over stored %v", got.settings, got.stored)
	}
	// The refused update changes nothing.
	cur, _ := r.Config(signalRef)
	if _, err := r.Put(admin(), withChannel(cur, "*"), nil); !errors.Is(err, integrations.ErrInvalid) {
		t.Fatalf("a wildcard update: err = %v", err)
	}
	if row, _ := e.stored(t, signalRef); row.Config.Settings["channel"] != "arkham" || row.Config.Version != 2 {
		t.Errorf("a refused update changed the row: %+v", row.Config)
	}
}

// TestKindValidator_OnlyItsKind: another kind's Put never reaches the
// validator, and a kind with no validator is unchanged.
func TestKindValidator_OnlyItsKind(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	v := &validatorLog{}
	if err := r.SetValidator(integrations.KindMetadata, v.validate); err != nil {
		t.Fatal(err)
	}
	mustPut(t, admin(), r, withChannel(signal("http://192.0.2.20/"), "*"), cred("alfred-1"))
	if v.count() != 0 {
		t.Errorf("a notify Put reached the metadata validator %d times", v.count())
	}
}

// TestKindValidator_SeesTheEffectiveLayer: the validator is handed the layer
// the write would serve, the stored settings with the operator overlay applied,
// the same one the schema judges, so an operator value that breaks the kind's
// rule is refused even though the request carries no such value.
func TestKindValidator_SeesTheEffectiveLayer(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, overlay("integrations.notify.gcpd-roof.channel", "*"), nil)
	v := &validatorLog{}
	if err := r.SetValidator(integrations.KindNotify, v.validate); err != nil {
		t.Fatal(err)
	}
	// The request leaves the operator-locked channel out, so only the effective
	// layer carries it.
	inst := signal("http://192.0.2.20/")
	delete(inst.Settings, "channel")
	_, err := r.Put(admin(), inst, cred("alfred-1"))
	if !errors.Is(err, integrations.ErrInvalid) || !errors.Is(err, errNoWildcards) {
		t.Fatalf("a Put whose operator-locked channel is a wildcard: err = %v, want the validator's refusal", err)
	}
	if got := v.last(t); got.settings["channel"] != "*" {
		t.Errorf("the validator saw channel %v, want the operator's %q", got.settings["channel"], "*")
	}
}

// TestKindValidator_StoredIsWhatTheRowServes: the "stored" layer a validator is
// handed is what the row being replaced serves now, the stored layer with the
// operator overlay applied, so a rule that grandfathers a value the row
// already holds sees one an operator locks. Without it the value is in no
// layer the rule can read as already there, and an unrelated Put of the
// instance is refused over a field only the operator can change. A create has
// no row and still gets nil.
func TestKindValidator_StoredIsWhatTheRowServes(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	// retired is the kind's rule: a channel of "retired" is refused unless the
	// row being replaced already serves it.
	var mu sync.Mutex
	var gotStored []map[string]any
	retired := func(_ integrations.AdapterID, settings, stored map[string]any) error {
		mu.Lock()
		gotStored = append(gotStored, stored)
		mu.Unlock()
		if settings["channel"] == "retired" && (stored == nil || stored["channel"] != "retired") {
			return errors.New("channel retired is not accepted")
		}
		return nil
	}
	if err := r.SetValidator(integrations.KindNotify, retired); err != nil {
		t.Fatal(err)
	}
	created := mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))

	// The operator locks the channel to a value the rule would refuse in a
	// request, after the row was stored.
	if err := r.SetOverlay(context.Background(), overlay("integrations.notify.gcpd-roof.channel", "retired")); err != nil {
		t.Fatalf("SetOverlay: %v", err)
	}
	cur, ok := r.Config(signalRef)
	if !ok || cur.Settings["channel"] != "retired" || cur.Locked["channel"] == "" {
		t.Fatalf("the operator overlay did not lock the channel: %+v", cur)
	}
	cur.Name = "GCPD rooftop"
	if _, err := r.Put(admin(), cur, nil); err != nil {
		t.Fatalf("a Put that changes only the name, over a channel the operator locks: %v", err)
	}
	mu.Lock()
	if last := gotStored[len(gotStored)-1]; last["channel"] != "retired" {
		t.Errorf("the validator was handed stored %v, want the layer the row serves (channel retired)", last)
	}
	mu.Unlock()
	// The stored layer itself still holds the value the operator replaced.
	if row, _ := e.stored(t, signalRef); row.Config.Settings["channel"] != "gcpd" || row.Config.Version != created.Version+1 {
		t.Errorf("the stored row = %+v, want channel gcpd at version %d", row.Config, created.Version+1)
	}

	// A create has no row to replace: the same locked value reaches the rule
	// with stored nil and is refused.
	other := signal("http://192.0.2.20/")
	other.Ref.Instance = "gcpd-lab"
	delete(other.Settings, "channel") // the operator's value is the only one
	if err := r.SetOverlay(context.Background(), overlay("integrations.notify.gcpd-lab.channel", "retired")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Put(admin(), other, cred("alfred-1")); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("a create over a locked value the rule refuses: err = %v, want ErrInvalid", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if last := gotStored[len(gotStored)-1]; last != nil {
		t.Errorf("a create reached the validator with stored %v, want nil", last)
	}
}

// TestKindValidator_CannotMutateTheRegistrysMaps: the validator is handed
// copies. Put reads the same maps after the validator returns to decide whether
// the destination moved and so whether the stored credential is still bound to
// where it was granted, so a validator that edits them must not move that
// decision.
func TestKindValidator_CannotMutateTheRegistrysMaps(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	const dest, elsewhere = "http://192.0.2.20/", "http://192.0.2.99/"
	if err := r.SetValidator(integrations.KindNotify, func(_ integrations.AdapterID, settings, stored map[string]any) error {
		settings["url"] = elsewhere
		if stored != nil {
			stored["url"] = elsewhere
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first := mustPut(t, admin(), r, signal(dest), cred("alfred-1"))
	row, _ := e.stored(t, signalRef)
	if row.SecretsOrigin != "url=http://192.0.2.20:80" {
		t.Errorf("SecretsOrigin = %q: the validator's edit of the effective layer reached the credential's binding", row.SecretsOrigin)
	}
	// Replace without resupplying the credential, same destination: it is kept
	// unless the validator's edit of the stored layer made the destination look moved.
	next := withChannel(signal(dest), "gotham")
	next.Version = first.Version
	mustPut(t, admin(), r, next, nil)
	row, _ = e.stored(t, signalRef)
	if _, ok := row.Config.Settings["password"].(secrets.Ref); !ok {
		t.Errorf("the credential was dropped on a same-destination replace: settings = %v", row.Config.Settings)
	}
}

// TestKindValidator_TestRunsIt: Registry.Test applies the same rule as Put,
// so a settings block that could not be saved cannot be tested.
func TestKindValidator_TestRunsIt(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	v := &validatorLog{}
	if err := r.SetValidator(integrations.KindNotify, v.validate); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Test(admin(), withChannel(signal("http://192.0.2.20/"), "*"), cred("alfred-1")); !errors.Is(err, integrations.ErrInvalid) {
		t.Fatalf("Test of a wildcard channel: err = %v, want ErrInvalid", err)
	}
	if len(e.fake.Built()) != 0 {
		t.Error("a refused Test built an instance")
	}
}

// TestKindValidator_ServePathIsUnvalidated: a stored row a validator would
// refuse still loads and serves, because the validator runs on writes only
// (M1.4 step 2: such a row is kept).
func TestKindValidator_ServePathIsUnvalidated(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, withChannel(signal("http://192.0.2.20/"), "*"), cred("alfred-1"))
	if err := r.SetValidator(integrations.KindNotify, func(integrations.AdapterID, map[string]any, map[string]any) error {
		return errors.New("refuse everything")
	}); err != nil {
		t.Fatal(err)
	}
	if h, err := r.Health(ctx, signalRef); err != nil || h.Status != integrations.HealthOK {
		t.Errorf("Health of a stored row the validator would refuse = %+v, %v", h, err)
	}
	// A fresh Registry over the same store, with the validator set before Load.
	r2, err := integrations.New(e.options())
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Register("hello", e.descriptors(t)...); err != nil {
		t.Fatal(err)
	}
	if err := r2.SetValidator(integrations.KindNotify, func(integrations.AdapterID, map[string]any, map[string]any) error {
		return errors.New("refuse everything")
	}); err != nil {
		t.Fatal(err)
	}
	if err := r2.Load(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r2.Close(ctx) })
	if h, err := r2.Health(ctx, signalRef); err != nil || h.Status != integrations.HealthOK {
		t.Errorf("a loaded row under a refusing validator: %+v, %v", h, err)
	}
}

// TestKindValidator_SetValidatorRules: a kind has at most one validator, set
// for a real kind, with a func; it may be set after Load.
func TestKindValidator_SetValidatorRules(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil) // already loaded
	v := &validatorLog{}
	if err := r.SetValidator(integrations.KindNotify, v.validate); err != nil {
		t.Fatalf("SetValidator after Load: %v", err)
	}
	if err := r.SetValidator(integrations.KindNotify, v.validate); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("a second validator for one kind: err = %v, want ErrInvalid", err)
	}
	if err := r.SetValidator(integrations.Kind("scheme"), v.validate); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("a validator for an unknown kind: err = %v, want ErrInvalid", err)
	}
	if err := r.SetValidator(integrations.KindAI, nil); !errors.Is(err, integrations.ErrInvalid) {
		t.Errorf("a nil validator: err = %v, want ErrInvalid", err)
	}
}

// TestKindValidator_ImporterRefusesAndStagesNothing: with ImportValidator the
// importer runs the kind's validator over the non-secret settings of a record
// whose adapter is registered; a refusal wraps ErrInvalid and stages nothing.
// A record whose adapter is not registered is kept, and without the option the
// importer is unchanged.
func TestKindValidator_ImporterRefusesAndStagesNothing(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	v := &validatorLog{}
	if err := r.SetValidator(integrations.KindNotify, v.validate); err != nil {
		t.Fatal(err)
	}
	im := integrations.NativeImporter(e.ks, integrations.ImportAudit(e.rec), integrations.ImportValidator(r))
	stage := func(im integrations.Importer, inst integrations.StoredInstance) (int, error) {
		t.Helper()
		b := e.ks.NewBatch()
		defer func() { _ = b.Close() }()
		if err := im.ImportInstance(ctx, b, inst); err != nil {
			return b.Count(), err
		}
		n := b.Count()
		return n, b.Commit(ctx, storage.Sync)
	}

	bad := gcpdImport()
	bad.Config.Settings = map[string]any{"url": "http://192.0.2.20/", "channel": "*", "password": secrets.RefTo("integrations.notify.gcpd-roof.password")}
	n, err := stage(im, bad)
	if !errors.Is(err, integrations.ErrInvalid) || !errors.Is(err, errNoWildcards) {
		t.Fatalf("an invalid import: err = %v, want ErrInvalid wrapping the validator's error", err)
	}
	if n != 0 {
		t.Errorf("a refused import staged %d writes", n)
	}
	if _, ok := e.stored(t, signalRef); ok {
		t.Error("a refused import stored a row")
	}
	// The validator saw no secret field and no stored row.
	if got := v.last(t); got.stored != nil || got.adapter != batsignal {
		t.Errorf("the import reached the validator as %+v", got)
	} else if _, has := got.settings["password"]; has {
		t.Errorf("the validator was handed a secret field: %v", got.settings)
	}

	if _, err := stage(im, gcpdImport()); err != nil {
		t.Fatalf("a valid import: %v", err)
	}
	if _, ok := e.stored(t, signalRef); !ok {
		t.Error("a valid import stored nothing")
	}

	// An adapter this binary does not register is kept (M1.4 step 2).
	calls := v.count()
	unknown := gcpdImport()
	unknown.Config.Ref.Instance = "arkham-roof"
	unknown.Config.Adapter = "hello.scarecrow"
	unknown.Config.Settings = map[string]any{"channel": "*", "password": secrets.RefTo("integrations.notify.arkham-roof.password")}
	if _, err := stage(im, unknown); err != nil {
		t.Errorf("an import of an unregistered adapter: %v", err)
	}
	if v.count() != calls {
		t.Error("the validator ran for an adapter this binary does not register")
	}

	// A record whose adapter is of another kind than its ref is not judged
	// either, whichever validator the ref's kind has: the Registry reports it.
	var metaCalls int
	if err := r.SetValidator(integrations.KindMetadata, func(integrations.AdapterID, map[string]any, map[string]any) error {
		metaCalls++
		return errNoWildcards
	}); err != nil {
		t.Fatal(err)
	}
	crossed := gcpdImport()
	crossed.Config.Ref = integrations.Ref{Kind: integrations.KindMetadata, Instance: "riddler-roof"}
	crossed.Config.Settings = map[string]any{"url": "http://192.0.2.20/", "channel": "gcpd", "password": secrets.RefTo("integrations.metadata.riddler-roof.password")}
	if _, err := stage(im, crossed); err != nil {
		t.Errorf("an import whose adapter is of another kind than its ref: %v", err)
	}
	if metaCalls != 0 {
		t.Error("the ref kind's validator judged a record whose adapter is of another kind")
	}

	// Without the option the importer validates nothing beyond its own rules.
	plain := integrations.NativeImporter(e.ks, integrations.ImportAudit(e.rec))
	loose := gcpdImport()
	loose.Config.Ref.Instance = "wayne-manor"
	loose.Config.Settings = map[string]any{"url": "http://192.0.2.20/", "channel": "*", "password": secrets.RefTo("integrations.notify.wayne-manor.password")}
	if _, err := stage(plain, loose); err != nil {
		t.Errorf("an import without ImportValidator: %v", err)
	}
}

// TestOptionsProbed_FiresOnEveryProbe: Options.Probed is called for every
// probe, including one that leaves the status where it was, which
// HealthChanged never reports.
func TestOptionsProbed_FiresOnEveryProbe(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var mu sync.Mutex
	var probed []integrations.HealthStatus
	var changed int
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.HealthChanged = func(integrations.Ref, integrations.Health) { changed++ }
		o.Probed = func(ref integrations.Ref, h integrations.Health) {
			if ref != signalRef {
				t.Errorf("Probed named %s", ref)
			}
			mu.Lock()
			probed = append(probed, h.Status)
			mu.Unlock()
		}
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	for range 3 {
		if _, err := r.Health(ctx, signalRef); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ProbeAll(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(probed) != 4 {
		t.Errorf("Probed fired %d times for 4 probes: %v", len(probed), probed)
	}
	for _, s := range probed {
		if s != integrations.HealthOK {
			t.Errorf("Probed saw %v, want ok", s)
		}
	}
	if changed != 1 {
		t.Errorf("HealthChanged fired %d times, want once (the first status)", changed)
	}
}

// TestOptionsProbed_FiresOnAStaticFinding: Probed also reports a result that
// involved no remote call, an instance that is disabled or misconfigured, as
// its doc promises: the health task needs every answer, and a consumer that
// ends a bench on a probe decides for itself which statuses count.
func TestOptionsProbed_FiresOnAStaticFinding(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var mu sync.Mutex
	var probed []integrations.HealthStatus
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.Probed = func(_ integrations.Ref, h integrations.Health) {
			mu.Lock()
			probed = append(probed, h.Status)
			mu.Unlock()
		}
	})
	off := signal("http://192.0.2.20/")
	off.Enabled = false
	mustPut(t, admin(), r, off, cred("alfred-1"))
	h, err := r.Health(ctx, signalRef)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != integrations.HealthDisabled {
		t.Fatalf("a disabled instance's health = %v, want disabled (a finding with no probe)", h.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(probed) != 1 || probed[0] != integrations.HealthDisabled {
		t.Errorf("Probed saw %v for a static finding, want [disabled]", probed)
	}
}
