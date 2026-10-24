- [ ] **RETIRED-VERSIONS-4139-REBASE** When #4139 rebases onto the round-4 fixes
      of #4136: drop #4139's own copies of `HasRetiredVersions`,
      `retiredVersionFromPrefix` and `retiredVersionProbe` in
      `retired_hash_lookup.go` (the same probe now lives in
      `internal/database/retired_version_probe.go`); dedupe its `mustPlan*`
      helpers in `plan_virtual_file_lookup_error_test.go` against
      `plan_virtual_retired_test.go`; resolve the `planVirtualFile` conflict
      (use `BookFileRowsAtPathForWrite`, keep its error return); and delete the
      `pending4139` entries from `reviewedFilteredGetterCallers` in
      `internal/arch/filtered_getter_callers_test.go` for every call site #4139
      converts (the ratchet fails on a stale entry).
