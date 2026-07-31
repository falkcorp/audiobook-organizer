<!-- file: changelog.d/20260730_090000_abs-sync-id-survival-tests.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1018e16b-75c5-42a9-b135-2aa21de776c0 -->
<!-- last-edited: 2026-07-30 -->

### Added

- **ID-survival acceptance suite for ABS sync identity (spec §4.3).** Added
  `internal/merge/sync_identity_survival_test.go`, proving the client-visible
  `libraryItemId` (syncID) and its associated listening progress survive
  rename, in-place ("tagged") move, retag, and file replacement — the
  scenarios that had no dedicated end-to-end test yet. It also adds a
  pathological-cycle regression for `ResolveSyncItem` (two books redirecting
  to each other) that no existing test covered, proving the resolver fails
  loudly instead of looping forever, and a composed-lifecycle test that
  carries ONE book's identity through rename+move+retag and then through
  BOTH merge-family hops (MergeBooks then CombineBooks) in sequence on the
  same originating ID — the cross-mechanism proof no single-operation test,
  new or existing, can provide. The merge.Service.MergeBooks/CombineBooks
  paths, the separate dedup.MergeBooks hard-delete path (used by
  `internal/reconcile/itunes_heal.go`), the untagged-move version-link path,
  and the happy-path redirect chain (B→A→C) already had thorough,
  mechanism-specific coverage elsewhere (`internal/merge/sync_follow_test.go`,
  `internal/merge/sync_follow_concurrent_test.go`,
  `internal/dedup/book_dedup_sync_follow_test.go`,
  `internal/scanner/sync_identity_move_test.go`), so this suite does not
  duplicate them — it only adds the scenarios that were genuinely missing.
  `dedup.MergeBooks` cannot join the composed-lifecycle chain (`internal/dedup`
  imports `internal/merge`, so the reverse import would cycle); its
  hard-delete case stays covered by its own package's dedicated test instead.
  **Scope note:** the file-replace-primitive test
  (`TestSyncIdentitySurvives_FileReplace_Primitive`) exercises the
  `RepointSyncFile` primitive directly, because no production code path
  invokes it yet — today's only real file-replacement mechanism is an
  in-place `UpdateBookFile` with the same `BookFile.ID` (covered by
  `TestSyncIdentitySurvives_FileReplace_SameID`). A delete-and-recreate
  replacement path is a hypothetical future case, not something this suite's
  green result proves is wired end-to-end. Test-only change; zero production
  code touched.
