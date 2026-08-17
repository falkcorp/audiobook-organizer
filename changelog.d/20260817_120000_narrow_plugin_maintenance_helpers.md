### Changed

- `internal/plugins/maintenance` — 19 helpers now take narrow, explicitly-declared store
  slices instead of the full `database.Store` (398 methods). The new
  `store_slices.go` declares 15 interfaces of 1-11 methods each, with
  `var _ Slice = (*database.PebbleStore)(nil)` assertions so a `database` signature change
  breaks at the declaration rather than at whichever call site compiles first.

  The declarations now state what each helper may do. `bookFileCoreScanner` (1 method, 3
  users) cannot write; `bookFileBulkDeleter` (1 method) is the destructive half of the
  missing-file repair and cannot touch a book row; `fsRegroupStore` is the widest at 11
  methods because that path genuinely moves files between books and deletes rows — which is
  now visible at a glance instead of hidden behind a 398-method parameter.

  Narrowing a parameter is monotone, so **no call site and no test changed** — 19 signature
  lines, 19 insertions, 19 deletions across 13 files. One `database` import became unused in
  `missing_file_repair.go` and was removed. Verified: `go build ./...`, full-tree
  `go vet ./...`, and `go test ./internal/plugins/maintenance/...` all clean.

  Single-method slices are deliberate rather than an oversight. Option D from the audit (pass
  the method value, no interface) would say the same thing with no type declaration, but it
  rewrites every call site; a one-method interface keeps them untouched, which is what makes a
  19-site sweep reviewable in one pass.

### Fixed

- Two counts corrected in place. `internal/maintenance/jobs/store_slices.go` said 31 job types
  implement `MaintenanceJob.Run`; it is 37. The `todo.d` fragment said 24 store-parameter
  declarations remain; re-measured it is 54 — the earlier figure undercounted free functions
  by 3 and counted only the maintenance packages.
