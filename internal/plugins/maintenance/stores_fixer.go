// file: internal/plugins/maintenance/stores_fixer.go
// version: 1.0.0
// guid: 3b8f1d27-6a4e-4c95-8d02-5e7a9c1b4f68
// last-edited: 2026-11-12

package maintenance

import (
	"context"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// The stores the Repairs fixers read through come from the shell that runs
// them, not from what a fixer captured when it was built (05-tasks §5T.8.2
// conversion rule 3). The native shell (internal/repairs.Kinds) puts a
// FixerStores on the context of every Plan, Replan, Apply, BeginApply and
// AfterApply call (repairs.WithStores); a fixer takes it with p.fixerStores.
//
// The declaration lives here and not in internal/repairs because its result
// types are this package's interfaces (OpsStore and the rest) and repairs
// cannot import this package; repairs carries the set opaquely
// (repairs.WithStores / StoresFrom, generic over the type).

// FixerStores is every store-returning accessor the Repairs fixers call: the
// same names and result types as the ServerDeps methods they replace. Two
// groups, so no interface here exceeds the width ratchet.
type FixerStores interface {
	FixerBookStores
	FixerUserStores
}

// FixerBookStores serves the fixers' reads and writes of books and what hangs
// off them.
type FixerBookStores interface {
	OpsStore() OpsStore
	OperationQueueStore() OpQueueReader
	VersionPrimaryStore() VersionPrimaryStore
	IngestVersionStore() versions.IngestStore
	FragmentRepairReader() FragmentRepairReader
	DedupVerdictReader() DedupVerdictReader
	BookTagReader() BookTagReader
	ExternalIDBooks() ExternalIDBookLookup
}

// FixerUserStores serves the fixers that read users' state, the master lists,
// the owner's vetoes and the metadata caches.
type FixerUserStores interface {
	UserReadStateStore() UserReadStateStore
	MergeUserStateStore() merge.UserStateRepairStore
	MasterLists() MasterListStore
	OwnerVetoes() OwnerVetoReader
	CombineJournals() CombineJournalScanner
	MetadataCacheStore() database.MetadataCacheStore
	VersionTwinMetadataService() VersionTwinMetadataService
}

// pluginFixerStores is HEAD's stores: exactly what the plugin's deps return,
// for the callers that run a fixer without the shell (HEAD's repairs.plan and
// repairs.apply operations and the fixers' own tests, which call Plan, Replan
// and Apply directly). Every method is promoted from the embedded deps.
type pluginFixerStores struct{ ServerDeps }

var _ FixerStores = pluginFixerStores{}

// fixerStores is the stores a fixer call reads through: the shell's, when it
// put them on ctx, else HEAD's (pluginFixerStores). The native shell always
// binds, so nothing it runs reaches HEAD's. nil when neither exists (a plugin
// with no deps, which only a test builds); callers refuse a nil store as they
// always have.
func (p *Plugin) fixerStores(ctx context.Context) FixerStores {
	if st, ok := repairs.StoresFrom[FixerStores](ctx); ok {
		return st
	}
	if p == nil || p.deps == nil {
		return nil
	}
	return pluginFixerStores{p.deps}
}

// shellDeps is the plugin's deps with the shell's stores in place of its own:
// the 15 store accessors answer from st, everything else from the embedded
// deps. It is how a fixer calls a body shared with an operation (outside the
// fixer files) so that body, too, reads the shell's stores.
type shellDeps struct {
	ServerDeps
	st FixerStores
}

func (d shellDeps) OpsStore() OpsStore                 { return d.st.OpsStore() }
func (d shellDeps) OperationQueueStore() OpQueueReader { return d.st.OperationQueueStore() }
func (d shellDeps) UserReadStateStore() UserReadStateStore {
	return d.st.UserReadStateStore()
}
func (d shellDeps) MergeUserStateStore() merge.UserStateRepairStore {
	return d.st.MergeUserStateStore()
}
func (d shellDeps) VersionPrimaryStore() VersionPrimaryStore { return d.st.VersionPrimaryStore() }
func (d shellDeps) IngestVersionStore() versions.IngestStore { return d.st.IngestVersionStore() }
func (d shellDeps) FragmentRepairReader() FragmentRepairReader {
	return d.st.FragmentRepairReader()
}
func (d shellDeps) DedupVerdictReader() DedupVerdictReader { return d.st.DedupVerdictReader() }
func (d shellDeps) BookTagReader() BookTagReader           { return d.st.BookTagReader() }
func (d shellDeps) MasterLists() MasterListStore           { return d.st.MasterLists() }
func (d shellDeps) ExternalIDBooks() ExternalIDBookLookup  { return d.st.ExternalIDBooks() }
func (d shellDeps) OwnerVetoes() OwnerVetoReader           { return d.st.OwnerVetoes() }
func (d shellDeps) CombineJournals() CombineJournalScanner { return d.st.CombineJournals() }
func (d shellDeps) MetadataCacheStore() database.MetadataCacheStore {
	return d.st.MetadataCacheStore()
}
func (d shellDeps) VersionTwinMetadataService() VersionTwinMetadataService {
	return d.st.VersionTwinMetadataService()
}

// withFixerStores is a plugin value whose deps hand out st's stores, for a
// fixer that calls a plugin method shared with an operation. Its fields are
// copied one by one (the plugin holds a sync.Once); the registry is not
// carried, nothing reached through it needs it.
func (p *Plugin) withFixerStores(st FixerStores) *Plugin {
	if p == nil || p.deps == nil || st == nil {
		return p
	}
	return &Plugin{
		deps:               shellDeps{ServerDeps: p.deps, st: st},
		standDownWait:      p.standDownWait,
		reflinkFile:        p.reflinkFile,
		itunesLibraryPaths: p.itunesLibraryPaths,
		ownerEmail:         p.ownerEmail,
	}
}
