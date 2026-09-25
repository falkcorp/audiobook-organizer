<!-- file: .claude/notes/refactor-naming-op-id-aliases-progress.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c0f2e61-8a3d-4b7e-9f14-2d6a8c1e7b30 -->
<!-- last-edited: 2026-09-25 -->

# refactor/naming-op-id-aliases progress

Spec: docs/audits/2026-09-25-interface-naming-consistency.md section 8.

Design: OperationDef.FormerIDs; registry alias table (atomic.Pointer map);
canonicalize at every boundary (EnqueueOp, dispatcher, resume, retry,
queued_merge, subprocess, handlers timeline/display); Prometheus counter
audiobook_organizer_operation_deprecated_def_id_total{alias,entry}.

Renames:
- maintenance.itunes-regroup -> itunes.regroup
- maintenance.itunes-playlist-import -> itunes.playlist-import
- maintenance.itunes-heal -> itunes.heal
- maintenance.itunes-clone-into-library -> itunes.clone-into-library
- library.optimize -> maintenance.library-optimize
- maintenance.dedup-llm-review -> alias of dedup.llm-review (duplicate op; def removed)

Done:
- (in progress) registry alias layer

Next: renames, handlers, scheduler hasActiveV2Op, web/docs, guard test, changelog.
