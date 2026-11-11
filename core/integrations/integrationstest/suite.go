// file: core/integrations/integrationstest/suite.go
// version: 1.0.0
// guid: 5f898dba-3c41-4e7a-b2d9-8e6f0a1c4b57
// last-edited: 2026-11-11

// Package integrationstest is the conformance kit of integrations.InstanceStore
// (05-metadata M1.9, R4-1): InstanceStoreSuite runs every case an
// implementation must pass. It also holds the fakes the Registry's tests and
// the kind packages' tests share: a permission checker and principals, a
// fake adapter and a fake outbound policy that never touches a network.
//
// It is test support (03 R4, MOD5-6): it may import any core package and no
// core non-test package imports it.
package integrationstest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// Fixture is one store under test: an empty store over a fresh keyspace.
type Fixture struct {
	// Store is the instance store.
	Store integrations.InstanceStore
	// NewBatch opens a batch every write of Store can join.
	NewBatch func() *storage.Batch
	// Corrupt stores bytes that do not decode as an instance at the row of
	// ref, the way a damaged or foreign record would sit there.
	Corrupt func(t *testing.T, ref integrations.Ref)
}

// Harness builds fixtures. New is called once per case.
type Harness struct {
	New func(t *testing.T) Fixture
}

// InstanceStoreSuite runs the conformance cases of integrations.InstanceStore.
func InstanceStoreSuite(t *testing.T, h Harness) {
	t.Helper()
	if h.New == nil {
		t.Fatal("integrationstest: Harness.New is nil")
	}
	cases := []struct {
		name string
		run  func(t *testing.T, f Fixture)
	}{
		{"RoundTrip", roundTrip},
		{"LoadEveryKind", loadEveryKind},
		{"VersionRules", versionRules},
		{"ConcurrentWritersConflict", concurrentWriters},
		{"Delete", deleteRow},
		{"FailedWriteStagesNothing", failedWriteStagesNothing},
		{"MalformedRowKeptThenRepaired", malformedRow},
		{"RefusesInvalidRef", refusesInvalidRef},
		{"NeverStoresASecretValue", neverStoresSecretValue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := h.New(t)
			if f.Store == nil || f.NewBatch == nil {
				t.Fatal("integrationstest: Fixture needs Store and NewBatch")
			}
			c.run(t, f)
		})
	}
}

// Batcave is the instance the cases write: every field of a row set.
func Batcave() integrations.StoredInstance {
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: "batcave"}
	return integrations.StoredInstance{
		Config: integrations.InstanceConfig{
			Ref: ref, Adapter: "core.batsignal", Name: "The Batcave", Enabled: true, Priority: 2,
			Origin: integrations.OriginUI, Local: true,
			Settings: map[string]any{
				"url":      "http://192.0.2.7:8080/signal",
				"retries":  float64(3),
				"password": secrets.RefTo("integrations.notify.batcave.password"),
			},
			Limits: &integrations.Limits{
				RPS: 2, Burst: 4, Timeout: 3 * time.Second,
				Daily:   &integrations.DailyLimits{Total: 100, Background: 40, Zone: time.UTC, Counters: []string{"requests"}},
				Breaker: integrations.BreakerPolicy{Threshold: 5, Cooldown: 30 * time.Second},
				Holds:   integrations.HoldPolicy{Durations: map[integrations.Class]time.Duration{integrations.ClassAuth: 6 * time.Hour}, Persist: true},
			},
			Updated: time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC),
		},
		LocalOrigin:          "http://192.0.2.7:8080",
		SecretsOrigin:        "http://192.0.2.7:8080",
		OperatorSecretOrigin: map[string]string{"url": "http://192.0.2.7:8080"},
	}
}

// put stages inst into a fresh batch and commits it.
func put(t *testing.T, f Fixture, inst integrations.StoredInstance) uint64 {
	t.Helper()
	b := f.NewBatch()
	defer closeBatch(t, b)
	v, err := f.Store.Put(context.Background(), b, inst)
	if err != nil {
		t.Fatalf("Put %s at version %d: %v", inst.Config.Ref, inst.Config.Version, err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatalf("commit %s: %v", inst.Config.Ref, err)
	}
	return v
}

func closeBatch(t *testing.T, b *storage.Batch) {
	t.Helper()
	if err := b.Close(); err != nil {
		t.Errorf("close batch: %v", err)
	}
}

// load is f.Store.Load, failing t on an error.
func load(t *testing.T, f Fixture) []integrations.StoredInstance {
	t.Helper()
	rows, err := f.Store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rows
}

// find is ref's row of rows.
func find(rows []integrations.StoredInstance, ref integrations.Ref) (integrations.StoredInstance, bool) {
	for _, r := range rows {
		if r.Config.Ref == ref {
			return r, true
		}
	}
	return integrations.StoredInstance{}, false
}

func roundTrip(t *testing.T, f Fixture) {
	in := Batcave()
	if v := put(t, f, in); v != 1 {
		t.Fatalf("a create returned version %d, want 1", v)
	}
	got, ok := find(load(t, f), in.Config.Ref)
	if !ok {
		t.Fatal("the row is not loaded back")
	}
	want := in
	want.Config.Version = 1
	want.Config.Stored = true
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
	if got.Config.Limits.Daily.Zone.String() != "UTC" {
		t.Errorf("daily zone = %v, want UTC", got.Config.Limits.Daily.Zone)
	}
}

func loadEveryKind(t *testing.T, f Fixture) {
	var want []integrations.Ref
	for _, k := range integrations.Kinds() {
		for _, id := range []integrations.InstanceID{"alfred", "robin"} {
			in := Batcave()
			in.Config.Ref = integrations.Ref{Kind: k, Instance: id}
			in.Config.Settings = map[string]any{"url": "http://192.0.2.8/"}
			put(t, f, in)
			want = append(want, in.Config.Ref)
		}
	}
	var got []integrations.Ref
	for _, r := range load(t, f) {
		got = append(got, r.Config.Ref)
	}
	cmp := func(a, b integrations.Ref) int {
		switch {
		case a.String() < b.String():
			return -1
		case a.String() > b.String():
			return 1
		}
		return 0
	}
	slices.SortFunc(got, cmp)
	slices.SortFunc(want, cmp)
	if !slices.Equal(got, want) {
		t.Errorf("Load = %v, want %v", got, want)
	}
}

func versionRules(t *testing.T, f Fixture) {
	ctx := context.Background()
	in := Batcave()
	put(t, f, in)
	for _, v := range []uint64{0, 2, 7} {
		b := f.NewBatch()
		stale := in
		stale.Config.Version = v
		if _, err := f.Store.Put(ctx, b, stale); !errors.Is(err, integrations.ErrVersionConflict) {
			t.Errorf("Put at version %d over version 1: err = %v, want ErrVersionConflict", v, err)
		}
		closeBatch(t, b)
	}
	in.Config.Version = 1
	in.Config.Name = "The Batcave, rebuilt"
	if v := put(t, f, in); v != 2 {
		t.Fatalf("an update returned version %d, want 2", v)
	}
	got, _ := find(load(t, f), in.Config.Ref)
	if got.Config.Version != 2 || got.Config.Name != "The Batcave, rebuilt" {
		t.Errorf("after the update: version %d name %q", got.Config.Version, got.Config.Name)
	}
}

func concurrentWriters(t *testing.T, f Fixture) {
	ctx := context.Background()
	in := Batcave()
	put(t, f, in)
	in.Config.Version = 1
	b1, b2 := f.NewBatch(), f.NewBatch()
	defer closeBatch(t, b1)
	defer closeBatch(t, b2)
	a, b := in, in
	a.Config.Name, b.Config.Name = "Wayne Manor", "Arkham Asylum"
	if _, err := f.Store.Put(ctx, b1, a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Store.Put(ctx, b2, b); err != nil {
		t.Fatal(err)
	}
	if err := b1.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := b2.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("the second writer's commit: err = %v, want storage.ErrConflict", err)
	}
	got, _ := find(load(t, f), in.Config.Ref)
	if got.Config.Name != "Wayne Manor" || got.Config.Version != 2 {
		t.Errorf("the first writer's row was lost: %+v", got.Config)
	}
}

func deleteRow(t *testing.T, f Fixture) {
	ctx := context.Background()
	in := Batcave()
	put(t, f, in)
	b := f.NewBatch()
	if err := f.Store.Delete(ctx, b, in.Config.Ref); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	closeBatch(t, b)
	if _, ok := find(load(t, f), in.Config.Ref); ok {
		t.Fatal("the row survived its delete")
	}
	b = f.NewBatch()
	defer closeBatch(t, b)
	if err := f.Store.Delete(ctx, b, in.Config.Ref); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Errorf("Delete of an absent row: err = %v, want ErrNotConfigured", err)
	}
}

func failedWriteStagesNothing(t *testing.T, f Fixture) {
	ctx := context.Background()
	in := Batcave()
	put(t, f, in)
	b := f.NewBatch()
	defer closeBatch(t, b)
	if _, err := f.Store.Put(ctx, b, in); err == nil {
		t.Fatal("a create over an existing row succeeded")
	}
	other := in
	other.Config.Ref.Instance = "arkham"
	if err := f.Store.Delete(ctx, b, other.Config.Ref); err == nil {
		t.Fatal("a delete of an absent row succeeded")
	}
	if n := b.Count(); n != 0 {
		t.Errorf("failed writes staged %d writes", n)
	}
}

func malformedRow(t *testing.T, f Fixture) {
	if f.Corrupt == nil {
		t.Skip("the fixture cannot corrupt a row")
	}
	ref := integrations.Ref{Kind: integrations.KindIndexer, Instance: "riddler"}
	f.Corrupt(t, ref)
	got, ok := find(load(t, f), ref)
	if !ok {
		t.Fatal("a malformed row is not loaded (it must be kept and reported)")
	}
	if got.Malformed == "" || !got.Config.Stored {
		t.Fatalf("malformed row loaded as %+v", got)
	}
	repair := Batcave()
	repair.Config.Ref = ref
	repair.Config.Version = got.Config.Version
	v := put(t, f, repair)
	if v != got.Config.Version+1 {
		t.Errorf("the repair returned version %d, want %d", v, got.Config.Version+1)
	}
	fixed, _ := find(load(t, f), ref)
	if fixed.Malformed != "" || fixed.Config.Name != "The Batcave" {
		t.Errorf("after the repair: %+v", fixed)
	}
}

func refusesInvalidRef(t *testing.T, f Fixture) {
	ctx := context.Background()
	b := f.NewBatch()
	defer closeBatch(t, b)
	for _, ref := range []integrations.Ref{
		{Kind: "gadgets", Instance: "belt"},
		{Kind: integrations.KindAI, Instance: "Bat-Computer"},
		{Kind: integrations.KindAI, Instance: ""},
	} {
		in := Batcave()
		in.Config.Ref = ref
		if _, err := f.Store.Put(ctx, b, in); !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("Put %q: err = %v, want ErrInvalid", ref, err)
		}
		if err := f.Store.Delete(ctx, b, ref); !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("Delete %q: err = %v, want ErrInvalid", ref, err)
		}
	}
	if n := b.Count(); n != 0 {
		t.Errorf("refused writes staged %d writes", n)
	}
}

func neverStoresSecretValue(t *testing.T, f Fixture) {
	ctx := context.Background()
	b := f.NewBatch()
	defer closeBatch(t, b)
	in := Batcave()
	in.Config.Settings = map[string]any{"password": secrets.NewValue("alfred-knows")}
	if _, err := f.Store.Put(ctx, b, in); err == nil {
		t.Fatal("a row holding a secret value was staged")
	}
	if n := b.Count(); n != 0 {
		t.Errorf("the refused row staged %d writes", n)
	}
}
