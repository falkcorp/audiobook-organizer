### Changed

- `internal/maintenance/jobs` — the 19 free helper functions beneath
  `MaintenanceJob.Run` now take narrow, explicitly-declared store slices instead of the full
  `database.Store` (398 methods). Each helper uses 1-4 methods; the new slices in
  `store_slices.go` declare exactly those, with `var _ Slice = (*database.PebbleStore)(nil)`
  assertions so signature drift is a build error rather than a call-site surprise.

  `MaintenanceJob.Run` itself is unchanged — an interface method's parameter type is fixed for
  all 31 implementers. Narrowing the layer beneath it captures the volume without a 31-file
  atomic edit.

  Narrowing a parameter is monotone (`Store` is composed purely of embedded sub-interfaces,
  so anything satisfying `Store` satisfies every slice), so **no call site and no test
  changed**. Verified: `go build ./...`, full-tree `go vet ./...`, and
  `go test ./internal/maintenance/...` all clean.

  Rationale and the measured baseline: `docs/audits/2026-08-16-store-interface-decomposition.md`.
