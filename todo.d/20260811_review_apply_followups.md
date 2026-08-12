- [ ] **Repair books applied from the Metadata Review screen before the tag-write fix.**
  `BatchApplyFromCache` updated the database without ever writing tags or
  embedding cover art (fixed in `fix/review-apply-writes-tags`). Every book
  applied from that screen while the defect was live has correct metadata in
  the DB and stale tags on disk. A repair path already exists and does not need
  to be built: the `library.bulk-write-back` operation
  (`internal/server/metadata_ops.go:808` `runBulkWriteBack`, HTTP entry
  `handleBulkWriteBack` in `internal/server/handlers/metadata/handler.go:1175`)
  re-writes tags for a filtered set of books with a worker pool and resume.
  What is missing is the *selection*: there is no record of which books were
  applied from the review screen specifically, so scoping the repair means
  either running it library-wide or deriving the set from the activity log.
  Owner decision — no code needed if a library-wide run is acceptable.

- [ ] **Consider the same file-I/O audit for the remaining apply-shaped
  endpoints.** Two apply paths existed and only one wrote tags. Nothing
  structurally prevents a third from drifting the same way — a shared
  "apply + schedule file I/O" helper would, and neither path uses one today.

- [ ] **17 API calls will now surface an expired session that previously
  returned silent success.** `apiFetch` throws `ApiAuthRedirectError` on a
  login-page response; callers in `web/src/services/api.ts` that check only
  `response.ok` and never read the body (quarantineBook, unquarantineBook,
  restoreSoftDeletedBook, removeImportPath, changePassword, linkBookVersion,
  markNoMatch, includeFilesystemPath, deleteBackup, clearMetadataNoMatch,
  runMaintenanceWindow, updateTaskConfig, saveUserColumnConfig,
  saveSavedFilterPresets, mergeDedupCandidate, dismissDedupCandidate,
  revokeAPIKey) will now throw where they used to succeed. That is the fix
  working, but each caller's catch handler should be checked for a message
  that makes sense to a user.
