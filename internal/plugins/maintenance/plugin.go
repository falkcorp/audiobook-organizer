// file: internal/plugins/maintenance/plugin.go
// version: 1.52.0
// guid: b2c3d4e5-f6a7-8901-bcde-123456789012
// last-edited: 2026-09-24

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
		p.repairLibraryStateDef(),
		p.purgeDeletedDef(),
		p.tombstoneCleanupDef(),
		p.tempFileCleanupDef(),
		p.cleanupActivityLogDef(),
		p.compactActivityLogDef(),
		p.nightlyCompactActivityLogDef(),
		p.recompactActivityDigestsDef(),
		p.activityFilterIndexBackfillDef(),
		p.optimizeActivityDBDef(),
		p.purgeOldLogsDef(),
		p.pruneAIJournalDef(),
		p.cleanupOldBackupsDef(),
		p.trashCleanupDef(),
		p.orphanBookFilesCleanupDef(),
		p.orphanBookFilesRepointPlanDef(),
		p.dedupeBookFileRowsDef(),
		p.repairJunkTitlesDef(),
		p.seriesDenumberDef(),
		p.seriesPhantomRepairDef(),
		p.integrityCheckDef(),
		p.itunesPlaylistImportDef(),

		// --- database ---
		p.dbOptimizeDef(),
		p.activityReclaimDef(),

		// --- version groups ---
		// version-group-primary-report is REPORT ONLY (read capability, no
		// schedule): it counts version groups with more than one effective
		// primary (VG-DOUBLE-PRIMARY) and changes nothing.
		p.versionGroupPrimaryReportDef(),
		// version-group-primary-repair fixes zero- and double-primary groups
		// with versionprimary's rule. Dry run by default; apply needs
		// explicit group_ids and refuses while library.scan runs.
		p.versionGroupPrimaryRepairDef(),
		p.itunesCloneIntoLibraryDef(),

		// --- author/series ---
		p.authorDedupScanDef(),
		p.authorSplitScanDef(),
		// author-title-fragment-scan is REPORT ONLY (read capability, no
		// schedule): it enumerates title-fragment author rows and changes nothing.
		p.authorTitleFragmentScanDef(),
		// author-whitespace-collision-report is REPORT ONLY (read capability, no
		// schedule): it lists author rows that collide once NormalizeAuthor
		// collapses internal whitespace, and changes nothing.
		p.authorWhitespaceCollisionReportDef(),
		// author-conjunction-repair is NOT reachable from author-split-scan:
		// SplitCompositeAuthorName("& Conrad Westmaas") returns nil (no
		// delimiter, three words), so the split scan skips these rows entirely.
		p.authorConjunctionRepairDef(),
		// author-duplicate-merge is the OPERATOR-DRIVEN twin of the two ops above:
		// it merges only the author names it is explicitly handed, so it can never
		// launder a book-title row into a plausible-looking author the way an
		// automatic "these two look like the same person" classifier would.
		p.authorDuplicateMergeDef(),
		p.authorIDRepairDef(),
		// author-path-link fills the NIL scalars author-id-repair counts and
		// deliberately leaves alone, from the path the book is already filed under.
		p.authorPathLinkDef(),
		p.purgeEmptyAuthorsDef(),
		// purge-empty-narrators is the narrator twin, but with two guards the
		// author op lacks: a per-narrator link re-check immediately before each
		// delete, and the scan stand-down on apply.
		p.purgeEmptyNarratorsDef(),
		p.splitJoinedNarratorsDef(),
		p.authorStripMergeDef(),
		p.missingFileAuditDef(),
		// book_atpath: index: read-only verify + rollback-runbook rebuild.
		p.bookAtPathIndexVerifyDef(),
		p.bookAtPathIndexBackfillDef(),
		p.missingFileRepairDef(),
		p.missingFileRepointDef(),
		// rewrite-path-prefix is the repoint op's complement: repoint repairs a
		// row whose bytes moved WITHIN its own recorded directory, which is the
		// only shape it can derive. A renamed PARENT directory is invisible to it
		// (every row under it lands in "no-candidate-bytes"), and a library scan
		// — the other thing that re-discovers paths — is banned here, so this op
		// is the only route back from a folder rename.
		p.rewritePathPrefixDef(),
		// filepath-collision-report is a standing library-health check, decoupled
		// from the missing-file-audit/repair/repoint trio above: it answers
		// whether Book.FilePath is safe to trust as an identity signal at all,
		// which any future write path touching Book.FilePath must re-check first.
		p.filePathCollisionReportDef(),
		// book-shape-report is the report-only classifier for the oversized-book
		// and same-path split shapes measured on 2026-09-19. No existing op covers
		// them, and three of the shapes it names must NOT be merged -- so it
		// classifies and recommends, and has no apply mode at all.
		p.bookShapeReportDef(),
		// unknown-author-audit is the report-only census of books already filed
		// under an "Unknown Author" directory -- the backlog the HasResolvedAuthor
		// rename gate cannot reach because it only stops NEW placeholder paths.
		p.unknownAuthorAuditDef(),
		// mark-missing-files is the WRITER for the book_file.Missing flag that the
		// dashboard's BrokenFiles counter now reads. missing-file-audit measures the
		// same disk truth but is read-only; this op persists it so the counter is
		// accurate without a full stat sweep on every stats refresh.
		p.markMissingFilesDef(),
		// recover-missing-files is missing-file-repoint's COMPLEMENT: repoint recovers a
		// missing row when it can DERIVE the new path from the old one's shape; this op
		// recovers the residue by matching the row's recorded FileSize to an unclaimed
		// file anywhere in the tree. Run repoint FIRST — its fixes drop out of this op's
		// population automatically. In-tree repoint + outside/nowhere census (Branch A+C);
		// reflink from a source dir (Branch B) is a separate follow-up op.
		p.recoverMissingFilesDef(),
		p.mergeSamePathDupesDef(),
		p.metadataCacheReapDef(),
		p.fileProvenanceCaptureDef(),
		p.fileProvenanceExportDef(),
		p.reviewStatusIndexRepairDef(),
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
		p.clearApplyRenameFailuresDef(),
		p.repointUnrecordedRenamesDef(),
		p.introMigrateSingleFileDef(),
		p.extractWAVClipsDef(),

		// --- title cleanup ---
		p.titleBackfillDef(),
		p.titleRepairDef(),

		// --- duration repair: ONE op (absorbed duration-reextract and
		// purge-millisecond-durations, 2026-09-21) ---
		p.durationBackfillDef(),

		// --- booksig/description recovery audit (STOR-1/STOR-2, read-only dry-run) ---
		p.bookSigRecoveryAuditDef(),

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
