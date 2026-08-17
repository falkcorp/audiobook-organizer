### Changed

- `internal/maintenance/jobs` — 19 declarations beneath `MaintenanceJob.Run` (16 free helper
  functions and 3 methods) now take narrow, explicitly-declared store slices instead of the full
  `database.Store` (398 methods). Each uses 1-4 methods; the new slices in
  `store_slices.go` declare exactly those, with `var _ Slice = (*database.PebbleStore)(nil)`
  assertions so signature drift is a build error rather than a call-site surprise.

  `MaintenanceJob.Run` itself is unchanged — an interface method's parameter type is fixed for
  all **37** implementers (measured: 37 `Run` receivers, 37 files, 37 non-test
  `maintenance.Register` calls, one each). Narrowing the layer beneath it captures the volume
  without a 37-file atomic edit.

  Narrowing a parameter is monotone (`Store` is composed purely of embedded sub-interfaces,
  so anything satisfying `Store` satisfies every slice), so **the narrowing itself changed no
  call site and no test**. The separate `deleteOldOperations` split in this PR does change
  both, deliberately — see the entry below. Verified: `go build ./...`, full-tree
  `go vet ./...`, and `go test ./internal/maintenance/...` all clean.

  Rationale and the measured baseline: `docs/audits/2026-08-16-store-interface-decomposition.md`.
