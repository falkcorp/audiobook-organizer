// file: internal/repairs/kinds_fixers_test.go
// version: 1.0.0
// guid: 9e5b2d71-4a8c-4e03-b6f9-1d3c7a0e5b84
// last-edited: 2026-11-12

package repairs_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/books"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// These tests build the real maintenance plugin's registry, as that package's
// own tests do, and ask what the 22 fixers it registers at this base become.

// atomScopes is books' atom table, the resolver the kernel hands KindDeps.
func atomScopes(atom auth.Permission) ([]rbac.Permission, bool) {
	for _, a := range books.AtomTable() {
		if a.Name == string(atom) && len(a.Scopes) > 0 {
			return a.Scopes, true
		}
	}
	return nil, false
}

// noDeps is a plugin whose every store accessor fails the test: nothing the
// shell runs may reach the plugin's own stores.
type noDeps struct {
	maintenance.ServerDeps
	t *testing.T
}

func (d noDeps) hit(name string) {
	d.t.Errorf("a fixer reached the plugin's own %s instead of the shell's stores", name)
}
func (d noDeps) OpsStore() maintenance.OpsStore { d.hit("OpsStore"); return nil }
func (d noDeps) OperationQueueStore() maintenance.OpQueueReader {
	d.hit("OperationQueueStore")
	return nil
}
func (d noDeps) UserReadStateStore() maintenance.UserReadStateStore {
	d.hit("UserReadStateStore")
	return nil
}
func (d noDeps) MergeUserStateStore() merge.UserStateRepairStore {
	d.hit("MergeUserStateStore")
	return nil
}
func (d noDeps) VersionPrimaryStore() maintenance.VersionPrimaryStore {
	d.hit("VersionPrimaryStore")
	return nil
}
func (d noDeps) IngestVersionStore() versions.IngestStore { d.hit("IngestVersionStore"); return nil }
func (d noDeps) FragmentRepairReader() maintenance.FragmentRepairReader {
	d.hit("FragmentRepairReader")
	return nil
}
func (d noDeps) DedupVerdictReader() maintenance.DedupVerdictReader {
	d.hit("DedupVerdictReader")
	return nil
}
func (d noDeps) BookTagReader() maintenance.BookTagReader { d.hit("BookTagReader"); return nil }
func (d noDeps) MasterLists() maintenance.MasterListStore { d.hit("MasterLists"); return nil }
func (d noDeps) ExternalIDBooks() maintenance.ExternalIDBookLookup {
	d.hit("ExternalIDBooks")
	return nil
}
func (d noDeps) OwnerVetoes() maintenance.OwnerVetoReader { d.hit("OwnerVetoes"); return nil }
func (d noDeps) CombineJournals() maintenance.CombineJournalScanner {
	d.hit("CombineJournals")
	return nil
}
func (d noDeps) MetadataCacheStore() database.MetadataCacheStore {
	d.hit("MetadataCacheStore")
	return nil
}
func (d noDeps) VersionTwinMetadataService() maintenance.VersionTwinMetadataService {
	d.hit("VersionTwinMetadataService")
	return nil
}

// shellStores is the shell's FixerStores: every accessor is counted and hands
// back no store.
type shellStores struct{ hits *atomic.Int64 }

var _ maintenance.FixerStores = shellStores{}

func (s shellStores) OpsStore() maintenance.OpsStore { s.hits.Add(1); return nil }
func (s shellStores) OperationQueueStore() maintenance.OpQueueReader {
	s.hits.Add(1)
	return nil
}
func (s shellStores) UserReadStateStore() maintenance.UserReadStateStore {
	s.hits.Add(1)
	return nil
}
func (s shellStores) MergeUserStateStore() merge.UserStateRepairStore {
	s.hits.Add(1)
	return nil
}
func (s shellStores) VersionPrimaryStore() maintenance.VersionPrimaryStore {
	s.hits.Add(1)
	return nil
}
func (s shellStores) IngestVersionStore() versions.IngestStore { s.hits.Add(1); return nil }
func (s shellStores) FragmentRepairReader() maintenance.FragmentRepairReader {
	s.hits.Add(1)
	return nil
}
func (s shellStores) DedupVerdictReader() maintenance.DedupVerdictReader {
	s.hits.Add(1)
	return nil
}
func (s shellStores) BookTagReader() maintenance.BookTagReader { s.hits.Add(1); return nil }
func (s shellStores) MasterLists() maintenance.MasterListStore { s.hits.Add(1); return nil }
func (s shellStores) ExternalIDBooks() maintenance.ExternalIDBookLookup {
	s.hits.Add(1)
	return nil
}
func (s shellStores) OwnerVetoes() maintenance.OwnerVetoReader { s.hits.Add(1); return nil }
func (s shellStores) CombineJournals() maintenance.CombineJournalScanner {
	s.hits.Add(1)
	return nil
}
func (s shellStores) MetadataCacheStore() database.MetadataCacheStore {
	s.hits.Add(1)
	return nil
}
func (s shellStores) VersionTwinMetadataService() maintenance.VersionTwinMetadataService {
	s.hits.Add(1)
	return nil
}

// retiredAtBase are the fixers `52e732a28` retired after `ac4368bdb`: the
// brief's 25 are 22 at this base.
var retiredAtBase = []string{"maintenance.normalize-letter-l-ordinals", "maintenance.relink-stale-series", "dedup.split-books"}

func registryAt(t *testing.T) *repairs.Registry {
	t.Helper()
	return maintenance.New(noDeps{t: t}).Repairs()
}

func TestRepairsKinds_Count(t *testing.T) {
	fixers := registryAt(t).List()
	require.Len(t, fixers, 22, "the maintenance plugin registers 22 fixers at this base (25 at ac4368bdb less the 3 retired since)")
	defs := repairs.Kinds(fixers, repairs.KindDeps{Scopes: atomScopes})
	require.Len(t, defs, len(fixers))

	seen := map[tasks.Kind]bool{}
	for i, d := range defs {
		want := tasks.Kind(repairs.KindPrefix + fixers[i].ID())
		require.Equal(t, want, d.Kind(), "kinds follow the lane's order")
		_, err := tasks.ParseKind(string(d.Kind()))
		require.NoError(t, err, "%s is a valid kind", d.Kind())
		require.False(t, seen[d.Kind()], "kinds are distinct: %s", d.Kind())
		seen[d.Kind()] = true
		require.Equal(t, tasks.ShapeFixer, d.Shape())
		require.Empty(t, tasks.Problems(d), "%s has no constructor problem", d.Kind())
	}
	for _, id := range retiredAtBase {
		require.False(t, seen[tasks.Kind(repairs.KindPrefix+id)], "%s is retired", id)
	}
	// The fixer id to kind table, in the lane's order, for the PR description.
	for i, f := range fixers {
		t.Logf("fixer %s ==> %s", f.ID(), defs[i].Kind())
	}
}

func TestRepairsKinds_Conformance(t *testing.T) {
	fixers := registryAt(t).List()
	taskstest.Conformance(t, repairs.Kinds(fixers, repairs.KindDeps{Scopes: atomScopes})...)
}

// Every fixer keeps HEAD's gate on its kind: starting, approving and reading
// the plan need the scopes of the permissions it names, and an admin-only
// fixer is not left at the default.
func TestRepairsKinds_EveryFixersGateResolves(t *testing.T) {
	fixers := registryAt(t).List()
	defs := repairs.Kinds(fixers, repairs.KindDeps{Scopes: atomScopes})
	for i, f := range fixers {
		c := tasks.CommonOf(defs[i])
		var want []rbac.Permission
		for _, atom := range repairs.RequiredPermissions(f) {
			s, ok := atomScopes(atom)
			require.True(t, ok, "%s: atom %s resolves", f.ID(), atom)
			want = append(want, s...)
		}
		require.ElementsMatch(t, want, c.Perms.Start.Permissions(), f.ID())
		require.ElementsMatch(t, want, c.Perms.Approve.Permissions(), f.ID())
		require.ElementsMatch(t, want, c.Perms.View.Permissions(), f.ID())
	}
}

// quietReporter is a reporter for calls that report nothing.
type quietReporter struct{}

func (quietReporter) UpdateProgress(_, _ int, _ string) error          { return nil }
func (quietReporter) Log(_ slog.Level, _ string, _ ...slog.Attr) error { return nil }
func (quietReporter) Logger() *slog.Logger                             { return slog.Default() }
func (quietReporter) Checkpoint(any) error                             { return nil }
func (quietReporter) IsCanceled() bool                                 { return false }
func (r quietReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}
func (quietReporter) Trigger(context.Context, string, any) error { return nil }
func (quietReporter) SetCurrentItem(string)                      {}

// call runs fn and swallows a panic: a fixer handed a nil store (the fake
// below has none) may dereference it; what the test asks is only where it
// looked for the store.
func call(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// Invariant 7: with the shell's stores on the context, no fixer reaches the
// plugin's own. noDeps fails the test on any such access.
func TestRepairsKinds_FixersUseShellStores(t *testing.T) {
	reg := registryAt(t)
	for _, f := range reg.List() {
		t.Run(f.ID(), func(t *testing.T) {
			var hits atomic.Int64
			ctx := repairs.WithStores[maintenance.FixerStores](context.Background(), shellStores{hits: &hits})
			if sc, ok := f.(repairs.ApplyScoped); ok {
				call(func() {
					scoped, end := sc.BeginApply(ctx, true)
					defer end()
					ctx = scoped
				})
			}
			call(func() { _, _ = f.Plan(ctx, json.RawMessage(`{}`), quietReporter{}) })
			call(func() {
				_, _ = f.Replan(ctx, json.RawMessage(`{}`), repairs.Row{RowID: "r", BookIDs: []string{"b"}, State: json.RawMessage(`{}`)}, quietReporter{})
			})
			call(func() {
				_ = f.Apply(ctx, nil, repairs.Row{RowID: "r", BookIDs: []string{"b"}, State: json.RawMessage(`{}`)})
			})
			if aa, ok := f.(repairs.AfterApplier); ok {
				call(func() { _, _ = aa.AfterApply(ctx, []string{"b"}) })
			}
			t.Logf("%s: %d shell store reads", f.ID(), hits.Load())
		})
	}
}

// Without the shell a fixer reads HEAD's stores: the plugin's own accessors.
// The fixers' own tests rely on it; the native shell never takes this path.
func TestRepairsKinds_FixersFallBackToPluginStoresWithoutShell(t *testing.T) {
	reached := atomic.Int64{}
	deps := countingDeps{reached: &reached}
	reg := maintenance.New(deps).Repairs()
	f, ok := reg.Get("maintenance.tag-franchise")
	require.True(t, ok)
	call(func() { _, _ = f.Plan(context.Background(), json.RawMessage(`{}`), quietReporter{}) })
	require.Positive(t, reached.Load(), "with no shell stores on the context the fixer reads the plugin's, as at HEAD")
}

// countingDeps counts the accessors instead of failing.
type countingDeps struct {
	maintenance.ServerDeps
	reached *atomic.Int64
}

func (d countingDeps) OpsStore() maintenance.OpsStore { d.reached.Add(1); return nil }
func (d countingDeps) BookTagReader() maintenance.BookTagReader {
	d.reached.Add(1)
	return nil
}
