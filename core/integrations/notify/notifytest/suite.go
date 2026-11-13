// file: core/integrations/notify/notifytest/suite.go
// version: 1.0.0
// guid: b18d3f64-9e27-4a50-8c71-d4a6e0f2b936
// last-edited: 2026-11-13

package notifytest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/storage"
)

// Fixture is one store under test: an empty store over a fresh keyspace.
type Fixture struct {
	Store notify.RuleStore
	// NewBatch opens a batch every write of Store can join.
	NewBatch func() *storage.Batch
	// Corrupt stores bytes that do not decode as a rule at the row of id, the
	// way a damaged or foreign record would sit there.
	Corrupt func(t *testing.T, id string)
}

// Harness builds fixtures. New is called once per case.
type Harness struct {
	New func(t *testing.T) Fixture
}

// RuleStoreSuite runs the conformance cases of notify.RuleStore.
func RuleStoreSuite(t *testing.T, h Harness) {
	t.Helper()
	if h.New == nil {
		t.Fatal("notifytest: Harness.New is nil")
	}
	cases := []struct {
		name string
		run  func(t *testing.T, f Fixture)
	}{
		{"RoundTrip", roundTrip},
		{"LoadIsOrderedByID", loadOrdered},
		{"PutReplaces", putReplaces},
		{"Delete", deleteRule},
		{"DeleteMissingIsNotFound", deleteMissing},
		{"FailedWriteStagesNothing", discardedWrite},
		{"ConcurrentWritersConflict", concurrentWriters},
		{"MalformedRowFailsLoad", malformedRow},
		{"RefusesInvalidID", refusesInvalidID},
		{"RefusesAMissingBatch", refusesNilBatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := h.New(t)
			if f.Store == nil || f.NewBatch == nil {
				t.Fatal("notifytest: Fixture needs Store and NewBatch")
			}
			c.run(t, f)
		})
	}
}

// Rule is the rule the cases write: every field of a rule set.
func Rule(id string) notify.Rule {
	return notify.Rule{
		ID:          id,
		Topics:      []string{"gotham.signal.raised.v1", "wayne.*"},
		MinSeverity: notify.SeverityWarning,
		Instances: []integrations.Ref{
			{Kind: integrations.KindNotify, Instance: "batphone"},
			{Kind: integrations.KindNotify, Instance: "belfry"},
		},
		Enabled: true,
	}
}

func put(t *testing.T, f Fixture, r notify.Rule) {
	t.Helper()
	b := f.NewBatch()
	defer b.Close()
	if err := f.Store.Put(context.Background(), b, r); err != nil {
		t.Fatalf("Put %s: %v", r.ID, err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatalf("commit %s: %v", r.ID, err)
	}
}

func load(t *testing.T, f Fixture) []notify.Rule {
	t.Helper()
	rules, err := f.Store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rules
}

func ids(rules []notify.Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.ID
	}
	return out
}

func roundTrip(t *testing.T, f Fixture) {
	want := Rule("signal")
	put(t, f, want)
	got := load(t, f)
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("Load = %+v, want [%+v]", got, want)
	}
	off := Rule("quiet")
	off.Enabled, off.MinSeverity = false, notify.SeverityCritical
	put(t, f, off)
	for _, r := range load(t, f) {
		if r.ID == "quiet" && (r.Enabled || r.MinSeverity != notify.SeverityCritical) {
			t.Errorf("a disabled critical-only rule came back as %+v", r)
		}
	}
}

func loadOrdered(t *testing.T, f Fixture) {
	for _, id := range []string{"robin", "alfred", "oracle"} {
		put(t, f, Rule(id))
	}
	if got, want := ids(load(t, f)), []string{"alfred", "oracle", "robin"}; !slices.Equal(got, want) {
		t.Errorf("Load order = %v, want %v", got, want)
	}
}

func putReplaces(t *testing.T, f Fixture) {
	put(t, f, Rule("signal"))
	r := Rule("signal")
	r.Topics, r.Enabled = []string{"gotham.*"}, false
	put(t, f, r)
	got := load(t, f)
	if len(got) != 1 || !reflect.DeepEqual(got[0], r) {
		t.Errorf("Load after replace = %+v, want [%+v]", got, r)
	}
}

func deleteRule(t *testing.T, f Fixture) {
	put(t, f, Rule("signal"))
	put(t, f, Rule("alarm"))
	b := f.NewBatch()
	defer b.Close()
	if err := f.Store.Delete(context.Background(), b, "signal"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	if got := ids(load(t, f)); !slices.Equal(got, []string{"alarm"}) {
		t.Errorf("Load after delete = %v", got)
	}
}

func deleteMissing(t *testing.T, f Fixture) {
	b := f.NewBatch()
	defer b.Close()
	if err := f.Store.Delete(context.Background(), b, "joker"); !errors.Is(err, notify.ErrRuleNotFound) {
		t.Errorf("Delete of a missing rule: err = %v, want ErrRuleNotFound", err)
	}
}

// A batch that is closed uncommitted leaves nothing behind.
func discardedWrite(t *testing.T, f Fixture) {
	b := f.NewBatch()
	if err := f.Store.Put(context.Background(), b, Rule("signal")); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := load(t, f); len(got) != 0 {
		t.Errorf("a discarded batch left %v", ids(got))
	}
}

// Two writers that both read the rule before either commits do not both win:
// the second commit conflicts instead of overwriting the first.
func concurrentWriters(t *testing.T, f Fixture) {
	ctx := context.Background()
	put(t, f, Rule("signal"))
	a, b := f.NewBatch(), f.NewBatch()
	defer a.Close()
	defer b.Close()
	ra, rb := Rule("signal"), Rule("signal")
	ra.Topics, rb.Topics = []string{"gotham.*"}, []string{"wayne.*"}
	if err := f.Store.Put(ctx, a, ra); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.Put(ctx, b, rb); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("second commit: err = %v, want storage.ErrConflict", err)
	}
	if got := load(t, f); len(got) != 1 || !slices.Equal(got[0].Topics, ra.Topics) {
		t.Errorf("the loser overwrote the winner: %+v", got)
	}
}

// A row that cannot be told apart fails Load rather than serving a shorter
// list: a rule that silently routes nothing is a notification lost.
func malformedRow(t *testing.T, f Fixture) {
	if f.Corrupt == nil {
		t.Skip("the fixture cannot corrupt a row")
	}
	put(t, f, Rule("signal"))
	f.Corrupt(t, "joker")
	if _, err := f.Store.Load(context.Background()); err == nil {
		t.Fatal("Load served a list with a malformed row in it")
	}
}

func refusesInvalidID(t *testing.T, f Fixture) {
	for _, id := range []string{"", "Joker", "-x", "has space", "a/b", "waytoolong-waytoolong-waytoolong-x"} {
		b := f.NewBatch()
		err := f.Store.Put(context.Background(), b, Rule(id))
		if err == nil {
			t.Errorf("Put accepted the rule ID %q", id)
		}
		_ = b.Close()
	}
}

func refusesNilBatch(t *testing.T, f Fixture) {
	if err := f.Store.Put(context.Background(), nil, Rule("signal")); err == nil {
		t.Error("Put without a batch succeeded")
	}
	if err := f.Store.Delete(context.Background(), nil, "signal"); err == nil {
		t.Error("Delete without a batch succeeded")
	}
}
