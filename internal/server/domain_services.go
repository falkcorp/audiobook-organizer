// file: internal/server/domain_services.go
// version: 0.1.0
// guid: 9e3b6d52-7a14-4c08-b1f9-6d2a85c3e470
// last-edited: 2026-11-22

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/activity"
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/quarantine"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/falkcorp/audiobook-organizer/internal/work"
)

// domainServices holds the service clusters that plain constructors build
// (R3-05a, 06 §6.11.1 step 4): work, audiobook, organize, quarantine, scan,
// merge, importpath and batch. They used to be ServiceDefs in the registry's
// one explicit list and were read back with serviceregistry.Get; the server
// now holds the constructed values directly, and the dedup def (the one
// registry-built service that uses one of them) closes over its merge service.
// The ninth cluster of the row, itunes, is still registry-built: see
// serviceDefs.
//
// Construction is in two stages because two clusters read values the registry
// builds, and the registry's Build is one atomic step.
//
//	stage 1, newDomainServices, before the registry: their only dependency is
//	  the store. Dependency order: merge, audiobook, organize, work, importpath,
//	  then batch (the audiobook service is its trash purger).
//	stage 2, completeFromRegistry, after the registry's Build and before its
//	  PostInit: quarantine (needs the event bus) and scan (needs the embedding
//	  store), each fetched from the container once. The same call hands the
//	  audiobook service the activity service and the scan service the activity
//	  writer, which their PostInit methods did when the container drove them.
//
// Each cluster is a field, never a local, so an ordering mistake is a nil
// handle the tests catch rather than a compile error that hides it.
type domainServices struct {
	trace func(step string)

	// Stage 1.
	merge      *merge.Service
	audiobook  *audiobookspkg.AudiobookService
	organize   *OrganizeService
	work       *work.WorkService
	importPath *importer.ImportPathService
	batch      *batch.BatchService

	// Stage 2.
	quarantine *quarantine.QuarantineService
	scan       *scanner.ScanService
}

// newDomainServices runs stage 1. trace, when non-nil, is told each
// constructor's name as it returns (serverOptions.trace, set by tests).
func newDomainServices(store database.Store, trace func(step string)) *domainServices {
	d := &domainServices{trace: trace}
	d.merge = merge.NewService(store)
	d.record("merge")
	d.audiobook = audiobookspkg.NewAudiobookService(store)
	d.record("audiobook")
	d.organize = audiobookspkg.NewOrganizeService(store)
	d.record("organize")
	d.work = work.NewWorkService(store)
	d.record("work")
	d.importPath = importer.NewImportPathService(store)
	d.record("importpath")
	// The batch hard delete of a trashed book takes the purge's own path.
	d.batch = batch.NewBatchService(store, batch.WithTrashPurger(d.audiobook))
	d.record("batch")
	return d
}

// completeFromRegistry runs stage 2 against the built container c.
func (d *domainServices) completeFromRegistry(store database.Store, cfg *config.Config, c *serviceregistry.Container) {
	d.quarantine = quarantine.NewQuarantineService(store, cfg, plugin.GetEventBus(c))
	d.record("quarantine")

	d.scan = scanner.NewScanService(store)
	// EmbeddingStore for metadata hash dedup detection. A typed nil (the store
	// is not a PebbleStore) leaves it unset.
	if es := serviceregistry.Get[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore); es != nil {
		d.scan.SetEmbeddingStore(es)
	}
	// The activity pair is DatabasePath-gated: absent, scan events are dropped
	// and the audiobook tags read takes its non-snapshot path.
	if aw, ok := serviceregistry.TryGet[*activity.Writer](c, "activitywriter"); ok && aw != nil {
		d.scan.SetActivityWriter(aw)
	}
	d.record("scan")
	if as, ok := serviceregistry.TryGet[*activity.Service](c, "activity"); ok && as != nil {
		d.audiobook.SetActivityService(as)
	}
}

func (d *domainServices) record(step string) {
	if d.trace != nil {
		d.trace(step)
	}
}
