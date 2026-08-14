// file: internal/plugins/maintenance/plugin.go
// version: 1.18.0
// guid: b2c3d4e5-f6a7-8901-bcde-123456789012
// last-edited: 2026-08-13

package maintenance

import "github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"

// Plugin is the UOS maintenance plugin. It holds a reference to a ServerDeps
// implementation (provided by *server.Server at startup) so that Run functions
// can call server methods without an import cycle.
type Plugin struct {
	deps ServerDeps
}

// New constructs a maintenance Plugin. deps must not be nil.
func New(deps ServerDeps) *Plugin {
	return &Plugin{deps: deps}
}

// ID implements sdk.Plugin.
func (p *Plugin) ID() string { return "maintenance" }

// Name implements sdk.Plugin.
func (p *Plugin) Name() string { return "Maintenance" }

// Version implements sdk.Plugin.
func (p *Plugin) Version() string { return "1.0.0" }

// Register registers all maintenance OperationDefs with the UOS registry.
func (p *Plugin) Register(r sdk.Registry) error {
	defs := []sdk.OperationDef{
		// --- cleanup ---
		p.purgeDeletedDef(),
		p.tombstoneCleanupDef(),
		p.tempFileCleanupDef(),
		p.cleanupActivityLogDef(),
		p.purgeOldLogsDef(),
		p.cleanupOldBackupsDef(),
		p.trashCleanupDef(),
		p.archiveSweepDef(),
		p.orphanBookFilesCleanupDef(),
		p.dedupeBookFileRowsDef(),
		p.repairJunkTitlesDef(),
		p.seriesDenumberDef(),
		p.purgeMillisecondDurationsDef(),
		p.integrityCheckDef(),
		p.itunesPlaylistImportDef(),

		// --- database ---
		p.dbOptimizeDef(),

		// --- author/series ---
		p.authorDedupScanDef(),
		p.authorSplitScanDef(),
		p.seriesNormalizeDef(),
		p.seriesPruneDef(),
		p.resolveProductionAuthorsDef(),

		// --- metadata ---
		p.metadataRefreshDef(),
		p.metadataUpgradeDef(),
		p.isbnEnrichmentDef(),
		p.autoMatchTranscribedDef(),

		// --- dedup ---
		p.dedupLLMReviewDef(),
		p.aiDedupBatchDef(),
		p.dedupExactTriageDef(),

		// --- batch poller ---
		p.batchPollerDef(),

		// --- write-back ---
		p.bulkWriteBackDef(),

		// --- reconcile ---
		p.reconcileScanDef(),
		// relink-unlinked-books is reconcile-scan's COMPLEMENT: that op flags a
		// book only when os.Stat on its path FAILS, this one flags books whose
		// path resolves fine but which own zero book_file rows (17,149 of 44,886
		// on 2026-08-05). Neither can see the other's population.
		p.relinkUnlinkedBooksDef(),
		// probe-directory-books is relink's TIER-2 escalation, not a rival: relink
		// classifies 44,887 books on a one-stat budget and must pass nil durations,
		// which leaves ClassifyDir's series guard inert and parks every multi-file
		// folder (1,019 of them). This op re-runs the same classifier over just
		// that flagged set with real ffprobe durations filled in.
		p.probeDirectoryBooksDef(),
		p.chaptersBackfillDef(),
		p.itunesHealDef(),
		p.introTranscribeDef(),
		p.repairTranscribeStatusDef(),
		p.introMigrateSingleFileDef(),
		p.extractWAVClipsDef(),

		// --- title cleanup ---
		p.titleBackfillDef(),
		p.titleRepairDef(),

		// --- duration repair ---
		p.durationBackfillDef(),

		// --- booksig/description recovery audit (STOR-1/STOR-2, read-only dry-run) ---
		p.bookSigRecoveryAuditDef(),

		// --- duration re-extract (real ffprobe duration; PR #1555) ---
		p.durationReextractDef(),

		// --- iTunes re-group heal (CONS-FRAG) ---
		p.itunesRegroupDef(),

		// --- filesystem shattered-book heal (tag-anchored) ---
		p.fsRegroupXMLDef(),

		// --- shattered-book regroup (dry-run, review-queue producer; PR-B1) ---
		p.regroupShatteredAIDef(),

		// --- lossless tag backfill for existing rows ---
		p.tagBackfillDef(),

		// --- one-shot startup backfills ---
		p.externalIDBackfillDef(),
		p.movementAtomCleanupDef(),
		p.malformedM4BRemuxDef(),
		p.malformedM4BTranscodeDef(),

		// --- optimize sweep ---
		p.optimizeDef(),

		// --- storage migrations ---
		p.bookSigSidecarMigrateDef(),
	}
	for _, d := range defs {
		if err := r.RegisterOp(d); err != nil {
			return err
		}
	}
	return nil
}
