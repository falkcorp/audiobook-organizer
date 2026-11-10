// file: core/config/apply_test.go
// version: 1.0.7
// guid: 5f60ccc8-90a9-456e-8065-47814de5ed19
// last-edited: 2026-11-10

package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unsafe"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/secrets"
)

// refused asserts that a refused Apply persisted, published and committed
// nothing: the committer was not called, the generation is unchanged and
// every snapshot, row and the boot record are byte-equal.
func refused(t *testing.T, r *rig, before string, calls int) {
	t.Helper()
	if n := r.cm.Calls(); n != calls {
		t.Errorf("committer called %d times, want %d", n, calls)
	}
	if after := r.state(); after != before {
		t.Errorf("a refused Apply changed the store:\nbefore %s\nafter  %s", before, after)
	}
}

// TestApply_RefusesLocked: a key an operator layer supplies is refused with
// *LockedError naming the layer.
func TestApply_RefusesLocked(t *testing.T) {
	r := started(t, rigOpts{o: config.Options{Flags: map[config.Path]string{kLevel: "warn"}}})
	before := r.state()
	_, err := r.apply(admin(), map[config.Path]string{kLevel: `"debug"`})
	var le *config.LockedError
	if !errors.As(err, &le) || !errors.Is(err, config.ErrLocked) || le.Path != kLevel || le.By != "flag core.batcave.level" {
		t.Fatalf("err = %v, want *LockedError{%s, flag core.batcave.level}", err, kLevel)
	}
	refused(t, r, before, 0)
	// The same through the env layer names the variable.
	r = started(t, rigOpts{env: map[config.Path]string{kCount: `5`}})
	before = r.state()
	_, err = r.apply(admin(), nil, kCount)
	if !errors.As(err, &le) || le.By != "env GOTHAM_CORE_BATCAVE_GADGET_COUNT" {
		t.Fatalf("unset of an env-locked key: err = %v", err)
	}
	refused(t, r, before, 0)
}

// TestApply_RefusesProtected: each protection class refuses an actor below
// it with *ProtectedError naming the class; ProtectOperator refuses everyone.
func TestApply_RefusesProtected(t *testing.T) {
	// Every class refuses an Unset exactly as it refuses a Set: removing a
	// stored value changes the key as much as writing one.
	notInteractive := admin()
	notInteractive.Interactive = false
	cases := []struct {
		name  string
		a     config.Actor
		key   config.Path
		val   string
		need  config.Protection
		unset bool // unset a stored value instead of setting val (T4 review MINOR-6)
	}{
		{"interactive key, non-interactive actor", notInteractive, kSignal, `"spotlight"`, config.ProtectInteractive, false},
		{"admin key, no core:admin", manager(), kPath, `"/cave"`, config.ProtectAdmin, false},
		{"admin key, core:admin but not interactive", notInteractive, kPath, `"/cave"`, config.ProtectAdmin, false},
		{"owner key, no core:admin", manager(), kOwner, `"bruce@example.com"`, config.ProtectOwner, false},
		{"operator key, admin", admin(), kVault, `"arkham"`, config.ProtectOperator, false},
		{"unset interactive key, non-interactive actor", notInteractive, kSignal, `"spotlight"`, config.ProtectInteractive, true},
		{"unset admin key, no core:admin", manager(), kPath, `"/cave"`, config.ProtectAdmin, true},
		{"unset owner key, no core:admin", manager(), kOwner, `"bruce@example.com"`, config.ProtectOwner, true},
		{"unset operator key, admin", admin(), kVault, `"arkham"`, config.ProtectOperator, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ro := rigOpts{}
			set := map[config.Path]string{c.key: c.val}
			var unset []config.Path
			if c.unset {
				// A stored value to remove, so a skipped check would delete it.
				ro.rows, set, unset = map[config.Path]string{c.key: c.val}, nil, []config.Path{c.key}
			}
			r := started(t, ro)
			before := r.state()
			_, err := r.apply(c.a, set, unset...)
			var pe *config.ProtectedError
			if !errors.As(err, &pe) || !errors.Is(err, config.ErrProtected) || pe.Path != c.key || pe.Need != c.need {
				t.Fatalf("err = %v, want *ProtectedError{%s, %s}", err, c.key, c.need)
			}
			refused(t, r, before, 0)
		})
	}
	// The meets rule's positive side: each class admits the actor that has it.
	r := started(t, rigOpts{})
	ok := manager()
	if _, err := r.apply(ok, map[config.Path]string{kSignal: `"spotlight"`}); err != nil {
		t.Errorf("interactive manager on an interactive key: %v", err)
	}
	if _, err := r.apply(admin(), map[config.Path]string{kPath: `"/cave"`, kOwner: `"bruce@example.com"`}); err != nil {
		t.Errorf("interactive admin, no owner set, on admin and owner keys: %v", err)
	}
}

// TestApply_RefusesForbidden: without the section's write permission the
// patch is refused with ErrForbidden; a module section asks for its own
// namespace's permission.
func TestApply_RefusesForbidden(t *testing.T) {
	r := started(t, rigOpts{})
	before := r.state()
	noCore := config.Actor{Subject: "joker", Interactive: true, Can: func(p string) bool { return p == "gotham:settings.manage" }}
	if _, err := r.apply(noCore, map[config.Path]string{kLevel: `"warn"`}); !errors.Is(err, config.ErrForbidden) || !strings.Contains(err.Error(), "core:settings.manage") {
		t.Fatalf("core key without core:settings.manage: err = %v", err)
	}
	noModule := config.Actor{Subject: "joker", Interactive: true, Can: func(p string) bool { return p == "core:settings.manage" }}
	if _, err := r.apply(noModule, map[config.Path]string{kMax: `20`}); !errors.Is(err, config.ErrForbidden) || !strings.Contains(err.Error(), "gotham:settings.manage") {
		t.Fatalf("module key without gotham:settings.manage: err = %v", err)
	}
	if _, err := r.apply(config.Actor{Interactive: true}, map[config.Path]string{kLevel: `"warn"`}); !errors.Is(err, config.ErrForbidden) {
		t.Fatalf("an actor with no Can: err = %v", err)
	}
	refused(t, r, before, 0)
	if _, err := r.apply(noCore, map[config.Path]string{kMax: `20`}); err != nil {
		t.Fatalf("module key with its own permission: %v", err)
	}
}

// TestApply_RefusesUnknownKey: an unregistered path, a section path and a
// passthrough path are unknown.
func TestApply_RefusesUnknownKey(t *testing.T) {
	r := started(t, rigOpts{o: config.Options{Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.") }}})
	before := r.state()
	for _, p := range []config.Path{"core.batcave.utility_belt", configtest.BatcavePath, "core.batcave.gadget", ptBaseURL, "modules.gotham.watch.min.deep"} {
		if _, err := r.apply(admin(), map[config.Path]string{p: `"x"`}); !errors.Is(err, config.ErrUnknownKey) {
			t.Errorf("Apply %s: err = %v, want ErrUnknownKey", p, err)
		}
		if _, err := r.apply(admin(), nil, p); !errors.Is(err, config.ErrUnknownKey) {
			t.Errorf("Unset %s: err = %v, want ErrUnknownKey", p, err)
		}
	}
	refused(t, r, before, 0)
}

// TestApply_RefusesInvalid: a value failing the schema, the type or the
// section's Validator is refused with ErrInvalid and *FieldError on the key.
func TestApply_RefusesInvalid(t *testing.T) {
	cases := []struct {
		set   map[config.Path]string
		unset []config.Path
		key   config.Path
	}{
		{set: map[config.Path]string{kLevel: `"loud"`}, key: kLevel},                               // enum
		{set: map[config.Path]string{kWorkers: `0`}, key: kWorkers},                                // minimum
		{set: map[config.Path]string{kCount: `"three"`}, key: kCount},                              // type
		{set: map[config.Path]string{kInterval: `90`}, key: kInterval},                             // a duration is a string
		{set: map[config.Path]string{kMin: `50`}, key: kMin},                                       // Validator: min above max
		{set: map[config.Path]string{kAPIKey: `""`}, key: kAPIKey},                                 // an empty secret
		{set: map[config.Path]string{kAPIKey: `42`}, key: kAPIKey},                                 // a secret is a JSON string
		{set: map[config.Path]string{kLevel: `"warn"`}, unset: []config.Path{kLevel}, key: kLevel}, // both
		{set: map[config.Path]string{kLevel: `not json`}, key: kLevel},
	}
	for _, c := range cases {
		r := started(t, rigOpts{})
		before := r.state()
		_, err := r.apply(admin(), c.set, c.unset...)
		var fe *config.FieldError
		if !errors.Is(err, config.ErrInvalid) || !errors.As(err, &fe) || fe.Path != c.key {
			t.Errorf("Apply %v unset %v: err = %v, want ErrInvalid with a FieldError on %s", c.set, c.unset, err, c.key)
		}
		refused(t, r, before, 0)
	}
}

// TestApply_RefusalOrder: unknown, then permission, then protection, then
// the lock, then the values; each stage names every key it refuses.
func TestApply_RefusalOrder(t *testing.T) {
	r := started(t, rigOpts{o: config.Options{Flags: map[config.Path]string{kLevel: "warn"}}})
	noModule := manager()
	noModule.Can = func(p string) bool { return p == "core:settings.manage" }
	steps := []struct {
		set  map[config.Path]string
		a    config.Actor
		want error
	}{
		{map[config.Path]string{"core.batcave.nope": `1`, kMax: `20`, kPath: `"/x"`, kLevel: `"debug"`, kCount: `"bad"`}, noModule, config.ErrUnknownKey},
		{map[config.Path]string{kMax: `20`, kPath: `"/x"`, kLevel: `"debug"`, kCount: `"bad"`}, noModule, config.ErrForbidden},
		{map[config.Path]string{kPath: `"/x"`, kLevel: `"debug"`, kCount: `"bad"`}, manager(), config.ErrProtected},
		{map[config.Path]string{kLevel: `"debug"`, kCount: `"bad"`}, manager(), config.ErrLocked},
		{map[config.Path]string{kCount: `"bad"`}, manager(), config.ErrInvalid},
	}
	for i, s := range steps {
		_, err := r.apply(s.a, s.set)
		if !errors.Is(err, s.want) {
			t.Errorf("step %d: err = %v, want %v", i, err, s.want)
		}
		for _, other := range []error{config.ErrUnknownKey, config.ErrForbidden, config.ErrProtected, config.ErrLocked, config.ErrInvalid} {
			if other != s.want && errors.Is(err, other) {
				t.Errorf("step %d: err also matches %v", i, other)
			}
		}
	}
	_, err := r.apply(manager(), map[config.Path]string{kPath: `"/x"`, kOwner: `"x@example.com"`})
	var pe *config.ProtectedError
	if !errors.As(err, &pe) || strings.Count(err.Error(), "needs") != 2 {
		t.Errorf("two protected keys: err = %v, want both named", err)
	}
	if r.cm.Calls() != 0 {
		t.Errorf("committer called %d times", r.cm.Calls())
	}
}

// TestApply_SettingsManageCannotChangeAdminKeys (fixture half; the kernel
// half runs over the real sign-in, proxy and updater keys).
func TestApply_SettingsManageCannotChangeAdminKeys(t *testing.T) {
	r := started(t, rigOpts{})
	before := r.state()
	_, err := r.apply(manager(), map[config.Path]string{kPath: `"/cave"`})
	var pe *config.ProtectedError
	if !errors.As(err, &pe) || pe.Need != config.ProtectAdmin {
		t.Fatalf("err = %v, want *ProtectedError{Need: ProtectAdmin}", err)
	}
	refused(t, r, before, 0)
}

// TestOwnerTrustRootKeysAreOwnerProtected (fixture half): with an owner
// set, an interactive core:admin actor without owner proof is refused an
// owner key; with the proof, or with no owner set, it succeeds (SEC3-6).
func TestOwnerTrustRootKeysAreOwnerProtected(t *testing.T) {
	var ownerSet atomic.Bool
	ownerSet.Store(true)
	r := started(t, rigOpts{o: config.Options{OwnerSet: ownerSet.Load}})
	before := r.state()
	_, err := r.apply(admin(), map[config.Path]string{kOwner: `"bruce@example.com"`})
	var pe *config.ProtectedError
	if !errors.As(err, &pe) || pe.Need != config.ProtectOwner {
		t.Fatalf("owner set, no proof: err = %v, want *ProtectedError{Need: ProtectOwner}", err)
	}
	refused(t, r, before, 0)
	proof := admin()
	proof.Owner = true
	if _, err := r.apply(proof, map[config.Path]string{kOwner: `"bruce@example.com"`}); err != nil {
		t.Fatalf("owner proof: %v", err)
	}
	proof.Interactive = false
	if _, err := r.apply(proof, map[config.Path]string{kOwner: `"selina@example.com"`}); !errors.Is(err, config.ErrProtected) {
		t.Fatalf("owner proof without an interactive admin session: err = %v", err)
	}
	ownerSet.Store(false)
	if _, err := r.apply(admin(), map[config.Path]string{kOwner: `"selina@example.com"`}); err != nil {
		t.Fatalf("no owner set: checked as ProtectAdmin, so an admin succeeds: %v", err)
	}
}

// TestAuthOff_ProtectedKeysRefused (fixture half): the auth-disabled
// principal changes no key at ProtectAdmin or higher (SEC4-8).
func TestAuthOff_ProtectedKeysRefused(t *testing.T) {
	r := started(t, rigOpts{})
	before := r.state()
	for _, k := range []config.Path{kPath, kOwner, kVault, kSignal} {
		if _, err := r.apply(authOff(), map[config.Path]string{k: `"x"`}); !errors.Is(err, config.ErrProtected) {
			t.Errorf("auth off, %s: err = %v, want ErrProtected", k, err)
		}
	}
	refused(t, r, before, 0)
}

// TestApply_RestartKeyNotPublished: a restart key and a boot key are
// committed and Pending, Get keeps the boot value, RestartPending names them.
func TestApply_RestartKeyNotPublished(t *testing.T) {
	r := started(t, rigOpts{})
	var calls atomic.Int32
	stop := r.cave.OnChange(func(context.Context, *configtest.Batcave, *configtest.Batcave) { calls.Add(1) })
	defer stop()
	gen := r.cave.Generation()
	res, err := r.apply(admin(), map[config.Path]string{kWorkers: `8`, kMode: `"day"`})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.cave.Get(); got.Workers != 4 || got.Mode != "night" {
		t.Errorf("Get = workers %d mode %s, want the boot values 4, night", got.Workers, got.Mode)
	}
	if got := r.cave.Pending(); got.Workers != 8 || got.Mode != "day" {
		t.Errorf("Pending = workers %d mode %s, want 8, day", got.Workers, got.Mode)
	}
	if want := []config.Path{kMode, kWorkers}; !slices.Equal(res.RestartPending, want) {
		t.Errorf("RestartPending = %v, want %v", res.RestartPending, want)
	}
	if r.cave.Generation() != gen {
		t.Errorf("Handle.Generation moved to %d though the effective value did not change", r.cave.Generation())
	}
	if res.Generation != 1 || r.st.Generation() != 1 {
		t.Errorf("store generation %d / %d, want 1", res.Generation, r.st.Generation())
	}
	for _, k := range []config.Path{kWorkers, kMode} {
		if ks, _ := r.st.Lookup(k); !ks.Pending {
			t.Errorf("%s: KeyState.Pending false", k)
		}
	}
	cms := r.cm.Commits()
	if len(cms) != 1 || len(cms[0].Set) != 1 || cms[0].Set[0].Path != kWorkers || !slices.Equal(cms[0].Boot, []config.Path{kMode}) {
		t.Fatalf("commits = %+v, want workers staged and mode named as a boot key", cms)
	}
	if rec := string(r.record()); !strings.Contains(rec, `"core.batcave.mode": "day"`) {
		t.Errorf("boot record = %s, want mode day", rec)
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("OnChange called %d times for a restart-only change", calls.Load())
	}
	// Setting them back to the effective values clears the pending state.
	res, err = r.apply(admin(), map[config.Path]string{kWorkers: `4`, kMode: `"night"`})
	if err != nil || len(res.RestartPending) != 0 {
		t.Fatalf("revert: %v, RestartPending %v", err, res.RestartPending)
	}
}

// TestApply_CommitFailureNothingPublished: a Commit error publishes
// nothing, leaves the generation unchanged and restores the boot record.
func TestApply_CommitFailureNothingPublished(t *testing.T) {
	for _, prior := range []string{"", `{"format":"mediacore-boot/1","gen":3,"values":{"core.batcave.mode":"day","core.modules.joker.enabled":true}}`} {
		r := started(t, rigOpts{bootRecord: prior})
		before := r.state()
		boom := errors.New("the batcomputer is down")
		r.cm.Fail(boom)
		_, err := r.apply(admin(), map[config.Path]string{kLevel: `"warn"`, kMode: `"night"`, kBatman: `false`})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the commit's error", err)
		}
		refused(t, r, before, 1)
		if got := string(r.record()); got != prior {
			t.Errorf("boot record = %q, want it restored to %q", got, prior)
		}
		r.cm.Fail(nil)
		if _, err := r.apply(admin(), map[config.Path]string{kLevel: `"warn"`}); err != nil || r.cave.Get().Level != "warn" {
			t.Fatalf("after the failure: %v, level %s", err, r.cave.Get().Level)
		}
	}
}

// TestApply_SecretAndOverrideOneCommit (the config half of
// TestApply_SecretAndOverrideOneBatch): a secret and an override in one patch
// reach the Committer as one Commit; the secret value never enters a row,
// a snapshot or a KeyState.
func TestApply_SecretAndOverrideOneCommit(t *testing.T) {
	r := started(t, rigOpts{})
	if _, err := r.apply(admin(), map[config.Path]string{kLevel: `"warn"`, kAPIKey: `"wayne-tech"`}); err != nil {
		t.Fatal(err)
	}
	cms := r.cm.Commits()
	if len(cms) != 1 {
		t.Fatalf("%d commits, want 1", len(cms))
	}
	c := cms[0]
	if len(c.Set) != 1 || c.Set[0].Path != kLevel || len(c.Secrets) != 1 {
		t.Fatalf("commit = %+v, want the override and the secret together", c)
	}
	w := c.Secrets[0]
	if w.Name != secrets.Name(kAPIKey) || w.Value.Reveal() != "wayne-tech" || w.Delete || w.By != "alfred" {
		t.Errorf("secret write = %v %v %v %v", w.Name, w.Delete, w.By, w.Value.IsZero())
	}
	if c.Gen != 1 || c.Set[0].Gen != 1 || c.Set[0].By != "alfred" {
		t.Errorf("commit gen %d, row gen %d by %q", c.Gen, c.Set[0].Gen, c.Set[0].By)
	}
	ks, _ := r.st.Lookup(kAPIKey)
	if ks.Secret == nil || !ks.Secret.Set || ks.Effective != nil {
		t.Errorf("KeyState = %+v, want Meta set and no Effective", ks)
	}
	for _, s := range []string{mustJSON(t, r.mem.Rows()), mustJSON(t, r.cave.Get()), mustJSON(t, ks)} {
		if strings.Contains(s, "wayne-tech") {
			t.Errorf("the secret value leaked: %s", s)
		}
	}
	if _, err := r.apply(admin(), nil, kAPIKey); err != nil {
		t.Fatal(err)
	}
	if c := r.cm.Commits()[1]; len(c.Secrets) != 1 || !c.Secrets[0].Delete || !c.Secrets[0].Value.IsZero() {
		t.Errorf("unset of a secret = %+v, want a delete", c.Secrets)
	}
}

// TestApply_DefaultIsUnset (C4): a value equal to the default is staged as
// an unset; with no row it commits nothing.
func TestApply_DefaultIsUnset(t *testing.T) {
	r := started(t, rigOpts{rows: map[config.Path]string{kLevel: `"debug"`}})
	if _, err := r.apply(admin(), map[config.Path]string{kLevel: `"info"`}); err != nil {
		t.Fatal(err)
	}
	c := r.cm.Commits()
	if len(c) != 1 || len(c[0].Set) != 0 || !slices.Equal(c[0].Unset, []config.Path{kLevel}) {
		t.Fatalf("commits = %+v, want one unset of %s", c, kLevel)
	}
	if _, ok := r.mem.Rows()[kLevel]; ok {
		t.Error("the row survived")
	}
	gen := r.st.Generation()
	res, err := r.apply(admin(), map[config.Path]string{kLevel: `"info"`, kInterval: `"1m30s"`}, kCount)
	if err != nil || r.cm.Calls() != 1 || r.st.Generation() != gen || res.Generation != gen {
		t.Fatalf("a patch that changes nothing: %v, %d commits, gen %d -> %d", err, r.cm.Calls(), gen, r.st.Generation())
	}
	if want := []config.Path{kCount, kInterval, kLevel}; !slices.Equal(res.Applied, want) {
		t.Errorf("Applied = %v, want %v", res.Applied, want)
	}
}

// TestApply_BootKeyNeedsTheRecord: without a boot record (no state
// directory) a boot key is refused.
func TestApply_BootKeyNeedsTheRecord(t *testing.T) {
	r := started(t, rigOpts{noRecord: true})
	before := r.state()
	if _, err := r.apply(admin(), map[config.Path]string{kMode: `"day"`}); !errors.Is(err, config.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	refused(t, r, before, 0)
}

// TestApply_PublishesLive: a live key is published at once with a new
// generation and the OnChange subscriber sees it.
func TestApply_PublishesLive(t *testing.T) {
	r := started(t, rigOpts{})
	got := make(chan [2]string, 4)
	stop := r.cave.OnChange(func(_ context.Context, prev, next *configtest.Batcave) { got <- [2]string{prev.Level, next.Level} })
	defer stop()
	res, err := r.apply(admin(), map[config.Path]string{kLevel: `"warn"`})
	if err != nil {
		t.Fatal(err)
	}
	if r.cave.Get().Level != "warn" || r.cave.Generation() != res.Generation || len(res.RestartPending) != 0 {
		t.Fatalf("Get level %s gen %d, result %+v", r.cave.Get().Level, r.cave.Generation(), res)
	}
	if r.watch.Generation() != 0 {
		t.Errorf("an untouched section's generation moved to %d", r.watch.Generation())
	}
	select {
	case pn := <-got:
		if pn != [2]string{"info", "warn"} {
			t.Errorf("OnChange prev/next = %v", pn)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnChange never ran")
	}
	if ks, _ := r.st.Lookup(kLevel); ks.Source != config.SourceOverride || string(ks.Effective) != `"warn"` {
		t.Errorf("KeyState = %v %s", ks.Source, ks.Effective)
	}
}

// TestAfterCommit_ErrorReportedNotRolledBack: a failing or panicking hook
// is reported as *PostCommitError; the change stays committed.
func TestAfterCommit_ErrorReportedNotRolledBack(t *testing.T) {
	r := started(t, rigOpts{})
	var seen [][2]string
	stopOK := r.cave.AfterCommit(func(_ context.Context, prev, next *configtest.Batcave) error {
		seen = append(seen, [2]string{prev.Level, next.Level})
		return nil
	})
	defer stopOK()
	stopErr := r.cave.AfterCommit(func(context.Context, *configtest.Batcave, *configtest.Batcave) error {
		return errors.New("the bat-signal is broken")
	})
	stopPanic := r.cave.AfterCommit(func(context.Context, *configtest.Batcave, *configtest.Batcave) error { panic("joker") })
	res, err := r.apply(admin(), map[config.Path]string{kLevel: `"warn"`})
	if err != nil {
		t.Fatalf("Apply: %v (a hook must never veto)", err)
	}
	var pce *config.PostCommitError
	if !errors.As(res.PostCommit, &pce) || pce.Section != configtest.BatcavePath ||
		!strings.Contains(res.PostCommit.Error(), "bat-signal") || !strings.Contains(res.PostCommit.Error(), "joker") {
		t.Fatalf("PostCommit = %v, want both hook failures on %s", res.PostCommit, configtest.BatcavePath)
	}
	if r.cave.Get().Level != "warn" || r.mem.Rows()[kLevel].Value == nil {
		t.Fatal("the change was rolled back")
	}
	if !slices.Equal(seen, [][2]string{{"info", "warn"}}) {
		t.Errorf("hook saw %v, want info -> warn", seen)
	}
	stopErr()
	stopErr()
	stopPanic()
	// A restart key changes Pending only: the hooks run with Pending values.
	res, err = r.apply(admin(), map[config.Path]string{kWorkers: `9`})
	if err != nil || res.PostCommit != nil {
		t.Fatalf("after stop: %v, PostCommit %v", err, res.PostCommit)
	}
	if len(seen) != 2 {
		t.Errorf("hook calls = %d, want 2 (a restart key changes the stored value)", len(seen))
	}
	// A patch that leaves the section's stored value unchanged runs no hook.
	if _, err := r.apply(admin(), map[config.Path]string{kWorkers: `9`}); err != nil || len(seen) != 2 {
		t.Errorf("no-op patch ran a hook: %v, calls %d", err, len(seen))
	}
}

// TestAfterCommit_RunsOutsideTheLock: a hook may call Apply.
func TestAfterCommit_RunsOutsideTheLock(t *testing.T) {
	r := started(t, rigOpts{})
	// The nested Apply changes this section too, so the hook runs again
	// inside it: a CAS, not a sync.Once, whose Do would deadlock on
	// re-entry.
	var fired atomic.Bool
	stop := r.cave.AfterCommit(func(ctx context.Context, _, _ *configtest.Batcave) error {
		if !fired.CompareAndSwap(false, true) {
			return nil
		}
		_, err := r.st.Apply(ctx, config.Patch{Set: map[config.Path]json.RawMessage{kCount: json.RawMessage(`9`)}}, admin())
		return err
	})
	defer stop()
	done := make(chan error, 1)
	go func() {
		_, err := r.apply(admin(), map[config.Path]string{kLevel: `"warn"`})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil || r.cave.Get().Gadget.Count != 9 {
			t.Fatalf("err %v, count %d", err, r.cave.Get().Gadget.Count)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Apply deadlocked on a hook that applies")
	}
}

// TestOnChange_CoalescesUnderSynctest: 100 applies against a slow
// subscriber are seen in at most two calls, the last with the latest value;
// no goroutine runs before Load and Close joins them all.
func TestOnChange_CoalescesUnderSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, rigOpts{})
		var mu sync.Mutex
		var calls []int
		stop := r.cave.OnChange(func(_ context.Context, _, next *configtest.Batcave) {
			mu.Lock()
			calls = append(calls, next.Gadget.Count)
			mu.Unlock()
			time.Sleep(time.Second)
		})
		synctest.Wait()
		r.start()
		for i := 1; i <= 100; i++ {
			if _, err := r.apply(admin(), map[config.Path]string{kCount: mustJSON(t, 100+i)}); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		mu.Lock()
		got := slices.Clone(calls)
		mu.Unlock()
		if len(got) == 0 || len(got) > 2 || got[len(got)-1] != 200 {
			t.Errorf("calls = %v, want at most 2 ending with 200", got)
		}
		stop()
		stop()
		if err := r.st.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestOnChange_StalePostDropped (T4 review MINOR-7, surviving mutant M23):
// Apply posts to OnChange after it releases the store mutex, so two Applies
// can post out of order. Here Apply A's AfterCommit hook runs Apply B to
// completion before A posts, so B (the newer generation) posts first and A
// second. A's post is stale and is dropped: the subscriber never receives A's
// snapshot, and its last delivery is the effective one.
func TestOnChange_StalePostDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, rigOpts{})
		var mu sync.Mutex
		var got []int
		stop := r.cave.OnChange(func(_ context.Context, _, next *configtest.Batcave) {
			mu.Lock()
			got = append(got, next.Gadget.Count)
			mu.Unlock()
		})
		r.start()
		var once sync.Once
		var berr error
		unhook := r.cave.AfterCommit(func(_ context.Context, _, next *configtest.Batcave) error {
			if next.Gadget.Count == 7 {
				once.Do(func() { _, berr = r.apply(admin(), map[config.Path]string{kCount: "9"}) })
			}
			return nil
		})
		if _, err := r.apply(admin(), map[config.Path]string{kCount: "7"}); err != nil {
			t.Fatal(err)
		}
		if berr != nil {
			t.Fatalf("Apply B inside A's hook: %v", berr)
		}
		unhook()
		synctest.Wait()
		mu.Lock()
		deliveries := slices.Clone(got)
		mu.Unlock()
		if final := r.cave.Get().Gadget.Count; final != 9 || len(deliveries) == 0 || deliveries[len(deliveries)-1] != final || slices.Contains(deliveries, 7) {
			t.Errorf("deliveries %v with Get() = %d: want them to end with B's 9 and never carry A's stale 7", deliveries, final)
		}
		stop()
		if err := r.st.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestOnChange_NoGoroutineBeforeLoad: a subscription made before Load
// starts its goroutine at Load, and Close joins it.
func TestOnChange_NoGoroutineBeforeLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, rigOpts{})
		ran := make(chan struct{}, 1)
		r.cave.OnChange(func(context.Context, *configtest.Batcave, *configtest.Batcave) { ran <- struct{}{} })
		if err := r.st.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Had OnChange started a goroutine before Load, Close's join and the
		// bubble's exit check would see it; nothing ran.
		synctest.Wait()
		select {
		case <-ran:
			t.Fatal("a subscriber ran before Load")
		default:
		}
	})
}

// TestSnapshot_ImmutableUnderRace: every published snapshot keeps its
// fingerprint while Apply publishes new ones, and no two snapshots share a
// slice (run with -race).
func TestSnapshot_ImmutableUnderRace(t *testing.T) {
	r := started(t, rigOpts{})
	var seen sync.Map // *Batcave -> fingerprint at first sight
	var stopReaders atomic.Bool
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for !stopReaders.Load() {
				s := r.cave.Get()
				fp := mustJSON(t, s)
				if prev, loaded := seen.LoadOrStore(s, fp); loaded && prev.(string) != fp {
					t.Errorf("snapshot %p changed after publication", s)
					return
				}
			}
		})
	}
	var prevData *string
	for i := range 50 {
		allies := mustJSON(t, []string{"robin", "batgirl", strings.Repeat("n", i)})
		if _, err := r.apply(admin(), map[config.Path]string{kAllies: allies, kCount: mustJSON(t, i)}); err != nil {
			t.Fatal(err)
		}
		d := unsafe.SliceData(r.cave.Get().Allies)
		if d == prevData {
			t.Fatal("two snapshots share the allies slice")
		}
		prevData = d
		if p := unsafe.SliceData(r.cave.Pending().Allies); p == d && r.cave.Pending() != r.cave.Get() {
			t.Fatal("Pending and Get share a slice")
		}
	}
	stopReaders.Store(true)
	wg.Wait()
	seen.Range(func(k, v any) bool {
		if fp := mustJSON(t, k); fp != v.(string) {
			t.Errorf("snapshot %p: %s, first seen as %s", k, fp, v)
		}
		return true
	})
}

// FuzzPatchDecode: Apply never panics and returns only config's typed
// errors.
func FuzzPatchDecode(f *testing.F) {
	for _, seed := range []struct {
		p, v  string
		unset bool
	}{
		{string(kLevel), `"warn"`, false}, {string(kAllies), `["a",1]`, false}, {string(kAPIKey), `"x"`, false},
		{string(kBatman), `false`, false}, {string(kInterval), `"-1h"`, false}, {"core.modules.x-y.enabled", `true`, false},
		{string(kMin), `1e400`, false}, {string(kCount), `null`, false}, {"", ``, true}, {string(kMode), `"day"`, true},
	} {
		f.Add(seed.p, []byte(seed.v), seed.unset)
	}
	f.Fuzz(func(t *testing.T, p string, v []byte, unset bool) {
		r := started(t, rigOpts{})
		patch := config.Patch{}
		if unset {
			patch.Unset = []config.Path{config.Path(p)}
		} else {
			patch.Set = map[config.Path]json.RawMessage{config.Path(p): v}
		}
		_, err := r.st.Apply(context.Background(), patch, admin())
		if err == nil {
			return
		}
		for _, s := range []error{config.ErrUnknownKey, config.ErrInvalid, config.ErrLocked, config.ErrProtected, config.ErrForbidden} {
			if errors.Is(err, s) {
				return
			}
		}
		t.Fatalf("Apply(%q, %q, unset %v): untyped error %v", p, v, unset, err)
	})
}

// TestApply_CommitCarriesBy (T4 review MAJOR-3): every Commit names the
// Actor's Subject as Commit.By, including one that only unsets a key and one
// that only changes a boot key, so the committer can name the audit actor of
// a commit with no row to read it from.
func TestApply_CommitCarriesBy(t *testing.T) {
	r := started(t, rigOpts{})
	steps := []struct {
		name  string
		set   map[config.Path]string
		unset []config.Path
	}{
		{"set", map[config.Path]string{kLevel: `"warn"`}, nil},
		{"unset only", nil, []config.Path{kLevel}},
		{"boot only", map[config.Path]string{kMode: `"day"`}, nil},
	}
	for i, s := range steps {
		if _, err := r.apply(admin(), s.set, s.unset...); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		cms := r.cm.Commits()
		if len(cms) != i+1 {
			t.Fatalf("%s: %d commits, want %d", s.name, len(cms), i+1)
		}
		if got := cms[i]; got.By != "alfred" {
			t.Errorf("%s: Commit.By = %q, want alfred (set %d, unset %d, boot %d)", s.name, got.By, len(got.Set), len(got.Unset), len(got.Boot))
		}
	}
}

// TestMapEntryID_InvalidIsUnknown (T4 review MINOR-9, surviving mutant M28):
// a map section's entry ID must be a valid segment. keyFor is the one gate
// for every caller, so a patch naming an invalid ID is refused as an unknown
// key and writes nothing, and a stored row naming one is reported as unknown,
// never read as a key.
func TestMapEntryID_InvalidIsUnknown(t *testing.T) {
	bad := []config.Path{"core.modules.Robin.enabled", "core.modules.9lives.enabled", "core.modules.night-wing.enabled"}
	r := started(t, rigOpts{})
	before := r.state()
	for _, p := range bad {
		if _, err := r.apply(admin(), map[config.Path]string{p: "true"}); !errors.Is(err, config.ErrUnknownKey) {
			t.Errorf("Apply %s = %v, want ErrUnknownKey", p, err)
		}
	}
	refused(t, r, before, 0)
	rows := map[config.Path]string{}
	for _, p := range bad {
		rows[p] = "true"
	}
	rep := newRig(t, rigOpts{rows: rows}).start()
	for _, p := range bad {
		if !slices.Contains(rep.Unknown, p) || rep.Quarantined[p] != nil {
			t.Errorf("row %s: unknown %v, quarantined %v; want it reported as unknown", p, rep.Unknown, rep.Quarantined[p])
		}
	}
}

const (
	lanternSection config.Path = "core.lantern"
	lanternMode    config.Path = "core.lantern.mode"
	lanternRange   config.Path = "core.lantern.range"
)

// lantern is a boot-key section with a cross-field Validator, so a boot
// record can hold values that each pass their schema and fail together.
type lantern struct {
	Mode  string `json:"mode" reload:"boot"`
	Range int    `json:"range" reload:"boot"`
}

func (l *lantern) Validate() error {
	if l.Mode == "off" && l.Range > 0 {
		return &config.FieldError{Path: lanternRange, Err: errors.New("a lantern that is off has no range")}
	}
	return nil
}

// TestApply_QuarantinedValueIsNeverANoOp (05-config §1.4 step 6): Apply of a
// quarantined key's exact stored bytes is not skipped as unchanged. The value
// is written and validated again: when its section now passes, it is stored
// and the quarantine is cleared; when it still fails, Apply refuses with
// ErrInvalid and the key stays quarantined. Apply never returns nil while the
// key stays quarantined. Both the override rows and the boot record.
func TestApply_QuarantinedValueIsNeverANoOp(t *testing.T) {
	t.Run("override rows", func(t *testing.T) {
		r := newRig(t, rigOpts{
			o:    config.Options{Flags: map[config.Path]string{kMax: "5"}},
			rows: map[config.Path]string{kMin: `8`, kVillains: `["bane"]`},
		})
		if rep := r.start(); rep.Quarantined[kMin] == nil || rep.Quarantined[kVillains] == nil {
			t.Fatalf("report = %+v, want min and villains quarantined", rep)
		}
		// min=8 still fails max=5: refused, nothing written, still quarantined.
		before := r.state()
		if _, err := r.apply(admin(), map[config.Path]string{kMin: `8`}); !errors.Is(err, config.ErrInvalid) {
			t.Errorf("Apply of the quarantined min = %v, want ErrInvalid", err)
		}
		refused(t, r, before, 0)
		if ks, _ := r.st.Lookup(kMin); ks.Quarantined == nil {
			t.Error("min lost its quarantine on a refused Apply")
		}
		// villains passes on its own: stored, quarantine cleared, effective.
		if _, err := r.apply(admin(), map[config.Path]string{kVillains: `["bane"]`}); err != nil {
			t.Fatalf("Apply of the quarantined villains = %v", err)
		}
		if r.cm.Calls() != 1 {
			t.Errorf("committer called %d times, want 1: the same bytes must be written again", r.cm.Calls())
		}
		ks, _ := r.st.Lookup(kVillains)
		if ks.Quarantined != nil || ks.Source != config.SourceOverride || !slices.Equal(r.watch.Get().Villains, []string{"bane"}) {
			t.Errorf("villains: KeyState %+v, effective %v; want the override live and unquarantined", ks, r.watch.Get().Villains)
		}
	})
	t.Run("boot record", func(t *testing.T) {
		r := newRig(t, rigOpts{bootRecord: `{"format":"mediacore-boot/1","gen":1,"values":{"core.lantern.mode":"off","core.lantern.range":3}}`})
		if _, err := config.Register(r.st.Registry(), config.Define(lanternSection, lantern{})); err != nil {
			t.Fatal(err)
		}
		r.start()
		for _, p := range []config.Path{lanternMode, lanternRange} {
			if ks, _ := r.st.Lookup(p); ks.Quarantined == nil {
				t.Fatalf("%s: not quarantined at boot", p)
			}
		}
		// range=3 passes on its own (mode is quarantined): the record is
		// rewritten and the quarantine cleared.
		if _, err := r.apply(admin(), map[config.Path]string{lanternRange: `3`}); err != nil {
			t.Fatalf("Apply of the quarantined range = %v", err)
		}
		if r.cm.Calls() != 1 {
			t.Errorf("committer called %d times, want 1: the same bytes must be written again", r.cm.Calls())
		}
		if ks, _ := r.st.Lookup(lanternRange); ks.Quarantined != nil {
			t.Errorf("range is still quarantined: %v", ks.Quarantined)
		}
		// mode=off now fails beside range=3: refused, still quarantined.
		if _, err := r.apply(admin(), map[config.Path]string{lanternMode: `"off"`}); !errors.Is(err, config.ErrInvalid) {
			t.Errorf("Apply of the quarantined mode = %v, want ErrInvalid", err)
		}
		if ks, _ := r.st.Lookup(lanternMode); ks.Quarantined == nil {
			t.Error("mode lost its quarantine on a refused Apply")
		}
		if r.cm.Calls() != 1 {
			t.Errorf("committer called %d times after the refusal, want 1", r.cm.Calls())
		}
	})
}

// TestApply_QuarantineHealthTracksState (05-config §1.4 step 6, §1.5): an
// Apply that clears quarantined values keeps the quarantine health token
// raised while any value of that layer is still quarantined and lowers it
// once none is left, for override rows and for boot-record values. A boot
// record set aside whole as .bad keeps its token raised after an Apply.
func TestApply_QuarantineHealthTracksState(t *testing.T) {
	raised := func(r *rig, token string) bool { return slices.Contains(r.st.HealthTokens(), token) }
	t.Run("override rows", func(t *testing.T) {
		const token = "config_overrides_quarantined"
		r := newRig(t, rigOpts{
			o:    config.Options{Flags: map[config.Path]string{kMax: "5"}},
			rows: map[config.Path]string{kMin: `8`, kVillains: `["bane"]`},
		})
		r.start()
		if !raised(r, token) {
			t.Fatalf("health %v after Attach, want %s", r.st.HealthTokens(), token)
		}
		if _, err := r.apply(admin(), map[config.Path]string{kVillains: `["bane"]`}); err != nil {
			t.Fatal(err)
		}
		if !raised(r, token) {
			t.Errorf("health %v with min still quarantined, want %s", r.st.HealthTokens(), token)
		}
		if _, err := r.apply(admin(), map[config.Path]string{kMin: `2`}); err != nil {
			t.Fatal(err)
		}
		if raised(r, token) {
			t.Errorf("health %v with nothing quarantined, want %s lowered", r.st.HealthTokens(), token)
		}
	})
	t.Run("boot record values", func(t *testing.T) {
		const token = "config_boot_record_quarantined"
		r := newRig(t, rigOpts{bootRecord: `{"format":"mediacore-boot/1","gen":1,"values":{"core.lantern.mode":"off","core.lantern.range":3}}`})
		if _, err := config.Register(r.st.Registry(), config.Define(lanternSection, lantern{})); err != nil {
			t.Fatal(err)
		}
		r.start()
		if !raised(r, token) {
			t.Fatalf("health %v after boot, want %s", r.st.HealthTokens(), token)
		}
		if _, err := r.apply(admin(), map[config.Path]string{lanternRange: `3`}); err != nil {
			t.Fatal(err)
		}
		if !raised(r, token) {
			t.Errorf("health %v with mode still quarantined, want %s", r.st.HealthTokens(), token)
		}
		if _, err := r.apply(admin(), map[config.Path]string{lanternMode: `"on"`}); err != nil {
			t.Fatal(err)
		}
		if raised(r, token) {
			t.Errorf("health %v with nothing quarantined, want %s lowered", r.st.HealthTokens(), token)
		}
	})
	t.Run("boot record set aside whole", func(t *testing.T) {
		const token = "config_boot_record_quarantined"
		r := started(t, rigOpts{bootRecord: "{not json"})
		if !raised(r, token) {
			t.Fatalf("health %v after boot, want %s", r.st.HealthTokens(), token)
		}
		if _, err := r.apply(admin(), map[config.Path]string{kMode: `"day"`}); err != nil {
			t.Fatal(err)
		}
		if !raised(r, token) {
			t.Errorf("health %v after an Apply, want %s kept for the .bad record", r.st.HealthTokens(), token)
		}
	})
}
