// file: core/integrations/notify/rules_test.go
// version: 1.0.1
// guid: 2e95c0b7-6a41-4d83-9f27-c18a5d3e7b04
// last-edited: 2026-11-13

package notify_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/notify/notifytest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

func ruleIDs(t testing.TB, svc *notify.Service) []string {
	t.Helper()
	var out []string
	for r, err := range svc.Rules(context.Background()) {
		must(t, err)
		out = append(out, r.ID)
	}
	return out
}

// A rule routes a topic to a shared channel, so its author must be allowed to
// see every topic it names (and, for a pattern, every notifiable topic it
// covers), on top of core:integrations.manage.
func TestPutRule_RequiresTopicPermission(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	vault := rule("vault", []string{vaultName}, commissioner)

	for name, tc := range map[string]struct {
		ctx  context.Context
		rule notify.Rule
		want error
	}{
		"no principal":                     {context.Background(), vault, integrations.ErrForbidden},
		"no manage scope":                  {nobody(), vault, integrations.ErrForbidden},
		"manage but not the topic's scope": {managerWith("gotham:view"), vault, integrations.ErrForbidden},
		"a pattern covers a topic the author may not see": {managerWith("gotham:view"),
			rule("everything", []string{"gotham.*", "wayne.*"}, commissioner), integrations.ErrForbidden},
		"one topic of two": {managerWith("gotham:view"), rule("both", []string{signalName, vaultName}, commissioner), integrations.ErrForbidden},
	} {
		err := r.svc.PutRule(tc.ctx, tc.rule)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if ids := ruleIDs(t, r.svc); len(ids) != 0 {
		t.Fatalf("refused rules were stored: %v", ids)
	}
	if rules, err := notify.NativeRuleStore(r.notifyKS).Load(context.Background()); err != nil || len(rules) != 0 {
		t.Fatalf("the store holds %v, %v after refusals", rules, err)
	}

	must(t, r.svc.PutRule(managerWith("wayne:view"), vault))
	must(t, r.svc.PutRule(managerWith("gotham:view", "wayne:view"), rule("everything", []string{"gotham.*", "wayne.*"}, commissioner)))
	if got := ruleIDs(t, r.svc); !slices.Equal(got, []string{"everything", "vault"}) {
		t.Errorf("rules = %v", got)
	}

	// Deleting needs manage too, and says what is missing.
	if err := r.svc.DeleteRule(nobody(), "vault"); !errors.Is(err, integrations.ErrForbidden) {
		t.Errorf("DeleteRule without manage: %v", err)
	}
	if err := r.svc.DeleteRule(admin(), "no-such-rule"); !errors.Is(err, notify.ErrRuleNotFound) {
		t.Errorf("DeleteRule of a missing rule: %v", err)
	}
	must(t, r.svc.DeleteRule(admin(), "vault"))
	if got := ruleIDs(t, r.svc); !slices.Equal(got, []string{"everything"}) {
		t.Errorf("rules after delete = %v", got)
	}
}

// PutRule refuses a rule that cannot work, with ErrInvalid and nothing written.
func TestPutRule_RefusesAnInvalidRule(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	webhook := integrations.Ref{Kind: integrations.KindMetadata, Instance: "oracle"}
	for name, bad := range map[string]notify.Rule{
		"an empty ID":                rule("", []string{signalName}, commissioner),
		"an ID with capitals":        rule("Roof", []string{signalName}, commissioner),
		"no topics":                  rule("roof", nil, commissioner),
		"no instances":               rule("roof", []string{signalName}),
		"a topic nobody declared":    rule("roof", []string{"gotham.nothing.v1"}, commissioner),
		"a topic with no notify":     rule("roof", []string{escapedName}, commissioner),
		"a pattern matching nothing": rule("roof", []string{"arkham.*"}, commissioner),
		"a stray wildcard":           rule("roof", []string{"gotham.sig*"}, commissioner),
		"a non-notify instance":      rule("roof", []string{signalName}, webhook),
		"an unknown severity":        {ID: "roof", Topics: []string{signalName}, Instances: []integrations.Ref{commissioner}, MinSeverity: 42, Enabled: true},
	} {
		if err := r.svc.PutRule(admin(), bad); !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if ids := ruleIDs(t, r.svc); len(ids) != 0 {
		t.Errorf("invalid rules were stored: %v", ids)
	}
}

// A rule written through one Service is read back by another opened later on
// the same keyspace (the rules are loaded at start), and an edit replaces the
// rule whole.
func TestPutRule_SurvivesAReopenAndReplaces(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	first := rule("roof", []string{signalName}, commissioner)
	first.MinSeverity = notify.SeverityWarning
	r.putRule(first)
	second := rule("roof", []string{signalName, "gotham.*"}, alfred, oracle)
	second.Enabled = false
	r.putRule(second)

	again, err := notify.New(context.Background(), notify.Options{
		Keyspace: r.notifyKS, Catalog: r.bus.Catalog(), Outbox: r.ob, Registry: r.reg,
	})
	must(t, err)
	var got []notify.Rule
	for x, err := range again.Rules(context.Background()) {
		must(t, err)
		got = append(got, x)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], second) {
		t.Errorf("reloaded rules = %+v, want only %+v", got, second)
	}
}

// Writers racing on rules each land: the in-memory set ends equal to the
// store, whatever the order.
func TestPutRule_ConcurrentWritersConverge(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	var wg sync.WaitGroup
	ids := []string{"a-rule", "b-rule", "c-rule", "d-rule", "e-rule", "f-rule", "g-rule", "h-rule"}
	for _, id := range ids {
		wg.Go(func() {
			if err := r.svc.PutRule(admin(), rule(id, []string{signalName}, commissioner)); err != nil {
				t.Errorf("PutRule %s: %v", id, err)
			}
		})
	}
	wg.Wait()
	stored, err := notify.NativeRuleStore(r.notifyKS).Load(context.Background())
	must(t, err)
	var fromStore []string
	for _, x := range stored {
		fromStore = append(fromStore, x.ID)
	}
	if got := ruleIDs(t, r.svc); !slices.Equal(got, ids) || !slices.Equal(fromStore, ids) {
		t.Errorf("memory %v, store %v, want %v", got, fromStore, ids)
	}
}

// New refuses to start over a store it cannot read: serving an empty rule set
// would drop every notification quietly.
func TestNew_FailsOnAnUnreadableRuleStore(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	corruptRow(t, r.notifyKS, "roof")
	_, err := notify.New(context.Background(), notify.Options{
		Keyspace: r.notifyKS, Catalog: r.bus.Catalog(), Outbox: r.ob, Registry: r.reg,
	})
	if err == nil {
		t.Fatal("New served an unreadable rule store")
	}
}

func TestNew_NeedsItsInputs(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	good := notify.Options{Keyspace: r.notifyKS, Catalog: r.bus.Catalog(), Outbox: r.ob, Registry: r.reg}
	for name, edit := range map[string]func(*notify.Options){
		"no catalog":              func(o *notify.Options) { o.Catalog = nil },
		"no outbox":               func(o *notify.Options) { o.Outbox = nil },
		"no registry":             func(o *notify.Options) { o.Registry = nil },
		"someone else's keyspace": func(o *notify.Options) { o.Keyspace = r.gothamKS },
	} {
		o := good
		edit(&o)
		if _, err := notify.New(context.Background(), o); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}

// ---- the store ----------------------------------------------------------

// corruptRow stores bytes that are no rule at id's row. The rows are in a
// Serialized range, so the write reads the row for update first, as a writer
// of the store does.
func corruptRow(t testing.TB, ks storage.Keyspace, id string) {
	t.Helper()
	ctx := context.Background()
	key := keys.Join(keys.Str("rules"), keys.Str(id))
	b := ks.NewBatch()
	defer b.Close()
	b.Bind(ks)
	if _, err := b.GetForUpdate(ctx, ks, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	must(t, b.In(ks).Set(key, []byte("this is not a rule")))
	must(t, b.Commit(ctx, storage.Sync))
}

func nativeFixture(t *testing.T) notifytest.Fixture {
	db := openDB(t)
	ks := keyspace(t, db, notify.Owner)
	return notifytest.Fixture{
		Store: notify.NativeRuleStore(ks), NewBatch: ks.NewBatch,
		Corrupt: func(t *testing.T, id string) { corruptRow(t, ks, id) },
	}
}

func TestNativeRuleStore_PassesTheSuite(t *testing.T) {
	notifytest.RuleStoreSuite(t, notifytest.Harness{New: nativeFixture})
}

// TestNativeRuleStore_RoundTrip: every field of a rule survives the store.
func TestNativeRuleStore_RoundTrip(t *testing.T) {
	f := nativeFixture(t)
	want := notify.Rule{
		ID: "roof-and-vault", Topics: []string{signalName, "wayne.*"}, MinSeverity: notify.SeverityError,
		Instances: []integrations.Ref{commissioner, alfred}, Enabled: true,
	}
	off := notify.Rule{ID: "dormant", Topics: []string{signalName}, Instances: []integrations.Ref{oracle}}
	for _, r := range []notify.Rule{want, off} {
		b := f.NewBatch()
		must(t, f.Store.Put(context.Background(), b, r))
		must(t, b.Commit(context.Background(), storage.Sync))
		b.Close()
	}
	got, err := f.Store.Load(context.Background())
	must(t, err)
	if !reflect.DeepEqual(got, []notify.Rule{off, want}) { // by ID: dormant < roof-and-vault
		t.Errorf("Load = %+v\nwant   %+v", got, []notify.Rule{off, want})
	}
}

// ImportRule validates a migrated rule against the catalog and stages it in
// the caller's batch, so the rule commits with the rest of the migration step
// or not at all; re-running a step that committed is a no-op.
func TestImportRule_ValidatesAndStagesInCallerBatch(t *testing.T) {
	r := newRig(t, rigOpts{noRelay: true})
	imp := notify.NativeImporter(r.notifyKS, notify.WithCatalog(r.bus.Catalog()))
	ctx := context.Background()
	store := notify.NativeRuleStore(r.notifyKS)
	loaded := func() int {
		rules, err := store.Load(ctx)
		must(t, err)
		return len(rules)
	}
	good := rule("migrated", []string{signalName}, commissioner)

	// Invalid rules are refused and stage nothing.
	b := r.notifyKS.NewBatch()
	for name, bad := range map[string]notify.Rule{
		"an undeclared topic": rule("migrated", []string{"gotham.nothing.v1"}, commissioner),
		"a bad ID":            rule("Not Valid", []string{signalName}, commissioner),
		"no instances":        rule("migrated", []string{signalName}),
	} {
		if err := imp.ImportRule(ctx, b, bad); !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	if loaded() != 0 {
		t.Fatal("a refused import left a row")
	}

	// A batch that is abandoned leaves nothing; a committed one carries it.
	lost := r.notifyKS.NewBatch()
	must(t, imp.ImportRule(ctx, lost, good))
	lost.Close()
	if loaded() != 0 {
		t.Fatal("an abandoned batch left a row: the import did not go through the caller's batch")
	}
	kept := r.notifyKS.NewBatch()
	must(t, imp.ImportRule(ctx, kept, good))
	if loaded() != 0 {
		t.Fatal("the row is visible before the caller's batch committed")
	}
	must(t, kept.Commit(ctx, storage.Sync))
	kept.Close()
	if loaded() != 1 {
		t.Fatal("the committed batch did not carry the rule")
	}

	// Idempotent for the same content, a conflict for another.
	again := r.notifyKS.NewBatch()
	defer again.Close()
	if err := imp.ImportRule(ctx, again, good); err != nil {
		t.Errorf("re-import of the same rule: %v", err)
	}
	other := good
	other.Topics = []string{signalName, brokenName}
	if err := imp.ImportRule(ctx, again, other); !errors.Is(err, storage.ErrImportConflict) {
		t.Errorf("import over a different row: err = %v, want ErrImportConflict", err)
	}

	// Without a catalog it fails closed.
	bare := notify.NativeImporter(r.notifyKS)
	if err := bare.ImportRule(ctx, again, rule("another", []string{signalName}, commissioner)); !errors.Is(err, notify.ErrNoCatalog) {
		t.Errorf("an importer with no catalog: err = %v, want ErrNoCatalog", err)
	}
}
