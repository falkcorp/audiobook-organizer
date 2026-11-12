// file: internal/plugins/maintenance/stores_fixer.go
// version: 1.1.0
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
